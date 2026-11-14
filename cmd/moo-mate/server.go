package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moo/internal/secret"
	"moo/internal/source"
)

// mateSourceName 统一聚合源名：伴侣只向 Moo 加这一条源，索引里所有
// synced 应用都收敛进这条源（moo.json 单源多应用，与 Moo 原生模型一致）。
const mateSourceName = "moo-mate"

// companion 服务主体：持有持久化索引 + 托管中的单应用 moo.json + 聚合 moo.json + Moo 客户端。
type companion struct {
	mu        sync.RWMutex
	idx       *index            // 持久化应用索引
	hosted    map[string][]byte // appname → 单应用 moo.json（托管内容）
	aggregate []byte            // 聚合 moo.json（全部 synced 应用，统一源托管内容）

	moo     *mooClient
	base    string // 伴侣自身对外地址（用于加源 url）
	httpc   *http.Client
	token   string         // GitHub token 兜底（-token / MOO_MATE_TOKEN，仅调试用）
	cfgPath string         // 配置文件路径（config.json）
	bootDir string         // 引导目录（datadir 指针文件所在，默认 = 初始数据目录）
	cfg     *mateConfig    // 配置（不含 token：0.2.28 起复用 Moo 的那一份）
	sealer  *secret.Sealer // 敏感字段加解密器（保留：旧配置兼容/后续敏感项）
	webFS   http.Handler

	// GitHub token 缓存：向 Moo 取（Moo 未配置时回落 token 兜底）。
	// 2 分钟 TTL，避免每个 GitHub 请求都打一次 Moo。
	tokMu    sync.Mutex
	tokCache string
	tokSrc   string // "moo" | "flag" | ""
	tokAt    time.Time

	// 0.2.35：GitHub 加速镜像表（向 Moo 取 /api/mate/mirrors，10 分钟缓存）。
	// 自更新下载按它展开候选，避免伴侣侧再维护第二份镜像列表。
	mirrorMu    sync.Mutex
	mirrorCache []string
	mirrorAt    time.Time

	syncWake    chan struct{} // 同步循环唤醒信号（配置变更/手动触发）
	syncStateMu sync.Mutex
	lastSync    time.Time // 上次同步时间
	syncErr     string    // 上次同步错误（空 = 成功）

	// 0.2.63（B8）：整轮同步互斥。定时同步与「同步更新」按钮此前可以并行跑，
	// 两轮各自持有代次并写 upsert / applyCheckResults，后写者会把前者刚写的
	// 红点与检查状态冲掉。TryLock 失败 = 已有一轮在跑，直接跳过本轮。
	syncMu   sync.Mutex
	syncBusy atomic.Bool

	iconMu sync.RWMutex
	icons  map[string][]byte // 应用图标内存缓存（appname → 图片字节；经本机代理下载，避免浏览器直连失败）
}

// newCompanion 构造伴侣（cfg/sealer 由 main 初始化后经 setConfig 注入，可为空）。
func newCompanion(mooBase, selfBase, token string, webFS http.Handler, idx *index) *companion {
	return &companion{
		idx:      idx,
		hosted:   map[string][]byte{},
		moo:      newMooClient(mooBase),
		base:     strings.TrimRight(selfBase, "/"),
		httpc:    mateExternalClient(60 * time.Second),
		token:    token,
		cfg:      &mateConfig{},
		webFS:    webFS,
		syncWake: make(chan struct{}, 1),
		icons:    map[string][]byte{},
	}
}

// setConfig 注入配置与加解密器（main 启动时调用）。
func (c *companion) setConfig(cfgPath string, cfg *mateConfig, sealer *secret.Sealer) {
	c.cfgPath = cfgPath
	if cfg != nil {
		c.cfg = cfg
	}
	c.sealer = sealer
}

// githubToken 返回当前生效的 GitHub token。
//
// 0.2.28 起唯一正式来源 = Moo 设置里配置的 token（GET /api/mate/github-token），
// 伴侣侧不再单独保存凭据；Moo 未配置或不可达时回落 -token / MOO_MATE_TOKEN
// （仅供本地调试）。结果缓存 2 分钟，避免每个 GitHub 请求都打一次 Moo。
func (c *companion) githubToken() string {
	tok, _, _ := c.githubTokenWithSource()
	return tok
}

// githubTokenWithSource 额外返回来源（"moo" / "flag" / ""），供设置页展示。
func (c *companion) githubTokenWithSource() (string, string, error) {
	const ttl = 2 * time.Minute
	c.tokMu.Lock()
	if time.Since(c.tokAt) < ttl {
		tok, src := c.tokCache, c.tokSrc
		c.tokMu.Unlock()
		return tok, src, nil
	}
	c.tokMu.Unlock()

	var tok, src string
	var err error
	if c.moo != nil {
		var data []byte
		var code int
		data, code, err = c.moo.doTimeout(http.MethodGet, "/api/mate/github-token", nil, 3*time.Second)
		if err == nil && code == http.StatusOK {
			var out struct {
				Set   bool   `json:"github_token_set"`
				Token string `json:"github_token"`
			}
			if json.Unmarshal(data, &out) == nil && out.Set {
				tok, src = strings.TrimSpace(out.Token), "moo"
			}
		}
	}
	if tok == "" && c.token != "" {
		tok, src = c.token, "flag"
	}
	c.tokMu.Lock()
	c.tokCache, c.tokSrc, c.tokAt = tok, src, time.Now()
	c.tokMu.Unlock()
	return tok, src, err
}

// mateMirrorPrefixes 取 Moo 侧健康 GitHub 加速前缀（10 分钟缓存）。
//
// 0.2.35（用户令「moo 的 github 加速给 mate 复用」）：镜像表只在 Moo 维护
// 一份（GET /api/mate/mirrors，requireAdmin + X-Moo-Mate），Mate 跟着 Moo
// 的健康度走。Moo 不可达/未配置 → 返回 nil，调用方回落 githubMirrorPrefixes。
// 空结果同样缓存，避免 Moo 挂掉时每个下载都先等 3s 超时。
func (c *companion) mateMirrorPrefixes() []string {
	const ttl = 10 * time.Minute
	c.mirrorMu.Lock()
	if time.Since(c.mirrorAt) < ttl {
		out := c.mirrorCache
		c.mirrorMu.Unlock()
		return out
	}
	c.mirrorMu.Unlock()

	var out []string
	if c.moo != nil {
		data, code, err := c.moo.doTimeout(http.MethodGet, "/api/mate/mirrors", nil, 3*time.Second)
		if err == nil && code == http.StatusOK {
			var resp struct {
				Prefixes []string `json:"prefixes"`
			}
			if json.Unmarshal(data, &resp) == nil {
				for _, p := range resp.Prefixes {
					if p = strings.TrimSpace(p); p != "" {
						out = append(out, p)
					}
				}
			}
		}
	}
	c.mirrorMu.Lock()
	c.mirrorCache, c.mirrorAt = out, time.Now()
	c.mirrorMu.Unlock()
	return out
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// handleProbe POST /api/probe：探测仓库是否已发布索引，命中则与 Moo 现有源比对去重。
func (c *companion) handleProbe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	raw := strings.TrimSpace(body.URL)
	name := probeIndex(raw)
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"hit": false, "index": "",
			"message": "未发现已发布的索引，将进入发现 + 生成流程",
		})
		return
	}
	// 命中协议 → 与 Moo 现有源比对（归一化 owner/repo），判断是否重复。
	dup := false
	dupName := ""
	if c.moo.ping() == nil {
		if srcs, err := c.moo.listSources(); err == nil {
			if s := findDuplicateSource(raw, srcs); s != nil {
				dup = true
				dupName = s.Name
			}
		}
	}
	if dup {
		writeJSON(w, http.StatusOK, map[string]any{
			"hit": true, "index": name, "duplicate": true, "dup_source": dupName,
			"message": fmt.Sprintf("该仓库已发布 %s 索引，且已在 Moo 源列表（源「%s」）", name, dupName),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hit": true, "index": name, "duplicate": false,
		"message": fmt.Sprintf("该仓库已发布 %s 索引，可加入 Moo 应用源列表", name),
	})
}

