package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
	"moo/internal/panel"
	"moo/internal/platform"
)

// OfficialSourceID 官方应用中心在目录/源列表中的标识。
const OfficialSourceID = "fnos-official"

// Panel 是官方应用中心直连组件（面板未配置时为 nil）。
type Panel struct {
	mu     sync.Mutex
	client *panel.Client
	apps   []AppInfo // 缓存
	appsAt time.Time
	err    string

	iconCacheMu sync.Mutex
	iconCache   map[string]iconEntry

	// detailCache：官方应用 app/detail 缓存（desc/开发者/发布者/安装体积/截图）。
	// 面板 app/list 不带描述字段，列表简介靠后台逐条回填（ensureBackfill）。
	detailMu    sync.Mutex
	detailCache map[string]detailEntry
	backfilling bool
}

// detailEntry 区分"取过且有数据"与"取过但失败/为空"：失败条目下次回填时重试。
type detailEntry struct {
	detail panel.PanelDetail
	ok     bool
}

type iconEntry struct {
	data  []byte
	ctype string
	at    time.Time
}

const iconCacheTTL = 24 * time.Hour

// NewPanel 从配置构造；未配置或账号不全返回 nil。
func NewPanel(cfg *config.Config) *Panel {
	if !cfg.PanelEnabled || strings.TrimSpace(cfg.PanelUsername) == "" || strings.TrimSpace(cfg.PanelPassword) == "" {
		return nil
	}
	// 安全兜底（2026-09-27 审核）：存量配置里 panel_base_url 若非本地
	// 回环（旧版未校验时可写入外网地址），口令会被发往该地址——直接
	// 回退默认本地面板并记日志（设置页新写入已被限制为回环）。
	base := cfg.PanelBaseURL
	if tb := strings.TrimSpace(base); tb != "" {
		if ub, err := url.Parse(tb); err != nil || !netguard.IsLoopback(ub.Hostname()) {
			log.Printf("面板地址 %s 非本地回环，安全策略回退默认 127.0.0.1:5666", tb)
			base = ""
		}
	}
	client := panel.NewClient(base, cfg.PanelUsername, cfg.PanelPassword)
	return &Panel{
		client:      client,
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
}

// Enabled 面板通道是否可用。
func (s *Panel) Enabled() bool { return s != nil && s.client != nil && s.client.Configured() }

// Client 返回底层面板客户端。
func (s *Panel) Client() *panel.Client {
	if s == nil {
		return nil
	}
	return s.client
}

// Apps 官方目录（30 分钟缓存；拉取失败保留旧缓存并记录错误）。
func (p *Panel) Apps(ctx context.Context) ([]AppInfo, string) {
	if p == nil {
		return nil, ""
	}
	p.mu.Lock()
	fresh := p.appsAt.IsZero() || time.Since(p.appsAt) < 30*time.Minute
	if fresh && len(p.apps) > 0 {
		cachedErr := p.err
		p.mu.Unlock()
		p.ensureBackfill()
		return p.appsSnapshot(), cachedErr
	}
	p.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	list, err := p.client.AppList(cctx)
	apps := make([]AppInfo, 0, len(list))
	if err == nil {
		for _, a := range list {
			ai := AppInfo{
				Key:           a.AppName + "@" + OfficialSourceID,
				AppName:       a.AppName,
				DisplayName:   a.Name,
				LatestVersion: a.Version,
				IconURL:       a.Icon,
				Source:        OfficialSourceID,
				Platform:      "fnos",
			}
			if a.Download > 0 {
				d := int(a.Download)
				ai.DownloadCount = &d
			}
			if len(a.Tags) > 0 {
				ai.Category = a.Tags[0]
			}
			if a.Docker {
				ai.AppType = "docker"
			} else {
				ai.AppType = "fpk"
			}
			apps = append(apps, ai)
		}
	}
	p.mu.Lock()
	if err == nil {
		p.apps = apps
		p.appsAt = time.Now()
		p.err = ""
	}
	perr := p.err
	if err != nil {
		p.err = err.Error()
		perr = p.err
	}
	p.mu.Unlock()
	p.ensureBackfill()
	return p.appsSnapshot(), perr
}

// appsSnapshot 返回缓存列表副本，并从详情缓存回填官方应用的
// 描述/开发者/发布者（列表载荷瘦身：desc 由后台异步补齐）。
func (p *Panel) appsSnapshot() []AppInfo {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	out := make([]AppInfo, len(p.apps))
	copy(out, p.apps)
	p.mu.Unlock()
	p.enrichOfficial(out)
	return out
}

// enrichOfficial 用 detailCache 填充官方目录条目的描述与归属信息（幂等）。
func (p *Panel) enrichOfficial(apps []AppInfo) {
	if p == nil || len(apps) == 0 {
		return
	}
	for i := range apps {
		a := &apps[i]
		if a.Source != OfficialSourceID || a.Description != "" {
			continue
		}
		d, ok := p.detailGet(a.AppName)
		if !ok {
			continue
		}
		a.Description = d.AppDetail.Desc
		if a.Maintainer == "" {
			a.Maintainer = d.AppDetail.Maintainer
		}
		if a.MaintainerURL == "" {
			a.MaintainerURL = d.AppDetail.MaintainerURL
		}
		if a.Distributor == "" {
			a.Distributor = d.AppDetail.Distributor
		}
		if a.DistributorURL == "" {
			a.DistributorURL = d.AppDetail.DistributorURL
		}
		if a.SizeBytes == 0 && d.AppDetail.InstallSize > 0 {
			a.SizeBytes = d.AppDetail.InstallSize
		}
	}
}

// detailGet 查官方应用详情缓存（失败/未取的条目视为不存在）。
func (p *Panel) detailGet(appName string) (panel.PanelDetail, bool) {
	if p == nil {
		return panel.PanelDetail{}, false
	}
	p.detailMu.Lock()
	defer p.detailMu.Unlock()
	e, ok := p.detailCache[appName]
	if !ok || !e.ok {
		return panel.PanelDetail{}, false
	}
	return e.detail, true
}

// detailSet 写官方详情进常驻缓存（与 ensureBackfill 同格式）。
func (p *Panel) detailSet(appName string, d panel.PanelDetail) {
	if p == nil {
		return
	}
	p.detailMu.Lock()
	p.detailCache[appName] = detailEntry{detail: d, ok: true}
	p.detailMu.Unlock()
}

// ensureBackfill 异步回填官方应用详情：面板 app/list 不带描述字段，
// 列表简介/详情页归属信息必须逐条 app/detail 拉取（New Store 1.20.12
// 同款方案）。并发 8、单轮预算 10 分钟；成功条目常驻缓存，失败条目
// 下一轮（下次目录刷新/进程重启）重试。
func (p *Panel) ensureBackfill() {
	if p == nil || p.client == nil {
		return
	}
	p.mu.Lock()
	names := make([]string, 0, len(p.apps))
	for _, a := range p.apps {
		names = append(names, a.AppName)
	}
	p.mu.Unlock()
	if len(names) == 0 {
		return
	}

	p.detailMu.Lock()
	if p.backfilling {
		p.detailMu.Unlock()
		return
	}
	missing := 0
	for _, n := range names {
		if e, ok := p.detailCache[n]; !ok || !e.ok {
			missing++
		}
	}
	if missing == 0 {
		p.detailMu.Unlock()
		return
	}
	p.backfilling = true
	p.detailMu.Unlock()

	go func() {
		defer func() {
			p.detailMu.Lock()
			p.backfilling = false
			p.detailMu.Unlock()
		}()
		deadline := time.Now().Add(10 * time.Minute)
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, n := range names {
			p.detailMu.Lock()
			if e, ok := p.detailCache[n]; ok && e.ok {
				p.detailMu.Unlock()
				continue
			}
			p.detailMu.Unlock()
			if time.Now().After(deadline) {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(name string) {
				defer wg.Done()
				defer func() { <-sem }()
				cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				d, err := p.client.AppDetail(cctx, name)
				p.detailMu.Lock()
				if err == nil && d != nil {
					p.detailCache[name] = detailEntry{detail: *d, ok: true}
				} else if _, done := p.detailCache[name]; !done {
					// 仅首记失败（ok=false），保留下一轮重试机会
					p.detailCache[name] = detailEntry{detail: panel.PanelDetail{AppName: name}, ok: false}
				}
				p.detailMu.Unlock()
			}(n)
		}
		wg.Wait()
	}()
}

// sourceEntry 官方源在 /api/sources 的条目（置顶，不可删）。
func (p *Panel) sourceEntry() SourceEntry {
	if p == nil || !p.Enabled() {
		return SourceEntry{}
	}
	n := len(p.apps)
	e := SourceEntry{
		ID:       OfficialSourceID,
		Name:     "飞牛应用中心",
		URL:      p.client.BaseURL,
		AppCount: n,
		Enabled:  true,
	}
	if !p.appsAt.IsZero() {
		e.LastFetched = p.appsAt.Format(time.RFC3339)
	}
	return e
}

// panelTest POST /api/panel/test：面板登录实测（可带表单覆盖值）。
func (s *Server) panelTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		BaseURL  string `json:"base_url"`
	}
	_ = jsonDecode(r, &body)

	var client *panel.Client
	if strings.TrimSpace(body.Username) != "" || body.Password != "" || strings.TrimSpace(body.BaseURL) != "" {
		client = panel.NewClient(body.BaseURL, body.Username, body.Password)
	} else if s.Panel.Enabled() {
		client = s.Panel.Client()
	}
	if client == nil || !client.Configured() {
		writeErr(w, http.StatusBadRequest, errors.New("面板账号未配置：请在设置里填写面板账号（可先测试再保存）"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	count, err := client.TestLogin(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "app_count": count})
}

// panelInstallParams 官方安装的附加参数（?panel=<json>）。
type panelInstallParams struct {
	VolumeID int `json:"volumeID"`
}

// parsePanelParams 解析 ?panel 参数。
func parsePanelParams(r *http.Request) panelInstallParams {
	var p panelInstallParams
	if raw := r.URL.Query().Get("panel"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	return p
}

// isPanelApp 目录条目是否走面板 cloud 通道。
func (s *Server) isPanelApp(ai AppInfo) bool {
	return ai.Source == OfficialSourceID && s.Panel.Enabled()
}

// panelApp 查官方目录里的应用（sourceID 等安装所需字段）。
func (s *Server) panelApp(appName string) (*panel.PanelApp, error) {
	list, err := s.Panel.Client().AppList(context.Background())
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].AppName == appName {
			return &list[i], nil
		}
	}
	return nil, errors.New("官方目录里没有该应用（请点「立即检查」后重试）")
}

// panelDefaultVolume cloud 安装建议卷。
func (s *Server) panelDefaultVolume() int {
	if s.Cfg.InstallVolume > 0 {
		return s.Cfg.InstallVolume
	}
	return 1
}

// runPanelInstall 官方 cloud 安装：依赖（自动填充）→ 主应用 → 验证。
func (s *Server) runPanelInstall(ctx context.Context, appName string, pparams panelInstallParams, wizardParams []panel.WizardParam, progress func(string, float64)) error {
	client := s.Panel.Client()
	volume := pparams.VolumeID
	if volume <= 0 {
		volume = s.panelDefaultVolume()
	}

	// 已安装官方应用 → 就地升级（与应用中心 UI「更新」按钮同一路径）。
	if ia, err := platform.SingleInstalled(ctx, appName); err == nil && ia != nil && ia.Version != "" {
		return s.runOfficialUpgrade(ctx, appName, wizardParams, progress)
	}

	detail, err := client.AppDetail(ctx, appName)
	if err != nil {
		return fmt.Errorf("获取应用详情失败: %w", err)
	}

	// 1) 依赖：未安装的按向导自动填充后顺序安装
	for _, dep := range detail.InstallDepApps {
		if dep.Status != "noinstall" {
			progress(fmt.Sprintf("依赖 %s 已安装（%s），跳过", dep.Name, dep.Status), 2)
			continue
		}
		progress(fmt.Sprintf("安装依赖 %s v%s…", dep.Name, dep.Version), 4)
		if err := s.panelInstallOne(ctx, client, dep.AppName, dep.SourceID, dep.Version, volume, nil, progress); err != nil {
			return fmt.Errorf("依赖 %s 安装失败: %w", dep.Name, err)
		}
	}

	// 2) 主应用
	pa, err := s.panelApp(appName)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("安装 %s v%s…", appName, pa.Version), 10)
	if err := s.panelInstallOne(ctx, client, appName, pa.SourceID, pa.Version, volume, wizardParams, progress); err != nil {
		return err
	}

	// 3) 验证出现在已安装列表
	return s.verifyPanelInstalled(ctx, appName)
}

