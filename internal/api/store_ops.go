package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/lang"
	"moo/internal/netguard"
	"moo/internal/netx"
	"moo/internal/operation"
	"moo/internal/panel"
	"moo/internal/platform"
	"moo/internal/source"
	"moo/internal/task"
)

// appCenterDir 是飞牛应用中心程序目录（本地图标回退用）。
const appCenterDir = "/vol1/@appcenter"

// resolveKey 把目录 key（appname 或 appname@源名）解析为源名 + 应用。
func (s *Server) resolveKey(key string) (string, *source.App, error) {
	a, err := s.Src.GetByKey(key)
	if err != nil {
		return "", nil, err
	}
	return a.Source, a, nil
}

// daemonAppNameFor 解析 daemon 通道操作（启动/停用/卸载/暂停-继续）用的
// appname。源目录 key → 源应用的 appname；裸 key（自装 FPK / 系统自带
// 应用不在任何源目录里，如 fndepot/moo，卡片 key 就是 appname）→ 直接用
// key 本身（带 @源名 后缀时剥掉——源已删除的残留 key 也尽量还原 appname）。
// key 非法时不在此拦截：交给 daemon 报「应用不存在」，错误语义保持诚实。
// （停用按钮对自装应用恒 404「应用不存在: fndepot」的根因修复。）
//
// 0.6.208：daemon 以安装时的 appname 记账，与源 feed appname 大小写可能
// 不一致（daemon "Gitea" vs feed "gitea"）；已装应用的规范卡 key 恰是
// daemon appname。故已装条目优先按 EqualFold 解析出 daemon 名，避免把
// feed 名（大小写不同者）交给 daemon 导致启动/停用/卸载落空。
func (s *Server) daemonAppNameFor(ctx context.Context, key string) string {
	if s.isOfficialKey(key) {
		ai, _ := s.catalogByKey(key)
		return ai.AppName
	}
	bare := key
	if i := strings.Index(key, "@"); i > 0 {
		bare = key[:i]
	}
	if installed, err := platform.ListInstalled(ctx); err == nil {
		for _, ia := range installed {
			if strings.EqualFold(ia.AppName, key) || strings.EqualFold(ia.AppName, bare) {
				return ia.AppName
			}
		}
	}
	if _, a, err := s.resolveKey(key); err == nil {
		return a.Name
	}
	if i := strings.Index(key, "@"); i > 0 {
		return key[:i]
	}
	return key
}

// catalogByKey 从合并目录（含官方应用中心）按 key 查应用。
// 兼容两种 key 形态：精确 key；以及 appname@fnos-official（已装官方应用
// 的条目 key 是裸 appname，前端两种形态都可能出现）。
// catalogByKey 在目录里按 key 查找条目。目录只构建一次（buildCatalog
// 含已装状态合并，单次 ~50ms；此前每个回退分支各构建一次，panel-detail
// 一条请求最多触发 7 次 → 详情页「卡一下」的主因之一）。
func (s *Server) catalogByKey(key string) (AppInfo, bool) {
	// 0.6.269：目录语言——查表用（key 匹配与语言无关），统一按后台语言取。
	catalog := s.cachedCatalog(s.bgLang())
	return catalogByKeyIn(catalog, key)
}

// catalogByKeyIn 在已构建的目录切片里查找（调用方复用同一次构建结果）。
func catalogByKeyIn(catalog []AppInfo, key string) (AppInfo, bool) {
	for _, a := range catalog {
		if a.Key == key {
			return a, true
		}
	}
	if rest, ok := strings.CutSuffix(key, "@"+OfficialSourceID); ok {
		for _, a := range catalog {
			if a.AppName == rest && a.Source == OfficialSourceID {
				return a, true
			}
		}
	}
	// 裸 appname 回退：纯官方卡 Key 带 @fnos-official 后缀，但前端多处按
	// 裸 appname 请求（panel-detail 取 poster 等）。只认官方卡——社区重名
	// 条目 Source 不同，不会误命中。
	for _, a := range catalog {
		if a.AppName == key && a.Source == OfficialSourceID {
			return a, true
		}
	}
	return AppInfo{}, false
}

// isOfficialKey 目录条目是否走面板 cloud 通道。
// 0.6.255：官方源恒存在（不再以面板账号开关判定）。
func (s *Server) isOfficialKey(key string) bool {
	ai, ok := s.catalogByKey(key)
	return ok && ai.Source == OfficialSourceID
}

// appWizard GET /api/apps/{key}/wizard：官方应用走面板（先下载再取 install/info），
// FPK 应用暂存包后读取向导定义（不安装）。
func (s *Server) appWizard(w http.ResponseWriter, r *http.Request) {
	if s.isOfficialKey(r.PathValue("key")) {
		ai, _ := s.catalogByKey(r.PathValue("key"))
		s.panelWizard(w, r, ai.AppName)
		return
	}
	srcName, a, err := s.resolveKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	staged, err := s.Pipe.StageOnly(r.Context(), srcName, a.Name, nil)
	if err != nil {
		writeJSON(w, map[string]any{"appname": a.Name, "has_wizard": false, "error": err.Error()})
		return
	}
	wz, err := platform.FetchWizard(r.Context(), staged)
	if err != nil {
		writeJSON(w, map[string]any{"appname": a.Name, "has_wizard": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"appname":           a.Name,
		"version":           staged.Version,
		"has_wizard":        wz.HasWizard,
		"content":           wz.Content,
		"install_volume_id": wz.InstallVolumeID,
	})
}

// appInstallSSE POST /api/apps/{key}/install?wizard=[{key,value}]&panel={volumeID}。
func (s *Server) appInstallSSE(w http.ResponseWriter, r *http.Request) {
	if s.isOfficialKey(r.PathValue("key")) {
		s.panelInstallSSE(w, r, "install")
		return
	}
	srcName, a, err := s.resolveKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var params []platform.WizardParam
	if qz := r.URL.Query().Get("wizard"); qz != "" {
		if err := jsonUnmarshal([]byte(qz), &params); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("wizard 参数解析失败: "+err.Error()))
			return
		}
	}
	f := sseStart(w)
	s.sseRunOp(r, w, f, "install", a.Name, func(ctx context.Context, progress func(string, float64)) error {
		return s.Pipe.InstallWithParams(ctx, srcName, a.Name, params, progress)
	})
}

// panelInstallSSE 官方应用中心 cloud 安装/更新（SSE）。
func (s *Server) panelInstallSSE(w http.ResponseWriter, r *http.Request, opName string) {
	ai, _ := s.catalogByKey(r.PathValue("key"))
	f := sseStart(w)
	pparams := parsePanelParams(r)
	var wparams []panel.WizardParam
	if qz := r.URL.Query().Get("wizard"); qz != "" {
		_ = jsonUnmarshal([]byte(qz), &wparams)
	}
	s.sseRunOp(r, w, f, opName, ai.AppName, func(ctx context.Context, progress func(string, float64)) error {
		return s.runPanelInstall(ctx, ai.AppName, pparams, wparams, progress)
	})
}

// appUpdateSSE POST /api/apps/{key}/update：官方应用走面板 cloud 通道，其余走 FPK 升级管线。
func (s *Server) appUpdateSSE(w http.ResponseWriter, r *http.Request) {
	if s.isOfficialKey(r.PathValue("key")) {
		s.panelInstallSSE(w, r, "update")
		return
	}
	srcName, a, err := s.resolveKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	installed, _ := platform.ListInstalled(r.Context())
	// 0.6.208：daemon appname 与源 feed appname 大小写可能不一致
	//（daemon "Gitea" vs feed "gitea"），按 EqualFold 匹配，
	// 否则已装应用会被误判「尚未安装」。
	found := false
	for _, ia := range installed {
		if strings.EqualFold(ia.AppName, a.Name) {
			found = true
			if compareVersions(a.Version, ia.Version) <= 0 {
				writeErr(w, http.StatusConflict, errors.New("没有可更新的更高版本"))
				return
			}
			break
		}
	}
	if !found {
		writeErr(w, http.StatusNotFound, errors.New("应用尚未安装"))
		return
	}
	f := sseStart(w)
	s.sseRunOp(r, w, f, "update", a.Name, func(ctx context.Context, progress func(string, float64)) error {
		return s.Pipe.InstallWithParams(ctx, srcName, a.Name, nil, progress)
	})
}

// appUninstallSSE POST /api/apps/{key}/uninstall。
func (s *Server) appUninstallSSE(w http.ResponseWriter, r *http.Request) {
	appName := s.daemonAppNameFor(r.Context(), r.PathValue("key"))
	f := sseStart(w)
	s.sseRunOp(r, w, f, "uninstall", appName, func(ctx context.Context, progress func(string, float64)) error {
		return s.Pipe.Uninstall(ctx, appName, false, progress)
	})
}

