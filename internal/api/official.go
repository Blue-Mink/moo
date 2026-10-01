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

// officialRoutes 注册 /api/official/* 路由。
func (s *Server) officialRoutes(mux *http.ServeMux, store *officialStore) {
	mux.HandleFunc("GET /api/official/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, store.mgr.Status(time.Now()))
	})
	mux.HandleFunc("GET /api/official/authorize", s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		urlStr, err := store.mgr.BeginAuthorization()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"url": urlStr})
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