// runOfficialUpgrade 已装官方应用就地升级（与应用中心 UI「更新」按钮同一
// 路径，保留 @appdata）。手动更新与「自动更新应用」共用。
// 目标版本优先取 daemon upgradeInfo（平台权威），目录版本兜底：目录滞后时
// 走下载通道只会拉到目录里的旧包（实测 trim.preview：目录 0.1.16 vs
// 升级目标 0.2.4）。无更高版本时返回错误（调用方展示/记录）。
func (s *Server) runOfficialUpgrade(ctx context.Context, appName string, wizardParams []panel.WizardParam, progress func(string, float64)) error {
	ia, err := platform.SingleInstalled(ctx, appName)
	if err != nil || ia == nil || ia.Version == "" {
		return fmt.Errorf("%s 不在已装列表，无法升级", appName)
	}
	target := ""
	if ia.UpgradeInfo != nil && ia.UpgradeInfo.Version != "" {
		target = ia.UpgradeInfo.Version
	}
	if pa, err := s.panelApp(appName); err == nil && pa != nil && pa.Version != "" &&
		(target == "" || compareVersions(pa.Version, target) > 0) {
		target = pa.Version
	}
	if target == "" || compareVersions(target, ia.Version) <= 0 {
		return fmt.Errorf("没有可更新的更高版本（当前 %s）", ia.Version)
	}
	progress(fmt.Sprintf("升级 %s → v%s（官方通道，保留数据）…", appName, target), 2)
	params := make([]platform.WizardParam, 0, len(wizardParams))
	for _, p := range wizardParams {
		params = append(params, platform.WizardParam{Key: p.Key, Value: p.Value})
	}
	if err := platform.UpgradeCloud(ctx, appName, ia.SourceID, target, params, func(pct float64) {
		progress(fmt.Sprintf("升级 %s → v%s…", appName, target), pct)
	}); err != nil {
		return err
	}
	if ia2, err := platform.SingleInstalled(ctx, appName); err == nil && ia2 != nil && ia2.Version != "" {
		if compareVersions(ia2.Version, target) < 0 {
			return fmt.Errorf("升级未生效: 当前版本 %s，预期 %s", ia2.Version, target)
		}
	}
	progress(fmt.Sprintf("%s 已升级到 v%s", appName, target), 100)
	return nil
}

