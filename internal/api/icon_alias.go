package api

// icon_alias.go —— 图标「目录名 ≠ appname」别名回落（0.6.326d）。
//
// 背景：图标布局候选按 <appname>/ICON.PNG 推导，但上游仓库目录名常与 appname
// 不一致，且 raw 路径大小写敏感 → 永久 404（0.6.326d 全量实测：1217 张无
// icon_url 卡里 1195 张后端可取到真图，余 22 张中 5 张属此类真实缺口）：
//
//	smartdns@33205q / @710850609 / @ryxiang / @tzi-shue-fndepot-1
//	                                  → 仓库目录实为 SmartDNS
//	小松鼠@KKKK987                     → 仓库唯一目录 xiansongshu
//
// 通道：jsDelivr 数据 API 列举仓库文件树（无需 GitHub token，不吃匿名 60/h
// 限额；实测 33205q/FnDepot 全量返回）：
//
//	https://data.jsdelivr.com/v1/package/gh/{owner}/{repo}@{branch}/flat
//
// 匹配规则（按优先级）：
//
//	a. 归一化相等：norm(dir) == norm(appname)（norm = 小写 + 只留 a-z0-9）
//	b. 唯一目录别名：仓库顶层只有 1 个非隐藏目录 → 用它
//
// 两条规则都要求该目录下确实存在图标文件（用列举结果判定，不做大小写猜测），
// 命中后按 <dir>/ICON.PNG 等生成候选，复用主链路的镜像竞速 + 占位图黑名单。
//
// 成本/纪律：按 owner/repo 落盘缓存 72h；失败按 30min 起指数退避（上限 6h）；
// 限流/超时/解析失败/仓库超 jsDelivr 50MB 上限 一律静默降级（维持现状 404），
// 绝不拖慢主链路，也绝不把「无图标」变成「错图标」。
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/netx"
	"moo/internal/source"
)

const (
	iconAliasTTL      = 72 * time.Hour   // 成功列举的缓存有效期
	iconAliasFailMin  = 30 * time.Minute // 首次失败退避
	iconAliasFailMax  = 6 * time.Hour    // 退避上限
	iconAliasMaxDirs  = 400              // 顶层目录枚举上限（防超大仓）
	iconAliasMaxBody  = 8 << 20          // flat 响应体上限（8MB）
	iconAliasAPIRoot  = "https://data.jsdelivr.com/v1/package/gh"
	iconAliasFetchTO  = 10 * time.Second // 列目录整体上限
	iconAliasMaxCands = 6                // 别名候选上限（main/master × 图标名）
)

// iconAliasIconNames 目录内被视作应用图标的文件名（按优先级；与
// localAppIconFrom 清单同源，只取最通用的若干，避免把预览图当图标）。
var iconAliasIconNames = []string{
	"ICON.PNG", "ICON.png", "icon.PNG", "icon.png",
	"ICON_256.PNG", "ICON_256.png", "icon_256.png", "icon-256.png",
	"icon_0_256.png", "icon_0.png", "icon-64.png", "icon_64.png",
}

// iconAliasEntry 单个仓库的目录清单缓存（落盘结构）。
type iconAliasEntry struct {
	OK        bool                `json:"ok"`                 // 成功列举过（含 0 目录）
	TopDirs   []string            `json:"top_dirs,omitempty"` // 顶层非隐藏目录（字典序）
	Icons     map[string][]string `json:"icons,omitempty"`    // 目录 → 该目录下图标相对路径（优先级序）
	Failures  int                 `json:"failures,omitempty"` // 连续失败次数（退避用）
	FetchedAt time.Time           `json:"fetched_at"`
}

// iconAliasLister 仓库目录列举器（并发安全；newIconAliasLister 构造）。
type iconAliasLister struct {
	mu      sync.Mutex
	path    string
	apiRoot string
	client  *http.Client
	repos   map[string]*iconAliasEntry
	// 0.6.326（审计 M-2）：同仓在飞请求合并表（key 同 repos）。
	inflight map[string]*iconAliasCall
}