// handleDiscover POST /api/discover：扫描仓库发现 .fpk 应用。
func (c *companion) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	ctx, cancel := timeoutCtx(r.Context(), 60*time.Second)
	defer cancel()
	apps, err := discoverFromRepo(ctx, strings.TrimSpace(body.URL), c.githubToken(), c.httpc)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(apps),
		"apps":  apps,
	})
}

// buildAcc 发现阶段的累积结果：appname → 扁平 primary + 各架构包 + 最高版本。
type buildAcc struct {
	ia      *indexedApp
	version string
}

// accProgress 解析阶段进度/结果回调（nil 安全）。
// Start 在开始下载第 i 个包时触发；Done 在解析出结果（成功或跳过）后触发，
// 让任务弹窗能逐条打印「文件名 → appname 版本 架构」。
type accProgress struct {
	Start func(done, total int, name string)
	Done  func(name, appname, version, arch string, err error)
}

func (p *accProgress) start(done, total int, name string) {
	if p != nil && p.Start != nil {
		p.Start(done, total, name)
	}
}

func (p *accProgress) done(name, appname, version, arch string, err error) {
	if p != nil && p.Done != nil {
		p.Done(name, appname, version, arch, err)
	}
}

// discoverAccs 从 discovered 列表收集「appname → 累积条目」（版本合并 + 架构合并）。
// 同名多架构 FPK（如 motrix 的 x86/arm 两包）同版本合并进 packages；primary 指向 x86。
// p 为 nil 时等同原行为（不推送进度）。
func (c *companion) discoverAccs(ctx context.Context, discovered []discoveredApp, raw, owner string, p *accProgress) (map[string]*buildAcc, []string) {
	accs := map[string]*buildAcc{}
	var skipped []string
	total := len(discovered)
	for i, d := range discovered {
		if err := ctx.Err(); err != nil {
			skipped = append(skipped, "任务已取消，剩余未解析")
			break
		}
		p.start(i+1, total, d.Name)
		meta, err := c.downloadAndInspect(ctx, d.DownloadURL)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", d.Name, err))
			p.done(d.Name, "", "", "", err)
			continue
		}
		appname := strings.TrimSpace(meta.Manifest["appname"])
		if appname == "" {
			err := fmt.Errorf("manifest 缺少 appname")
			skipped = append(skipped, d.Name+": "+err.Error())
			p.done(d.Name, "", "", "", err)
			continue
		}
		// 0.2.38：构建索引时顺手把包内图标落盘（零额外下载；供 /api/icon 出图）。
		if len(meta.Icon) > 0 {
			c.saveIconFile(appname, meta.Icon, meta.IconExt)
		}
		e := metaToEntry(meta, d.DownloadURL, "")
		if e.Version == "" {
			e.Version = d.Version
		}
		arch := archForFPK(meta.Manifest, d.Name)
		acc := accs[appname]
		if acc == nil {
			acc = &buildAcc{ia: &indexedApp{appEntry: *e, Appname: appname, Source: raw, Publisher: owner}, version: e.Version}
			accs[appname] = acc
		}
		if e.Version != "" && acc.version != "" && versionGreater(e.Version, acc.version) {
			// 更高版本：重置 primary 并清空 packages（旧版本架构丢弃）
			acc.ia.appEntry = *e
			acc.version = e.Version
			acc.ia.Packages = nil
		} else if e.Version != "" && acc.version != "" && e.Version != acc.version {
			err := fmt.Errorf("低于最高版 %s", acc.version)
			skipped = append(skipped, fmt.Sprintf("%s@%s: %v", appname, e.Version, err))
			p.done(d.Name, appname, e.Version, arch, err)
			continue
		}
		if arch == "" {
			err := fmt.Errorf("无法识别架构（%s）", d.Name)
			skipped = append(skipped, appname+": "+err.Error())
			p.done(d.Name, appname, e.Version, "", err)
			continue
		}
		if acc.ia.Packages == nil {
			acc.ia.Packages = map[string]*appPackage{}
		}
		acc.ia.Packages[arch] = &appPackage{DownloadURL: d.DownloadURL, Sha256: e.Sha256, SizeBytes: e.SizeBytes}
		p.done(d.Name, appname, e.Version, arch, nil)
	}
	// 收尾：扁平 primary 指向 x86（Moo 0.6.352 起按本机架构覆盖选包；x86 为约定默认）。
	for _, acc := range accs {
		if p, ok := acc.ia.Packages["x86"]; ok {
			acc.ia.appEntry.DownloadURL = p.DownloadURL
			acc.ia.appEntry.Sha256 = p.Sha256
			acc.ia.appEntry.Size = round2(float64(p.SizeBytes) / (1024 * 1024))
			acc.ia.appEntry.SizeBytes = p.SizeBytes
		}
		if len(acc.ia.Packages) > 0 {
			acc.ia.appEntry.Platform = archKeys(acc.ia.Packages)
		}
	}
	return accs, skipped
}

// buildRequest 是 handleBuild 的请求体。
type buildRequest struct {
	URL         string `json:"url"`
	Name        string `json:"name"`
	Distributor string `json:"distributor"`
	Homepage    string `json:"homepage"`
}

// handleBuild POST /api/build：发现 .fpk 应用、解析 manifest，落索引（不立即加源）。
// buildHTTPErr 携带非流式路径原有的 HTTP 状态码（502 探测失败 / 404 无 .fpk）。
type buildHTTPErr struct {
	code int
	msg  string
}

func (e *buildHTTPErr) Error() string { return e.msg }

// sseStream 把响应升级为 text/event-stream。nil 接收者表示「非流式」，事件静默丢弃，
// 这样 runBuild 里可以无条件 s.send(...)，不必到处判分支。
type sseStream struct {
	mu   sync.Mutex
	w    http.ResponseWriter
	f    http.Flusher
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// startSSE 写 SSE 响应头并立即 flush，让客户端马上进入「已连接」态。
// 同时启动 15s 心跳：反向代理按空闲超时切断长任务（解析几十个包时容易触发）。
func startSSE(w http.ResponseWriter) *sseStream {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseStream{w: w, done: make(chan struct{})}
	if f, ok := w.(http.Flusher); ok {
		s.f = f
		f.Flush()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.heartbeat()
	}()
	return s
}

// heartbeat 定时发 ping，直到 close。
func (s *sseStream) heartbeat() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.send(map[string]any{"phase": "ping", "t": time.Now().Unix()})
		}
	}
}

