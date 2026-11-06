package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// GHStats 同步 GitHub Release 资产的下载量（assets[].download_count）。
// 背景：FnDepot 规范第三方源不在 fnpack.json 提供 download_count（外部源
// 不统计下载量），但 FPK 基本都挂在 GitHub Releases 上——Release 资产有
// 真实的全网下载次数。按仓库批量拉（59 个源仓库覆盖 895 个应用），
// 匿名 API 限流 60 次/小时：ETag 条件请求（304 不计数）、最旧优先轮换、
// 限流余量为 0 时立即停手，缓存落盘重启不丢。
// 展示层零改动：目录合并时填入 AppInfo.DownloadCount，前端按
// 「N 次下载」渲染（与官方下载量同款回退链的最优先位）。

var ghReleaseURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/releases/download/(.+)/([^/]+)$`)

// 0.6.390：`releases/latest/download/<asset>`（无 tag）。tnnevol 等 203 个
// 应用用这种链接——旧解析器只认带 tag 的路径，这些应用永远拿不到下载量/
// 体积。latest 只是"最新 Release"的别名，按资产名查同一份缓存即可。
var ghLatestURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/releases/latest/download/([^/]+)$`)

// ParseGhReleaseURL 从 Release 下载链接解析 owner/repo/资产文件名。
// 非 GitHub Release 链接（raw/jsDelivr/其它托管）返回 ok=false。
//
// 0.6.336：tag 允许包含 `/`（conversun/fnos-apps 的 tag 形如
// `1panel/v1.10.34-lts-r8`）。旧正则把 tag 当单个路径段，asset 变成
// `v1.10.34-lts-r8/1panel_..._x86.fpk` → 查 GitHub 资产元数据永远查不到
// （下载量/大小两处都拿不到值）。
func ParseGhReleaseURL(raw string) (owner, repo, asset string, ok bool) {
	raw = strings.TrimSpace(raw)
	if m := ghLatestURLRe.FindStringSubmatch(raw); m != nil {
		asset = m[3]
		if u, err := url.PathUnescape(asset); err == nil && u != "" {
			asset = u
		}
		return m[1], m[2], asset, true
	}
	m := ghReleaseURLRe.FindStringSubmatch(raw)
	if m == nil {
		return "", "", "", false
	}
	asset = m[4]
	if u, err := url.PathUnescape(asset); err == nil && u != "" {
		asset = u
	}
	return m[1], m[2], asset, true
}

// 应用级累计下载量的资产归一化。
// 目标：fnmusic-ext-2.12.0-8.fpk / fnmusic-ext-2.11.0-4.fpk → fnmusic-ext；
// moo_0.6.506_x86.fpk → moo；1panel_1.10.34-lts-r8_x86.fpk → 1panel；
// EasyTier-EUI-fnos-x86_64-0.9.020604.fpk → easytier-eui-fnos。
var (
	ghArchTokenRe = regexp.MustCompile(`(?i)(?:^|[-_.])(x86[_-]?64|amd64|x86|arm64|aarch64|armv7l|armhf|arm|all|universal|noarch)(?:$|[-_.])`)
	ghDateTokenRe = regexp.MustCompile(`(?:^|[-_.])20\d{6}(?:$|[-_.])`)
	// 只认「带点」的版本 token（1.2 / 1.2.3 / v1.2.3 / 1.2.3+build）。
	// 取最后一个匹配的位置往前切：php-8.3_8.3.10 → php-8.3（保留应用名里的 8.3），
	// fnmusic-ext-2.12.0-8 → fnmusic-ext。
	ghVersionTokenRe = regexp.MustCompile(`(?i)(?:^|[-_.])v?\d+\.\d+(?:\.\d+)*(?:\+[0-9a-z]+)?`)
)

// trimTrailingVersion 从最后一个版本 token 起截断（含前面的分隔符）。
func trimTrailingVersion(s string) string {
	ms := ghVersionTokenRe.FindAllStringIndex(s, -1)
	if len(ms) == 0 {
		return s
	}
	last := ms[len(ms)-1]
	return strings.TrimRight(s[:last[0]], "-_.")
}

func normalizeAppKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// assetBase 去掉扩展名、架构、日期、版本号，得到应用级归一键。
// 只用于「同一仓库内同一应用的各版本资产」聚合；当前资产优先，
// 不拿短应用名做前缀匹配（moo 不能吃掉 moo-mate）。
func assetBase(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimSuffix(s, ".fpk")
	s = ghArchTokenRe.ReplaceAllString(s, "-")
	s = strings.TrimRight(s, "-_.")
	s = ghDateTokenRe.ReplaceAllString(s, "-")
	s = strings.TrimRight(s, "-_.")
	s = trimTrailingVersion(s)
	s = strings.TrimRight(s, "-_.")
	return normalizeAppKey(s)
}

// isCountableFPK 只统计安装包本体：.sha256 / .minisig / 源码包等不计。
func isCountableFPK(name string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(name)), ".fpk")
}

// selfProbeAssets 历史「本机镜像测速探针」资产（仓库 + Release tag + 资产名）。
//
// 这些资产被 Moo 自己当成 GitHub 加速镜像的测速目标：每实例每 5 分钟对全部
// 镜像各下前 2MB 就断，GitHub 每次请求记一次 download_count。于是它们的计数
// 几乎全是本机自测流量，不是用户下载——实测 moo_0.6.325_x86.fpk 被灌到
// 41 万（占该应用累计 95%），剔除后 moo 全部版本合计仅 2.1 万。
//
// 0.6.511：累加应用级累计（AppTotals）时跳过这些资产，被跳过的量单独记进
// AppProbeTotals，供详情页透明展示「已排除本机测速请求 N 次」。
//
// ⚠️ 只增不删：GitHub 的 download_count 不可回滚，且全网未升级的老实例会
// 继续探测旧资产。删掉任一历史条目都会让该应用的累计重新虚高。
var selfProbeAssets = []struct{ Repo, Tag, Asset string }{
	// 0.6.325 的 x86 FPK 曾被 ghProbeAssets[0] 当作探针（0.6.333~0.6.510）。
	{Repo: "Blue-Mink/moo", Tag: "v0.6.325", Asset: "moo_0.6.325_x86.fpk"},
}

// isSelfProbeAsset 判断某 Release 资产是否是本机测速探针（大小写不敏感）。
func isSelfProbeAsset(owner, repo, tag, asset string) bool {
	full := strings.ToLower(strings.TrimSpace(owner + "/" + repo))
	for _, p := range selfProbeAssets {
		if strings.ToLower(p.Repo) == full && p.Tag == tag && p.Asset == asset {
			return true
		}
	}
	return false
}

type ghRepoStats struct {
	ETag      string         `json:"etag"`
	FetchedAt string         `json:"fetched_at"`
	Assets    map[string]int `json:"assets"` // 资产文件名 → download_count（当前版本用）
	// Sizes 资产文件名 → 字节数（GitHub Release asset.size）。
	// 0.6.336：源索引不写 size 的应用（如 conversun/fnos-apps 的 apps.json）
	// 用这里补「安装包大小」——同一次 releases 请求里就有，零额外请求。
	Sizes map[string]int64 `json:"sizes,omitempty"`
	// AppTotals 规范化资产基名 → 应用级累计 download_count。
	// 0.6.509: 第三方应用不再只显示「当前版本资产」的计数（发新版归零），
	// 而是把同一仓库里同一应用所有正式版本的 FPK 资产相加。基名由
	// assetBase() 去掉架构/版本/扩展名得到（如 fnmusic-ext-2.12.0-8.fpk
	// → fnmusic-ext），当前资产的基名直接查这张表。
	AppTotals map[string]int `json:"app_totals,omitempty"`
	// AppProbeTotals 规范化资产基名 → 被排除的「本机测速探针」下载量。
	// 0.6.511：累计里不含探针流量，但把被剔除的量留档，详情页可透明展示
	// 「已排除本机镜像测速请求 N 次」，不隐藏事实。
	AppProbeTotals map[string]int `json:"app_probe_totals,omitempty"`
	// TotalsBuilt 标记 AppTotals 已按新口径构建过；旧缓存缺失时忽略 ETag
	// 强制重拉一次（与 0.6.336 的 sizes 迁移同款）。
	TotalsBuilt bool `json:"totals_built,omitempty"`
	// ProbeExcludedBuilt 标记「探针排除」口径已构建过（0.6.511）。
	// 缺这个标记的旧缓存必须重拉一次，否则升级后仍显示灌水后的累计值。
	ProbeExcludedBuilt bool `json:"probe_excluded_built,omitempty"`
}