// iconAliasCall 一次在飞的仓库列举（done 关闭即结果就绪，字段由 close 之前
// 在锁内写入 → 读侧 <-done 之后读无需再加锁）。
type iconAliasCall struct {
	done chan struct{}
	e    *iconAliasEntry
	ok   bool
}

// iconAliasClient 列目录专用客户端（12s 整体超时；调用方另有 ctx 封顶）。
var iconAliasClient = netx.NewClient(12 * time.Second)

// newIconAliasLister 构造并加载落盘缓存（损坏/缺失当空，绝不阻塞）。
func newIconAliasLister(path string) *iconAliasLister {
	l := &iconAliasLister{
		path:     path,
		apiRoot:  iconAliasAPIRoot,
		client:   iconAliasClient,
		repos:    map[string]*iconAliasEntry{},
		inflight: map[string]*iconAliasCall{},
	}
	l.load()
	return l
}

func (l *iconAliasLister) load() {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var wrap struct {
		Repos map[string]*iconAliasEntry `json:"repos"`
	}
	if json.Unmarshal(b, &wrap) != nil || wrap.Repos == nil {
		return
	}
	l.repos = wrap.Repos
}

// save 原子落盘（best-effort，失败静默：缓存只是加速手段）。
func (l *iconAliasLister) save() {
	if l.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(struct {
		Repos map[string]*iconAliasEntry `json:"repos"`
	}{l.repos})
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, l.path)
}

// fresh 缓存是否仍在有效期：成功条目 TTL 72h，失败条目按次数指数退避。
func (l *iconAliasLister) fresh(e *iconAliasEntry) bool {
	if e == nil {
		return false
	}
	if e.OK {
		return time.Since(e.FetchedAt) < iconAliasTTL
	}
	return time.Since(e.FetchedAt) < iconAliasBackoff(e.Failures)
}

// iconAliasBackoff 失败退避时长（30min × 2^(n-1)，上限 6h）。
func iconAliasBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := iconAliasFailMin
	for i := 1; i < failures; i++ {
		if d >= iconAliasFailMax {
			return iconAliasFailMax
		}
		d *= 2
	}
	if d > iconAliasFailMax {
		return iconAliasFailMax
	}
	return d
}

// dirs 取仓库目录清单；ok=false = 拿不到（网络/限流/解析失败）→ 静默降级用。
func (l *iconAliasLister) dirs(ctx context.Context, owner, repo string) (*iconAliasEntry, bool) {
	key := strings.ToLower(owner + "/" + repo)
	l.mu.Lock()
	if e := l.repos[key]; l.fresh(e) {
		l.mu.Unlock()
		return e, e.OK
	}
	// 0.6.326（审计 M-2）：同一仓库在飞时只发一次 jsDelivr 请求——一次列表里
	// 同仓多张卡（20~40 张）冷缓存下原本会各发一次，量级=卡数×请求数。
	// 等待侧 ctx 到期就静默降级（不阻塞本次图标请求）。
	if call := l.inflight[key]; call != nil {
		l.mu.Unlock()
		select {
		case <-call.done:
			return call.e, call.ok
		case <-ctx.Done():
			return nil, false
		}
	}
	if l.inflight == nil { // 老构造路径（单测零值构造）兜底
		l.inflight = map[string]*iconAliasCall{}
	}
	call := &iconAliasCall{done: make(chan struct{})}
	l.inflight[key] = call
	l.mu.Unlock()

	got, err := l.fetch(ctx, owner, repo)

	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		fails := 1
		if prev := l.repos[key]; prev != nil {
			fails = prev.Failures + 1
		}
		l.repos[key] = &iconAliasEntry{Failures: fails, FetchedAt: time.Now()}
		l.save()
	} else {
		got.FetchedAt = time.Now()
		l.repos[key] = got
		call.e, call.ok = got, true
		l.save()
	}
	delete(l.inflight, key)
	close(call.done)
	return call.e, call.ok
}