// close 停心跳并置空 writer（幂等；handler 收尾必须调用）。
func (s *sseStream) close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
		s.wg.Wait()
		s.mu.Lock()
		s.w = nil
		s.mu.Unlock()
	})
}

// send 输出一条 data: {json}\n\n 事件并 flush（持锁，兼容心跳并发写）。
func (s *sseStream) send(ev map[string]any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return
	}
	if s.f != nil {
		s.f.Flush()
	}
}

// logf 发一条短日志事件（nil 接收者安全，非流式路径可无脑调用）。
func (s *sseStream) logf(level, msg string) {
	if s == nil {
		return
	}
	s.send(map[string]any{
		"phase": "log", "level": level, "msg": msg,
		"t": time.Now().Format("15:04:05"),
	})
}

// handleSyncNow POST /api/sync-now：手动「同步更新」入口，立即唤醒同步循环。
// handleSyncNow POST /api/sync-now：**双重同步**（0.2.62，用户令合并成一个按钮）。
//
//	① 补远端默认应用清单：只把索引里没有的条目加进来，已有条目一律不动；
//	② 唤醒上游版本检查：逐个应用看仓库作者是否发了新版本（异步，前端轮询状态看结果）。
//
// 响应里带 ① 的结果，前端可提示「补充 N 个默认应用」。
func (c *companion) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	remote := ""
	if c.cfg != nil {
		remote = c.cfg.defaultIndexURL()
	}
	added, src := 0, ""
	var derr error
	if c.idx != nil { // 测试里可能只构造了最小 companion
		added, src, derr = c.idx.mergeDefaultList(remote, c.httpc)
	}
	c.wakeSync()
	resp := map[string]any{"ok": true, "default_added": added, "default_source": src,
		// 0.2.63：让前端知道「已有一轮在跑」，不要傻等 last_sync 变化（B4 的一半）。
		"running": c.syncBusy.Load()}
	if derr != nil {
		resp["default_error"] = derr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (c *companion) handleBuild(w http.ResponseWriter, r *http.Request) {
	var body buildRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	raw := strings.TrimSpace(body.URL)
	owner := repoOwner(raw)
	// stream=1：SSE 推送进度（前端任务弹窗用）；不带时走原 JSON 路径，行为与结构完全不变。
	var s *sseStream
	if r.URL.Query().Get("stream") == "1" {
		s = startSSE(w)
		defer s.close()
	}
	// 45 分钟总预算：大仓库「release + 仓库文件」双通道扫描 + 逐个下载解析可能远超 5 分钟；
	// 单次下载另有 4 分钟上限（见 tryDownload），不会挂死在单个镜像上。
	// 长任务靠 SSE 15s 心跳保活（见 startSSE）。
	ctx, cancel := timeoutCtx(r.Context(), 45*time.Minute)
	defer cancel()
	added, skipped, err := c.runBuild(ctx, s, raw, owner, func(ctx context.Context) ([]discoveredApp, error) {
		return discoverFromRepoLog(ctx, raw, c.githubToken(), c.httpc, s.logf)
	})
	// 0.2.61b：用户主动扫描且确实新增了应用 = 重新开始用索引，此时才清「主动清除」标记，
	// 让后续「空索引补默认清单」重新生效。自动同步不经过这里，不会再误清用户标记。
	// 0.2.62：同名应用被重新加回来时，同时撤销「用户删过」的排除记录。
	if err == nil && len(added) > 0 {
		c.idx.clearUserClearedFlag()
		for _, name := range added {
			_ = c.idx.unmarkRemovedDefault(name)
		}
	}
	respondBuild(w, s, added, skipped, err)
}

// respondBuild 输出 build 结果：流式走 done / error 事件，非流式走原 JSON（契约不变）。
func respondBuild(w http.ResponseWriter, s *sseStream, added, skipped []string, err error) {
	if err != nil {
		if s != nil {
			// 流已开（200 已写出），错误只能以事件形式下发，避免半截 JSON。
			s.send(map[string]any{"phase": "error", "msg": err.Error()})
			return
		}
		code := http.StatusBadGateway
		if be, ok := err.(*buildHTTPErr); ok {
			code = be.code
		}
		writeErr(w, code, err.Error())
		return
	}
	if s != nil {
		// skipped 可能上百条：SSE 只带前 30 条 + 总数，避免事件过大拖慢前端渲染。
		lim := skipped
		if len(lim) > 30 {
			lim = lim[:30]
		}
		s.send(map[string]any{"phase": "done", "count": len(added), "added": added,
			"skipped": lim, "skipped_count": len(skipped)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"added":   added,
		"count":   len(added),
		"skipped": skipped,
	})
}

// runBuild 执行「扫描 → 解析 → 落索引」；s 非 nil 时逐条推送进度事件。
// discover 参数可注入（测试用假实现，不触网、不真下载）。
func (c *companion) runBuild(ctx context.Context, s *sseStream, raw, owner string,
	discover func(context.Context) ([]discoveredApp, error)) ([]string, []string, error) {

	s.logf("info", "开始扫描："+raw)
	s.send(map[string]any{"phase": "discover", "msg": "扫描 release 与仓库文件…"})
	discovered, err := discover(ctx)
	if err != nil {
		return nil, nil, &buildHTTPErr{http.StatusBadGateway, err.Error()}
	}
	if len(discovered) == 0 {
		return nil, nil, &buildHTTPErr{http.StatusNotFound, "仓库 release 与仓库文件里都没有 .fpk，无法生成"}
	}
	s.send(map[string]any{"phase": "discover", "done": len(discovered),
		"msg": fmt.Sprintf("发现 %d 个候选 .fpk", len(discovered))})
	s.logf("ok", fmt.Sprintf("共 %d 个候选 .fpk，开始逐个下载解析", len(discovered)))
	accs, skipped := c.discoverAccs(ctx, discovered, raw, owner, &accProgress{
		Start: func(done, total int, name string) {
			s.send(map[string]any{"phase": "parse", "done": done, "total": total, "app": name})
			s.logf("info", fmt.Sprintf("解析 %d/%d：%s", done, total, name))
		},
		Done: func(name, appname, version, arch string, err error) {
			if err != nil {
				s.logf("warn", fmt.Sprintf("%s 跳过：%v", name, err))
				return
			}
			s.logf("ok", fmt.Sprintf("%s → %s %s（%s）", name, appname, version, arch))
		},
	})
	total := len(accs)
	seq := 0
	var added []string
	for appname, acc := range accs {
		seq++
		s.send(map[string]any{"phase": "index", "done": seq, "total": total})
		if prev := c.idx.get(appname); prev != nil {
			if acc.version == "" || prev.Version == "" {
				skipped = append(skipped, appname+": 版本不可比，跳过")
				s.logf("warn", appname+"：版本不可比，跳过")
				continue
			}
			switch {
			case versionGreater(acc.version, prev.Version):
				// 更高版本：覆盖（upsert 保留 synced 状态）
				s.logf("ok", fmt.Sprintf("覆盖 %s：%s → %s", appname, prev.Version, acc.version))
			case acc.version == prev.Version:
				// 同版本：合并架构（补 prev 缺的 arch，幂等重跑可补全多架构）
				if len(prev.Packages) == 0 {
					prev.Packages = acc.ia.Packages
					prev.appEntry.Platform = acc.ia.appEntry.Platform
					if p, ok := acc.ia.Packages["x86"]; ok {
						prev.appEntry.DownloadURL = p.DownloadURL
						prev.appEntry.Sha256 = p.Sha256
						prev.appEntry.Size = acc.ia.appEntry.Size
						prev.appEntry.SizeBytes = p.SizeBytes
					}
				} else {
					if prev.Packages == nil {
						prev.Packages = map[string]*appPackage{}
					}
					for arch, p := range acc.ia.Packages {
						prev.Packages[arch] = p
					}
					prev.appEntry.Platform = archKeys(prev.Packages)
				}
				if err := c.idx.upsert(prev); err != nil {
					skipped = append(skipped, appname+": 合并架构落索引失败 "+err.Error())
					s.logf("err", appname+"：合并架构落索引失败 "+err.Error())
					continue
				}
				added = append(added, appname)
				s.logf("ok", fmt.Sprintf("合并架构 %s %s（%s）", appname, acc.version, strings.Join(archKeys(prev.Packages), "/")))
				continue
			default:
				skipped = append(skipped, appname+": 索引已有更高版本，跳过")
				s.logf("warn", fmt.Sprintf("%s：索引已有更高版本 %s，跳过", appname, prev.Version))
				continue
			}
		}
		if err := c.idx.upsert(acc.ia); err != nil {
			skipped = append(skipped, appname+": 落索引失败 "+err.Error())
			s.logf("err", appname+"：落索引失败 "+err.Error())
			continue
		}
		added = append(added, appname)
		s.logf("ok", fmt.Sprintf("新增 %s %s（%s）", appname, acc.version, strings.Join(archKeys(acc.ia.Packages), "/")))
	}
	s.logf("ok", fmt.Sprintf("索引完成：写入 %d 个应用，跳过 %d 条", len(added), len(skipped)))
	return added, skipped, nil
}

// downloadAndInspect 下载 FPK 并解析 manifest。GitHub release 资产并发竞速
// （镜像前缀 + 直连），首个 200 且可解析的候选胜出。
//
// 加固：候选按 120ms 阶梯启动（避免 16 路并发抢同一份带宽）；首个成功即 cancel
// 其余下载（否则败者会继续把整个包读完，白耗带宽/流量）；单候选 4 分钟上限。
func (c *companion) downloadAndInspect(ctx context.Context, u string) (*fpkMeta, error) {
	cands := mirrorCandidates(u)
	type result struct {
		meta *fpkMeta
		err  error
	}
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan result, len(cands))
	for i, cand := range cands {
		go func(cand string, delay time.Duration) {
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-raceCtx.Done():
					ch <- result{nil, raceCtx.Err()}
					return
				}
			}
			meta, err := c.tryDownload(raceCtx, cand)
			ch <- result{meta, err}
		}(cand, time.Duration(i)*120*time.Millisecond)
	}
	var lastErr error
	for range cands {
		r := <-ch
		if r.err == nil {
			cancel()
			return r.meta, nil
		}
		lastErr = r.err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("无可用候选")
	}
	return nil, lastErr
}

