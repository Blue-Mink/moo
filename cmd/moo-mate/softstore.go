package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// softstoreApp 软仓官方源 /api/apps 里的单个应用条目（只取需要的字段）。
type softstoreApp struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Desc            string   `json:"desc"`
	Author          string   `json:"author"`
	Tags            string   `json:"tags"`
	DownloadURL     string   `json:"download_url"`
	Icon            string   `json:"icon"`
	Type            string   `json:"type"`
	Platform        string   `json:"platform"`
	Sha256          string   `json:"sha256"`
	Size            int64    `json:"size"`
	Repo            string   `json:"repo"`
	DownloadURLs    []string `json:"download_urls"`
	OrigDownloadURL string   `json:"_original_download_url"`
}

// softstoreType 软仓 type（原生/docker/空）→ moo app_type（fpk/docker）。
func softstoreType(t string) string {
	l := strings.ToLower(strings.TrimSpace(t))
	if strings.Contains(l, "docker") || strings.Contains(l, "容器") {
		return "docker"
	}
	return "fpk"
}

// pickSoftstoreURL 选主下载链：原始链（GitHub/gitee release 直链，稳定，
// 且 Moo 对 GitHub 链会自动镜像竞速）优先；回落软仓服务器直链。
func pickSoftstoreURL(a *softstoreApp) string {
	if v := strings.TrimSpace(a.OrigDownloadURL); v != "" {
		return v
	}
	if v := strings.TrimSpace(a.DownloadURL); v != "" {
		return v
	}
	if len(a.DownloadURLs) > 0 {
		return strings.TrimSpace(a.DownloadURLs[0])
	}
	return ""
}

// softstoreCandidateURLs 汇总一条软仓记录的候选下载链：原始链 + 软仓服务器直链，
// 去重保序（测速选优用；软仓 download_urls 里的 fastgit/gh-proxy 等镜像对 Moo
// 无意义，Moo 对 GitHub 链有自家镜像竞速，故不展开）。
func softstoreCandidateURLs(a *softstoreApp) []string {
	seen := map[string]bool{}
	var out []string
	add := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	add(a.OrigDownloadURL)
	add(a.DownloadURL)
	return out
}

// mergeUniqueStrings 把 src 去重合并进 dst（保序，原地追加）。
func mergeUniqueStrings(dst []string, src ...string) []string {
	seen := map[string]bool{}
	for _, v := range dst {
		seen[v] = true
	}
	for _, v := range src {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		dst = append(dst, v)
	}
	return dst
}

