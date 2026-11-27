package api

// 2026-11-26 B-SRC-LISTSYNC 缺陷 3：新增源后目录必须立即失效（卡片立刻可见）。
//
// 反向验证目标：删掉 autoSyncSourceList / syncList 里的 invalidateCatalog()
// 调用 → 本文件两条用例必须 FAIL（目录缓存 TTL 内仍返回旧目录）。
// 全程 httptest 本地假源，零外网。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// newListCatalogServer 清单 1 条（指向 /src），/src 返回含 demo 的合法 fnpack。
func newListCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(srv.URL + "/src\n"))
	})
	// 兜底：除 /list 外任意候选路径都回同一份 fnpack（索引名候选见 AddSource）。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func catalogHasApp(c []AppInfo, name string) bool {
	for _, a := range c {
		if a.AppName == name {
			return true
		}
	}
	return false
}

// TestListSyncAutoAddedInvalidatesCatalog 自动路径：Added>0 → 目录立即失效。
func TestListSyncAutoAddedInvalidatesCatalog(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListCatalogServer(t)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.catalogTTL = time.Hour // 拉长 TTL：不失效就一定会读到旧目录

	if catalogHasApp(s.cachedCatalog("zh-CN"), "demo") {
		t.Fatal("前置：初始目录不应含 demo")
	}
	s.autoSyncSourceList(t.Context())
	if len(cfg.Sources) != 1 {
		t.Fatalf("前置：应新增 1 源, got %d", len(cfg.Sources))
	}
	if !catalogHasApp(s.cachedCatalog("zh-CN"), "demo") {
		t.Fatal("新增源后目录应立即含新源卡片（缺 invalidateCatalog → 等下一轮才可见）")
	}
}

// TestListSyncManualAddedInvalidatesCatalog 手动「同步源」路径：同款失效。
func TestListSyncManualAddedInvalidatesCatalog(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListCatalogServer(t)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.catalogTTL = time.Hour

	if catalogHasApp(s.cachedCatalog("zh-CN"), "demo") {
		t.Fatal("前置：初始目录不应含 demo")
	}
	rec := httptest.NewRecorder()
	s.syncList(rec, httptest.NewRequest("POST", "/api/sources/sync-list", nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("syncList 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !catalogHasApp(s.cachedCatalog("zh-CN"), "demo") {
		t.Fatal("手动同步新增源后目录应立即含新源卡片（缺 invalidateCatalog）")
	}
}