func (c *companion) tryDownload(ctx context.Context, u string) (*fpkMeta, error) {
	// 单候选 4 分钟：慢镜像不能吃掉整个任务预算（总预算 30 分钟见 handleBuild）。
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	tok := c.githubToken()
	if tok != "" && strings.Contains(u, "api.github.com") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 HTTP %d", resp.StatusCode)
	}
	return inspectFPK(io.LimitReader(resp.Body, 1<<30))
}

// handleAddDirect POST /api/add-direct：命中协议且不重复时，把仓库地址直接加为 Moo 源。
func (c *companion) handleAddDirect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.URL) == "" {
		writeErr(w, http.StatusBadRequest, "url 不能为空")
		return
	}
	raw := strings.TrimSpace(body.URL)
	if err := c.moo.ping(); err != nil {
		writeErr(w, http.StatusBadGateway, "本机 Moo 不可达："+err.Error())
		return
	}
	name := hostFromURL(raw)
	if o, r, ok := parseGHRepo(raw); ok {
		name = o + "/" + r
	}
	src, err := c.moo.addSource(name, raw)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	synced, serr := c.moo.syncSource(src.ID)
	if serr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"added": true, "source": src,
			"sync_error": serr.Error(),
			"message":    "已加源，但同步失败（应用可能稍后随周期刷新出现）",
		})
		return
	}
	msg := "已加源并同步"
	if synced.AppCount == 0 {
		msg = "已加源，但该源收录应用数为 0"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added": true, "source": synced, "message": msg,
	})
}

// handleSyncApp POST /api/sync-app {appname}：把应用推送进统一聚合源（标记 synced，
// 重建聚合 moo.json，确保统一源存在并 sync）。
func (c *companion) handleSyncApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appname string `json:"appname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Appname) == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	appname := strings.TrimSpace(body.Appname)
	ia := c.idx.get(appname)
	if ia == nil {
		writeErr(w, http.StatusNotFound, "索引里没有该应用："+appname)
		return
	}
	if err := c.moo.ping(); err != nil {
		writeErr(w, http.StatusBadGateway, "本机 Moo 不可达："+err.Error())
		return
	}
	if err := c.idx.setSynced(appname, true, mateSourceName); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	src, err := c.ensureMateSource()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	synced, serr := c.moo.syncSource(src.ID)
	if serr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"synced": true, "source": src,
			"sync_error": serr.Error(),
			"message":    "已推送，但源同步失败（应用可能稍后随周期刷新出现）",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"synced": true, "source": synced,
		"message": fmt.Sprintf("已推送「%s」到 Moo Mate 源（app_count=%d）", appname, synced.AppCount),
	})
}

// handleUnsyncApp POST /api/unsync-app {appname}：从统一聚合源撤回应用（标记未同步，
// 重建聚合 moo.json；还有应用则 sync 统一源，空则删统一源）。
func (c *companion) handleUnsyncApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appname string `json:"appname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Appname) == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	appname := strings.TrimSpace(body.Appname)
	if c.idx.get(appname) == nil {
		writeErr(w, http.StatusNotFound, "索引里没有该应用："+appname)
		return
	}
	if err := c.moo.ping(); err != nil {
		writeErr(w, http.StatusBadGateway, "本机 Moo 不可达："+err.Error())
		return
	}
	if err := c.idx.setSynced(appname, false, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 统一源还有应用 → sync；空 → 删源。
	if len(c.aggregateApps()) == 0 {
		if src, err := c.findMateSource(); err == nil && src != nil {
			_ = c.moo.deleteSource(src.ID)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"unsynced": true, "message": fmt.Sprintf("已撤回「%s」，聚合源已清空并移除", appname),
		})
		return
	}
	if src, err := c.findMateSource(); err == nil && src != nil {
		// 0.2.62：撤回只更新已存在的源，不因撤回动作重建被用户删掉的源。
		_, _ = c.moo.syncSource(src.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"unsynced": true, "message": fmt.Sprintf("已从聚合源撤回「%s」", appname),
	})
}

