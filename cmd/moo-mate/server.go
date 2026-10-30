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
	token   string       // GitHub token 兜底（-token / MOO_MATE_TOKEN，仅调试用）
	cfgPath string          // 配置文件路径（config.json）
	bootDir string          // 引导目录（datadir 指针文件所在，默认 = 初始数据目录）
	cfg     *mateConfig     // 配置（不含 token：0.2.28 起复用 Moo 的那一份）
	sealer  *secret.Sealer  // 敏感字段加解密器（保留：旧配置兼容/后续敏感项）
	webFS   http.Handler

	// GitHub token 缓存：向 Moo 取（Moo 未配置时回落 token 兜底）。
	// 2 分钟 TTL，避免每个 GitHub 请求都打一次 Moo。
	tokMu    sync.Mutex
	tokCache string
	tokSrc   string // "moo" | "flag" | ""
	tokAt    time.Time

	syncWake    chan struct{} // 软仓同步循环唤醒信号（配置变更/手动触发）
	syncStateMu sync.Mutex
	lastSync    time.Time // 上次软仓同步时间
	syncErr     string    // 上次软仓同步错误（空 = 成功）

	iconMu sync.RWMutex
	icons  map[string][]byte // 应用图标内存缓存（appname → 图片字节；经本机代理下载，绕过 Gitee 防盗链）
}

// newCompanion 构造伴侣（cfg/sealer 由 main 初始化后经 setConfig 注入，可为空）。
func newCompanion(mooBase, selfBase, token string, webFS http.Handler, idx *index) *companion {
	return &companion{
		idx:      idx,
		hosted:   map[string][]byte{},
		moo:      newMooClient(mooBase),
		base:     strings.TrimRight(selfBase, "/"),
		httpc:    &http.Client{Timeout: 60 * time.Second},
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

// discoverAccs 从 discovered 列表收集「appname → 累积条目」（版本合并 + 架构合并）。
// 同名多架构 FPK（如 motrix 的 x86/arm 两包）同版本合并进 packages；primary 指向 x86。
func (c *companion) discoverAccs(ctx context.Context, discovered []discoveredApp, raw, owner string) (map[string]*buildAcc, []string) {
	accs := map[string]*buildAcc{}
	var skipped []string
	for _, d := range discovered {
		meta, err := c.downloadAndInspect(ctx, d.DownloadURL)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", d.Name, err))
			continue
		}
		appname := strings.TrimSpace(meta.Manifest["appname"])
		if appname == "" {
			skipped = append(skipped, d.Name+": manifest 缺少 appname")
			continue
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
			skipped = append(skipped, fmt.Sprintf("%s@%s: 低于最高版 %s", appname, e.Version, acc.version))
			continue
		}
		if arch == "" {
			skipped = append(skipped, fmt.Sprintf("%s: 无法识别架构（%s）", appname, d.Name))
			continue
		}
		if acc.ia.Packages == nil {
			acc.ia.Packages = map[string]*appPackage{}
		}
		acc.ia.Packages[arch] = &appPackage{DownloadURL: d.DownloadURL, Sha256: e.Sha256, SizeBytes: e.SizeBytes}
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
	ctx, cancel := timeoutCtx(r.Context(), 5*time.Minute)
	defer cancel()
	discovered, err := discoverFromRepo(ctx, raw, c.githubToken(), c.httpc)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if len(discovered) == 0 {
		writeErr(w, http.StatusNotFound, "仓库 release 里没有 .fpk 资产，无法生成")
		return
	}
	accs, skipped := c.discoverAccs(ctx, discovered, raw, owner)
	var added []string
	for appname, acc := range accs {
		if prev := c.idx.get(appname); prev != nil {
			if acc.version == "" || prev.Version == "" {
				skipped = append(skipped, appname+": 版本不可比，跳过")
				continue
			}
			switch {
			case versionGreater(acc.version, prev.Version):
				// 更高版本：覆盖（upsert 保留 synced 状态）
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
					continue
				}
				added = append(added, appname)
				continue
			default:
				skipped = append(skipped, appname+": 索引已有更高版本，跳过")
				continue
			}
		}
		if err := c.idx.upsert(acc.ia); err != nil {
			skipped = append(skipped, appname+": 落索引失败 "+err.Error())
			continue
		}
		added = append(added, appname)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"added":   added,
		"count":   len(added),
		"skipped": skipped,
	})
}