// appDownloadTaskSSE POST /api/apps/{key}/download-task：FPK 应用后台下载到缓存目录；
// 官方应用走面板 cloud 下载（落到面板下载目录），完成后把 .fpk 复制进
// moo 下载缓存（设置页「FPK 下载目录」列表/删除/直接安装同一套）。
func (s *Server) appDownloadTaskSSE(w http.ResponseWriter, r *http.Request) {
	if s.isOfficialKey(r.PathValue("key")) {
		ai, _ := s.catalogByKey(r.PathValue("key"))
		f := sseStart(w)
		sseRunOpDownload := func(ctx context.Context, progress func(string, float64)) error {
			// 0.6.261：不再按 app_type 拦截——TPK 型官方应用（原生/docker）
			// 下载产物是目录，由 copyOfficialFpk 重打包为标准 FPK；真正无法
			// 产包的应用由 daemon 下载环节给出诚实报错。
			pa, err := s.panelApp(ai.AppName)
			if err != nil {
				return err
			}
			ver := pa.Version
			// 已装官方应用：目录版本可能滞后于平台可升级版本（实测
			// trim.preview 目录 0.1.16 vs 目标 0.2.4），下载目标取高者。
			if ia, err := platform.SingleInstalled(ctx, ai.AppName); err == nil && ia != nil &&
				ia.UpgradeInfo != nil && ia.UpgradeInfo.Version != "" &&
				compareVersions(ia.UpgradeInfo.Version, ver) > 0 {
				ver = ia.UpgradeInfo.Version
			}
			// 0.6.261：daemon unix socket 免登录通道（与应用中心 UI「下载」
			// 同一路径，不触发任何面板登录、不受面板限流影响）；下载过程
			// 回报进度。sourceID 由统一目录入口提供，OAuth/面板账号两通道
			// 均可用（底层同一 app/list API）。
			pth, err := platform.DownloadCloud(ctx, ai.AppName, pa.SourceID, ver, s.panelDefaultVolume(),
				func(pct float64) {
					progress(fmt.Sprintf("官方云下载中 %d%%", int(pct)), pct)
				})
			if err != nil {
				return err
			}
			progress("官方下载完成，存入 FPK 下载目录…", 90)
			return s.copyOfficialFpk(ai.AppName, ver, pth, progress)
		}
		s.sseRunOp(r, w, f, "download", ai.AppName, sseRunOpDownload)
		return
	}
	_, a, err := s.resolveKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if a.DownloadURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("该应用没有下载链接"))
		return
	}
	// A3 缓存复用：成品已完整（无 aria2 续传残留、大小已知时匹配）则跳过下载
	fileName := a.Name + ".fpk"
	sizeCands := task.ParseSizeMB(a.SizeMB)
	sizeBytes := int64(0)
	if len(sizeCands) > 0 {
		sizeBytes = sizeCands[0]
	}
	if _, ok := s.Tasks.Cached(fileName, sizeCands...); ok {
		f := sseStart(w)
		sseSend(w, f, map[string]any{"step": "done", "message": "安装包已缓存，跳过下载"})
		return
	}
	f := sseStart(w)
	t, err := s.Tasks.Start(a.Source, a.Name, s.FpkCandidates(a), fileName, sizeBytes, a.Sha256)
	if err != nil {
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	s.streamDownloadTask(r, w, f, t)
}

// copyOfficialFpk 把官方 cloud 下载完成的产物存入 moo 下载缓存目录
// （与社区源同一目录：设置页「FPK 下载目录」可列表/删除/直接安装）。
// 产物两种形态：.fpk 文件直接复制；TPK 目录（原生/docker 型官方应用，
// 布局 = manifest + app.tgz + cmd/ + config/ + wizard/ + ICON*）重打包为
// 标准 FPK（0.6.261：生命周期脚本/向导全保留，保真度高于旧的
// 「没有 FPK 安装包」拦截）。
func (s *Server) copyOfficialFpk(appName, version, srcPath string, progress func(string, float64)) error {
	if srcPath == "" {
		return errors.New("官方下载完成但未返回安装包路径")
	}
	fi, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("官方下载完成后安装包不存在: %w", err)
	}
	if fi.IsDir() {
		name := appName + "-" + version + ".fpk"
		dst := filepath.Join(s.Tasks.DownloadDir(), name)
		if err := repackTpkDirToFpk(srcPath, dst); err != nil {
			return fmt.Errorf("官方 TPK 包重打包失败: %w", err)
		}
		progress(fmt.Sprintf("已存入 FPK 下载目录: %s", name), 100)
		return nil
	}
	name := filepath.Base(srcPath)
	if !strings.HasSuffix(strings.ToLower(name), ".fpk") {
		name = appName + "-" + version + ".fpk"
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("读取安装包失败: %w", err)
	}
	dst := filepath.Join(s.Tasks.DownloadDir(), name)
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("保存安装包失败: %w", err)
	}
	progress(fmt.Sprintf("已存入 FPK 下载目录: %s", name), 100)
	return nil
}

// FpkCandidates 生成 GitHub 托管 FPK 的候选 URL 顺序（手选优先、直连兜底）：
// 手选镜像（未失效）排首位 + 吞吐测速最快的健康镜像补位（共 3 个）；
// 手选源失效时自动切到智能优选；custom 排首位；direct = 仅直连。
// 非 GitHub 链接（自建/CDN）原样返回。
func (s *Server) FpkCandidates(a *source.App) []string {
	base := a.DownloadURL
	if base == "" {
		return nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Host != "github.com" {
		return []string{base}
	}
	s.Mirrors.ensureFresh(false)
	urlByKey := map[string]string{}
	for _, opt := range config.GitHubMirrorOptions() {
		urlByKey[opt.Key] = opt.URL
	}
	// 手选优先：手动指定源（未失效）排首位，其后按吞吐优选序，共取 3 个，直连兜底
	var prefixes []string
	for _, k := range s.ghOrderedKeys() {
		if len(prefixes) >= 3 {
			break
		}
		if k == "custom" {
			if s.Cfg.CustomGitHubMirror != "" {
				prefixes = append(prefixes, ensureTrailingSlash(s.Cfg.CustomGitHubMirror))
			}
			continue
		}
		if p, ok := urlByKey[k]; ok && p != "" {
			prefixes = append(prefixes, p)
		}
	}
	out := make([]string, 0, len(prefixes)+1)
	for _, p := range prefixes {
		out = append(out, p+base)
	}
	out = append(out, base)
	return out
}

// ensureTrailingSlash 保证前缀以 / 结尾。
func ensureTrailingSlash(s string) string {
	if s == "" {
		return s
	}
	if s[len(s)-1] == '/' {
		return s
	}
	return s + "/"
}

// streamDownloadTask 轮询下载任务直至终态（download-task 与暂停/继续后恢复共用）。
func (s *Server) streamDownloadTask(r *http.Request, w http.ResponseWriter, f http.Flusher, t *task.Task) {
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(600 * time.Millisecond):
		}
		v := task.ViewOf(t)
		ev := map[string]any{
			"step":       "downloading", // 前端按此值更新进度
			"downloaded": v.Done,
			"total":      v.Size,
		}
		if v.Size > 0 {
			ev["progress"] = v.Percent
		}
		switch v.State {
		case task.StateDone:
			// 终态事件对齐满值：aria2 进度行有显示取整/尾行竞态，
			// 完成即 100%（真实字节数已由引擎落盘校验）。
			if v.Size > 0 {
				ev["downloaded"] = v.Size
				ev["progress"] = 100
			}
			ev["step"] = "done"
			ev["message"] = "下载完成"
			sseSend(w, f, ev)
			return
		case task.StateError:
			ev["step"] = "error"
			ev["error"] = v.Err
			sseSend(w, f, ev)
			return
		default:
			sseSend(w, f, ev)
		}
	}
}

// appTaskPause POST /api/apps/{key}/task/pause。
func (s *Server) appTaskPause(w http.ResponseWriter, r *http.Request) {
	appName := s.daemonAppNameFor(r.Context(), r.PathValue("key"))
	t := s.Tasks.FindByApp(appName)
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("没有进行中的下载任务"))
		return
	}
	if err := s.Tasks.Pause(t.ID); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, task.ViewOf(t))
}