type GHStats struct {
	mu      sync.Mutex
	path    string
	apiBase string                  // 默认 https://api.github.com（测试可指向 httptest）
	stats   map[string]*ghRepoStats // repoKey(owner/repo) → 统计
	queue   []string                // 待拉仓库（最旧优先轮换）
	queued  map[string]bool
	client  *http.Client
	// 0.6.390：可选 GitHub token。匿名 60 次/小时（30 仓库/轮）；
	// 配了 token 是 5000 次/小时，99 个源仓库可更快覆盖一轮。
	tokenMu sync.RWMutex
	token   string
}

// SetToken 设置 GitHub API token（空串 = 回到匿名）。
// 与 GHForks.SetToken 同语义：设置页更新后立即生效，无需重启。
func (g *GHStats) SetToken(tok string) {
	g.tokenMu.Lock()
	g.token = strings.TrimSpace(tok)
	g.tokenMu.Unlock()
}

func (g *GHStats) authToken() string {
	g.tokenMu.RLock()
	defer g.tokenMu.RUnlock()
	return g.token
}

var ghStatsClient = &http.Client{Timeout: 15 * time.Second}

// NewGHStats 创建同步器并加载落盘缓存（损坏容忍：读不了当空）。
func NewGHStats(cachePath string) *GHStats {
	g := &GHStats{
		path:    cachePath,
		apiBase: "https://api.github.com",
		stats:   map[string]*ghRepoStats{},
		queued:  map[string]bool{},
		client:  ghStatsClient,
	}
	g.load()
	return g
}

func (g *GHStats) repoKey(owner, repo string) string { return owner + "/" + repo }

// Enqueue 把一批 Release 链接涉及的仓库排入待拉队列（幂等、非阻塞，
// buildCatalog 热路径调用无负担）。
func (g *GHStats) Enqueue(releaseURLs []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, u := range releaseURLs {
		owner, repo, _, ok := ParseGhReleaseURL(u)
		if !ok {
			continue
		}
		k := g.repoKey(owner, repo)
		if !g.queued[k] {
			g.queued[k] = true
			g.queue = append(g.queue, k)
		}
	}
}

// Lookup 查仓库里某资产的下载量。
func (g *GHStats) Lookup(owner, repo, asset string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rs, ok := g.stats[g.repoKey(owner, repo)]
	if !ok || rs == nil || len(rs.Assets) == 0 {
		return 0, false
	}
	n, ok := rs.Assets[asset]
	return n, ok
}

// LookupAppProbeExcluded 查某应用累计里被排除的「本机测速探针」下载量。
// 仅用于详情页透明展示（0.6.511）；没有可排除的量时返回 0。
func (g *GHStats) LookupAppProbeExcluded(owner, repo, currentAsset string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	rs, exists := g.stats[g.repoKey(owner, repo)]
	if !exists || rs == nil || len(rs.AppProbeTotals) == 0 {
		return 0
	}
	base := assetBase(currentAsset)
	if base == "" {
		return 0
	}
	return rs.AppProbeTotals[base]
}

// LookupAppTotal 查某应用的应用级累计下载量。
// total 来自 AppTotals[assetBase(currentAsset)]（同仓库、同应用、全部正式版本、
// x86/arm 都算）；current 仍是当前版本资产的 download_count。找不到累计时
// 返回 (0, current, false)，调用方按「当前版本」降级展示。
func (g *GHStats) LookupAppTotal(owner, repo, currentAsset string) (total, current int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rs, exists := g.stats[g.repoKey(owner, repo)]
	if !exists || rs == nil {
		return 0, 0, false
	}
	current = rs.Assets[currentAsset]
	base := assetBase(currentAsset)
	if base != "" {
		total = rs.AppTotals[base]
	}
	// 旧缓存/资产改名导致基名缺失时，至少保住当前版本的数。
	if total > 0 && total < current {
		total = current
	}
	if total > 0 {
		return total, current, true
	}
	return 0, current, false
}

// LookupSize 查仓库里某资产的字节大小（GitHub Release 资产 size）。
// 用于源索引未声明 size 的应用补「安装包大小」（0.6.336）。
func (g *GHStats) LookupSize(owner, repo, asset string) (int64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rs, ok := g.stats[g.repoKey(owner, repo)]
	if !ok || rs == nil || len(rs.Sizes) == 0 {
		return 0, false
	}
	n, ok := rs.Sizes[asset]
	return n, ok
}

