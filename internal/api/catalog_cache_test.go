package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// newCatalogTestServer 造一个带 1 个真实应用的目录测试环境
//（httptest 源 + 空 daemon，ListInstalled 失败被忽略→仅源条目）。
func newCatalogTestServer(t *testing.T) *Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	cfg := &config.Config{}
	m := source.NewManager(cfg)
	if err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return &Server{Src: m, Cfg: cfg}
}

// TestCatalogCache 目录缓存：TTL 内命中（同底层切片）、过期重建、
// 显式失效、副本隔离（列表瘦身改副本不得污染共享缓存——污染会让
// 详情响应丢失 changelog 等字段）。
func TestCatalogCache(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour // 拉长 TTL，单独控制过期路径

	c1 := s.cachedCatalog("zh-CN")
	if len(c1) != 1 || c1[0].AppName != "demo" {
		t.Fatalf("目录应有 1 条 demo，实际 %d", len(c1))
	}
	c2 := s.cachedCatalog("zh-CN")
	if &c1[0] != &c2[0] {
		t.Error("TTL 内二次读取应命中同一缓存切片")
	}

	// 副本隔离：瘦身式修改副本，共享缓存不受影响
	cp := s.cachedCatalogCopy("zh-CN")
	cp[0].DisplayName = "被污染"
	cp[0].ChangelogEntries = nil
	if got := s.cachedCatalog("zh-CN")[0].DisplayName; got == "被污染" {
		t.Error("修改副本污染了共享缓存（缓存切片被共享引用）")
	}

	// 显式失效 → 重建
	s.invalidateCatalog()
	c3 := s.cachedCatalog("zh-CN")
	if &c3[0] == &c1[0] {
		t.Error("失效后应重建新切片")
	}
	if c3[0].DisplayName != "演示" {
		t.Errorf("重建后内容应完整，DisplayName=%q", c3[0].DisplayName)
	}

	// TTL 过期 → 重建
	s.catalogTTL = 30 * time.Millisecond
	time.Sleep(60 * time.Millisecond)
	c4 := s.cachedCatalog("zh-CN")
	if &c4[0] == &c3[0] {
		t.Error("TTL 过期后应重建新切片")
	}
}

// TestAppDetailUsesCache 详情接口走缓存：连续两次详情请求内容一致，
// 且列表瘦身（appsWithEtag 会剥离副本字段）后详情仍带完整字段。
func TestAppDetailUsesCache(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/{key}", s.appDetail)
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil).WithContext(t.Context()))
		return rec
	}

	rec1 := get("/api/apps/demo")
	if rec1.Code != http.StatusOK {
		t.Fatalf("detail 应 200, got %d", rec1.Code)
	}

	// 列表瘦身走副本：剥离 ChangelogEntries 等字段
	recList := httptest.NewRecorder()
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /api/apps", s.appsWithEtag)
	mux2.ServeHTTP(recList, httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context()))
	if recList.Code != http.StatusOK {
		t.Fatalf("list 应 200, got %d", recList.Code)
	}

	rec2 := get("/api/apps/demo")
	if rec2.Code != http.StatusOK {
		t.Fatalf("瘦身后的详情应仍 200, got %d", rec2.Code)
	}
	if rec1.Body.String() != rec2.Body.String() {
		t.Error("列表瘦身（副本）不得改变共享缓存里的详情内容")
	}
}