// appTaskResume POST /api/apps/{key}/task/resume。
func (s *Server) appTaskResume(w http.ResponseWriter, r *http.Request) {
	appName := s.daemonAppNameFor(r.Context(), r.PathValue("key"))
	t := s.Tasks.FindByApp(appName)
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("没有进行中的下载任务"))
		return
	}
	if err := s.Tasks.Resume(t.ID); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, task.ViewOf(t))
}

// maxUpdateIgnored 忽略更新集合上限（防误操作膨胀，同收藏）。
const maxUpdateIgnored = 500

// ignoreUpdate PUT/DELETE /api/apps/{key}/ignore-update：加入/移出忽略更新
// 集合（幂等，0.6.181 起真实落库 config.UpdateIgnored，顺序 = 忽略先后）。
// 被忽略应用的更新信号在目录构建期统一抑制（HasUpdate=false +
// UpdateIgnored=true），更新角标/计数/自动更新/更新摘要通知自动排除；
// available_version 保留供发现页「忽略更新」列表展示旧→新版本。
func (s *Server) ignoreUpdate(w http.ResponseWriter, r *http.Request) {
	appName := strings.TrimSpace(s.daemonAppNameFor(r.Context(), r.PathValue("key")))
	if appName == "" {
		writeErr(w, http.StatusBadRequest, errors.New("无法解析应用标识"))
		return
	}
	add := r.Method == http.MethodPut
	ignored := s.Cfg.UpdateIgnored[:0:0] // 重建切片，避免 alias 原底层数组
	found := false
	for _, n := range s.Cfg.UpdateIgnored {
		if n == appName {
			found = true
			if add {
				ignored = append(ignored, n) // PUT 幂等：保留原位置
			}
			continue
		}
		ignored = append(ignored, n)
	}
	if add && !found {
		if len(ignored) >= maxUpdateIgnored {
			writeErr(w, http.StatusConflict, errors.New("忽略数量已达上限（500）"))
			return
		}
		ignored = append(ignored, appName)
	}
	if ignored == nil {
		ignored = []string{}
	}
	s.Cfg.UpdateIgnored = ignored
	_ = s.Cfg.Save(dataDirOf(s))
	s.invalidateCatalog() // 忽略态影响更新判定，旧缓存立即作废
	writeJSON(w, map[string]any{"ok": true, "ignored": ignored})
}

// appStart / appStop。
func (s *Server) appStart(w http.ResponseWriter, r *http.Request) {
	s.appStartStop(w, r, true)
}

func (s *Server) appStop(w http.ResponseWriter, r *http.Request) {
	s.appStartStop(w, r, false)
}

func (s *Server) appStartStop(w http.ResponseWriter, r *http.Request, start bool) {
	appName := s.daemonAppNameFor(r.Context(), r.PathValue("key"))
	kind := "stop"
	fn := func(ctx context.Context, progress func(string, float64)) error {
		return s.Pipe.StopApp(ctx, appName, progress)
	}
	if start {
		kind = "start"
		fn = func(ctx context.Context, progress func(string, float64)) error {
			return s.Pipe.StartApp(ctx, appName, progress)
		}
	}
	op, err := s.Ops.Start(kind, appName, fn)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	// 轮询到终态后返回。
	// 注意：Queue 在操作完成时会清空 current，Current() 随后返回 nil——
	// 直接解引用 nil 会 panic 掐断连接（停用/启动按钮空响应事故，
	// 操作其实已执行但客户端收到网络错误并误报「失败」）。
	// nil 即完成（与 sseRunOp 同款语义），终态从 history 按 ID 取回，
	// 保留错误信息用于回报。
	for op.State == operation.StateRunning {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(700 * time.Millisecond):
		}
		if cur := s.Ops.Current(); cur != nil {
			op = *cur
			continue
		}
		found := false
		for _, h := range s.Ops.History() {
			if h.ID == op.ID {
				op = h
				found = true
				break
			}
		}
		if !found && op.State == operation.StateRunning {
			// 极端情况（历史被 20 条上限挤出）：按完成处理
			op.State = operation.StateDone
		}
	}
	if op.State == operation.StateError {
		writeErr(w, http.StatusBadGateway, errors.New(orAnon(op.Err)))
		return
	}
	writeJSON(w, op)
}

// appPanelDetail 官方应用中心详情（描述/依赖/建议卷）；非官方应用未启用。
func (s *Server) appPanelDetail(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	// 目录走缓存：官方判定 + 条目解析 + panelDetail 同名依赖共用
	// （此前每次 buildCatalog ~50ms，isOfficialKey/catalogByKey 各自再
	// 构建会把本请求拖到 ~300ms——详情页「卡一下」的主因之一）。
	catalog := s.cachedCatalog(lang.From(r.Context()))
	ai, ok := catalogByKeyIn(catalog, key)
	// 0.6.255：官方源恒存在（纯 OAuth；未连接时 panelDetail 内部透传「未连接」）
	if !ok || ai.Source != OfficialSourceID {
		writeErr(w, http.StatusNotFound, errors.New("官方应用中心尚未连接，或该应用不走官方通道"))
		return
	}
	s.panelDetail(w, r, ai.AppName, catalog)
}

// installedDetail GET /api/installed-detail?appname=X：
// 已安装但不在任何源里的应用（自装 FPK / 系统自带）目录条目没有元数据，
// 详情页靠面板 app/detail 补描述/开发者/截图（面板对已装应用同样有详情）。
// 官方目录应用优先走回填缓存（免一次面板往返）；都拿不到返回 404。
func (s *Server) installedDetail(w http.ResponseWriter, r *http.Request) {
	appName := r.URL.Query().Get("appname")
	if appName == "" {
		writeErr(w, http.StatusBadRequest, errors.New("appname 不能为空"))
		return
	}
	// 0.6.255：走 OAuth 包装的 detailFn（未连接时返回「未连接」错误）
	if s.Panel == nil {
		writeErr(w, http.StatusNotFound, errors.New("官方应用中心通道不可用，无法补全应用详情"))
		return
	}
	if d, ok := s.Panel.detailGet(appName); ok &&
		(d.AppDetail.Desc != "" || d.AppDetail.Maintainer != "") {
		writeJSON(w, map[string]any{"app": d})
		return
	}
	detail, err := s.Panel.detailFn(r.Context(), appName)
	if err != nil || detail == nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"app": detail})
}

// appAsset GET /api/apps/{key}/asset?type=icon|readme|preview。
// icon 走后端代理（前端外部源图标统一经此端点）：应用声明的 icon_url →
// 源仓库布局候选 → 本机已装应用目录，全部失败 404（前端回退占位图）。
func (s *Server) appAsset(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("type")
	key := r.PathValue("key")
	ai, inCatalog := s.catalogByKey(key)
	// 0.6.255：官方源恒存在（纯 OAuth；图标抓取失败时下方回退社区通道/占位图）
	officialCard := inCatalog && ai.Source == OfficialSourceID && s.Panel != nil

	switch kind {
	case "icon":
		iconCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if officialCard {
			// 官方卡：优先官方图标；官方通道失败/地址无效时回退社区通道。
			// 源徽章统一会把社区条目（Key 仍指向社区源）的 Source 改成官方，
			// 此时 IconURL 还是空的或社区相对路径——只走面板通道必 404，
			// 这正是「已装官方重名应用图标缺失」的根因。
			ictx, icancel := context.WithTimeout(iconCtx, 20*time.Second)
			// 0.6.200 缓存 key 带版本：官方应用升级后图标可能随包更新，
			// 不带版本时 24h 内存层会一直供旧图标
			iconVer := ai.InstalledVersion
			if iconVer == "" {
				iconVer = ai.LatestVersion
			}
			data, ctype, perr := s.Panel.panelIconFetch(ictx, key+"@"+iconVer, s.officialIconURL(ai))
			icancel()
			if perr == nil {
				writeIcon(w, data, ctype)
				return
			}
			if _, a, rerr := s.resolveKey(key); rerr == nil {
				if data, ctype, ierr := s.resolveIcon(iconCtx, a); ierr == nil {
					writeIcon(w, data, ctype)
					return
				}
			}
			// 本机已装应用的 UI 图标（bunjs/moo 等面板图标 404 或无源条目）
			if data, ok := localAppIcon(ai.AppName); ok {
				writeIcon(w, data, "image/png")
				return
			}
			writeErr(w, http.StatusNotFound, perr)
			return
		}
		_, a, err := s.resolveKey(key)
		if err != nil {
			// 裸 key（已装应用无对应源条目，如 moo/fndepot）→ 本机 UI 图标兜底
			if data, ok := localAppIcon(key); ok {
				writeIcon(w, data, "image/png")
				return
			}
			writeErr(w, http.StatusNotFound, err)
			return
		}
		data, ctype, ierr := s.resolveIcon(iconCtx, a)
		if ierr != nil {
			writeErr(w, http.StatusNotFound, ierr)
			return
		}
		writeIcon(w, data, ctype)
	case "readme":
		// 官方卡（源徽章统一）也先走社区源 README——合并条目常带社区
		// readme_url，比面板 404 更实用；纯官方条目解析不到源即无 README
		var a *source.App
		if _, ra, rerr := s.resolveKey(key); rerr == nil {
			a = ra
		}
		if a == nil || a.ReadmeURL == "" {
			writeErr(w, http.StatusNotFound, errors.New("该应用没有 README"))
			return
		}
		s.proxyReadme(w, r, a.ReadmeURL)
	case "readme-img":
		// README 内嵌图片代理：社区 README 的图片大量指向 github.com /
		// raw.githubusercontent.com（国内浏览器直连慢/挂），前端把这类
		// src 改写到本端点，后端走镜像竞速 + 缓存取回（仅放行 github 系
		// host，其余拒绝——防 SSRF）。
		s.proxyReadmeImg(w, r)
	case "preview":
		idx, perr := strconv.Atoi(r.URL.Query().Get("index"))
		if perr != nil || idx < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("preview index 无效"))
			return
		}
		// 优先 catalog 条目：官方合并卡的 Key 指向社区源条目，但预览可能
		// 是从别的同名源借来的（borrowOfficialPreviews）——source.App 里
		// 只有合并目标源自己的 preview_urls，会误判「没有预览图」。
		var urls []string
		if ai, ok := s.catalogByKey(key); ok {
			urls = ai.PreviewURLs
		}
		if len(urls) == 0 {
			if _, a, err := s.resolveKey(key); err == nil {
				urls = a.PreviewURLs
			}
		}
		if idx >= len(urls) {
			writeErr(w, http.StatusNotFound, errors.New("该应用没有预览图"))
			return
		}
		s.proxyPreview(w, r, urls[idx])
	default:
		writeErr(w, http.StatusBadRequest, errors.New("type 必须是 icon、readme 或 preview"))
	}
}