// ErrRateLimited 限流余量耗尽（本轮停手，下一轮继续）。
var ErrRateLimited = fmt.Errorf("GitHub API 限流余量为 0，本轮暂停")

// RefreshRound 从队列拉最多 maxRepos 个仓库（最久未拉优先），
// 限流余量耗尽或队列为空时提前返回。返回实际拉取的仓库数。
func (g *GHStats) RefreshRound(ctx context.Context, maxRepos int) (int, error) {
	g.mu.Lock()
	// 最久未拉优先：按 FetchedAt 升序排（没拉过的最前）
	// 0.6.336 迁移优先：历史缓存缺 sizes 的仓库排最前，尽快补齐体积数据。
	needsSizes := func(k string) bool {
		s, ok := g.stats[k]
		return ok && s != nil && len(s.Assets) > 0 && len(s.Sizes) == 0
	}
	byAge := func(i, j int) bool {
		if ni, nj := needsSizes(g.queue[i]), needsSizes(g.queue[j]); ni != nj {
			return ni
		}
		fi, fj := "", ""
		if s, ok := g.stats[g.queue[i]]; ok && s != nil {
			fi = s.FetchedAt
		}
		if s, ok := g.stats[g.queue[j]]; ok && s != nil {
			fj = s.FetchedAt
		}
		return fi < fj
	}
	sort.SliceStable(g.queue, byAge)
	batch := g.queue
	g.queue = nil
	for _, k := range batch {
		delete(g.queued, k) // 出队即清标记，失败/限流回队时才能重新入队
	}
	g.mu.Unlock()

	updated := 0
	for _, k := range batch {
		if ctx.Err() != nil {
			break
		}
		if updated >= maxRepos {
			g.requeueRest(batch[updated:])
			break
		}
		owner, repo, _ := strings.Cut(k, "/")
		err := g.fetchRepo(ctx, owner, repo)
		if err == ErrRateLimited {
			g.requeueRest(batch[updated:])
			return updated, ErrRateLimited
		}
		if err != nil {
			// 单仓库失败不拖垮整轮：放回队尾下一轮重试
			g.requeueOne(k)
			continue
		}
		updated++
	}
	g.save()
	return updated, nil
}

func (g *GHStats) requeueRest(keys []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range keys {
		if !g.queued[k] {
			g.queued[k] = true
			g.queue = append(g.queue, k)
		}
	}
}

func (g *GHStats) requeueOne(k string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.queued[k] {
		g.queued[k] = true
		g.queue = append(g.queue, k)
	}
}

type ghReleaseAsset struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DownloadCount int    `json:"download_count"`
	Size          int64  `json:"size"`
}

type ghRelease struct {
	TagName    string           `json:"tag_name"`
	Prerelease bool             `json:"prerelease"`
	Draft      bool             `json:"draft"`
	CreatedAt  string           `json:"created_at"`
	Assets     []ghReleaseAsset `json:"assets"`
}