// handleDeleteApp DELETE /api/index/{appname}：从索引删除应用；若已同步，重建聚合 moo.json 并 sync。
func (c *companion) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	appname := strings.TrimSpace(r.PathValue("appname"))
	if appname == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	if c.idx.get(appname) == nil {
		writeErr(w, http.StatusNotFound, "索引里没有该应用："+appname)
		return
	}
	if err := c.idx.delete(appname); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 记下「用户主动删过」：后续补默认清单不得把这条又加回来（删了就是删了）。
	_ = c.idx.markRemovedDefault(appname)
	c.mu.Lock()
	delete(c.hosted, appname)
	c.mu.Unlock()
	// 0.2.62：顺手清掉本机残留（图标缓存 / 图标失败负缓存），
	// 否则「删了再加回来」仍会命中旧缓存或负缓存，看起来像是没生效。
	purged, perr := c.purgeAppArtifacts(appname)
	if perr != nil {
		logf("清理「%s」残留失败（忽略）：%v", appname, perr)
	}
	// 联动聚合源：删的是 synced 应用则重建并 sync（空则删源）。
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c.moo.ping() == nil {
		if len(c.aggregateApps()) == 0 {
			if src, err := c.findMateSource(); err == nil && src != nil {
				_ = c.moo.deleteSource(src.ID)
			}
		} else if src, err := c.findMateSource(); err == nil && src != nil {
			// 0.2.62：删除应用只更新「已存在的」源；源被用户从 Moo 删掉后不得因删应用又建回来。
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
		"purged":  purged,
		"message": fmt.Sprintf("已删除「%s」", appname),
	})
}

// handleBatchSync POST /api/batch-sync {appnames:[...]}：批量推送多个应用到统一聚合源。
// 一次性标记 synced、重建聚合 moo.json、确保源存在并 sync（避免逐个推送时重复重建/同步）。
func (c *companion) handleBatchSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appnames []string `json:"appnames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Appnames) == 0 {
		writeErr(w, http.StatusBadRequest, "appnames 不能为空")
		return
	}
	if len(body.Appnames) > 500 {
		writeErr(w, http.StatusBadRequest, "单次最多批量推送 500 个应用")
		return
	}
	if err := c.moo.ping(); err != nil {
		writeErr(w, http.StatusBadGateway, "本机 Moo 不可达："+err.Error())
		return
	}
	names := make([]string, 0, len(body.Appnames))
	for _, appname := range body.Appnames {
		if s := strings.TrimSpace(appname); s != "" {
			names = append(names, s)
		}
	}
	synced, serr := c.idx.setSyncedMany(names, true, mateSourceName)
	if serr != nil {
		writeErr(w, http.StatusInternalServerError, serr.Error())
		return
	}
	if synced == 0 {
		writeErr(w, http.StatusNotFound, "选中的应用均不在索引里")
		return
	}
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	src, err := c.ensureMateSource()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	syncedInfo, serr := c.moo.syncSource(src.ID)
	if serr != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"synced":     synced,
			"sync_error": serr.Error(),
			"message":    fmt.Sprintf("已推送 %d 个应用，但源同步失败", synced),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"synced":    synced,
		"app_count": syncedInfo.AppCount,
		"message":   fmt.Sprintf("已批量推送 %d 个应用到 Moo Mate 源（app_count=%d）", synced, syncedInfo.AppCount),
	})
}

// handleBatchDelete POST /api/batch-delete {appnames:[...]}：批量删除索引里的多个应用。
// 一次性重建聚合 moo.json 并 sync / 删源（空则移除统一源）。
func (c *companion) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appnames []string `json:"appnames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Appnames) == 0 {
		writeErr(w, http.StatusBadRequest, "appnames 不能为空")
		return
	}
	if len(body.Appnames) > 500 {
		writeErr(w, http.StatusBadRequest, "单次最多批量删除 500 个应用")
		return
	}
	deleted := 0
	purged := 0
	for _, appname := range body.Appnames {
		appname = strings.TrimSpace(appname)
		if appname == "" || c.idx.get(appname) == nil {
			continue
		}
		if err := c.idx.delete(appname); err != nil {
			continue
		}
		// 与单条删除口径一致：记「用户主动删过」+ 清本机残留。
		_ = c.idx.markRemovedDefault(appname)
		c.mu.Lock()
		delete(c.hosted, appname)
		c.mu.Unlock()
		if ok, perr := c.purgeAppArtifacts(appname); perr != nil {
			logf("清理「%s」残留失败（忽略）：%v", appname, perr)
		} else if ok {
			purged++
		}
		deleted++
	}
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c.moo.ping() == nil {
		if len(c.aggregateApps()) == 0 {
			if src, err := c.findMateSource(); err == nil && src != nil {
				_ = c.moo.deleteSource(src.ID)
			}
		} else if src, err := c.findMateSource(); err == nil && src != nil {
			// 0.2.62：删除应用只更新「已存在的」源；源被用户从 Moo 删掉后不得因删应用又建回来。
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
		"purged":  purged,
		"message": fmt.Sprintf("已删除 %d 个应用", deleted),
	})
}

// handleHost GET /moo/{appname}.json：托管应用 moo.json。
// appname == "mate" → 统一聚合源（全部 synced 应用）；否则单应用（兼容旧源/调试）。
// 内存未命中时从持久索引现重建（进程重启后 hosted map 清空，若不重建，
// Moo 对已加源的周期性再同步会 404）。
func (c *companion) handleHost(w http.ResponseWriter, r *http.Request) {
	// 兼容 /moo/{app}.json（加源 url 形态）与 /moo/{app} 两种访问；
	// 应用名本身不会含「.json」。
	appname := strings.TrimSuffix(strings.TrimSpace(r.PathValue("appname")), ".json")
	// 0.2.62：Moo 的候选链里还有 <基址>/ICON.PNG、<基址>/ICON_256.PNG 这类
	// 根路径探测——不可能是应用名，这里直接 404（逐应用图标见 handleHostedIcon），
	// 免得拿图标文件名去查索引。
	if looksLikeIconFile(appname) {
		writeErr(w, http.StatusNotFound, "根路径不提供图标")
		return
	}
	if appname == "mate" {
		raw := c.aggregateRaw()
		if raw == nil {
			writeErr(w, http.StatusNotFound, "聚合源为空（尚无已推送应用）")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
		return
	}
	c.mu.RLock()
	raw := c.hosted[appname]
	c.mu.RUnlock()
	if raw == nil {
		if ia := c.idx.get(appname); ia != nil {
			if b, err := buildMooJSON(appname, ia.Publisher, ia.Source, c.base, map[string]*indexedApp{appname: ia}); err == nil {
				c.mu.Lock()
				c.hosted[appname] = b
				c.mu.Unlock()
				raw = b
			}
		}
	}
	if raw == nil {
		writeErr(w, http.StatusNotFound, "该应用尚未同步，无托管内容")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// handleStatus GET /api/status：当前状态（索引数量、Moo 是否可达、上次同步状态）。
func (c *companion) handleStatus(w http.ResponseWriter, r *http.Request) {
	apps := c.idx.list()
	mooOK := c.moo.ping() == nil
	synced := 0
	for _, a := range apps {
		if a.Synced {
			synced++
		}
	}
	lastSync, syncErr := c.syncState()
	status := map[string]any{
		"total":   len(apps),
		"synced":  synced,
		"moo_ok":  mooOK,
		"version": buildVersion,
	}
	if !lastSync.IsZero() {
		status["last_sync"] = lastSync.Format(time.RFC3339)
	}
	if syncErr != "" {
		status["sync_error"] = syncErr
	}
	// 0.2.55（F4）：索引损坏被隔离/读取失败时不再静默，状态接口明确暴露。
	if lerr, badFile := c.idx.diag(); lerr != "" {
		status["last_index_error"] = lerr
		if badFile != "" {
			status["last_index_bad_file"] = badFile
		}
	}
	writeJSON(w, http.StatusOK, status)
}

// handleListIndex GET /api/index：列出索引里的全部应用（含来源与同步状态）。
func (c *companion) handleListIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apps": c.idx.list(),
	})
}

