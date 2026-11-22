package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"moo/internal/source"
)

// ghReleaseAsset GitHub release 里的单个资产（只取需要的字段）。
type ghReleaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"` // 0.2.73：形如 "sha256:abcd…"，旧接口可能为空
}

// ghRelease GitHub release（只取需要的字段）。
type ghRelease struct {
	TagName     string           `json:"tag_name"`
	Body        string           `json:"body"`         // 0.2.73：release notes，供更新日志展示
	PublishedAt string           `json:"published_at"` // 0.2.73：RFC3339
	Assets      []ghReleaseAsset `json:"assets"`
}

// discoveredApp 发现结果：一个应用的下载直链（后续下载解析 manifest 补全）。
type discoveredApp struct {
	Name        string // asset 文件名（可能不是 appname，appname 待解包确认）
	Version     string // 从 tag_name 推导
	DownloadURL string
	Size        int64

	// 0.2.73：上游 release 元数据，用于补全 moo.json 协议字段（README / 更新日志 / 历史版本）。
	// 非 GitHub release 通道（仓库文件、Gitee）留空，行为与旧版一致。
	Changelog string
	UpdatedAt string // RFC3339
	Sha256    string
	PrevVer   string // 次高版本号（历史版本，只留 1 个）
	PrevAt    string
	PrevNotes string
	PrevPkgs  map[string]*appPackage // 历史版本按架构的下载信息
}

var ghRepoRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([^/]+)/([^/]+)/?`)

// parseGHRepo 从 URL 提取 owner/repo（GitHub 仓库形态）。
func parseGHRepo(raw string) (owner, repo string, ok bool) {
	m := ghRepoRe.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 3 {
		return "", "", false
	}
	repo = strings.TrimSuffix(m[2], ".git")
	return m[1], repo, true
}

// repoOwner 取仓库 owner（GitHub 仓库形态）。
func repoOwner(raw string) string {
	if o, _, ok := parseGHRepo(raw); ok {
		return o
	}
	return ""
}

// ghReleases 拉取 GitHub releases（分页，最多 max 页）。
func ghReleases(ctx context.Context, owner, repo string, httpc *http.Client, token string) ([]ghRelease, error) {
	var all []ghRelease
	page := 1
	for {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", ghAPIBase, owner, repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "moo-mate")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode != http.StatusOK {
			body := strings.TrimSpace(string(data))
			if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(body), "rate limit") {
				return nil, fmt.Errorf("GitHub API 限流（403）：在 Moo 设置里配置 GitHub Token 可解除 60 次/小时限制，或稍后重试")
			}
			return nil, fmt.Errorf("GitHub API HTTP %d: %s", resp.StatusCode, body)
		}
		var batch []ghRelease
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 || page >= 3 {
			break
		}
		page++
	}
	return all, nil
}

// versionFromFilename 从 .fpk 文件名提取版本（9router-0.5.55.fpk → 0.5.55，
// FnEnhance-1.0.3-x86.fpk → 1.0.3）。取最后一段「数字.数字[.数字…]」，失败返回空串。
func versionFromFilename(name string) string {
	base := strings.TrimSpace(name)
	if strings.HasSuffix(strings.ToLower(base), ".fpk") {
		base = base[:len(base)-4]
	}
	best := ""
	for i := 0; i < len(base); i++ {
		if base[i] < '0' || base[i] > '9' {
			continue
		}
		j := i
		dots := 0
		for j < len(base) {
			c := base[j]
			if c >= '0' && c <= '9' {
				j++
				continue
			}
			if c == '.' && j+1 < len(base) && base[j+1] >= '0' && base[j+1] <= '9' {
				dots++
				j++
				continue
			}
			break
		}
		if dots >= 1 {
			best = base[i:j]
		}
		i = j - 1
	}
	return best
}

// versionFromTag 从 tag 名（如 v1.2.3、1.2.3、app/v1.2.3）提取版本号。
// 失败返回空串（该 release 不能直接用作版本）。
func versionFromTag(tag string) string {
	tag = strings.TrimSpace(tag)
	// 先按 / 取最后一段（app/v1.2.3 → v1.2.3），再剥前导 v。
	if i := strings.LastIndex(tag, "/"); i >= 0 {
		tag = tag[i+1:]
	}
	tag = strings.TrimPrefix(tag, "v")
	// 只认以数字开头的版本串（含 x.y.z-N 构建号）。
	if tag == "" || (tag[0] < '0' || tag[0] > '9') {
		// 0.2.73：兼容带应用前缀的 tag（mate-v0.2.71 / release-1.2.3）——
		// 以前一律返回空，导致 Moo Mate 自己的 release 拿不到版本号、更新日志与历史版本。
		if m := reTagVersionSuffix.FindStringSubmatch(tag); m != nil {
			return m[1]
		}
		return ""
	}
	return tag
}

// reTagVersionSuffix 只接受「前缀-vX.Y.Z」形态（mate-v0.2.71）。
// 刻意不认 release-2026.1.1 这类无 v 的日期前缀标签：版本不可比，宁可留空。
var reTagVersionSuffix = regexp.MustCompile(`(?i)(?:^|[-_/])v(\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.\-]+)?)$`)

// releaseFPK release 里一条 .fpk 资产（GitHub 统一中间形态）。
type releaseFPK struct {
	name     string
	url      string
	size     int64
	ver      string // 从 tag / 文件名推导的版本（可能为空）
	fromRepo bool   // true = 仓库文件树里的包，false = release 资产

	// 0.2.73：仅 release 通道有值（仓库文件树/Gitee 为空）。
	changelog string
	updatedAt string
	sha256    string
}

// dedupFPKs 按资产文件名去重，同名只留最高版，返回 discoveredApp 列表。
// 仓库文件树里的包缺 size 时传 0，实际体积由 build 阶段 inspectFPK 下载后补全。
func dedupFPKs(items []releaseFPK) []discoveredApp {
	best := map[string]discoveredApp{}
	bestVer := map[string]string{}
	for _, it := range items {
		if !strings.HasSuffix(strings.ToLower(it.name), ".fpk") {
			continue
		}
		if it.url == "" {
			continue
		}
		if _, exists := best[it.name]; exists {
			if it.ver == "" || (bestVer[it.name] != "" && !versionGreater(it.ver, bestVer[it.name])) {
				continue
			}
		}
		best[it.name] = discoveredApp{
			Name:        it.name,
			Version:     it.ver,
			DownloadURL: it.url,
			Size:        it.size,
			Changelog:   it.changelog,
			UpdatedAt:   it.updatedAt,
			Sha256:      it.sha256,
		}
		bestVer[it.name] = it.ver
	}
	out := make([]discoveredApp, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	return out
}

// discoverFromRepo 从仓库发现 .fpk 应用（兼容旧调用，不带日志回调）。
func discoverFromRepo(ctx context.Context, rawURL, token string, httpc *http.Client) ([]discoveredApp, error) {
	return discoverFromRepoLog(ctx, rawURL, token, httpc, nil)
}

// discoverFromRepoLog 扫描两条通道并合并：
//   - release 资产（GitHub Releases）
//   - 仓库文件树里的 .fpk（包直接提交进仓库的形态）
//
// 两条通道互不阻塞：单条失败只记日志，另一条仍可产出候选；两条都失败才返回错误。
func discoverFromRepoLog(ctx context.Context, rawURL, token string, httpc *http.Client, logf func(string, string)) ([]discoveredApp, error) {
	log := func(l, m string) {
		if logf != nil {
			logf(l, m)
		}
	}
	// GitHub
	if owner, repo, ok := parseGHRepo(rawURL); ok {
		log("info", "GitHub：读取 release 列表…")
		var relItems []releaseFPK
		var relErr error
		if rels, err := ghReleases(ctx, owner, repo, httpc, token); err != nil {
			relErr = err
			log("warn", "release 读取失败："+err.Error())
		} else {
			assets, fpks := 0, 0
			for _, rel := range rels {
				ver := versionFromTag(rel.TagName)
				notes := cleanReleaseNotes(rel.Body)
				for _, a := range rel.Assets {
					assets++
					if !strings.HasSuffix(strings.ToLower(a.Name), ".fpk") {
						continue
					}
					fpks++
					relItems = append(relItems, releaseFPK{
						name: a.Name, url: a.URL, size: a.Size, ver: ver,
						changelog: notes, updatedAt: rel.PublishedAt, sha256: shaFromDigest(a.Digest),
					})
				}
			}
			log("ok", fmt.Sprintf("release：%d 个资产，%d 个 .fpk", assets, fpks))
		}
		fileItems, ferr := scanRepoFilesGitHub(ctx, owner, repo, token, httpc, logf)
		if ferr != nil {
			log("warn", "仓库文件扫描跳过："+ferr.Error())
		} else if len(fileItems) > 0 {
			log("ok", fmt.Sprintf("仓库文件：%d 个 .fpk", len(fileItems)))
		}
		merged := mergeFPKs(relItems, fileItems)
		if len(merged) == 0 {
			if relErr != nil && ferr != nil {
				return nil, relErr
			}
			return nil, nil
		}
		log("ok", fmt.Sprintf("合并去重：%d 个候选 .fpk", len(merged)))
		return dedupMerged(merged, logf), nil
	}
	return nil, fmt.Errorf("仅支持 GitHub 仓库地址: %s", rawURL)
}

// versionGreater 数值分段比较版本（与 gen-moo-json 同口径）。
func versionGreater(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := parseIntPrefix(av)
		bn, berr := parseIntPrefix(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an > bn
			}
			continue
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

func parseIntPrefix(s string) (int, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("非数字")
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// probeIndex 复用 Moo 的 ProbeIndex：探测仓库是否已发布可解析索引。
// 返回命中的索引文件名（"" = 无索引 / 网络失败）。
func probeIndex(repoURL string) string {
	return source.ProbeIndex(repoURL)
}

// hostFromURL 取 URL 的主机名（用于日志/去重展示）。
func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

// githubMirrorPrefixes GitHub 加速镜像前缀（与 Moo config.GitHubMirrorOptions 对齐，
// 排除 conversun（仅代理 conversun 仓）与 auto/custom/direct 特殊项）。
// 本机直连 objects.githubusercontent.com 常 0B/s（15s 只下 207KB），必须走镜像。
//
// 单一真源 = lists/github_mirrors.txt，本表顺序必须与它逐条一致（守护测试
// cmd/moo-mate/mirror_sync_test.go）。清单于 2026-11-22 按 16 场景实测重排
// （报告 MIRROR_SCENARIO_20261122.md）：下架 8 个失效源（hk.gh-proxy.org 500、
// cors.isteed.cc 403、wget.la ~25KB/s、felicity / memory-echoes / github.dpik.top /
// gitproxy.mrhjx / ghproxy.cxkpro 429），新增 edgeone，ghfast / yylx 排尾。
var githubMirrorPrefixes = []string{
	"https://gh-proxy.com/",
	"https://github.tbap.top/",
	"https://edgeone.gh-proxy.org/",
	"https://cdn.gh-proxy.org/",
	"https://gh-proxy.org/",
	"https://gh.dpik.top/",
	"https://cdn.gh-proxy.com/",
	"https://ghproxy.net/",
	"https://ghfast.top/",
	"https://git.yylx.win/",
}

// mirrorCandidates 把 GitHub release 资产 URL 展开成「镜像前缀 + 原 URL」候选。
// 镜像前缀排在前面（本机直连 objects CDN 常 0B/s），直连原 URL 兜底放最后。
// 非 GitHub 地址只保留原 URL。前缀服务会自行跟随 302 到 objects CDN。
func mirrorCandidates(raw string) []string {
	var out []string
	if strings.Contains(raw, "github.com/") {
		for _, p := range githubMirrorPrefixes {
			out = append(out, p+raw)
		}
	}
	out = append(out, raw)
	return out
}

// mirrorCandidatesWith 用给定前缀列表展开候选（供 mate 图标下载复用 Moo 健康表）：
// 去重、前缀候选在前（自动补/去尾斜杠）、原地址直连始终最后。
// 非 GitHub 地址不加前缀（镜像代理不了，语义与 mirrorCandidates 一致）。
func mirrorCandidatesWith(prefixes []string, raw string) []string {
	out := make([]string, 0, len(prefixes)+1)
	seen := make(map[string]bool, len(prefixes)+1)
	add := func(u string) {
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	if strings.Contains(raw, "github.com/") {
		for _, p := range prefixes {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			add(strings.TrimSuffix(p, "/") + "/" + raw)
		}
	}
	add(raw)
	return out
}

// hasMirrorPrefix 判断候选列表里除直连原地址外是否还有镜像通道。
// mate 的 Moo 健康表 provider 为空时返回 false，调用方回落静态表。
func hasMirrorPrefix(cands []string, raw string) bool {
	for _, u := range cands {
		if u != raw {
			return true
		}
	}
	return false
}

// timeoutCtx 短超时上下文（发现/下载预算）。
func timeoutCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}
