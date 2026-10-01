// Package api 提供 Moo 的 HTTP API 与前端静态服务。
// 路由契约镜像 New Store 前端 src/api/client.ts（前端原样复用）。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
	"moo/internal/operation"
	"moo/internal/pipeline"
	"moo/internal/platform"
	"moo/internal/source"
	"moo/internal/task"
)

// GatewayPrefix 是统一网关声明的前缀（请求进来时携带，需剥离后路由）。
const GatewayPrefix = "/app/moo"

// Server 聚合 Moo 各组件。
type Server struct {
	Version   string
	StartedAt time.Time // 服务启动时刻（关于页运行时长用）
	Cfg       *config.Config
	Src       *source.Manager
	Tasks     *task.Manager
	Ops       *operation.Queue
	Pipe      *pipeline.Pipeline
	WebFS     fs.FS
	Mirrors   *mirrorMonitor
	Panel     *Panel
	// GHStats GitHub Release 资产下载量同步（第三方源目录不统计下载量的补数通道）
	GHStats *source.GHStats

	iconStoreOnce sync.Once
	iconStoreV    *iconStore // 图标两级缓存（内存 10min + 磁盘 7 天 + 负缓存 30min）

	readmeStoreOnce sync.Once
	readmeStoreV    *readmeStore // README 两级缓存（内存 1h + 磁盘 7 天 + 负缓存 30min）

	autoRefreshMu sync.Mutex // 自动源刷新协程互斥（防 main 重复启动）

	// 目录缓存：buildCatalog 每次要 ListInstalled daemon RPC + 1700+ 条目
	// 合并/模糊匹配（O(n²) 热点修复前实测 50-287ms）——列表轮询与详情点击
	// 都走它，不缓存则每次打开详情对话框都有可感知的骨架屏闪烁。
	// 5s TTL 兜底 + 写操作（安装/更新/卸载/启停/源同步）显式失效。
	catalogMu   sync.Mutex
	catalogData []AppInfo
	catalogAt   time.Time
	catalogTTL  time.Duration // 默认 5s，测试可缩

	selfUpdOnce sync.Once
	selfUpd     *selfUpdateProbe // 官方 release 探测缓存（自更新）
}

// User 是请求的用户上下文。
type User struct {
	Username string
	UserID   string
	IsAdmin  bool
	Trusted  bool // 是否来自可信通道（统一网关 socket）
}

