package api

// 官方应用中心（fnOS 官方源）API：OAuth 连接 + 商店列表/搜索/详情。
//
// 数据来自本机面板 /ogh/ac/h 代理（127.0.0.1:5666），OAuth token 由
// internal/official 管理（PKCE + 自动刷新 + 持久化）。列表 10 分钟缓存，
// 写操作前手动刷新。

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

	"moo/internal/official"
	"moo/internal/panel"
)

var errKeywordRequired = errors.New("keyword 不能为空")

// errSessionKeyUnavailable 本机密钥不可用（.moo-secret.key 缺失/损坏）。
// 与 internal/official 的 lastErr 文案区分：这条是「拒绝开启」，那条是「读不出」。
var errSessionKeyUnavailable = errors.New("本机密钥不可用（.moo-secret.key 缺失或损坏），无法开启会话加密")

type officialStore struct {
	mu       sync.Mutex
	mgr      *official.Manager
	data     []official.StoreApp
	at       time.Time
	ttl      time.Duration
	fetchErr string
	// 0.6.259：OAuth 不可用（无会话/拉空/失败）时的面板账号兜底（WireOfficialOAuth 注入）。
	fallback func(ctx context.Context) ([]official.StoreApp, error)
}

func newOfficialStore(mgr *official.Manager) *officialStore {
	return &officialStore{mgr: mgr, ttl: 10 * time.Minute}
}

// invalidate 授权/登出后调用：清空 OAuth 目录缓存，下次请求立即重拉。
func (s *officialStore) invalidate() {
	s.mu.Lock()
	s.data = nil
	s.at = time.Time{}
	s.fetchErr = ""
	s.mu.Unlock()
}

// officialStoreV 返回共享的官方目录缓存（懒创建；测试可直接赋值）。
func (s *Server) officialStoreV() *officialStore {
	if s.OfficialStore == nil {
		s.OfficialStore = newOfficialStore(s.Official)
	}
	return s.OfficialStore
}