// panelInstallOne 单个官方应用：cloud 下载 → install/task → 轮询。
func (s *Server) panelInstallOne(ctx context.Context, client *panel.Client, appName, sourceID, version string, volume int, customParams []panel.WizardParam, progress func(string, float64)) error {
	if sourceID == "" {
		return errors.New("缺少面板 sourceID（目录数据可能过期，请点「立即检查」后重试）")
	}
	dlID, err := client.DownloadTask(ctx, appName, sourceID, version, volume)
	if err != nil {
		return fmt.Errorf("启动官方下载失败: %w", err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("官方下载超时")
		}
		st, err := client.DownloadStatus(ctx, dlID)
		if err != nil {
			return err
		}
		if st.Status == panel.TaskSuccess {
			break
		}
		if st.Status == panel.TaskFailed {
			return fmt.Errorf("官方下载失败: %s", st.Message)
		}
		pct := int(st.Progress * 100)
		if pct < 0 {
			pct = 0
		}
		if pct > 95 {
			pct = 95
		}
		progress(fmt.Sprintf("从官方应用中心下载 %s… %d%%", appName, pct), float64(pct))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	taskID, err := client.InstallTask(ctx, appName, version, volume, customParams)
	if err != nil {
		return fmt.Errorf("提交安装失败: %w", err)
	}
	deadline = time.Now().Add(15 * time.Minute)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("安装超时")
		}
		st, err := client.InstallStatus(ctx, taskID)
		if err != nil {
			return err
		}
		switch st.Status {
		case panel.TaskSuccess:
			return nil
		case panel.TaskFailed:
			msg := st.OutputText
			if msg == "" {
				msg = "未知错误"
			}
			return fmt.Errorf("安装失败: %s", msg)
		default:
			pct := int(st.Progress * 100)
			if pct > 99 {
				pct = 99
			}
			progress(fmt.Sprintf("安装 %s… %d%%", appName, pct), float64(pct))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// verifyPanelInstalled 等待官方应用出现在 daemon 已安装列表。
func (s *Server) verifyPanelInstalled(ctx context.Context, appName string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if list, err := platform.ListInstalled(ctx); err == nil {
			for _, a := range list {
				if a.AppName == appName {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return errors.New("安装已提交，但 60s 内未出现在已安装列表")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// panelWizard GET /api/apps/{key}/wizard（官方应用）：先触发官方下载，再取 install/info。
func (s *Server) panelWizard(w http.ResponseWriter, r *http.Request, appName string) {
	client := s.Panel.Client()
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
	defer cancel()
	pa, err := s.panelApp(appName)
	if err != nil {
		writeJSON(w, map[string]any{"appname": appName, "has_wizard": false, "error": err.Error()})
		return
	}
	if _, err := s.panelDownloadOnly(ctx, client, appName, pa.SourceID, pa.Version, s.panelDefaultVolume()); err != nil {
		writeJSON(w, map[string]any{"appname": appName, "has_wizard": false, "error": err.Error()})
		return
	}
	info, err := client.InstallInfo(ctx, appName, pa.Version)
	if err != nil {
		writeJSON(w, map[string]any{"appname": appName, "has_wizard": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"appname":           appName,
		"version":           pa.Version,
		"has_wizard":        info.WizardInfo.HasWizard,
		"content":           info.WizardInfo.WizardContent,
		"install_volume_id": s.panelDefaultVolume(),
	})
}

// panelDownloadOnly 仅触发官方下载并等待完成（向导前置）。
// 返回下载完成后安装包在面板下载目录（/vol1/appcenter-downloads/）的路径：
// FPK 应用 = .fpk 文件；原生应用 = TPK 解压目录（无 FPK 安装包）。
func (s *Server) panelDownloadOnly(ctx context.Context, client *panel.Client, appName, sourceID, version string, volume int) (string, error) {
	dlID, err := client.DownloadTask(ctx, appName, sourceID, version, volume)
	if err != nil {
		return "", fmt.Errorf("启动官方下载失败: %w", err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if time.Now().After(deadline) {
			return "", errors.New("官方下载超时")
		}
		st, err := client.DownloadStatus(ctx, dlID)
		if err != nil {
			return "", err
		}
		if st.Status == panel.TaskSuccess {
			return st.Path, nil
		}
		if st.Status == panel.TaskFailed {
			return "", fmt.Errorf("官方下载失败: %s", st.Message)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// panelDetail GET /api/apps/{key}/panel-detail：官方应用详情 + 依赖 + 建议卷。
// 详情优先走 ensureBackfill 的常驻缓存（warm 后零网络，打开详情不再「卡一下」）；
// 缓存未命中（首开/回填未完成）才实时请求面板，并把结果写回缓存。
func (s *Server) panelDetail(w http.ResponseWriter, r *http.Request, appName string, catalog []AppInfo) {
	var detail panel.PanelDetail
	if d, ok := s.Panel.detailGet(appName); ok {
		detail = d
	} else {
		d, err := s.Panel.Client().AppDetail(r.Context(), appName)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		s.Panel.detailSet(appName, *d)
		detail = *d
	}
	// 依赖同名条目（目录里已有的同名应用，供「用已有的」选择）：
	// catalog 由调用方（appPanelDetail）构建一次传入，避免重复 buildCatalog。
	sameName := make(map[string][]string)
	for _, dep := range detail.InstallDepApps {
		var names []string
		for _, other := range catalog {
			if other.AppName == dep.AppName {
				label := other.DisplayName + " v" + other.InstalledVersion
				if other.Source != "" && other.Source != OfficialSourceID {
					label += "（" + other.Source + "）"
				}
				names = append(names, label)
			}
		}
		if len(names) > 0 {
			sameName[dep.AppName] = names
		}
	}
	writeJSON(w, map[string]any{
		"app":            detail,
		"volume":         s.panelDefaultVolume(),
		"same_name_apps": sameName,
	})
}

// panelIconFetch 以面板身份取官方图标（24h 内存缓存），只取数不写响应，
// 便于 appAsset 编排「官方通道失败 → 社区通道兜底」。
func (s *Panel) panelIconFetch(ctx context.Context, key, iconURL string) ([]byte, string, error) {
	p := s
	p.iconCacheMu.Lock()
	if e, ok := p.iconCache[key]; ok && time.Since(e.at) < iconCacheTTL {
		p.iconCacheMu.Unlock()
		return e.data, e.ctype, nil
	}
	p.iconCacheMu.Unlock()

	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, ctype, err := p.client.Fetch(fctx, iconURL)
	if err != nil {
		return nil, "", err
	}
	if ctype == "" {
		ctype = "image/png"
	}
	// 0.6.201 官方图标同样降采样（2026-09 实测 clientlink 官方图标 783KB）；
	// 缩后字节进 24h 缓存，避免每次请求重传全尺寸原图
	data, ctype, _ = downsampleIcon(data, ctype)
	p.iconCacheMu.Lock()
	p.iconCache[key] = iconEntry{data: data, ctype: ctype, at: time.Now()}
	if len(p.iconCache) > 1000 {
		var keys []string
		for k := range p.iconCache {
			keys = append(keys, k)
		}
		for i := 0; i < len(keys)/2; i++ {
			delete(p.iconCache, keys[i])
		}
	}
	p.iconCacheMu.Unlock()
	return data, ctype, nil
}