// Handler 构造 HTTP handler。trust=true 时（unix socket 通道）
// 剥离 GatewayPrefix 并信任 X-Trim-* 身份头；TCP 通道忽略身份头。
func (s *Server) Handler(trust bool) http.Handler {
	mux := http.NewServeMux()

	// ---- 基础 ----
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": s.Version, "trusted": trusted(r)})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": s.Version, "platform": "fnos"})
	})
	mux.HandleFunc("GET /api/about", s.aboutGet)

	// ---- 通知设置（0.6.120，形式对齐 fn-knock 事件中心）----
	mux.HandleFunc("GET /api/notify-settings", s.requireAdmin(s.notifySettingsGet))
	mux.HandleFunc("PUT /api/notify-settings", s.requireAdmin(s.notifySettingsPut))
	mux.HandleFunc("GET /api/notify-log", s.requireAdmin(s.notifyLogGet))
	mux.HandleFunc("POST /api/notify-log", s.requireAdmin(s.notifyLogPost))
	mux.HandleFunc("POST /api/notify-log/clear", s.requireAdmin(s.notifyLogClear))
	mux.HandleFunc("GET /api/notify-channels", s.requireAdmin(s.notifyChannelsGet))
	mux.HandleFunc("POST /api/notify-channels", s.requireAdmin(s.notifyChannelsPost))
	mux.HandleFunc("PUT /api/notify-channels/{id}", s.requireAdmin(s.notifyChannelPut))
	mux.HandleFunc("DELETE /api/notify-channels/{id}", s.requireAdmin(s.notifyChannelDelete))
	mux.HandleFunc("POST /api/notify-channels/{id}/test", s.requireAdmin(s.notifyChannelTest))
	mux.HandleFunc("POST /api/notify-channels/test", s.requireAdmin(s.notifyChannelTestDraft)) // 0.6.139 弹窗内测试（未保存草稿）
	mux.HandleFunc("POST /api/notify/welcome", s.requireAdmin(s.notifyWelcomeFire))            // 0.6.155 手动重发欢迎语
	mux.HandleFunc("POST /api/notify/fire", s.requireAdmin(s.notifyFire))                      // 0.6.170 手动触发任意事件（规则行门铃）
	mux.HandleFunc("GET /api/notify-view/{id}", s.notifyViewGet)                               // 0.6.144 通知详情页（卡片跳转）
	mux.HandleFunc("GET /api/daemon/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"available": platform.Available()})
	})

	// ---- 应用目录（合并安装状态） ----
	mux.HandleFunc("GET /api/installed-detail", s.installedDetail)
	mux.HandleFunc("GET /api/apps", s.appsWithEtag)
	mux.HandleFunc("GET /api/apps/{key}", s.appDetail)
	// 图标集版本戳（前端 SW 用：源同步时间变化 ⇒ 图标缓存换版）。
	// 几百字节的小 JSON，必须 no-store——SW 每次激活都取，缓存了会卡在旧版本。
	mux.HandleFunc("GET /api/icons/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"v": s.lastCheckStamp()})
	})
	mux.HandleFunc("GET /api/recommended", s.recommended)
	mux.HandleFunc("POST /api/check", s.requireAdmin(s.checkUpdates))
	mux.HandleFunc("POST /api/apps/reload", s.requireAdmin(s.reloadSSE))
	mux.HandleFunc("GET /api/store-update", s.storeUpdateInfo)
	mux.HandleFunc("POST /api/store-update", s.requireAdmin(s.storeUpdateSSE))

	// ---- 应用源 ----
	// 0.6.198 公开：搜索框贴源链接用的「归一化 key → 源名」map（不含原始 URL）
	mux.HandleFunc("GET /api/search/source-keys", s.searchSourceKeys)
	mux.HandleFunc("GET /api/sources", s.requireAdmin(s.listSources))
	mux.HandleFunc("POST /api/sources", s.requireAdmin(s.addSource))
	mux.HandleFunc("POST /api/sources/batch", s.requireAdmin(s.batchSources))
	mux.HandleFunc("POST /api/sources/reorder", s.requireAdmin(s.reorderSources))
	mux.HandleFunc("POST /api/sources/sync-list", s.requireAdmin(s.syncList))
	mux.HandleFunc("POST /api/sources/restore-defaults", s.requireAdmin(s.restoreDefaults)) // 0.6.172 一键恢复默认源列表（含去重）
	mux.HandleFunc("POST /api/sources/{id}/sync", s.requireAdmin(s.syncSource))
	mux.HandleFunc("POST /api/sources/sync-all", s.requireAdmin(s.syncAllSources)) // 0.6.171 一键刷新所有源
	mux.HandleFunc("POST /api/sources/{id}/toggle", s.requireAdmin(s.toggleSource))
	mux.HandleFunc("POST /api/sources/{id}/rename", s.requireAdmin(s.renameSource))
	mux.HandleFunc("POST /api/sources/{id}/favorite", s.requireAdmin(s.favoriteSource))
	mux.HandleFunc("DELETE /api/sources/{id}", s.requireAdmin(s.removeSource))

	// ---- 每应用操作（SSE） ----
	mux.HandleFunc("GET /api/apps/{key}/wizard", s.appWizard)
	mux.HandleFunc("POST /api/apps/{key}/install", s.requireAdmin(s.appInstallSSE))
	mux.HandleFunc("POST /api/apps/{key}/update", s.requireAdmin(s.appUpdateSSE))
	mux.HandleFunc("POST /api/apps/{key}/uninstall", s.requireAdmin(s.appUninstallSSE))
	mux.HandleFunc("POST /api/apps/{key}/download-task", s.requireAdmin(s.appDownloadTaskSSE))
	mux.HandleFunc("POST /api/apps/{key}/task/pause", s.requireAdmin(s.appTaskPause))
	mux.HandleFunc("POST /api/apps/{key}/task/resume", s.requireAdmin(s.appTaskResume))
	mux.HandleFunc("PUT /api/apps/{key}/ignore-update", s.requireAdmin(s.ignoreUpdate))
	mux.HandleFunc("DELETE /api/apps/{key}/ignore-update", s.requireAdmin(s.ignoreUpdate))
	mux.HandleFunc("POST /api/apps/{key}/start", s.requireAdmin(s.appStart))
	mux.HandleFunc("POST /api/apps/{key}/stop", s.requireAdmin(s.appStop))
	mux.HandleFunc("GET /api/apps/{key}/panel-detail", s.requireAdmin(s.appPanelDetail))
	mux.HandleFunc("GET /api/apps/{key}/asset", s.appAsset)
	mux.HandleFunc("GET /api/apps/{key}/diagnostic", s.appDiagnostic)

	// ---- 设置 / 加速源（M3/M4 逐步充实） ----
	// 应用收藏（列表/详情星标切换；发现页收藏列表）
	mux.HandleFunc("GET /api/favorites", s.requireAdmin(s.favoritesList))
	mux.HandleFunc("POST /api/favorites", s.requireAdmin(s.favoriteToggle))

	mux.HandleFunc("GET /api/settings", s.requireAdmin(s.getSettings))
	mux.HandleFunc("PUT /api/settings", s.requireAdmin(s.putSettings))
	mux.HandleFunc("POST /api/settings/proxy-test", s.requireAdmin(s.proxyTestHandler))
	mux.HandleFunc("GET /api/backups", s.requireAdmin(s.getBackups))
	mux.HandleFunc("POST /api/backups", s.requireAdmin(s.createBackup))
	mux.HandleFunc("DELETE /api/backups/{name}", s.requireAdmin(s.deleteBackup))
	mux.HandleFunc("POST /api/backups/clean", s.requireAdmin(s.cleanCacheHandler))
	mux.HandleFunc("GET /api/backups/{name}/download", s.requireAdmin(s.downloadBackup))
	mux.HandleFunc("POST /api/backups/{name}/restore", s.requireAdmin(s.restoreBackup))
	mux.HandleFunc("GET /api/settings/download-dirs", s.requireAdmin(s.downloadDirOptions))
	mux.HandleFunc("GET /api/settings/download-dirs/browse", s.requireAdmin(s.browseDownloadDirs))
	mux.HandleFunc("GET /api/mirrors/health", s.mirrorHealth)
	mux.HandleFunc("GET /api/mirrors/docker/health", s.dockerMirrorHealth)
	mux.HandleFunc("POST /api/mirrors/check", s.requireAdmin(s.mirrorsCheck))

	mux.HandleFunc("POST /api/panel/test", s.requireAdmin(s.panelTest))

	// ---- FPK 下载缓存 ----
	mux.HandleFunc("GET /api/fpk-downloads", s.requireAdmin(s.listDownloads))
	mux.HandleFunc("DELETE /api/fpk-downloads/{name}", s.requireAdmin(s.removeDownload))
	mux.HandleFunc("POST /api/fpk-downloads/{name}/install", s.requireAdmin(s.installDownloadSSE))

	// ---- 任务与操作 ----
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.requireAdmin(s.removeTask))
	mux.HandleFunc("GET /api/operations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"current": s.Ops.Current(), "history": s.Ops.History()})
	})
	mux.HandleFunc("GET /api/installed", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		list, err := platform.ListInstalled(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, list)
	}))

	var core http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodGet {
			fmt.Fprintf(os.Stderr, "[api] %s %s (remote=%s)\n", r.Method, r.URL.Path, r.RemoteAddr)
		}
		mux.ServeHTTP(w, r)
	})

	// JSON 透明 gzip（/api/apps 1.5MB→257KB；SSE/二进制直通）
	core = withGzip(core)

	// 静态前端：/ 与 /app/moo/ 下均提供 SPA（非 /api 路径回退 index.html）
	core = s.withStatic(core, trust)

	if trust {
		return http.StripPrefix(GatewayPrefix, withUser(true, core))
	}
	return withUser(false, core)
}