// officialBrowserBase 解析 authorize 请求的 ?base= 参数（用户浏览器可达的
// 面板地址）。安全约束：仅 http/https、host 非空、URL 不得携带用户凭据。
func (s *Server) officialBrowserBase(r *http.Request) string {
	base := strings.TrimSpace(r.URL.Query().Get("base"))
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// officialScopeParam 把 ?scope= / body.scope 白名单化为实际 scope 串。
// 缺省（""）= 完整权限（旧客户端行为不变）；非法值返回 error（上层 400）。
// 绝不透传任意串——scope 原样透传等于把「提权范围」交给调用方。
func officialScopeParam(v string) (string, error) {
	sc, err := official.NormalizeScope(v)
	if err != nil {
		return "", errors.New("scope 参数只能是 full 或 minimal")
	}
	return sc, nil
}

// officialRoutes 注册 /api/official/* 路由。
func (s *Server) officialRoutes(mux *http.ServeMux, store *officialStore) {
	mux.HandleFunc("GET /api/official/status", func(w http.ResponseWriter, r *http.Request) {
		st := store.mgr.Status(time.Now())
		// 0.6.254：面板前端授权 UI 支持状态（仅 fnOS 1.2.0800+ 前端有 PKCE 授权页；
		// 旧版只渲染普通登录页，iframe 流程走不完）。known=false = 检测中。
		sup, known := store.mgr.UISupportStatus()
		st["ui_supported"] = sup
		st["ui_known"] = known
		// 0.6.259：面板账号兜底是否已启用（老面板无 OAuth 时 headless 授权会置位）。
		st["panel_fallback"] = s.Panel.Enabled()
		// 2026-11-26（加固 B2）：会话落盘加密状态。**公开布尔**（非管理员也给）：
		// 只表达「磁盘上是不是密文」「本机密钥能不能用」，不含密钥、不含内容，
		// 前端据此渲染开关的可用性/当前态。
		st["session_encrypted"] = store.mgr.SessionEncrypted()
		st["encryption_available"] = store.mgr.EncryptionAvailable()
		// 2026-11-26（盲点修复）：把「开关意图」与「磁盘形态」分开暴露。
		// 无会话时开启加密只改意图（还没有会话可写），若前端只认 session_encrypted，
		// 开关会弹回「关」——用户以为没生效（实报）。encryption_mismatch 用于
		// 「意图开、文件仍是明文」（旧版本重授权覆盖）这种需要显式告知的状态。
		st["session_encryption_on"] = store.mgr.EncryptionEnabled()
		st["encryption_mismatch"] = store.mgr.EncryptionMismatch()
		// 2026-11-26（加固 A）：授权范围偏好（公开、非敏感）。配置值非法按 full
		// 返回；scope_minimal_supported 恒 true = 前端可展示两个选项（旧后端没有
		// 这两个键，前端缺省按 full，行为不变）。
		// 配置可能为 nil（最小 Server 夹具 / 早期启动）——nil 按默认 full 处理，
		// 不让一个状态查询因为配置未注入而 panic。
		scopeCfg := ""
		if c := s.cfg(); c != nil {
			scopeCfg = c.OfficialScopeMode
		}
		st["scope_mode"] = official.ScopeModeOf(scopeCfg)
		st["scope_minimal_supported"] = true
		// 2026-11-26（加固 C）：status 按身份裁剪，但**保持 200**（前端 / Mate /
		// 脚本都靠它判断连接状态，改成 403 会把这些调用方全打死）。
		// 公开字段：authorized / expired / expires_at / scopes / pending /
		// ui_supported / ui_known / panel_fallback / can_admin。
		// 仅管理员（可信通道 + 管理员身份）额外可见面板地址与错误详情——
		// last_error 会把面板基址与面板侧错误原文带出来，panel_base 本身就是
		// 内网拓扑，二者都不该给非管理员。
		u, _ := userFrom(r.Context())
		canAdmin := u.Trusted && u.IsAdmin
		st["can_admin"] = canAdmin
		if canAdmin {
			// 0.6.349：下发解析后的面板基址（前端局域网分支据此填默认值，
			// 不再靠猜 5666/5667；issue moo#7 自定义端口）。
			pb := panel.ResolveBaseCached("")
			st["panel_base"] = pb.URL
			st["panel_base_source"] = pb.Source
		} else {
			// 双保险：即使 Status 将来新增敏感键，非管理员也拿不到。
			delete(st, "last_error")
			delete(st, "panel_base")
			delete(st, "panel_base_source")
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("GET /api/official/authorize", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		// 0.6.253：?base= 指定用户浏览器可达的面板地址（如
		// http://192.0.2.22:5666）；缺省用本机回环（浏览器不可达，
		// 仅供本机调试）。授权 URL 不含密钥，覆盖仅改 host:port。
		// 2026-11-26（加固 A）：?scope=full|minimal（白名单，非法 400；缺省 full
		// → 旧客户端行为完全不变）。
		scope, serr := officialScopeParam(r.URL.Query().Get("scope"))
		if serr != nil {
			writeErr(w, http.StatusBadRequest, serr)
			return
		}
		urlStr, err := store.mgr.BeginAuthorizationAtScope(s.officialBrowserBase(r), scope)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"url": urlStr})
	}))
	mux.HandleFunc("POST /api/official/cancel", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		store.mgr.Cancel()
		writeJSON(w, map[string]any{"ok": true})
	}))
	// 0.6.254：无头授权（旧版 fnOS 前端无授权 UI 时的替代路径；
	// 复用面板账号登录取码换 token，授权后目录免面板登录）。
	mux.HandleFunc("POST /api/official/authorize-headless", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		s.headlessAuthorize(w, r)
	}))
	mux.HandleFunc("POST /api/official/callback", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		if err := jsonDecode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := store.mgr.CompleteAuthorization(r.Context(), body.Code); err != nil {
			// 2026-11-26（加固 A）：面板拒绝 minimal 范围 → **400 + error_code**
			// （其它失败仍是 502）。前端据此**弹窗让用户显式确认**是否改用完整权限重试
			// （2026-11-27 批次 3c：不再静默扩权；用户取消则保持未连接、不写任何设置）。
			if errors.Is(err, official.ErrScopeRejected) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":      err.Error(),
					"error_code": "scope_rejected",
					"scope":      official.ScopeModeMinimal,
				})
				return
			}
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		// 0.6.255：授权成功 → 清失败退避 + 失效目录缓存，官方目录立即
		// 按新会话拉取（否则退避窗口会压住授权后的首次刷新）。
		s.Panel.resetFailState()
		store.invalidate()
		// 0.6.313 B4：授权成功改变官方卡数据（buildCatalog 的
		// mergeOfficialCards 会并入官方目录条目，已装/更新判定随之变化）
		// → 失效目录 + body 缓存，/api/apps 立即反映新会话的官方卡。
		s.invalidateCatalog()
		writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("POST /api/official/logout", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		store.mgr.Logout()
		// 0.6.313 B4：登出移除官方卡（官方源转「未连接」，同一数据面的
		// 反向变化）→ 同样失效目录 + body 缓存。
		s.invalidateCatalog()
		writeJSON(w, map[string]any{"ok": true})
	}))
	// 2026-11-26（加固 B2）：会话落盘加密开关（opt-in，默认关）。
	// 语义：先切运行态（含把当前内存会话立即按新形态重写一次），成功后才落配置；
	// 配置落盘失败 → 回滚运行态与标志再报错，保证「配置文件 == 运行态」。
	// 开启需要本机密钥可用（否则会写出自己都解不开的密文）。
	mux.HandleFunc("POST /api/official/session-encryption", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if err := jsonDecode(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if body.Enabled == nil {
			writeErr(w, http.StatusBadRequest, errors.New("缺少 enabled 参数"))
			return
		}
		want := *body.Enabled
		if want && !store.mgr.EncryptionAvailable() {
			writeErr(w, http.StatusBadRequest, errSessionKeyUnavailable)
			return
		}
		prev := s.cfg().OfficialSessionEncrypt
		if err := store.mgr.SetEncryption(want); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.cfg().OfficialSessionEncrypt = want
		if err := s.cfg().Save(dataDirOf(s)); err != nil {
			s.cfg().OfficialSessionEncrypt = prev
			if rbErr := store.mgr.SetEncryption(prev); rbErr != nil {
				log.Printf("[official] 回滚会话加密状态失败: %v", rbErr)
			}
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("保存配置失败: %w", err))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "encrypted": store.mgr.SessionEncrypted()})
	}))
	mux.HandleFunc("GET /api/official/apps", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		apps, err := store.list(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"total": len(apps), "list": apps})
	}))
	mux.HandleFunc("GET /api/official/search", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		kw := r.URL.Query().Get("keyword")
		if kw == "" {
			writeErr(w, http.StatusBadRequest, errKeywordRequired)
			return
		}
		apps, err := store.mgr.StoreSearch(r.Context(), kw)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, map[string]any{"total": len(apps), "list": apps})
	}))
	mux.HandleFunc("GET /api/official/apps/{name}", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		detail, err := store.mgr.StoreDetail(r.Context(), r.PathValue("name"))
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, detail)
	}))
}