// fetchRepo 拉一个仓库的全部 Release 资产下载量（最多 8 页 × 100 条）。
func (g *GHStats) fetchRepo(ctx context.Context, owner, repo string) error {
	g.mu.Lock()
	etag := ""
	assets := map[string]int{}
	sizes := map[string]int64{}
	if s, ok := g.stats[g.repoKey(owner, repo)]; ok && s != nil {
		etag = s.ETag
		// 0.6.336 迁移：旧缓存只有 assets、没有 sizes → 忽略 ETag 强制重拉
		// 一次，把资产字节数补齐（之后照常 304，不再额外消耗限流）。
		// 0.6.509 迁移：同理补齐 AppTotals（应用级累计）。
		if len(s.Assets) > 0 && (len(s.Sizes) == 0 || !s.TotalsBuilt || !s.ProbeExcludedBuilt) {
			etag = ""
		}
		for k, v := range s.Assets {
			assets[k] = v
		}
		for k, v := range s.Sizes {
			sizes[k] = v
		}
	}
	g.mu.Unlock()

	// 本轮已处理资产名：跨 Release 同名资产以「最新 Release」为准
	// （API 按发布时间倒序，本轮先写者保留）；但跨轮必须用新值覆盖旧缓存。
	seen := map[string]bool{}
	seenSizes := map[string]bool{}
	seenAssetIDs := map[string]bool{}
	appTotals := map[string]int{}
	probeTotals := map[string]int{}
	newETag := ""

	commit := func() {
		g.mu.Lock()
		k := g.repoKey(owner, repo)
		rs, ok := g.stats[k]
		if !ok || rs == nil {
			rs = &ghRepoStats{}
			g.stats[k] = rs
		}
		rs.Assets = assets
		rs.Sizes = sizes
		rs.AppTotals = appTotals
		rs.AppProbeTotals = probeTotals
		rs.TotalsBuilt = true
		rs.ProbeExcludedBuilt = true
		if newETag != "" {
			rs.ETag = newETag
		}
		rs.FetchedAt = time.Now().Format(time.RFC3339)
		g.mu.Unlock()
	}

	// 0.6.336：页上限 3→8。conversun/fnos-apps 有 466 个 Release（5 页），
	// 只拉 3 页时 175 个应用里 37 个的资产不在窗口内 → 拿不到 size。
	// 循环本身按「本页不足 100 条」提前结束，多数仓库仍只 1 次请求。
	const ghStatsMaxPages = 8
	for page := 1; page <= ghStatsMaxPages; page++ {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100", strings.TrimRight(g.apiBase, "/"), owner, repo)
		if page > 1 {
			u += fmt.Sprintf("&page=%d", page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if tok := g.authToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := g.client.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			return nil // 304：数据未变，不消耗限流
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			remain := resp.Header.Get("X-RateLimit-Remaining")
			if remain == "0" {
				return ErrRateLimited
			}
			return fmt.Errorf("GitHub API %d（限流剩余 %s）", resp.StatusCode, remain)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("GitHub API HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}

		var releases []ghRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return fmt.Errorf("GitHub releases 解析失败: %w", err)
		}

		if page == 1 {
			newETag = resp.Header.Get("Etag")
		}
		for _, r := range releases {
			for _, a := range r.Assets {
				if a.Name == "" {
					continue
				}
				// 当前版本用：每轮覆盖旧值；同一轮内先到先得（最新 Release）。
				if !seen[a.Name] {
					assets[a.Name] = a.DownloadCount
					seen[a.Name] = true
				}
				if a.Size > 0 && !seenSizes[a.Name] {
					sizes[a.Name] = a.Size
					seenSizes[a.Name] = true
				}
				// 应用级累计只算正式版本的 FPK 安装包。
				if r.Draft || r.Prerelease || !isCountableFPK(a.Name) {
					continue
				}
				key := a.Name
				if a.ID > 0 {
					key = fmt.Sprintf("id:%d", a.ID)
				} else {
					key = r.TagName + "|" + a.Name
				}
				if seenAssetIDs[key] {
					continue
				}
				seenAssetIDs[key] = true
				if base := assetBase(a.Name); base != "" {
					// 0.6.511：本机测速探针的 download_count 几乎全是每台 Moo
					// 每 5 分钟一次的自测请求，不计入用户可见的应用累计；
					// 单独留档，详情页透明展示「已排除 N 次」。
					if isSelfProbeAsset(owner, repo, r.TagName, a.Name) {
						probeTotals[base] += a.DownloadCount
					} else {
						appTotals[base] += a.DownloadCount
					}
				}
			}
		}

		if len(releases) < 100 {
			commit()
			return nil // 没有更多页
		}
	}
	commit()
	return nil
}

// readWholeFile 读小文件（缓存用途）。
func readWholeFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeAtomic 临时文件 + rename 原子写。
func writeAtomic(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// load 读落盘缓存。
func (g *GHStats) load() {
	if g.path == "" {
		return
	}
	raw, err := readWholeFile(g.path)
	if err != nil {
		return
	}
	var doc struct {
		Stats map[string]*ghRepoStats `json:"stats"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	g.stats = doc.Stats
	if g.stats == nil {
		g.stats = map[string]*ghRepoStats{}
	}
}

// save 原子落盘。
func (g *GHStats) save() {
	if g.path == "" {
		return
	}
	g.mu.Lock()
	doc := struct {
		Stats map[string]*ghRepoStats `json:"stats"`
	}{Stats: g.stats}
	g.mu.Unlock()
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	writeAtomic(g.path, raw)
}