// handleIcon GET /api/icon/{appname}：返回应用图标（本机代理 + 磁盘缓存）。
// 0.2.38 起不再要求索引里有 icon_url：候选顺序 =
// 磁盘缓存 → 内存缓存 → icon_url（索引自带）→ 仓库根常见路径
// （ICON.PNG / <appname>/ICON.PNG 等）→ FPK 包内抽图兜底。
// 命中即落盘（<PKGVAR>/icons/，进程重启不丢）；全部失败回 404，前端回退首字母占位。
// 部分图床对 Referer 敏感，本机 client 不带 Referer。
func (c *companion) handleIcon(w http.ResponseWriter, r *http.Request) {
	appname := strings.TrimSpace(r.PathValue("appname"))
	if appname == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	ia := c.idx.get(appname)
	if ia == nil {
		writeErr(w, http.StatusNotFound, "应用不存在")
		return
	}
	iconCT := map[string]string{"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}
	writeIcon := func(b []byte, ext string) {
		ct := iconCT[ext]
		if ct == "" {
			ct = http.DetectContentType(b)
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(b)
	}
	// ① 磁盘缓存（跨重启）
	if b, ext, ok := c.loadIconFile(appname); ok {
		writeIcon(b, ext)
		return
	}
	// ② 内存缓存
	c.iconMu.RLock()
	b, ok := c.icons[appname]
	c.iconMu.RUnlock()
	if ok && len(b) > 0 {
		if ext, okExt := detectImageExt(b); okExt {
			writeIcon(b, ext)
			return
		}
	}
	// ③ 失败负缓存（30 分钟）：避免前端反复刷新时每轮都重探远端/重下包
	if mateIconRecentlyFailed(appname) {
		writeErr(w, http.StatusNotFound, "未找到应用图标")
		return
	}
	// ③ icon_url（索引自带）
	if u := strings.TrimSpace(ia.IconURL); u != "" {
		ctx, cancel := timeoutCtx(r.Context(), 8*time.Second)
		data, ext, okIcon := c.fetchMateIcon(ctx, u)
		cancel()
		if okIcon {
			c.saveIconFile(appname, data, ext)
			writeIcon(data, ext)
			return
		}
	}
	// ④ 仓库根常见路径（GitHub raw；命中率高的固定布局）
	{
		ctx, cancel := timeoutCtx(r.Context(), 6*time.Second)
		for _, u := range mateIconCandidates(appname, ia.Source) {
			if ctx.Err() != nil {
				break
			}
			if data, ext, okIcon := c.fetchMateIcon(ctx, u); okIcon {
				c.saveIconFile(appname, data, ext)
				cancel()
				writeIcon(data, ext)
				return
			}
		}
		cancel()
	}
	// ⑤ 包内抽图兜底（体积已知且 ≤20MB；并发 2）
	{
		ctx, cancel := timeoutCtx(r.Context(), 90*time.Second)
		data, ext, okIcon := c.fallbackIconFromFPK(ctx, ia)
		cancel()
		if okIcon {
			c.saveIconFile(appname, data, ext)
			writeIcon(data, ext)
			return
		}
	}
	markMateIconFailed(appname)
	writeErr(w, http.StatusNotFound, "未找到应用图标")
}

// hostedIconNames Moo 探测外部源图标时使用的文件名（含大小写变体）。
// 来源：Moo internal/api 的图标候选链（<源基址>/<app>/ICON_256.PNG、
// <app>/ICON.PNG、<app>/icon.png 以及根路径 /ICON.PNG）。
var hostedIconNames = map[string]bool{
	"ICON.PNG": true, "ICON.png": true, "icon.PNG": true, "icon.png": true,
	"ICON_256.PNG": true, "ICON_256.png": true, "icon_256.png": true, "icon-256.png": true,
	"ICON_512.PNG": true, "ICON_512.png": true, "icon_512.png": true,
	"ICON.jpg": true, "icon.jpg": true,
}

// looksLikeIconFile 判断路径段是否像图片文件名（Moo 根路径探测用）。
func looksLikeIconFile(name string) bool {
	if hostedIconNames[name] {
		return true
	}
	l := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.HasSuffix(l, ext) {
			return true
		}
	}
	return false
}

// handleHostedIcon GET /moo/{appname}/{file}：按 Moo 的仓库布局出应用图标。
// 复用 handleIcon 的全部解析链（磁盘缓存 → 内存 → icon_url → 仓库候选 → 包内抽图），
// 因此命中一次就落盘，后续请求零网络成本。
func (c *companion) handleHostedIcon(w http.ResponseWriter, r *http.Request) {
	if !hostedIconNames[strings.TrimSpace(r.PathValue("file"))] {
		writeErr(w, http.StatusNotFound, "不支持的图标文件名")
		return
	}
	c.handleIcon(w, r)
}

// handleConfigGet GET /api/config：透出 GitHub token 状态（是否可用 + 来源，
// 绝不下发明文）及同步间隔（非敏感）。
func (c *companion) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	interval := c.cfg.syncInterval()
	backupDir := c.cfg.BackupDir
	backupAuto := c.cfg.BackupAuto
	backupDays := c.cfg.backupIntervalDays()
	indexURL := c.cfg.defaultIndexURL()
	changelogURL := c.cfg.changelogURL()
	c.mu.RUnlock()
	tok, src, _ := c.githubTokenWithSource()
	writeJSON(w, http.StatusOK, map[string]any{
		"github_token_set":     tok != "",
		"github_token_source":  src,
		"sync_interval":        interval,
		"backup_dir":           backupDir,
		"backup_auto":          backupAuto,
		"backup_interval_days": backupDays,
		"last_backup_at":       c.lastBackupAt(),
		"default_index_url":    indexURL,
		"changelog_url":        changelogURL,
		"log_lines":            c.cfg.logLines(),
	})
}