// ---- 身份中间件 ----

// withUser 从（可信通道的）请求头中提取用户上下文放入 context。
func withUser(trust bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := User{Trusted: trust}
		if trust {
			u.Username = r.Header.Get("X-Trim-Username")
			u.UserID = r.Header.Get("X-Trim-Userid")
			u.IsAdmin = r.Header.Get("X-Trim-Isadmin") == "true"
		}
		r = r.WithContext(withUserCtx(r.Context(), u))
		next.ServeHTTP(w, r)
	})
}

// requireAdmin 要求请求来自可信通道（统一网关 socket）且用户为管理员。
// TCP 调试通道恒被拒绝（X-Trim-* 在不可信通道上被忽略）。
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, _ := userFrom(r.Context())
		if !u.Trusted {
			writeErr(w, http.StatusForbidden, errors.New("该操作需通过面板网关入口（管理员身份）"))
			return
		}
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, errors.New("需要管理员权限（当前用户: "+orAnon(u.Username)+"）"))
			return
		}
		// 0.6.207-panel D2（CSRF 纵深防御）：非 GET 变更请求必须携带 X-Moo-Admin
		// 自定义头。跨站 simple 请求（表单 POST / no-cors fetch）无法附加自定义头
		//（需 CORS 预检，而网关无 CORS 头 → 浏览器直接拦截），即使平台网关不校验
		// Origin，CSRF 也在应用层被封死。只读 GET 豁免（与前端 apiFetch 契约一致）。
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions &&
			r.Header.Get("X-Moo-Admin") == "" {
			writeErr(w, http.StatusBadRequest, errors.New("缺少请求校验头（X-Moo-Admin），请刷新 Moo 页面后重试"))
			return
		}
		next(w, r)
	}
}