// downloadAndInspect 下载 FPK 并解析 manifest。GitHub release 资产并发竞速
// （镜像前缀 + 直连），首个 200 且可解析的候选胜出。
func (c *companion) downloadAndInspect(ctx context.Context, u string) (*fpkMeta, error) {
	cands := mirrorCandidates(u)
	type result struct {
		meta *fpkMeta
		err  error
	}
	ch := make(chan result, len(cands))
	for _, cand := range cands {
		go func(cand string) {
			meta, err := c.tryDownload(ctx, cand)
			ch <- result{meta, err}
		}(cand)
	}
	var lastErr error
	for range cands {
		r := <-ch
		if r.err == nil {
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
	} else if o, r, ok := parseGiteeRepo(raw); ok {
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
	if src, err := c.ensureMateSource(); err == nil {
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
	c.mu.Lock()
	delete(c.hosted, appname)
	c.mu.Unlock()
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
		} else if src, err := c.ensureMateSource(); err == nil {
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": true,
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
		"synced":     synced,
		"app_count":  syncedInfo.AppCount,
		"message":    fmt.Sprintf("已批量推送 %d 个应用到 Moo Mate 源（app_count=%d）", synced, syncedInfo.AppCount),
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
	for _, appname := range body.Appnames {
		appname = strings.TrimSpace(appname)
		if appname == "" || c.idx.get(appname) == nil {
			continue
		}
		if err := c.idx.delete(appname); err != nil {
			continue
		}
		c.mu.Lock()
		delete(c.hosted, appname)
		c.mu.Unlock()
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
		} else if src, err := c.ensureMateSource(); err == nil {
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted": deleted,
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
			if b, err := buildMooJSON(appname, ia.Publisher, ia.Source, map[string]*indexedApp{appname: ia}); err == nil {
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

// handleStatus GET /api/status：当前状态（索引数量、Moo 是否可达、软仓同步状态）。
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
	writeJSON(w, http.StatusOK, status)
}

// handleListIndex GET /api/index：列出索引里的全部应用（含来源与同步状态）。
func (c *companion) handleListIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apps": c.idx.list(),
	})
}

// handleIcon GET /api/icon/{appname}：经本机代理返回应用图标。
// 软仓图标有的指向 Gitee raw（带 Referer 会 403 防盗链），浏览器 <img> 直连会失败；
// 由本机 http client（不带 Referer）下载并缓存，前端 img 指向本端点。
func (c *companion) handleIcon(w http.ResponseWriter, r *http.Request) {
	appname := strings.TrimSpace(r.PathValue("appname"))
	if appname == "" {
		writeErr(w, http.StatusBadRequest, "appname 不能为空")
		return
	}
	ia := c.idx.get(appname)
	if ia == nil || strings.TrimSpace(ia.IconURL) == "" {
		writeErr(w, http.StatusNotFound, "该应用无图标")
		return
	}
	c.iconMu.RLock()
	b, ok := c.icons[appname]
	c.iconMu.RUnlock()
	if !ok {
		ctx, cancel := timeoutCtx(r.Context(), 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ia.IconURL, nil)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		req.Header.Set("User-Agent", "Moo-Mate/1.0")
		resp, err := c.httpc.Do(req)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "图标下载失败："+err.Error())
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("图标下载 HTTP %d", resp.StatusCode))
			return
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil || len(data) == 0 {
			writeErr(w, http.StatusBadGateway, "图标内容为空")
			return
		}
		c.iconMu.Lock()
		c.icons[appname] = data
		c.iconMu.Unlock()
		b = data
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// handleConfigGet GET /api/config：透出 GitHub token 状态（是否可用 + 来源，
// 绝不下发明文）及软仓源选择与同步间隔（非敏感）。⚠️ 软仓个人源地址属私有链接，不下发。
func (c *companion) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	c.mu.RLock()
	source := c.cfg.softstoreSource()
	interval := c.cfg.syncInterval()
	backupDir := c.cfg.BackupDir
	backupAuto := c.cfg.BackupAuto
	backupDays := c.cfg.backupIntervalDays()
	c.mu.RUnlock()
	tok, src, _ := c.githubTokenWithSource()
	writeJSON(w, http.StatusOK, map[string]any{
		"github_token_set":     tok != "",
		"github_token_source":  src,
		"softstore_source":     source,
		"sync_interval":        interval,
		"backup_dir":           backupDir,
		"backup_auto":          backupAuto,
		"backup_interval_days": backupDays,
		"last_backup_at":       c.lastBackupAt(),
	})
}

// handleConfigPut PUT /api/config {softstore_url?, softstore_source?, sync_interval?}：
// 软仓源选择与同步间隔。0.2.28 起不再接受 github_token（统一由 Moo 提供）。
// softstore_url 保留兼容（老配置迁移），新 UI 不再暴露；缺省字段不改动（指针语义）。
func (c *companion) handleConfigPut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SoftstoreURL    *string `json:"softstore_url"`
		SoftstoreSource *string `json:"softstore_source"`
		SyncInterval    *string `json:"sync_interval"`
		BackupDir       *string `json:"backup_dir"`
		BackupAuto      *bool   `json:"backup_auto"`
		BackupDays      *int    `json:"backup_interval_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if c.cfgPath == "" {
		writeErr(w, http.StatusInternalServerError, "配置存储未初始化")
		return
	}
	if body.SoftstoreURL != nil {
		u := strings.TrimSpace(*body.SoftstoreURL)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeErr(w, http.StatusBadRequest, "softstore_url 必须是 http(s):// 开头")
			return
		}
		c.cfg.SoftstoreURL = u
	}
	if body.SoftstoreSource != nil {
		v := strings.TrimSpace(*body.SoftstoreSource)
		if v != "personal" && v != "gitee" {
			writeErr(w, http.StatusBadRequest, "softstore_source 非法（可选 personal / gitee）")
			return
		}
		c.cfg.SoftstoreSource = v
	}
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
	raw, err := buildMooJSON(mateSourceName, "Blue-Mink", "https://github.com/Blue-Mink/moo", apps)
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
	updated bool
	from    string // 旧版本
	to      string // 新版本（未更新时为上游最新版）
	message string // 用户可读消息（无更新场景）
}

// checkAppUpdateCore 检查单个应用上游是否有新版本：重新扫描上游仓库，
// 有更高版本则更新索引（下载新 FPK 算 sha256、合并架构，保留 synced 状态）。
// 返回错误 = 网络/解析失败；否则返回结果（含「无更新」与「已更新」两类）。
// 供 /api/check-update 与定时全量同步共用。
func (c *companion) checkAppUpdateCore(ctx context.Context, ia *indexedApp) (*updateCheckResult, error) {
	if strings.TrimSpace(ia.Source) == "" {
		return nil, fmt.Errorf("该应用无来源仓库，无法检查更新")
	}
	discovered, err := discoverFromRepo(ctx, ia.Source, c.githubToken(), c.httpc)
	if err != nil {
		return nil, err
	}
	if len(discovered) == 0 {
		return &updateCheckResult{updated: false, message: "上游 release 无 .fpk 资产"}, nil
	}
	accs, _ := c.discoverAccs(ctx, discovered, ia.Source, repoOwner(ia.Source))
	acc := accs[ia.Appname]
	if acc == nil {
		return &updateCheckResult{updated: false, message: "上游已不存在该应用"}, nil
	}
	if acc.version == "" || ia.Version == "" {
		return &updateCheckResult{updated: false, message: "版本不可比"}, nil
	}
	if !versionGreater(acc.version, ia.Version) {
		return &updateCheckResult{updated: false, from: ia.Version, to: acc.version, message: "已是最新"}, nil
	}
	// 更高版本：更新索引（保留 synced 状态）。
	acc.ia.Synced = ia.Synced
	acc.ia.SyncedName = ia.SyncedName
	if err := c.idx.upsert(acc.ia); err != nil {
		return nil, err
	}
	return &updateCheckResult{updated: true, from: ia.Version, to: acc.version, message: "已更新"}, nil
}

// handleCheckUpdate POST /api/check-update {appname}：重新扫描该应用的上游仓库，
// 若发现更高版本则更新索引，已推送则重建聚合并 sync。软仓来源应用的上游即软仓，
// 直接触发一次软仓同步。
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
	// 软仓来源应用：上游就是软仓，直接触发一次软仓同步。
	if c.isSoftstoreApp(ia) {
		added, updated, err := c.runSoftstoreSync()
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"updated":           updated > 0,
			"added":             added,
			"softstore_updated": updated,
			"message":           fmt.Sprintf("软仓同步完成：新增 %d、更新 %d", added, updated),
		})
		return
	}
	ctx, cancel := timeoutCtx(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := c.checkAppUpdateCore(ctx, ia)
	if err != nil {
		if strings.Contains(err.Error(), "无来源仓库") {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
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
			if src, err := c.ensureMateSource(); err == nil {
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