// handleConfigPut PUT /api/config {sync_interval?, ...}：同步间隔与备份/远程清单等设置。
// 0.2.28 起不再接受 github_token（统一由 Moo 提供）；缺省字段不改动（指针语义）。
func (c *companion) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SyncInterval    *string `json:"sync_interval"`
		BackupDir       *string `json:"backup_dir"`
		BackupAuto      *bool   `json:"backup_auto"`
		BackupDays      *int    `json:"backup_interval_days"`
		DefaultIndexURL *string `json:"default_index_url"`
		ChangelogURL    *string `json:"changelog_url"`
		LogLines        *int    `json:"log_lines"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if c.cfgPath == "" {
		writeErr(w, http.StatusInternalServerError, "配置存储未初始化")
		return
	}
	if c.cfg == nil {
		writeErr(w, http.StatusInternalServerError, "配置未初始化")
		return
	}
	// 0.2.71：记下请求前的内容。全部字段都没变就不落盘——前端打开设置页回填齿轮
	// 选择器时会派发 scroll 事件（旧版因此每次都 PUT 一次相同值），后端旧实现无条件
	// save，属无意义写盘。这里做幂等兜底，也兼容其他客户端。
	before := *c.cfg
	if body.SyncInterval != nil {
		v := strings.TrimSpace(*body.SyncInterval)
		if !validSyncInterval(v) {
			writeErr(w, http.StatusBadRequest, "sync_interval 非法（manual / 空 或 Go duration，如 30m / 1h / 6h30m）")
			return
		}
		c.cfg.SyncInterval = v
	}
	if body.BackupDir != nil {
		v := strings.TrimSpace(*body.BackupDir)
		if v != "" && !isValidBackupDir(v) {
			writeErr(w, http.StatusBadRequest, "备份目录须为 /volN 存储卷下的路径（空 = 默认应用数据目录）")
			return
		}
		c.cfg.BackupDir = v
	}
	if body.BackupAuto != nil {
		c.cfg.BackupAuto = *body.BackupAuto
	}
	if body.BackupDays != nil {
		if *body.BackupDays < 1 || *body.BackupDays > 30 {
			writeErr(w, http.StatusBadRequest, "备份周期须为 1–30 天")
			return
		}
		c.cfg.BackupIntervalDays = *body.BackupDays
	}
	// 0.2.37：远程清单地址（空 = 恢复内置默认公网地址）。
	for _, f := range []struct {
		name string
		dst  *string
		val  *string
	}{{"default_index_url", &c.cfg.DefaultIndexURL, body.DefaultIndexURL}, {"changelog_url", &c.cfg.ChangelogURL, body.ChangelogURL}} {
		if f.val == nil {
			continue
		}
		v := strings.TrimSpace(*f.val)
		if v != "" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
			writeErr(w, http.StatusBadRequest, f.name+" 必须是 http(s):// 开头")
			return
		}
		*f.dst = v
	}
	// 0.2.38：日志查看行数（50/100/200/500/1000，与 Moo 同档位）。
	if body.LogLines != nil {
		if !validLogLines(*body.LogLines) {
			writeErr(w, http.StatusBadRequest, "log_lines 只支持 50 / 100 / 200 / 500 / 1000")
			return
		}
		c.cfg.LogLines = *body.LogLines
	}
	if *c.cfg == before {
		// 值没变：不写盘、不唤醒同步循环，仍回 200（幂等）。
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unchanged": true})
		return
	}
	if err := c.cfg.save(c.cfgPath); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 间隔/源变化即时生效：唤醒同步循环重排下次 tick。
	c.wakeSync()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
	})
}

// appHostURL 单应用 moo.json 的托管地址（加源 url）。
// 必须带 .json 后缀：Moo 的 buildCandidates 对 .json 结尾的直链原样使用，
// 否则会被当「仓库根」展开候选链（/moo/{app}/moo.json 等）全部 404。
func (c *companion) appHostURL(appname string) string {
	return c.base + "/moo/" + url.PathEscape(appname) + ".json"
}

// mateHostURL 统一聚合源托管地址（加源 url），同样带 .json 后缀走直链单候选。
func (c *companion) mateHostURL() string {
	return c.base + "/moo/mate.json"
}

// aggregateApps 返回索引里全部已同步（synced=true）的应用（appname → 条目）。
func (c *companion) aggregateApps() map[string]*indexedApp {
	out := map[string]*indexedApp{}
	for _, ia := range c.idx.list() {
		if ia.Synced {
			out[ia.Appname] = ia
		}
	}
	return out
}

// rebuildAggregate 重建聚合 moo.json（全部 synced 应用）并缓存，返回字节。
func (c *companion) rebuildAggregate() ([]byte, error) {
	apps := c.aggregateApps()
	raw, err := buildMooJSON(mateSourceName, "Blue-Mink", "https://github.com/Blue-Mink/moo", c.base, apps)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.aggregate = raw
	c.mu.Unlock()
	return raw, nil
}

// aggregateRaw 返回当前聚合 moo.json（内存未命中时从索引现重建）。
func (c *companion) aggregateRaw() []byte {
	c.mu.RLock()
	raw := c.aggregate
	c.mu.RUnlock()
	if raw != nil {
		return raw
	}
	if b, err := c.rebuildAggregate(); err == nil {
		return b
	}
	return nil
}

// ensureMateSource 确保统一聚合源已在 Moo 源列表（不在则加），返回源信息。
func (c *companion) ensureMateSource() (*sourceInfo, error) {
	if s, err := c.findMateSource(); err != nil || s != nil {
		return s, err
	}
	return c.moo.addSource(mateSourceName, c.mateHostURL())
}

// findMateSource 在 Moo 源列表里找统一聚合源；不存在返回 (nil, nil)。
func (c *companion) findMateSource() (*sourceInfo, error) {
	url := c.mateHostURL()
	srcs, err := c.moo.listSources()
	if err != nil {
		return nil, err
	}
	for i := range srcs {
		if source.NormalizeSourceURL(srcs[i].URL) == source.NormalizeSourceURL(url) {
			return &srcs[i], nil
		}
	}
	return nil, nil
}

// findDuplicateSource 在 Moo 源列表里找与给定 URL 同源（归一化后相同）的源。
func findDuplicateSource(u string, srcs []sourceInfo) *sourceInfo {
	key := source.NormalizeSourceURL(u)
	if key == "" {
		return nil
	}
	for i := range srcs {
		if source.NormalizeSourceURL(srcs[i].URL) == key {
			return &srcs[i]
		}
	}
	return nil
}

// updateCheckResult 单个应用上游检查结果。
type updateCheckResult struct {
	updated  bool
	from     string // 旧版本
	to       string // 新版本（未更新时为上游最新版）
	message  string // 用户可读消息（无更新场景）
	prev     string // 本次更新前的版本（空 = 本次没有更新，界面据此熄灭红点）
	checkErr string // 「无法确认是否有更新」的原因（上游无包/应用已不存在/版本不可比）
}

// checkAppUpdateCore 检查单个应用上游是否有新版本：重新扫描上游仓库，
// 有更高版本则更新索引（下载新 FPK 算 sha256、合并架构，保留 synced 状态）。
// 返回错误 = 网络/解析失败；否则返回结果（含「无更新」与「已更新」两类）。
// 供 /api/check-update 与定时全量同步共用。
func (c *companion) checkAppUpdateCore(ctx context.Context, ia *indexedApp) (*updateCheckResult, error) {
	return c.checkAppUpdateCoreCached(ctx, ia, nil)
}

// checkAppUpdateCoreCached 同上，但可复用同一轮同步里「同一来源仓库」的发现结果，
// 避免 100 个应用共享一个仓库时把 release/文件树接口打 100 遍（也省 GitHub 限流额度）。
// cache 为 nil 时每次现扫。
func (c *companion) checkAppUpdateCoreCached(ctx context.Context, ia *indexedApp, cache map[string][]discoveredApp) (*updateCheckResult, error) {
	return c.checkAppUpdateCoreCachedGen(ctx, ia, cache, c.idx.generation())
}

// checkAppUpdateCoreCachedGen 同上，但「索引代次」由调用方传入。
//
// 为什么必须由调用方给：整轮同步（runFullSync）要在开头取一次代次并全程沿用。
// 若逐应用现取，用户在同步进行中点了「清除同步索引」，**clear 之后才轮到检查**的应用
// 会拿到清除后的新代次，于是写回成功 —— 清除依旧不彻底。0.2.61-test2 的实机强构造
// 验证（motrix 置 0.0.1 → 同步中 clear）正是这样 FAIL 的：apps 由 0 变 1、motrix 2.0.5 被写回。
func (c *companion) checkAppUpdateCoreCachedGen(ctx context.Context, ia *indexedApp, cache map[string][]discoveredApp, gen uint64) (*updateCheckResult, error) {
	src := strings.TrimSpace(ia.Source)
	if src == "" {
		return nil, fmt.Errorf("该应用无来源仓库，无法检查更新")
	}
	key := strings.TrimRight(src, "/")
	var discovered []discoveredApp
	if cache != nil {
		if v, ok := cache[key]; ok {
			discovered = v
		}
	}
	if discovered == nil {
		d, err := discoverFromRepo(ctx, src, c.githubToken(), c.httpc)
		if err != nil {
			return nil, err
		}
		discovered = d
		if cache != nil {
			cache[key] = d
		}
	}
	if len(discovered) == 0 {
		return &updateCheckResult{updated: false, message: "上游 release 与仓库文件都没有 .fpk",
			checkErr: "上游 release 与仓库文件都没有 .fpk"}, nil
	}
	// 单应用更新检查没必要下载整个仓库的包：文件名能对上 appname 的优先，
	// 一个都对不上（文件名不含应用名）才回落到全量扫描。大仓库从这里省下大量流量。
	discovered = filterCandidatesForApp(discovered, ia.Appname)
	accs, _ := c.discoverAccs(ctx, discovered, ia.Source, repoOwner(ia.Source), nil)
	acc := accs[ia.Appname]
	if acc == nil {
		return &updateCheckResult{updated: false, message: "上游已不存在该应用",
			checkErr: "上游仓库里已找不到该应用（可能改名或删库）"}, nil
	}
	if acc.version == "" || ia.Version == "" {
		return &updateCheckResult{updated: false, message: "版本不可比",
			checkErr: "版本号无法比较（上游或本地版本为空/格式异常）"}, nil
	}
	if !versionGreater(acc.version, ia.Version) {
		return &updateCheckResult{updated: false, from: ia.Version, to: acc.version, message: "已是最新"}, nil
	}
	// 更高版本：更新索引（保留 synced 状态）。
	acc.ia.Synced = ia.Synced
	acc.ia.SyncedName = ia.SyncedName
	wrote, err := c.idx.upsertIfGen(acc.ia, gen)
	if err != nil {
		return nil, err
	}
	if !wrote {
		// 0.2.63（A5）：写不进索引就绝不能把「上游最新版本」回传给状态写入方 ——
		// 否则 latest_version 被写成新版、version 仍是旧版，界面「有更新」会永久红，
		// 且该分支没有任何过期机制（真·永久红路径）。这里降级成可解释的「暂时无法确认」，
		// 由 applyCheckResults 记 last_check_err（琥珀），下一轮同步自愈。
		return &updateCheckResult{updated: false, from: ia.Version,
			message:  "索引已变更，稍后重试",
			checkErr: "索引在检查期间被重建/清除，结果未能写入（稍后自动重试）"}, nil
	}
	return &updateCheckResult{updated: true, from: ia.Version, to: acc.version, prev: ia.Version,
		message: "已更新"}, nil
}

// handleAckUpdates POST /api/ack-updates {appnames:[...]}：把「已更新」红点标记为已读。
// appnames 省略/为空 = 全部已读（前端「全部已读」按钮）。返回 acked = 实际标记数量。
// 为什么需要落盘：0.2.62 的红点靠 24h TTL 自然过期，用户既没法手动消除，
// 又会在反复发版时被无限续期；已读状态必须跟索引一起持久化，重启后仍然有效。
func (c *companion) handleAckUpdates(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appnames []string `json:"appnames"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
			return
		}
	}
	gen := c.idx.generation()
	n, err := c.idx.ackUpdates(body.Appnames, gen)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acked": n})
}