// discoverFromSoftstore 拉取软仓官方源 /api/apps，按 id 分组合并 x86/arm 双架构，
// 返回 appname → indexedApp。baseURL 形如 http://nas.hhxs2025.top:5660。
func discoverFromSoftstore(ctx context.Context, baseURL string, httpc *http.Client) (map[string]*indexedApp, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u := base + "/api/apps"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if rerr != nil {
		return nil, rerr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("软仓 API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var doc struct {
		Success bool           `json:"success"`
		Data    []softstoreApp `json:"data"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Data) == 0 {
		return nil, fmt.Errorf("软仓返回空应用列表")
	}

	// 按 id 分组（同名 x86/arm 两条记录合并成一个多架构应用）。
	type group struct {
		entries []*softstoreApp
	}
	groups := map[string]*group{}
	var order []string
	for i := range doc.Data {
		a := &doc.Data[i]
		id := strings.TrimSpace(a.ID)
		if id == "" {
			continue
		}
		if g, ok := groups[id]; ok {
			g.entries = append(g.entries, a)
		} else {
			groups[id] = &group{entries: []*softstoreApp{a}}
			order = append(order, id)
		}
	}

	out := map[string]*indexedApp{}
	for _, id := range order {
		g := groups[id]
		first := g.entries[0] // 元数据（name/desc/icon 等）取首条，架构记录各自独立
		entry := &indexedApp{
			appEntry: appEntry{
				DisplayName: firstNonEmpty(first.Name, id),
				Version:     strings.TrimSpace(first.Version),
				AppType:     softstoreType(first.Type),
				Sha256:      strings.TrimSpace(first.Sha256),
				DownloadURL: pickSoftstoreURL(first),
				IconURL:     strings.TrimSpace(first.Icon),
				Desc:        first.Desc,
				Author:      first.Author,
				Homepage:    strings.TrimSpace(first.Repo),
				Labels:      first.Tags,
			},
			Appname:    id,
			Source:     base, // 软仓源地址（baseURL）
			Publisher:  "softstore",
		}
		// 多架构合并 packages + 汇总候选下载链。
		pkgs := map[string]*appPackage{}
		for _, a := range g.entries {
			arch := normArch(a.Platform)
			if arch == "" {
				arch = "x86" // 平台缺省按 x86
			}
			pkgs[arch] = &appPackage{
				DownloadURL: pickSoftstoreURL(a),
				Sha256:      strings.TrimSpace(a.Sha256),
				SizeBytes:   a.Size,
			}
			entry.DownloadURLs = mergeUniqueStrings(entry.DownloadURLs, softstoreCandidateURLs(a)...)
		}
		if len(pkgs) > 0 {
			entry.Packages = pkgs
			entry.appEntry.Platform = archKeys(pkgs)
			// 主 download_url 指向 x86（Moo 0.6.352 起按本机架构覆盖选包）。
			if p, ok := pkgs["x86"]; ok {
				entry.appEntry.DownloadURL = p.DownloadURL
				entry.appEntry.Sha256 = p.Sha256
				entry.appEntry.SizeBytes = p.SizeBytes
			}
		}
		if entry.appEntry.SizeBytes > 0 {
			entry.appEntry.Size = round2(float64(entry.appEntry.SizeBytes) / (1024 * 1024))
		}
		out[id] = entry
	}
	return out, nil
}

// syncSoftstore 从软仓拉取全部应用并合并进索引：按配置的软仓源（个人源 / Gitee 源）
// 分派；软仓应用默认全部推送（synced=true，与用户「软仓所有应用做索引」一致）；
// 已存在的应用保留其同步状态、更新版本/图标/下载链。
// 返回 (新增数, 更新数)。软仓里没有的既有应用（如孤立 7 个）原样保留不动。
func (c *companion) syncSoftstore(ctx context.Context) (added, updated int, err error) {
	if c.cfg.softstoreSource() == "gitee" {
		return c.syncGiteeSoftstore(ctx)
	}
	return c.syncPersonalSoftstore(ctx)
}

// syncPersonalSoftstore 从个人源（HTTP API）拉取全部应用并合并进索引。
func (c *companion) syncPersonalSoftstore(ctx context.Context) (added, updated int, err error) {
	baseURL := c.cfg.softstoreURL()
	apps, err := discoverFromSoftstore(ctx, baseURL, c.httpc)
	if err != nil {
		return 0, 0, err
	}
	for appname, ia := range apps {
		prev := c.idx.get(appname)
		if prev == nil {
			ia.Synced = true
			ia.SyncedName = mateSourceName
			if err := c.idx.upsert(ia); err != nil {
				return added, updated, err
			}
			added++
			continue
		}
		if prev.Version != ia.Version || prev.IconURL != ia.IconURL || prev.DownloadURL != ia.DownloadURL {
			updated++
		}
		ia.Synced = prev.Synced
		ia.SyncedName = prev.SyncedName
		if err := c.idx.upsert(ia); err != nil {
			return added, updated, err
		}
	}
	return added, updated, nil
}

// syncGiteeSoftstore 从 Gitee 源（软仓官方仓库）扫描 release 里的 .fpk 并合并进索引。
// 与个人源同口径：应用默认全部推送（synced=true），标记 Publisher=softstore 以便
// isSoftstoreApp 识别（上游即软仓，check-update 走软仓同步）。
func (c *companion) syncGiteeSoftstore(ctx context.Context) (added, updated int, err error) {
	raw := defaultGiteeSoftstore
	discovered, err := discoverFromRepo(ctx, raw, c.githubToken(), c.httpc)
	if err != nil {
		return 0, 0, err
	}
	if len(discovered) == 0 {
		return 0, 0, fmt.Errorf("Gitee 源 release 无 .fpk 资产")
	}
	accs, _ := c.discoverAccs(ctx, discovered, raw, "softstore")
	for appname, acc := range accs {
		ia := acc.ia
		ia.Publisher = "softstore"
		prev := c.idx.get(appname)
		if prev == nil {
			ia.Synced = true
			ia.SyncedName = mateSourceName
			if err := c.idx.upsert(ia); err != nil {
				return added, updated, err
			}
			added++
			continue
		}
		if prev.Version != ia.Version || prev.IconURL != ia.IconURL || prev.DownloadURL != ia.DownloadURL {
			updated++
		}
		ia.Synced = prev.Synced
		ia.SyncedName = prev.SyncedName
		if err := c.idx.upsert(ia); err != nil {
			return added, updated, err
		}
	}
	return added, updated, nil
}

// runSoftstoreSync 执行一次完整软仓同步：拉取合并 → 重建聚合 → 触发 Moo 源同步。
// 记录同步状态（时间/错误），供状态展示与定时循环复用。
func (c *companion) runSoftstoreSync() (added, updated int, err error) {
	// Gitee 源需逐个下载 FPK 解析 manifest（串行，单个数秒~十几秒），预算放宽到 15 分钟。
	budget := 3 * time.Minute
	if c.cfg.softstoreSource() == "gitee" {
		budget = 15 * time.Minute
	}
	ctx, cancel := timeoutCtx(context.Background(), budget)
	defer cancel()
	added, updated, err = c.syncSoftstore(ctx)
	c.syncStateMu.Lock()
	c.lastSync = time.Now()
	if err != nil {
		c.syncErr = err.Error()
	} else {
		c.syncErr = ""
	}
	c.syncStateMu.Unlock()
	if err != nil {
		return added, updated, err
	}
	if _, rerr := c.rebuildAggregate(); rerr != nil {
		return added, updated, rerr
	}
	if c.moo.ping() == nil {
		if src, serr := c.ensureMateSource(); serr == nil {
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	return added, updated, nil
}

// isSoftstoreSource 判断一条来源 URL 是否就是软仓源地址（软仓应用的上游即软仓自身）。
func (c *companion) isSoftstoreSource(raw string) bool {
	return strings.TrimRight(strings.TrimSpace(raw), "/") == strings.TrimRight(c.cfg.softstoreURL(), "/")
}

// isSoftstoreApp 判断一条索引应用是否来自软仓（Publisher 标记，或来源地址即软仓）。
func (c *companion) isSoftstoreApp(ia *indexedApp) bool {
	if ia == nil {
		return false
	}
	if strings.TrimSpace(ia.Publisher) == "softstore" {
		return true
	}
	return c.isSoftstoreSource(ia.Source)
}

// runFullSync 执行一次完整同步：软仓 + 索引里全部 GitHub/Gitee 来源应用的上游检查，
// 最后统一重建聚合并触发 Moo 源同步。记录同步状态供 /api/status 与定时循环复用。
// 单个应用上游检查失败不阻塞整体（下一轮重试）。
func (c *companion) runFullSync() (softAdded, softUpdated, checked, updated int, err error) {
	ctx, cancel := timeoutCtx(context.Background(), 15*time.Minute)
	defer cancel()

	// 1) 软仓同步（软仓应用的上游即软仓）。
	softAdded, softUpdated, err = c.syncSoftstore(ctx)
	c.syncStateMu.Lock()
	c.lastSync = time.Now()
	if err != nil {
		c.syncErr = err.Error()
	} else {
		c.syncErr = ""
	}
	c.syncStateMu.Unlock()
	if err != nil {
		return softAdded, softUpdated, 0, 0, err
	}

	// 2) 索引里其余应用（GitHub/Gitee 来源）的上游更新检查。
	for _, ia := range c.idx.list() {
		if c.isSoftstoreApp(ia) || strings.TrimSpace(ia.Source) == "" {
			continue
		}
		checked++
		res, cerr := c.checkAppUpdateCore(ctx, ia)
		if cerr != nil {
			continue
		}
		if res.updated {
			updated++
		}
	}

	// 3) 重建聚合 + 触发 Moo 源同步（整轮一次）。
	if _, rerr := c.rebuildAggregate(); rerr != nil {
		return softAdded, softUpdated, checked, updated, rerr
	}
	if c.moo.ping() == nil {
		if src, serr := c.ensureMateSource(); serr == nil {
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	return softAdded, softUpdated, checked, updated, nil
}

// handleSoftstoreSync POST /api/softstore-sync：手动触发一次软仓同步。
func (c *companion) handleSoftstoreSync(w http.ResponseWriter, r *http.Request) {
	added, updated, err := c.runSoftstoreSync()
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"added":   added,
		"updated": updated,
		"message": fmt.Sprintf("软仓同步完成：新增 %d、更新 %d", added, updated),
	})
}

// syncState 返回当前软仓同步状态（供 /api/status 透出）。
func (c *companion) syncState() (last time.Time, errMsg string) {
	c.syncStateMu.Lock()
	defer c.syncStateMu.Unlock()
	return c.lastSync, c.syncErr
}