// fetch 列举仓库文件树：main 优先，失败再试 master（老仓库默认分支）。
func (l *iconAliasLister) fetch(ctx context.Context, owner, repo string) (*iconAliasEntry, error) {
	var last error
	for _, br := range []string{"main", "master"} {
		u := fmt.Sprintf("%s/%s/%s@%s/flat", l.apiRoot,
			url.PathEscape(owner), url.PathEscape(repo), br)
		files, err := l.fetchFlat(ctx, u)
		if err != nil {
			last = err
			continue
		}
		return buildIconAliasEntry(files), nil
	}
	if last == nil {
		last = fmt.Errorf("jsdelivr 无候选分支")
	}
	return nil, last
}

// fetchFlat 取一次 flat 清单（非 200 / 空清单 / 解析失败都算失败）。
func (l *iconAliasLister) fetchFlat(ctx context.Context, u string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jsdelivr %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, iconAliasMaxBody))
	if err != nil {
		return nil, err
	}
	var flat struct {
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &flat); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(flat.Files))
	for _, f := range flat.Files {
		if f.Name != "" {
			out = append(out, f.Name)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("jsdelivr 空清单")
	}
	return out, nil
}

// buildIconAliasEntry 由文件清单抽取顶层目录与各目录下的图标（纯函数，
// 单测直接喂 jsDelivr 真实形状的数据）。
func buildIconAliasEntry(files []string) *iconAliasEntry {
	e := &iconAliasEntry{OK: true, Icons: map[string][]string{}}
	seen := map[string]map[string]bool{} // 目录 → 该目录根下的文件名集合
	for _, f := range files {
		p := strings.Trim(f, "/")
		i := strings.IndexByte(p, '/')
		if i <= 0 {
			continue // 根级文件：主链路候选已覆盖
		}
		dir, rest := p[:i], p[i+1:]
		if strings.HasPrefix(dir, ".") {
			continue // .github / .gitignore 等隐藏项不算目录别名
		}
		names, ok := seen[dir]
		if !ok {
			if len(seen) >= iconAliasMaxDirs {
				continue
			}
			names = map[string]bool{}
			seen[dir] = names
		}
		if strings.IndexByte(rest, '/') >= 0 {
			continue // 子目录文件只证明顶层目录存在，不是该目录的图标
		}
		names[rest] = true
	}
	for dir, names := range seen {
		e.TopDirs = append(e.TopDirs, dir)
		var icons []string
		for _, wn := range iconAliasIconNames {
			if names[wn] {
				icons = append(icons, dir+"/"+wn)
			}
		}
		if len(icons) > 0 {
			e.Icons[dir] = icons
		}
	}
	sort.Strings(e.TopDirs)
	return e
}

