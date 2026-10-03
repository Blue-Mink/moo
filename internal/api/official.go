package api

// 官方应用中心（fnOS 官方源）API：OAuth 连接 + 商店列表/搜索/详情。
//
// 数据来自本机面板 /ogh/ac/h 代理（127.0.0.1:5666），OAuth token 由
// internal/official 管理（PKCE + 自动刷新 + 持久化）。列表 10 分钟缓存，
// 写操作前手动刷新。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"moo/internal/official"
)

var errKeywordRequired = errors.New("keyword 不能为空")

type officialStore struct {
	mu    sync.Mutex
	mgr   *official.Manager
	data  []official.StoreApp
	at    time.Time
	ttl   time.Duration
	fetchErr string
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

// officialRoutes 注册 /api/official/* 路由。
func (s *Server) officialRoutes(mux *http.ServeMux, store *officialStore) {
	mux.HandleFunc("GET /api/official/status", func(w http.ResponseWriter, r *http.Request) {
		st := store.mgr.Status(time.Now())
		// 0.6.254：面板前端授权 UI 支持状态（仅 fnOS 1.2.0800+ 前端有 PKCE 授权页；
		// 旧版只渲染普通登录页，iframe 流程走不完）。known=false = 检测中。
		sup, known := store.mgr.UISupportStatus()
		st["ui_supported"] = sup
		st["ui_known"] = known
		writeJSON(w, st)
	})
	mux.HandleFunc("GET /api/official/authorize", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		// 0.6.253：?base= 指定用户浏览器可达的面板地址（如
		// http://192.0.2.22:5666）；缺省用本机回环（浏览器不可达，
		// 仅供本机调试）。授权 URL 不含密钥，覆盖仅改 host:port。
		urlStr, err := store.mgr.BeginAuthorizationAt(s.officialBrowserBase(r))
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
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		// 0.6.255：授权成功 → 清失败退避 + 失效目录缓存，官方目录立即
		// 按新会话拉取（否则退避窗口会压住授权后的首次刷新）。
		s.Panel.resetFailState()
		store.invalidate()
		writeJSON(w, map[string]any{"ok": true})
	}))
	mux.HandleFunc("POST /api/official/logout", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		store.mgr.Logout()
		writeJSON(w, map[string]any{"ok": true})
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
	apps, err := s.mgr.StoreList(ctx)
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