// handleCheckUpdate POST /api/check-update {appname}：重新扫描该应用的上游仓库，
// 若发现更高版本则更新索引，已推送则重建聚合并 sync。
func (c *companion) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Appname string `json:"appname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Appname) == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	appname := strings.TrimSpace(body.Appname)
	ia := c.idx.get(appname)
	if ia == nil {
		writeErr(w, http.StatusNotFound, "索引里没有该应用："+appname)
		return
	}
	ctx, cancel := timeoutCtx(r.Context(), 3*time.Minute)
	defer cancel()
	// 单应用检查同样记录状态：成功后界面显示「已最新 / 有更新」，失败显示感叹号。
	gen := c.idx.generation()
	res, err := c.checkAppUpdateCoreCachedGen(ctx, ia, nil, gen)
	if err != nil {
		_, _ = c.idx.applyCheckResults([]checkOutcome{{Appname: appname, ErrMsg: err.Error()}}, gen)
		if strings.Contains(err.Error(), "无来源仓库") {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if res != nil {
		_, _ = c.idx.applyCheckResults([]checkOutcome{{Appname: appname, Latest: res.to, Prev: res.prev, ErrMsg: res.checkErr}}, gen)
	}
	if !res.updated {
		msg := res.message
		if msg == "已是最新" {
			msg = fmt.Sprintf("已是最新（v%s）", ia.Version)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"updated": false, "current": res.from, "latest": res.to, "message": msg,
		})
		return
	}
	synced := false
	if ia.Synced && c.moo.ping() == nil {
		if _, err := c.rebuildAggregate(); err == nil {
			if src, err := c.findMateSource(); err == nil && src != nil {
				if _, serr := c.moo.syncSource(src.ID); serr == nil {
					synced = true
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": true, "from": res.from, "to": res.to, "synced": synced,
		"message": fmt.Sprintf("已更新 v%s → v%s", res.from, res.to),
	})
}