// writeIcon 统一图标响应头（24h 浏览器缓存）。
func writeIcon(w http.ResponseWriter, data []byte, ctype string) {
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// officialIconURL 给官方卡挑一个面板通道能用的图标地址：
// 纯官方条目（已装/官方目录）IconURL 本身是面板静态地址，直接用；
// 社区条目被源徽章统一后 IconURL 仍是社区地址——改用面板官方列表
// 里同名条目的 IconURL（权威）；都没有则退回原值让通道自己判错。
func (s *Server) officialIconURL(ai AppInfo) string {
	if u := strings.TrimSpace(ai.IconURL); u != "" &&
		(strings.HasPrefix(u, "/") || strings.Contains(u, "app-center-static")) {
		return u
	}
	if s.Panel != nil {
		if official, _ := s.Panel.Apps(context.Background()); len(official) > 0 {
			for _, oa := range official {
				if oa.AppName == ai.AppName && oa.IconURL != "" {
					return oa.IconURL
				}
			}
		}
	}
	return ai.IconURL
}

// upstreamClient GitHub 上游出网（readme/图标抓取，0.6.206 起带「科学加速」
// 代理：GitHub 域名走本机代理、镜像域名直连）。不设整体超时，由调用方 ctx 封顶。
var upstreamClient = netx.NewClient(0)

// proxyAsset 代理拉取远端资源并透传（readme 等）。
func (s *Server) proxyAsset(w http.ResponseWriter, r *http.Request, rawURL, fallbackMime string) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	resp, err := upstreamClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("拉取失败: "+err.Error()))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, errors.New("源仓库返回 "+resp.Status))
		return
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = fallbackMime
	}
	w.Header().Set("Content-Type", mime)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

// readmeCandidateURLs 给 README 直链补候选：main↔master 分支互换、
// raw.githubusercontent → jsDelivr gh 变体（正确格式 gh/U/R@BRANCH/PATH——
// 旧的字符串直替会产生 gh/U/R/BRANCH/PATH，jsDelivr 301 弹回 raw，
// 候选等于绕回不稳定直链、纯浪费竞速槽位）、raw 直链附加最健康镜像
// （社区源 README 分支不统一，单链接一断详情页 README 整块失败）。
// normalizeReadmeURL 把 github.com blob/raw 页面归一化为 raw 直链：
// blob URL 抓到的是 GitHub HTML 页（整页塞进 README 区），不是 README
// 正文；相对图片补全也依赖这个 raw 目录基准。
func normalizeReadmeURL(raw string) string {
	if pu, err := url.Parse(raw); err == nil && pu.Hostname() == "github.com" {
		seg := strings.SplitN(strings.TrimPrefix(pu.Path, "/"), "/", 4)
		if len(seg) == 4 && (seg[2] == "blob" || seg[2] == "raw") {
			return "https://raw.githubusercontent.com/" + seg[0] + "/" + seg[1] + "/" + seg[3]
		}
	}
	return raw
}

func (s *Server) readmeCandidateURLs(raw string) []string {
	raw = normalizeReadmeURL(raw)
	cands := []string{raw}
	if u := strings.Replace(raw, "/main/", "/master/", 1); u != raw {
		cands = append(cands, u)
	} else if u := strings.Replace(raw, "/master/", "/main/", 1); u != raw {
		cands = append(cands, u)
	}
	const rawPrefix = "https://raw.githubusercontent.com/"
	if strings.HasPrefix(raw, rawPrefix) {
		parts := strings.SplitN(strings.TrimPrefix(raw, rawPrefix), "/", 3)
		if len(parts) == 3 {
			cands = append(cands, "https://cdn.jsdelivr.net/gh/"+parts[0]+"/"+parts[1]+"@"+parts[2])
		}
	}
	cands = append(cands, s.mirrorVariants(cands, 3)...)
	return dedupeMirrors(cands)
}

// fetchReadmeRace 并发竞速拉取 README（纯取，不写响应）。
// 返回 dead404=true 表示原始 URL 明确 404（README 确实不存在）。
// 瞬态失败（上游 429 限流窗口/镜像 5xx）重试一次：请求预算 25s、
// 单轮竞速 13s 封顶（2026-09-23 实锤：源同步风暴触发 GitHub raw
// 限流窗口，单轮竞速全部候选非 200，重试窗口内即恢复）。
func (s *Server) fetchReadmeRace(ctx context.Context, rawURL string) ([]byte, string, bool, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		body, mime, dead, err := s.raceFetch(ctx, s.readmeCandidateURLs(rawURL))
		if err == nil {
			return body, mime, false, nil
		}
		if dead {
			return nil, "", true, errors.New("源仓库返回 404（README 可能不在仓库内）")
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, "", false, lastErr
		}
	}
	return nil, "", false, errors.New("拉取失败: " + lastErr.Error())
}