func orAnon(name string) string {
	if name == "" {
		return "未知用户"
	}
	return name
}

// SetupNetguard 注册豁免主机重建器（启动时调用）：管理员显式配置的
// 应用源地址、自建 GitHub 镜像、源列表地址放行抓取；源数据驱动的 URL
// 一律过 netguard.Public 校验（SSRF 防护，2026-09-27 代码审核）。
func (s *Server) SetupNetguard() {
	netguard.SetRebuilder(func() []string {
		var hosts []string
		add := func(raw string) {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				return
			}
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" {
				return
			}
			hosts = append(hosts, u.Hostname())
		}
		for _, sr := range s.Cfg.Sources {
			add(sr.URL)
		}
		add(s.Cfg.CustomGitHubMirror)
		add(s.Cfg.SourceListURL)
		return hosts
	})
}

// withStatic 为非 API 路径提供 embed 的前端（SPA 回退）。
// gateway=true（统一网关通道）时，根路径 /app/moo（无尾斜杠）301 到 /app/moo/：
// 否则浏览器把 /app/moo 当文件名，相对资源 ./assets/… 会解析到 /app/assets/…（404 空白页）。
func (s *Server) withStatic(next http.Handler, gateway bool) http.Handler {
	if s.WebFS == nil {
		return next
	}
	sub, err := fs.Sub(s.WebFS, "dist")
	if err != nil {
		return next
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if gateway && r.URL.Path == "" {
			http.Redirect(w, r, GatewayPrefix+"/", http.StatusMovedPermanently)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// 缓存策略：index.html 永远强制重新验证（每次升级都换新 bundle 引用，
		// 不设头时手机 WebView 会启发式缓存旧 index.html → 升级后仍是旧界面）；
		// assets/ 下文件名带内容哈希，可长期缓存。
		cacheCtl := "no-cache"
		if strings.HasPrefix(p, "assets/") {
			cacheCtl = "public, max-age=31536000, immutable"
		}
		if _, err := fs.Stat(sub, p); err == nil {
			w.Header().Set("Cache-Control", cacheCtl)
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA 回退
		r.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

// ---- 工具 ----

func dataDirOf(s *Server) string { return config.DataDir() }

func trusted(r *http.Request) bool {
	u, _ := userFrom(r.Context())
	return u.Trusted
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}

// sseStart 初始化 SSE 响应并返回 flusher。
func sseStart(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return f
}

// sseSend 发送一条 SSE data 事件。
func sseSend(w http.ResponseWriter, f http.Flusher, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if f != nil {
		f.Flush()
	}
}