// iconAliasNorm 归一化：小写 + 只保留 a-z0-9。
// "SmartDNS" → "smartdns"；"小松鼠" → ""（非 ASCII 全剔，故规则 a 需空值
// 守卫——空 want 绝不参与匹配，交由规则 b（唯一目录）兜底）。
func iconAliasNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// iconAliasRepo 从源地址解析 GitHub owner/repo（非 GitHub 系 → ok=false，
// 即别名回落只对 GitHub 系源生效）。
func iconAliasRepo(sourceBase string) (owner, repo string, ok bool) {
	if sourceBase == "" {
		return "", "", false
	}
	u, err := url.Parse(sourceBase)
	if err != nil || u.Host == "" {
		if !strings.Contains(sourceBase, "github.com/") {
			return "", "", false
		}
		if u, err = url.Parse("https://" + sourceBase); err != nil || u == nil {
			return "", "", false
		}
	}
	if !strings.HasSuffix(u.Host, "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	repo = strings.TrimSuffix(parts[1], ".git")
	if parts[0] == "" || repo == "" {
		return "", "", false
	}
	return parts[0], repo, true
}

// aliasDir 按优先级挑别名目录；无匹配、或命中目录下没有图标文件 → 空串。
//
//	规则 a：归一化相等；
//	规则 b：仓库顶层只有 1 个非隐藏目录 **且 appname 无任何 ASCII 字母数字**
//	        （即归一化后为空，规则 a 天然无法适用）→ 用该唯一目录。
//
// 规则 b 的额外门槛来自实测副作用（0.6.326d）：kalisgp-cmyk/FnDepot 顶层恰好
// 只有 1 个目录 fntermx，而应用 lobechat 的包在仓库根（release_url 指向
// .../main/lobechat/lobechat.fpk）——字面照搬「唯一目录即别名」会把 fntermx
// 的图标配给 lobechat（错图标，比空白更糟）。appname 有 ASCII 字母数字时，
// 归一化比较才是可靠判据；比较不上就说明仓库布局与 appname 对不上，宁可不修。
// 纯中文名（如「小松鼠」）归一化为空，只能靠唯一目录兜底，这类才有规则 b。
func aliasDir(e *iconAliasEntry, appName string) string {
	if e == nil || !e.OK {
		return ""
	}
	want := iconAliasNorm(appName)
	if want != "" {
		var hits []string
		for _, d := range e.TopDirs {
			if iconAliasNorm(d) == want && len(e.Icons[d]) > 0 {
				hits = append(hits, d)
			}
		}
		if len(hits) > 0 {
			// 多个目录归一化同名（如 SmartDNS / smart_dns）：精确同名优先，
			// 其次字典序（确定性，不受列举顺序影响）
			sort.Slice(hits, func(i, j int) bool {
				if (hits[i] == appName) != (hits[j] == appName) {
					return hits[i] == appName
				}
				return hits[i] < hits[j]
			})
			return hits[0]
		}
		return ""
	}
	if len(e.TopDirs) == 1 && len(e.Icons[e.TopDirs[0]]) > 0 {
		return e.TopDirs[0]
	}
	return ""
}

// iconAliasRawBase 别名候选的 raw 根（生产恒为 GitHub raw；单测指向本地
// httptest 以打通「命中→取图→返回」整条路径）。
var iconAliasRawBase = "https://raw.githubusercontent.com"

// aliasIconCandidates 由别名目录 + 仓库实际图标文件名生成 raw 直链候选
// （非 ASCII 路径经 url.URL 转义；分支由调用方给 main/master）。
func aliasIconCandidates(owner, repo, branch, dir string, icons []string) []string {
	base, err := url.Parse(iconAliasRawBase)
	if err != nil || base.Host == "" {
		base = &url.URL{Scheme: "https", Host: "raw.githubusercontent.com"}
	}
	out := make([]string, 0, len(icons))
	for _, ic := range icons {
		u := url.URL{
			Scheme: base.Scheme,
			Host:   base.Host,
			Path:   "/" + owner + "/" + repo + "/" + branch + "/" + ic,
		}
		out = append(out, u.String())
	}
	return out
}

// raceAliasCandidates 别名候选竞速：与 resolveIcon 主链路同规则（镜像前缀
// 变体 + jsDelivr 变体、占位图黑名单、直链/镜像交叉验证）。命中即返回；
// 明确的 404/403/410 路径记入 defMissPaths 供调用方判定负缓存。
func (s *Server) raceAliasCandidates(ctx context.Context, cands []string, defMissPaths map[string]bool) ([]byte, string, bool) {
	cands = dedupeMirrors(append(cands, s.mirrorVariants(cands, 2)...))
	if len(cands) == 0 {
		return nil, "", false
	}
	var prefixes []string
	for _, opt := range config.GitHubMirrorOptions() {
		if p := strings.TrimRight(opt.URL, "/"); p != "" {
			prefixes = append(prefixes, p+"/")
		}
	}
	strip := func(u string) string {
		for _, p := range prefixes {
			if strings.HasPrefix(u, p) {
				return u[len(p):]
			}
		}
		return u
	}
	type res struct {
		u    string
		data []byte
		ct   string
		ok   bool
		miss bool
	}
	ch := make(chan res, len(cands))
	for _, u := range cands {
		go func(raw string) {
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			data, ct, ok, miss := fetchIconBytes(cctx, raw)
			cancel()
			ch <- res{u: raw, data: data, ct: ct, ok: ok, miss: miss}
		}(u)
	}
	var pending []res
	deadline := time.After(8 * time.Second)
	for n := 0; n < len(cands); n++ {
		select {
		case r := <-ch:
			if r.miss {
				defMissPaths[strip(r.u)] = true
				continue
			}
			if !r.ok {
				continue
			}
			if ghPlaceholderIconHash(r.data) {
				// 镜像/上游给的 404 占位图：拒收并视同该路径 404
				defMissPaths[strip(r.u)] = true
				continue
			}
			if strip(r.u) == r.u {
				return r.data, r.ct, true // 直链命中
			}
			if !defMissPaths[strip(r.u)] {
				pending = append(pending, r) // 镜像 200：待直链交叉验证
			}
		case <-ctx.Done():
			n = len(cands)
		case <-deadline:
			n = len(cands)
		}
	}
	for _, r := range pending {
		if !defMissPaths[strip(r.u)] {
			return r.data, r.ct, true
		}
	}
	return nil, "", false
}

// resolveIconAlias 目录名别名回落（图标链路最后一道，仅 GitHub 系源）。
// 任何失败——非 GitHub 源 / 列举失败（网络/限流/超限）/ 无匹配 / 取图全败
// ——一律 ok=false 静默降级，调用方维持原 404 行为。
func (s *Server) resolveIconAlias(ctx context.Context, a *source.App, defMissPaths map[string]bool) ([]byte, string, bool) {
	if a == nil {
		return nil, "", false
	}
	owner, repo, ok := iconAliasRepo(s.sourceBaseURL(a.Source))
	if !ok {
		return nil, "", false
	}
	l := s.iconAliasLister()
	if l == nil {
		return nil, "", false
	}
	actx, cancel := context.WithTimeout(ctx, iconAliasFetchTO+2*time.Second)
	defer cancel()
	e, ok := l.dirs(actx, owner, repo)
	if !ok || e == nil {
		return nil, "", false
	}
	dir := aliasDir(e, a.Name)
	if dir == "" {
		return nil, "", false
	}
	var cands []string
	for _, br := range []string{"main", "master"} {
		cands = append(cands, aliasIconCandidates(owner, repo, br, dir, e.Icons[dir])...)
	}
	if len(cands) > iconAliasMaxCands {
		cands = cands[:iconAliasMaxCands]
	}
	data, ct, ok := s.raceAliasCandidates(ctx, cands, defMissPaths)
	if !ok {
		return nil, "", false
	}
	small, ct2, _ := downsampleIcon(data, ct)
	return small, ct2, true
}

var (
	iconAliasOnce sync.Once
	iconAliasInst *iconAliasLister
)

// iconAliasTestHook 单测注入（非 nil 时替代全局单例；生产恒 nil）。
var iconAliasTestHook *iconAliasLister

// iconAliasListerFor 进程级单例（落盘 <dataDir>/cache/icon_alias.json，
// 重启不丢；懒加载只读一次）。
func iconAliasListerFor(dir string) *iconAliasLister {
	iconAliasOnce.Do(func() {
		iconAliasInst = newIconAliasLister(filepath.Join(dir, "cache", "icon_alias.json"))
	})
	return iconAliasInst
}

func (s *Server) iconAliasLister() *iconAliasLister {
	if iconAliasTestHook != nil {
		return iconAliasTestHook
	}
	return iconAliasListerFor(dataDirOf(s))
}