// proxyReadme 并发竞速拉取 README，第一个 200 即透传；全败返回 502。
// proxyReadme 拉取并透传 README：两级缓存优先（内存 1h + 磁盘 7 天，
// 上游限流/抖动窗口内直接吃缓存，详情页不再 13s 死等）；缓存未命中走
// 候选竞速；竞速失败回退旧缓存（stale-while-revalidate）；原始 URL
// 明确 404 记负缓存 30min（真缺失不反复重试）。
func (s *Server) proxyReadme(w http.ResponseWriter, r *http.Request, rawURL string) {
	cacheKey := "readme|" + rawURL
	base := normalizeReadmeURL(rawURL)
	st := s.readmeStore()
	if body, ct, ok := st.Get(cacheKey); ok && !looksLikeWebPage(body) {
		serveReadme(w, body, ct, base)
		return
	}
	if st.IsNegative(cacheKey) {
		writeErr(w, http.StatusNotFound, errors.New("该应用没有可用 README"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	body, mime, dead, err := s.fetchReadmeRace(ctx, rawURL)
	if err == nil {
		if looksLikeWebPage(body) {
			// blob 页等完整 HTML 文档不是 README：不入缓存，本次也拒服
			// （上游声明的 readme_url 指向了 GitHub 页面本身）
			writeErr(w, http.StatusNotFound, errors.New("README 链接指向页面而非文档"))
			return
		}
		st.Put(cacheKey, body, mime)
		serveReadme(w, body, mime, base)
		return
	}
	// 竞速失败：回退旧缓存（并发请求可能刚灌进来，或磁盘层尚存）
	if body, ct, ok := st.Get(cacheKey); ok && !looksLikeWebPage(body) {
		serveReadme(w, body, ct, base)
		return
	}
	if dead {
		st.MarkNegative(cacheKey)
		writeErr(w, http.StatusNotFound, errors.New(err.Error()))
		return
	}
	writeErr(w, http.StatusBadGateway, errors.New(err.Error()))
}

// proxyPreview 代理详情页预览图：与 README 同一套「镜像竞速 + 两级缓存」，
// 缓存键 preview|<url>（7 天磁盘层，预览图随版本走、不常变）。
// 魔数终审：抓到的不是图片（HTML 页/文本）拒服不入缓存。
func (s *Server) proxyPreview(w http.ResponseWriter, r *http.Request, rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	cacheKey := "preview|" + rawURL
	st := s.readmeStore()
	if body, ct, ok := st.Get(cacheKey); ok && looksLikeImage(body) {
		servePreview(w, body, ct)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	body, mime, dead, err := s.fetchReadmeRace(ctx, rawURL)
	if err == nil {
		if !looksLikeImage(body) {
			// 上游声明的预览图地址指向了非图片内容（网页/文本）：拒服
			writeErr(w, http.StatusNotFound, errors.New("预览图地址无效（非图片）"))
			return
		}
		st.Put(cacheKey, body, mime)
		servePreview(w, body, mime)
		return
	}
	if body, ct, ok := st.Get(cacheKey); ok && looksLikeImage(body) {
		servePreview(w, body, ct)
		return
	}
	if dead {
		writeErr(w, http.StatusNotFound, errors.New("该应用没有预览图"))
		return
	}
	writeErr(w, http.StatusBadGateway, errors.New(err.Error()))
}

// servePreview 统一预览图响应：按魔数纠正 content-type + 1h 浏览器缓存。
func servePreview(w http.ResponseWriter, body []byte, ct string) {
	switch {
	case len(body) >= 8 && body[0] == 0x89 && body[1] == 'P' && body[2] == 'N' && body[3] == 'G':
		ct = "image/png"
	case len(body) >= 3 && body[0] == 0xFF && body[1] == 0xD8 && body[2] == 0xFF:
		ct = "image/jpeg"
	case len(body) >= 12 && string(body[0:4]) == "RIFF" && string(body[8:12]) == "WEBP":
		ct = "image/webp"
	case len(body) >= 6 && string(body[0:6]) == "GIF87a":
		ct = "image/gif"
	case len(body) >= 6 && string(body[0:6]) == "GIF89a":
		ct = "image/gif"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

// serveReadme 统一 README 响应：serve 时按 README 所在目录把相对路径
// 图片补成绝对直链（幂等——已是绝对的不再匹配；github 系补全后前端
// 自动改走 readme-img 代理）+ 1h 浏览器缓存。
func serveReadme(w http.ResponseWriter, body []byte, ct string, readmeBase string) {
	body = rewriteRelImages(body, readmeBase)
	if ct == "" {
		ct = "text/markdown; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

// github 系图片 host 白名单（readme-img 代理只放行这些，防 SSRF）。
var readmeImgHosts = map[string]bool{
	"raw.githubusercontent.com":         true,
	"github.com":                        true,
	"github.githubassets.com":           true,
	"user-images.githubusercontent.com": true,
	"camo.githubusercontent.com":        true,
	"codeload.github.com":               true,
	"objects.githubusercontent.com":     true,
}

// proxyReadmeImg README 内嵌图片代理（type=readme-img&u=<原始URL>）：
// github 系走镜像竞速（raw 直链补 main↔master/jsDelivr/镜像候选，
// github.com 资源走 gh-proxy 全路径代理），命中两级缓存（7 天）。
func (s *Server) proxyReadmeImg(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("u")
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "https" && pu.Scheme != "http") || !readmeImgHosts[pu.Hostname()] {
		writeErr(w, http.StatusBadRequest, errors.New("仅支持 github 系图片代理"))
		return
	}
	cacheKey := "readme-img|" + u
	st := s.readmeStore()
	if body, ct, ok := st.Get(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(body)
		return
	}
	// 候选构造：raw 直链 → 完整竞速链；github.com/<u>/<r>/raw/BR/PATH →
	// 转 raw 直链再竞速；其余 github 资源（release 附件等）→ 原链 +
	// gh-proxy 全路径变体
	var cands []string
	switch pu.Hostname() {
	case "raw.githubusercontent.com":
		cands = s.readmeCandidateURLs(u)
	case "github.com":
		seg := strings.SplitN(strings.TrimPrefix(pu.Path, "/"), "/", 4)
		if len(seg) == 4 && seg[2] == "raw" {
			cands = s.readmeCandidateURLs("https://raw.githubusercontent.com/" + seg[0] + "/" + seg[1] + "/" + seg[3])
		} else {
			cands = []string{u}
			for _, st2 := range s.Mirrors.gh() {
				if st2.Status != "ok" {
					continue
				}
				if prefix := mirrorURLByKey(st2.Key); prefix != "" {
					cands = append(cands, prefix+u)
				}
			}
		}
	default:
		cands = []string{u}
		for _, st2 := range s.Mirrors.gh() {
			if st2.Status != "ok" {
				continue
			}
			if prefix := mirrorURLByKey(st2.Key); prefix != "" {
				cands = append(cands, prefix+u)
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	body, mime, dead, rerr := s.raceFetch(ctx, cands)
	if rerr != nil && !dead && ctx.Err() == nil {
		// 瞬态失败（raw 限流/黑洞窗口）：剩余预算内重试一次竞速
		body, mime, dead, rerr = s.raceFetch(ctx, cands)
	}
	if rerr == nil {
		if mime == "" || mime == "text/plain" || mime == "text/html" {
			if ct := detectImageCT(body); ct != "" {
				mime = ct
			}
		}
		st.Put(cacheKey, body, mime)
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(body)
		return
	}
	if dead {
		st.MarkNegative(cacheKey)
		writeErr(w, http.StatusNotFound, errors.New("图片源 404"))
		return
	}
	writeErr(w, http.StatusBadGateway, errors.New("图片拉取失败: "+rerr.Error()))
}

// raceFetch 通用候选竞速（README/图片共用）：任一 200 即胜，全败带
// dead404 判定（仅原始 URL 404 算真缺失）。
func (s *Server) raceFetch(ctx context.Context, cands []string) ([]byte, string, bool, error) {
	type rres struct {
		u      string
		body   []byte
		mime   string
		status int
	}
	log.Printf("[race] 候选 %d 个: %v", len(cands), cands)
	ch := make(chan rres, len(cands))
	for _, u := range cands {
		go func(u string) {
			t0 := time.Now()
			// 安全（2026-09-27 审核）：候选含源数据驱动的任意 URL
			// （readme_url/preview_urls/icon_url，发布者可控）——非公共
			// 地址（内网/环回/元数据）拒绝抓取，防 SSRF。
			if _, gerr := netguard.Public(u); gerr != nil {
				log.Printf("[race] 跳过（安全策略）%s: %v", u, gerr)
				ch <- rres{u: u, status: http.StatusForbidden}
				return
			}
			cctx, c2 := context.WithTimeout(ctx, 12*time.Second)
			defer c2()
			resp, err := upstreamClient.Do((&http.Request{Method: http.MethodGet, URL: mustParseURL(u)}).WithContext(cctx))
			if err != nil {
				// 胜出后落选者被父 ctx 取消（context canceled）属正常，
				// 不记日志；只记真实网络错误。
				if !errors.Is(err, context.Canceled) {
					log.Printf("[race] ERR %s: %v (t=%v)", u, err, time.Since(t0).Round(time.Millisecond))
				}
				return
			}
			defer resp.Body.Close()
			log.Printf("[race] %d %s (t=%v)", resp.StatusCode, u, time.Since(t0).Round(time.Millisecond))
			if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusForbidden {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
				mime := resp.Header.Get("Content-Type")
				if mime == "" {
					mime = detectImageCT(b)
					if mime == "" {
						mime = "application/octet-stream"
					}
				}
				ch <- rres{u, b, mime, resp.StatusCode}
				return
			}
			ch <- rres{u, nil, "", resp.StatusCode}
		}(u)
	}
	deadline := time.After(13 * time.Second)
	nf404 := 0
	for i := 0; i < len(cands); i++ {
		select {
		case res := <-ch:
			if res.status == http.StatusOK && len(res.body) > 0 {
				return res.body, res.mime, false, nil
			}
			// 404 证据：仅认直链/镜像（路径 1:1 语义）；jsDelivr 对
			// 非默认分支/未收录仓库的 404/301 有歧义，不作证据。
			// 两个独立来源都 404 → 文件真缺失，快速判死（不再让页面
			// 等满 13s；GenOffice 镜像仓库缺图 + raw 黑洞实锤）。
			if res.status == http.StatusNotFound && !strings.Contains(res.u, "cdn.jsdelivr.net") {
				nf404++
				if nf404 >= 2 {
					return nil, "", true, errors.New("源 404")
				}
			}
		case <-ctx.Done():
			return nil, "", nf404 >= 2, errors.New("拉取超时")
		case <-deadline:
			return nil, "", nf404 >= 2, errors.New("拉取超时")
		}
	}
	if nf404 >= 2 {
		return nil, "", true, errors.New("源 404")
	}
	return nil, "", false, errors.New("所有候选均不可达")
}

// looksLikeWebPage 判断缓存体是否完整 HTML 文档（GitHub 页面等）。
// 合法 HTML README 是片段（<p>/<div> 开头），完整文档（<!DOCTYPE/<html/
// <head）几乎必然是抓错了页面。
func looksLikeWebPage(b []byte) bool {
	t := strings.ToLower(strings.TrimLeft(string(b[:min(160, len(b))]), " \t\r\n"))
	return strings.HasPrefix(t, "<!doctype") || strings.HasPrefix(t, "<html") || strings.HasPrefix(t, "<head")
}

// detectImageCT 按魔数识别图片类型（gh-proxy 有时回 text/html 带图内容）。
func detectImageCT(b []byte) string {
	if len(b) < 12 {
		return ""
	}
	switch {
	case b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	case b[0] == 0xFF && b[1] == 0xD8:
		return "image/jpeg"
	case b[0] == 'G' && b[1] == 'I' && b[2] == 'F':
		return "image/gif"
	case b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' && b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P':
		return "image/webp"
	}
	if strings.HasPrefix(string(b[:min(200, len(b))]), "<svg") || strings.HasPrefix(string(b[:min(200, len(b))]), "<?xml") {
		return "image/svg+xml"
	}
	return ""
}

// mirrorURLByKey 镜像 key → URL 前缀。
func mirrorURLByKey(key string) string {
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.Key == key {
			return opt.URL
		}
	}
	return ""
}

func mustParseURL(u string) *url.URL {
	p, _ := url.Parse(u)
	return p
}

var (
	readmeMdImgRe   = regexp.MustCompile(`(!\[[^\]]*\]\()\s*([^)\s]+)\s*(\))`)
	readmeHtmlImgRe = regexp.MustCompile(`(src=")([^"]+)(")`)
)

// rewriteRelImages 把 README 里的相对路径图片补成绝对直链。
// 社区 README 常写 `![x](docs/shot.png)`——按 README 所在目录解析
// （raw.githubusercontent 与 jsDelivr gh 的目录结构一致，直接拼路径）。
// 绝对 URL / data: / #锚点 不动。
func rewriteRelImages(body []byte, readmeURL string) []byte {
	u, err := url.Parse(readmeURL)
	if err != nil {
		return body
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return body
	}
	dirSlash := strings.LastIndex(u.Path, "/") + 1
	if dirSlash <= 0 {
		return body
	}
	base := u.Scheme + "://" + u.Host + u.Path[:dirSlash]
	abs := func(target string) string {
		t := strings.TrimSpace(target)
		if t == "" || strings.Contains(t, "://") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "data:") {
			return target
		}
		return base + path.Clean(t)
	}
	out := readmeMdImgRe.ReplaceAllStringFunc(string(body), func(m string) string {
		sub := readmeMdImgRe.FindStringSubmatch(m)
		return sub[1] + abs(sub[2]) + sub[3]
	})
	out = readmeHtmlImgRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := readmeHtmlImgRe.FindStringSubmatch(m)
		return sub[1] + abs(sub[2]) + sub[3]
	})
	return []byte(out)
}

// resolveIcon 按优先级取应用图标字节：两级缓存（内存 10min + 磁盘 7 天）
// → 声明的 icon_url → 仓库布局候选 → 本机已装目录。候选间并发竞速，
// GitHub 直链附 jsDelivr 镜像变体；全败走负缓存 30min（不再每次重赛）。
// iconCacheKey 图标两级缓存键（0.6.200 起含版本）：应用升级后发布者常
// 同步换图标（icon_url 指向仓库 main 分支——URL 不变、内容更新）；不带
// 版本时 7 天磁盘层会一直供旧图标（对标 FnDepot v0.6.6 修复的同款根因）。
// 版本变化 → 新 key → 重新抓取；旧条目由磁盘层容量（iconDiskMax）淘汰。
func iconCacheKey(a *source.App) string {
	return a.Source + "@" + a.Name + "@" + a.Version
}

func (s *Server) resolveIcon(ctx context.Context, a *source.App) ([]byte, string, error) {
	cacheKey := iconCacheKey(a)
	st := s.iconStore()
	if data, ctype, ok := st.Get(cacheKey); ok {
		return data, ctype, nil
	}
	// 负缓存：最近 30min 已确认所有候选失败 → 直接本机兜底/404，不重赛
	if st.IsNegative(cacheKey) {
		if data, ok := localAppIcon(a.Name); ok {
			return data, "image/png", nil
		}
		return nil, "", errors.New("图标不存在或拉取失败")
	}
	var cands []string
	if a.IconURL != "" {
		cands = append(cands, normalizeGitHubURL(a.IconURL))
	}
	cands = append(cands, iconCandidates(a.Name, s.sourceBaseURL(a.Source))...)
	// raw 直链加 2 个最健康镜像进竞速（直连挂起时镜像兜底）
	cands = append(cands, s.mirrorVariants(cands, 2)...)
	cands = dedupeMirrors(cands)

	// 镜像交叉验证：镜像变体形如 "<镜像前缀>https://raw.githubusercontent.com/…"，
	// 部分镜像对不存在的文件返回 200+占位图（魔数合法，无法靠魔数拒绝）。
	// 规则：① 占位图黑名单哈希直接拒；② 直链 200 直接赢；③ 镜像 200 先
	// 挂起，若同一路径的直链随后明确 404/403 → 占位图，弃用；直链彻底
	// 失联（超时，发不出明确 404）→ 镜像结果放行。
	var mirrorPrefixes []string
	for _, opt := range config.GitHubMirrorOptions() {
		if p := strings.TrimRight(opt.URL, "/"); p != "" {
			mirrorPrefixes = append(mirrorPrefixes, p+"/")
		}
	}
	stripMirror := func(u string) string {
		for _, p := range mirrorPrefixes {
			if strings.HasPrefix(u, p) {
				return u[len(p):]
			}
		}
		return u
	}
	isMirror := func(u string) bool { return stripMirror(u) != u }

	type result struct {
		u       string
		data    []byte
		ctype   string
		ok      bool
		defMiss bool // 远端明确 404/403/410
	}
	ch := make(chan result, len(cands))
	for _, u := range cands {
		go func(rawURL string) {
			// 单候选 8s 子超时：挂起的候选不拖住整体
			cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			data, ctype, ok, miss := fetchIconBytes(cctx, rawURL)
			cancel()
			ch <- result{u: rawURL, data: data, ctype: ctype, ok: ok, defMiss: miss}
		}(u)
	}
	// ctx 15s 封顶 + 10s 兜底防挂起候选
	deadline := time.After(10 * time.Second)
	defMissPaths := map[string]bool{}
	var pending []result // 挂起的镜像 200（等待直链交叉验证）
	resolved := 0
	giveUp := false
	for resolved < len(cands) && !giveUp {
		select {
		case r := <-ch:
			resolved++
			if r.defMiss {
				defMissPaths[stripMirror(r.u)] = true
				continue
			}
			if !r.ok {
				continue
			}
			if ghPlaceholderIconHash(r.data) {
				// gh-proxy 系镜像的 404 占位图（2026-09 实测三镜像同哈希）：
				// 直接拒绝并视同该路径明确 404，连带拒掉同路径其他镜像
				defMissPaths[stripMirror(r.u)] = true
				continue
			}
			if !isMirror(r.u) {
				// 0.6.201 降采样后再入缓存/返回（只缩不放，失败原样）
				data, ct, _ := downsampleIcon(r.data, r.ctype)
				st.Put(cacheKey, data, ct)
				return data, ct, nil
			}
			if defMissPaths[stripMirror(r.u)] {
				continue // 直链已明确 404 → 这是占位图
			}
			pending = append(pending, r)
		case <-ctx.Done():
			giveUp = true
		case <-deadline:
			giveUp = true
		}
	}
	for _, r := range pending {
		if !defMissPaths[stripMirror(r.u)] {
			// 0.6.201 降采样后再入缓存/返回
			data, ct, _ := downsampleIcon(r.data, r.ctype)
			st.Put(cacheKey, data, ct)
			return data, ct, nil
		}
	}
	if data, ok := localAppIcon(a.Name); ok {
		// 0.6.201 本地图标同样过降采样（UI 图标多为小图，零开销）
		small, ct, _ := downsampleIcon(data, "image/png")
		st.Put(cacheKey, small, ct) // 本地图标也进缓存
		return small, ct, nil
	}
	// 负缓存只认真实缺失的确定性证据（明确 404/403 或占位图拒绝）：
	// 纯网络超时（镜像风暴/限流/直连挂起）不能记负，否则健康图标会被
	// 误判缺失 30 分钟（2026-09 预热风暴实测：igame 等被瞬时故障误杀）
	if len(defMissPaths) > 0 {
		st.MarkNegative(cacheKey)
	}
	return nil, "", errors.New("图标不存在或拉取失败")
}

// mirrorVariants 给候选里的 raw.githubusercontent 直链附加 n 个最健康镜像前缀变体
// （声明顺序即 2026-09 实测延迟序，取前 n 个健康镜像）。
func (s *Server) mirrorVariants(cands []string, n int) []string {
	rawCnt := 0
	for _, u := range cands {
		if strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
			rawCnt++
		}
	}
	if rawCnt == 0 || n <= 0 {
		return nil
	}
	urlByKey := map[string]string{}
	for _, opt := range config.GitHubMirrorOptions() {
		urlByKey[opt.Key] = opt.URL
	}
	// 镜像 key 选择：健康数据有 ok 镜像 → 按吞吐序；健康数据冷（重启后
	// 首探未完成）→ 退回声明顺序前 n 个作 best-effort 候选。没有这一步，
	// raw 黑洞窗口内竞速只剩 raw+jsDelivr（jsDelivr 对未收录仓库 301 回
	// raw，形同虚设）→ 全候选同主机全超时（GenOffice 2026-09-23 实锤）。
	// conversun 只代理 conversun 仓库，不能当通用镜像（别仓库恒 404，
	// 会污染 404 证据），一律排除。
	var keys []string
	if ordered := s.ghOrderedKeys(); len(ordered) > 0 {
		keys = ordered
	} else {
		for _, opt := range config.GitHubMirrorOptions() {
			if opt.URL == "" || opt.Key == "conversun" {
				continue
			}
			keys = append(keys, opt.Key)
		}
	}
	var out []string
	used := 0
	for _, key := range keys {
		if key == "conversun" {
			continue
		}
		prefix, ok := urlByKey[key]
		if key == "custom" {
			prefix, ok = "", false
			if s.Cfg != nil {
				prefix, ok = ensureTrailingSlash(s.Cfg.CustomGitHubMirror), s.Cfg.CustomGitHubMirror != ""
			}
		}
		if !ok || prefix == "" {
			continue
		}
		for _, u := range cands {
			if strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
				out = append(out, prefix+u)
			}
		}
		used++
		if used >= n {
			break
		}
	}
	return out
}

// dedupeMirrors 对 raw.githubusercontent 直链附 jsDelivr 镜像变体，并去重。
func dedupeMirrors(cands []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(cands)*2)
	for _, u := range cands {
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		if m := jsDelivrMirror(u); m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// jsDelivrMirror 把 raw.githubusercontent.com/o/r/<branch>/path 映射为
// cdn.jsdelivr.net/gh/o/r/path（jsDelivr 走默认分支，社区仓库基本都是 main）。
func jsDelivrMirror(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host != "raw.githubusercontent.com" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return ""
	}
	// 正确格式：/gh/owner/repo@branch/路径（把分支当路径段会 301/挂起）
	return "https://cdn.jsdelivr.net/gh/" + parts[0] + "/" + parts[1] + "@" + parts[2] + "/" + strings.Join(parts[3:], "/")
}

// ghPlaceholderIconHashes gh-proxy 系镜像对不存在文件返回的 404 占位图
// （2026-09 实测 hk/cdn/gh-proxy.com 三镜像字节一致）。合法社区图标
// 与占位图逐字节相同的概率可忽略；发现新变体往这里加。
var ghPlaceholderIconHashes = map[string]bool{
	"0e1557067c76e6e86a5f95264daa32aa43ae0764045f8172e14fbebb785aee3a": true,
}

func ghPlaceholderIconHash(data []byte) bool {
	if len(data) != 40528 {
		return false
	}
	sum := sha256.Sum256(data)
	return ghPlaceholderIconHashes[hex.EncodeToString(sum[:])]
}

// looksLikeImage 魔数校验真实图片字节。镜像对不存在的文件会返回
// 200+损坏字节（PNG 头 0x89 被转码成 U+FFFD）、源数据也可能把
// icon_url 错写成 README.md——200 状态码不可信，魔数才是终审。
func looksLikeImage(b []byte) bool {
	switch {
	case len(b) > 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return true
	case len(b) > 3 && b[0] == 'G' && b[1] == 'I' && b[2] == 'F' && b[3] == '8':
		return true
	case len(b) > 4 && b[0] == 0xFF && b[1] == 0xD8:
		return true
	case len(b) > 12 && b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' &&
		b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P':
		return true
	case len(b) > 4 && b[0] == 0x00 && b[1] == 0x00 && b[2] == 0x01 && b[3] == 0x00:
		return true // BMP / ICO
	}
	t := strings.TrimSpace(string(b[:min(64, len(b))]))
	return strings.HasPrefix(t, "<svg") || strings.HasPrefix(t, "<?xml")
}

// sniffImageMime 按魔数猜 MIME；认不出返回空（由调用方魔数校验把关）。
func sniffImageMime(b []byte) string {
	switch {
	case len(b) > 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G':
		return "image/png"
	case len(b) > 3 && b[0] == 'G' && b[1] == 'I' && b[2] == 'F' && b[3] == '8':
		return "image/gif"
	case len(b) > 4 && b[0] == 0xFF && b[1] == 0xD8:
		return "image/jpeg"
	case len(b) > 12 && b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' &&
		b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P':
		return "image/webp"
	case len(b) > 4 && b[0] == 0x00 && b[1] == 0x00 && b[2] == 0x01 && b[3] == 0x00:
		return "image/bmp"
	}
	t := strings.TrimSpace(string(b[:min(64, len(b))]))
	if strings.HasPrefix(t, "<svg") || strings.HasPrefix(t, "<?xml") {
		return "image/svg+xml"
	}
	return ""
}

// fetchIconBytes 拉取远端字节（整体由 ctx 封顶）。
// 返回 (data, ctype, ok, defMiss)：defMiss=true 表示远端明确
// 404/403/410（供镜像交叉验证判定占位图）。
func fetchIconBytes(ctx context.Context, rawURL string) ([]byte, string, bool, bool) {
	// 安全（2026-09-27 审核）：icon_url 由源数据提供（发布者可控），
	// 图标预热在启动时自动批量触发——非公共地址一律拒绝（防 SSRF）。
	if _, gerr := netguard.Public(rawURL); gerr != nil {
		log.Printf("[icon] 跳过（安全策略）%s: %v", rawURL, gerr)
		return nil, "", false, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false, false
	}
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, "", false, false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusGone:
		return nil, "", false, true
	case http.StatusOK:
		// 继续
	default:
		return nil, "", false, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil || len(body) == 0 {
		return nil, "", false, false
	}
	ctype := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ctype, "text/html") {
		return nil, "", false, false
	}
	if !looksLikeImage(body) {
		// 200 但不是图片（README 文本/JSON 错误体/损坏字节）→ 视为无效候选
		return nil, "", false, false
	}
	if ctype == "" {
		ctype = sniffImageMime(body)
	}
	return body, ctype, true, false
}

// normalizeGitHubURL 把 github.com blob 页面链接归一化为 raw 直链。
func normalizeGitHubURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.HasSuffix(u.Host, "github.com") {
		return rawURL
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, seg := range parts {
		if seg == "blob" && i+2 < len(parts) {
			filePath := strings.Join(parts[i+2:], "/")
			return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + parts[i+1] + "/" + filePath
		}
	}
	return rawURL
}

// sourceBaseURL 按源名查配置里的源地址。
func (s *Server) sourceBaseURL(name string) string {
	for _, sr := range s.Cfg.Sources {
		if sr.Name == name {
			return strings.TrimRight(sr.URL, "/")
		}
	}
	return ""
}

// iconCandidates 按 FnDepot 仓库布局推导候选图标地址：<appname>/ICON.PNG 等。
func iconCandidates(appName, sourceBase string) []string {
	if sourceBase == "" {
		return nil
	}
	u, err := url.Parse(sourceBase)
	if err != nil || u.Host == "" {
		// 无协议裸域名（如 github.com/Owner/Repo）：补 https
		if strings.Contains(sourceBase, "github.com/") {
			u, _ = url.Parse("https://" + sourceBase)
		} else {
			return nil
		}
	}
	var base string
	if strings.HasSuffix(u.Host, "github.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return nil
		}
		// raw 接口要求具体分支（HEAD 无效），main/master 都试
		baseMain := "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/main/"
		baseMaster := "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/master/"
		return []string{
			baseMain + appName + "/ICON.PNG",
			baseMain + appName + "/ICON_256.PNG",
			baseMain + appName + "/icon.png",
			baseMain + "ICON.PNG",
			baseMaster + appName + "/ICON.PNG",
			baseMaster + "ICON_256.PNG",
		}
	}
	base = strings.TrimSuffix(sourceBase, path.Base(sourceBase))
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return []string{
		base + appName + "/ICON.PNG",
		base + appName + "/ICON_256.PNG",
		base + appName + "/icon.png",
		base + "ICON.PNG",
		base + "ICON_256.PNG",
	}
}

// localAppIcon 读本机已装应用目录的图标（/vol1/@appcenter/<app>/ui/images/）。
func localAppIcon(appName string) ([]byte, bool) {
	// 安全（2026-09-27 审核）：appName 来自源数据，含路径分隔符时
	// 拒读（防目录穿越探测）。
	if appName == "" || strings.ContainsAny(appName, "/\\") || appName == "." || appName == ".." {
		return nil, false
	}
	dir := filepath.Join(appCenterDir, appName, "ui", "images")
	for _, f := range []string{
		// ICON.PNG 是 fnOS FPK 应用的标准图标名，优先
		"ICON.PNG", "ICON.png", "icon_256.png", "icon-256.png",
		"icon_0_256.png", "icon_256.PNG", "icon.png", "icon_0.png",
		"icon-64.png", "icon_64.png",
	} {
		if b, err := os.ReadFile(filepath.Join(dir, f)); err == nil && len(b) > 0 {
			return b, true
		}
	}
	return nil, false
}

// appDiagnostic 应用诊断（0.6.269，M4 排障能力；契约 = 前端
// ReportFailureDialog/fetchDiagnostic，此前 501 桩导致「上报」按钮恒败）：
// GET /api/apps/{key}/diagnostic?step=<步骤>&error=<错误> →
// {report: {app, display_name, version, arch, app_type, failed_step,
//  error_message, log_tail, log_truncated, store_version, platform,
//  timestamp}, issue_url}。只读收集，不改动任何状态。
func (s *Server) appDiagnostic(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	step := r.URL.Query().Get("step")
	errMsg := r.URL.Query().Get("error")

	ai, ok := s.catalogByKey(key)
	appname, displayName := key, key
	if ok {
		appname, displayName = ai.AppName, ai.DisplayName
	}
	version := ""
	appType := ""
	if ok {
		version = ai.InstalledVersion
		if version == "" {
			version = ai.LatestVersion
		}
		appType = ai.AppType
	}

	arch := "x86"
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		arch = "ARM"
	}
	lines, truncated := diagnosticLogTail(appname, 2000, 50, 8000)

	report := map[string]any{
		"app":            appname,
		"display_name":   displayName,
		"version":        version,
		"arch":           arch,
		"app_type":       appType,
		"failed_step":    step,
		"error_message":  errMsg,
		"log_tail":       strings.Join(lines, "\n"),
		"log_truncated":  truncated,
		"store_version":  s.Version,
		"platform":       "fnos",
		"timestamp":      time.Now().Format(time.RFC3339),
	}
	writeJSON(w, map[string]any{
		"report":    report,
		"issue_url": "https://github.com/Blue-Mink/FnDepot/issues/new",
	})
}

// diagnosticLogTail 读 Moo 日志尾部 scanLines 行，取最近 maxLines 条提及
// appname（大小写不敏感）的行；拼接超过 maxBytes 时截断并置 truncated。
// 文件缺失/无命中返回空切片。
func diagnosticLogTail(appname string, scanLines, maxLines, maxBytes int) ([]string, bool) {
	dir := dataDirOf(nil)
	logPath := resolveMooLogPath(dir)
	f, err := os.Open(logPath)
	if err != nil {
		return []string{}, false
	}
	defer f.Close()
	var all []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		all = append(all, sc.Text())
		if len(all) > scanLines {
			all = all[len(all)-scanLines:]
		}
	}
	needle := strings.ToLower(appname)
	var hit []string
	for i := len(all) - 1; i >= 0 && len(hit) < maxLines; i-- {
		if strings.Contains(strings.ToLower(all[i]), needle) {
			hit = append([]string{all[i]}, hit...)
		}
	}
	truncated := false
	joined := strings.Join(hit, "\n")
	if len(joined) > maxBytes {
		joined = joined[:maxBytes]
		hit = strings.Split(joined, "\n")
		truncated = true
	}
	return hit, truncated
}

