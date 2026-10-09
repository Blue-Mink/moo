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
	"sync/atomic"
	"time"

	"moo/internal/config"
	"moo/internal/lang"
	"moo/internal/netguard"
	"moo/internal/official"
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
	// Official 官方应用中心 OAuth 管理器（合并自主线：token 持久化 + 自动刷新）
	Official *official.Manager
	// OfficialStore 官方 OAuth 目录缓存（10min TTL）。0.6.253 起由
	// WireOfficialOAuth / panelApp / officialRoutes 共享同一实例。
	OfficialStore *officialStore

	iconStoreOnce sync.Once
	iconStoreV    *iconStore // 图标两级缓存（内存 1h，容量 env MOO_ICON_MEM_MAX 默认 256/0=禁用 + 磁盘 7 天 + 负缓存 30min）

	readmeStoreOnce sync.Once
	readmeStoreV    *readmeStore // README 两级缓存（内存 1h + 磁盘 7 天 + 负缓存 30min）

	detailStoreOnce sync.Once
	detailStoreV    *detailStore // 应用详情两级缓存（0.6.312 B3/F8：changelog 全文/releases 明细，内存 1h + 磁盘 7 天）

	autoRefreshMu sync.Mutex // 自动源刷新协程互斥（防 main 重复启动）

	// 刷新 single-flight（0.6.312 B1/F4）：后台刷新轮与手动「检查更新」
	// 互斥——同一时刻最多一轮 157 源全量刷新（双轮并发=峰值 CPU 最差形态）。
	refreshInflight atomic.Bool

	// 目录缓存：buildCatalog 每次要 ListInstalled daemon RPC + 1700+ 条目
	// 合并/模糊匹配（O(n²) 热点修复前实测 50-287ms）——列表轮询与详情点击
	// 都走它，不缓存则每次打开详情对话框都有可感知的骨架屏闪烁。
	// 0.6.313 B4：默认 TTL 5s → 60s（事件驱动失效：写操作/官方 OAuth
	// 授权走 invalidateCatalog，env MOO_CATALOG_TTL 可回落 5s）+ F11
	// 重建移出锁（singleflight + SWR：过期立即给旧数据+后台重建；
	// 刚失效 catalogData=nil 时同步重建保安装/更新反馈即时）。
	catalogMu   sync.Mutex
	catalogData []AppInfo
	catalogAt   time.Time
	catalogTTL  time.Duration // 显式设置（测试用）；0 = 默认 60s（env MOO_CATALOG_TTL 可覆盖）
	// 0.6.313 B4/F11：锁外重建 singleflight（原子标志 + done channel，
	// 无第三方库）。catalogBuilding=true 表示重建进行中；重建完成时
	// catalogRebuildDone 被 close（并发调用方等它，等待上限
	// catalogRebuildWait，超时也返回旧数据不阻塞请求）。
	catalogBuilding    bool
	catalogRebuildDone chan struct{}
	// 0.6.313 B4：生效 TTL 记忆化（env 只解析一次；catalogTTL>0 时优先）。
	catalogTTLMemo time.Duration

	// 0.6.313 B4：GET /api/apps body 缓存——只缓存**无 source 参数**的
	// 全量响应（前端 fetchApps 唯一热路径，不带 query 参数）marshal 后的
	// 字节 + 预计算 ETag（md5 在重建时算一次，不再每请求对 ~600KB 重算）。
	// 有 source 参数的请求不走本缓存（罕见路径：cachedCatalogCopy 重过滤
	// + 重 marshal，不读写这两个字段）。失效与目录缓存同一钩子：
	// invalidateCatalog 一并清空（陈旧 ≤ TTL，风险面与目录缓存相同）。
	// catalogBody 字节序列只读共享（多请求并发读，永不修改）。
	catalogBodyMu   sync.Mutex
	catalogBody     []byte
	catalogBodyETag string

	selfUpdOnce sync.Once
	selfUpd     *selfUpdateProbe // 官方 release 探测缓存（自更新）

	// UI 活性门控（0.6.312 B3/F6）：最近一次 HTTP 请求时间戳（unix 秒）。
	// 所有请求（两通道、/api 与静态）经 Handler 最外层 touch；pprof 端口
	// 是独立 listener（http.Serve(ln, nil)），天然不计入。
	// 无 UI 访问 >30min 时「展示性」后台轮次降级：图标/readme 预热跳过、
	// GHStats 30→120min、镜像探针 5→15min；核心轮次（源刷新/自监控/
	// 备份/日志轮转）不受影响。UI 一访问立即恢复。
	lastUIRequest    atomic.Int64
	lastGHStatsRound atomic.Int64 // 上一轮 GHStats 完成时间（F6 降级判定）
	lastProbeGh      atomic.Int64 // gh 组上次探测时间（F6）
	lastProbeDk      atomic.Int64 // dk 组上次探测时间（F6）
}

// 0.6.312 B3/F6（客户端活性门控）阈值：
const (
	uiInactiveGate     = 30 * time.Minute // 无 UI 访问超过 30 分钟 = 不活跃
	ghStatsIdleGap     = 120 * time.Minute // 不活跃时 GHStats 最多每 120min 一轮（活跃=30min tick）
	mirrorProbeIdleGap = 15 * time.Minute  // 不活跃时镜像探针最多每 15min 一次（活跃=配置间隔）
)