// list 带 TTL 缓存的商店全量列表。
func (s *officialStore) list(ctx context.Context) ([]official.StoreApp, error) {
	s.mu.Lock()
	if s.at.Add(s.ttl).After(time.Now()) && s.fetchErr == "" {
		d := s.data
		s.mu.Unlock()
		return d, nil
	}
	s.mu.Unlock()
	var apps []official.StoreApp
	var err error
	if s.hasOAuthSession() {
		apps, err = s.mgr.StoreList(ctx)
	}
	// 0.6.259：OAuth 不可用（无会话/拉空/失败）→ 面板账号兜底（老面板无 OAuth 时）。
	if (err != nil || len(apps) == 0) && s.fallback != nil {
		if fb, fbErr := s.fallback(ctx); fbErr == nil && len(fb) > 0 {
			apps, err = fb, nil
		} else if err == nil {
			err = fbErr // 面板兜底也失败 → 报告面板侧错误（区别于 OAuth 未连接）
		}
	}
	s.mu.Lock()
	s.at = time.Now()
	if err != nil {
		s.fetchErr = err.Error()
		s.mu.Unlock()
		// 拉取失败时退回旧缓存（若有）
		if len(s.data) > 0 {
			return s.data, nil
		}
		return nil, err
	}
	s.data = apps
	s.fetchErr = ""
	s.mu.Unlock()
	return apps, nil
}