// jsonUnmarshal 小工具。
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// bumpLocalInstalls 本机安装/更新次数加一并持久化（下载量展示回退链用）。
func (s *Server) bumpLocalInstalls(appName string) {
	if appName == "" {
		return
	}
	if s.Cfg.LocalInstalls == nil {
		s.Cfg.LocalInstalls = make(map[string]int)
	}
	s.Cfg.LocalInstalls[appName]++
	_ = s.Cfg.Save(dataDirOf(s))
}

// sseRunOp 启动长操作并轮询其进度，以 SSE 事件流式回报（前端 ProgressOverlay 消费）。
// 终态必须按操作 ID 从 history 取回：操作结束时「清空 current + 入 history」
// 是同一把锁内的原子动作，700ms 轮询可能恰好错过 running→error 的窗口，
// 只看 Current()==nil 会把失败误报成「完成」（2026-09-25 实锤：kspeeder
// 升级失败、fnlogpush 下载失败，SSE 却都发了 done）。
func (s *Server) sseRunOp(r *http.Request, w http.ResponseWriter, f http.Flusher, kind, target string, fn func(ctx context.Context, progress func(string, float64)) error) {
	sseSend(w, f, map[string]any{"step": kind, "progress": 0, "message": "启动…"})
	view, err := s.Ops.Start(kind, target, fn)
	if err != nil {
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	myID := view.ID
	lastMsg, lastPct := "", -1
	for {
		select {
		case <-r.Context().Done():
			return // 客户端断开，操作在后台继续
		case <-time.After(700 * time.Millisecond):
		}
		cur := s.Ops.Current()
		if cur != nil && cur.ID == myID {
			switch cur.State {
			case operation.StateDone:
				s.invalidateCatalog() // 安装/更新/卸载/启停都改变目录内容
				// 安装/更新成功累计本机次数（第三方源应用无全局下载量，列表回退展示）
				if kind == "install" || kind == "update" {
					s.bumpLocalInstalls(cur.Target)
				}
				sseSend(w, f, map[string]any{"step": "done", "progress": 100, "message": cur.Msg})
				return
			case operation.StateError:
				msg := cur.Err
				if msg == "" {
					msg = "操作失败"
				}
				sseSend(w, f, map[string]any{"step": "error", "error": msg})
				return
			default:
				// running：进度/消息变化才推送（避免刷屏）
				pct := int(cur.Progress / 2)
				if cur.Msg != lastMsg || pct != lastPct {
					sseSend(w, f, map[string]any{"step": kind, "progress": cur.Progress, "message": cur.Msg})
					lastMsg, lastPct = cur.Msg, pct
				}
			}
			continue
		}
		// current 已不是本操作：它已结束（current 清空与 history 入列原子完成）。
		// 终态从 history 按 ID 取回——绝不能默认「完成」。
		if h := s.Ops.Find(myID); h != nil {
			if h.State == operation.StateError {
				msg := h.Err
				if msg == "" {
					msg = "操作失败"
				}
				sseSend(w, f, map[string]any{"step": "error", "error": msg})
				return
			}
			s.invalidateCatalog()
			if kind == "install" || kind == "update" {
				s.bumpLocalInstalls(target)
			}
			sseSend(w, f, map[string]any{"step": "done", "progress": 100, "message": h.Msg})
			return
		}
		// 尚未入 history（结束动作的极短窗口）：等下一轮轮询
	}
}