// touchUI 0.6.312 B3/F6：更新最近一次 UI 请求时间戳（每请求一次，原子写）。
func (s *Server) touchUI() {
	s.lastUIRequest.Store(time.Now().Unix())
}

// uiInactive 0.6.312 B3/F6：距最近一次 UI 请求是否已超过 30 分钟。
// 从未有请求时以进程启动时刻为基线——刚启动算活跃（保证冷启动首轮预热
// 照跑，30 分钟无人访问才开始降级）。
func (s *Server) uiInactive() bool {
	last := s.lastUIRequest.Load()
	if last == 0 {
		base := s.StartedAt
		if base.IsZero() {
			base = time.Now()
		}
		last = base.Unix()
	}
	return time.Since(time.Unix(last, 0)) > uiInactiveGate
}

// ghStatsLastRound / probeGhLast / probeDkLast：F6 降级判定用，
// 从未跑过 = 视为已到期（返回足够旧的时间点）。
func (s *Server) ghStatsLastRound() time.Time {
	if v := s.lastGHStatsRound.Load(); v != 0 {
		return time.Unix(v, 0)
	}
	return time.Unix(0, 0)
}

func (s *Server) probeGhLast() time.Time {
	if v := s.lastProbeGh.Load(); v != 0 {
		return time.Unix(v, 0)
	}
	return time.Unix(0, 0)
}

func (s *Server) probeDkLast() time.Time {
	if v := s.lastProbeDk.Load(); v != 0 {
		return time.Unix(v, 0)
	}
	return time.Unix(0, 0)
}

// withUITouch 0.6.312 B3/F6：最外层中间件——每个 HTTP 请求更新最近 UI
// 请求时间戳（活性门控基线）。
func withUITouch(s *Server, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.touchUI()
		next.ServeHTTP(w, r)
	})
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
	// 去重三档计数（0.6.312 B2，首页胶囊预览）
	mux.HandleFunc("GET /api/dedup", s.dedupSummary)
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
	mux.HandleFunc("POST /api/sources/delete-defaults", s.requireAdmin(s.deleteDefaults))  // 0.6.318 一键删除历史默认社区源（官源 20 条保护）
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
	mux.HandleFunc("GET /api/logs", s.requireAdmin(s.getLogs)) // 0.6.249 设置页「日志」在线查看
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
	// 0.6.269 Docker 优选接入：把选中的镜像写入系统 daemon.json 并重启（可回滚）
	mux.HandleFunc("GET /api/settings/docker-mirror/status", s.requireAdmin(s.dockerMirrorStatus))
	mux.HandleFunc("POST /api/settings/docker-mirror/apply", s.requireAdmin(s.dockerMirrorApply))

	// 0.6.255：/api/panel/test 已移除（面板账号随设置卡片一并下线；
	// 授权时的临时账号校验由 /api/official/authorize-headless 承担）。

	// ---- 官方应用中心（OAuth 连接 + 商店列表/搜索/详情；合并自主线 moo-w）----
	if s.Official != nil {
		s.officialRoutes(mux, s.officialStoreV())
	}

	// ---- FPK 下载缓存 ----
	mux.HandleFunc("GET /api/fpk-downloads", s.requireAdmin(s.listDownloads))
	mux.HandleFunc("DELETE /api/fpk-downloads/{name}", s.requireAdmin(s.removeDownload))
	mux.HandleFunc("POST /api/fpk-downloads/{name}/install", s.requireAdmin(s.installDownloadSSE))
	mux.HandleFunc("GET /api/fpk-downloads/{name}/wizard", s.requireAdmin(s.fpkDownloadWizard))

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

	// 目录语言跟随（0.6.269）：最外层注入，所有下游 handler 的 r.Context()
	// 都能取到语言（显式配置 > Accept-Language > 默认 zh-CN）。
	core = s.langMiddleware(core)

	// 0.6.312 B3/F6：最外层记录 UI 活性（所有请求，含静态资源）
	if trust {
		return withUITouch(s, http.StripPrefix(GatewayPrefix, withUser(true, core)))
	}
	return withUITouch(s, withUser(false, core))
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

// langMiddleware（0.6.269，M4 语言跟随）：请求上下文注入目录语言——
// 显式配置 catalog_language（非 auto）优先，否则跟随请求 Accept-Language。
// 后台协程（无请求上下文）走 bgLang()：配置值或 zh-CN 默认。
func (s *Server) langMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(lang.WithContext(r.Context(), s.langForRequest(r))))
	})
}

// langForRequest 解析单请求的目录语言（显式配置 > Accept-Language > 默认）。
func (s *Server) langForRequest(r *http.Request) string {
	if v := s.Cfg.CatalogLanguage; v != "" && v != "auto" && lang.Supported[v] {
		return v
	}
	return lang.FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

// bgLang 后台协程的目录语言（无请求上下文）。
func (s *Server) bgLang() string {
	if v := s.Cfg.CatalogLanguage; v != "" && v != "auto" && lang.Supported[v] {
		return v
	}
	return lang.Default
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
