package api

// 0.6.313 B4 单测：body 缓存 / F11 SWR / 失效矩阵 / MOO_CATALOG_TTL 解析。
// 配套文件：icon_06313_test.go（图标纯磁盘）、cmd/server/memlimit_06313_test.go。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedAppsBody 经 GET /api/apps handler（无 source 参数）预热 body 缓存，返回 ETag。
func seedAppsBody(t *testing.T, s *Server) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", s.appsWithEtag)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/apps 应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("列表响应应带 ETag")
	}
	return etag
}

// assertBodyCleared 断言 body 缓存已清空（字节与 ETag 都复位）。
func assertBodyCleared(t *testing.T, s *Server) {
	t.Helper()
	s.catalogBodyMu.Lock()
	body, etag := s.catalogBody, s.catalogBodyETag
	s.catalogBodyMu.Unlock()
	if body != nil || etag != "" {
		t.Fatal("body 缓存应已清空")
	}
}

// catalogDataPtr 返回目录缓存首元素指针（nil=未构建/已失效）。
func catalogDataPtr(t *testing.T, s *Server) *AppInfo {
	t.Helper()
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalogData == nil {
		return nil
	}
	return &s.catalogData[0]
}

// indexDemo 找 demo 条目下标（找不到 -1；goroutine 安全，不用 t.Fatalf）。
func indexDemo(c []AppInfo) int {
	for i, a := range c {
		if a.AppName == "demo" {
			return i
		}
	}
	return -1
}

// TestInvalidateCatalog_ClearsBothCaches_06313 失效语义：目录 + body 两个
// 缓存一并清空（body 缓存与目录缓存同一钩子，B4 项 2b/2d）。
func TestInvalidateCatalog_ClearsBothCaches_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	etag := seedAppsBody(t, s)
	if catalogDataPtr(t, s) == nil {
		t.Fatal("目录缓存应已构建")
	}
	s.catalogBodyMu.Lock()
	body, cachedETag := s.catalogBody, s.catalogBodyETag
	s.catalogBodyMu.Unlock()
	if body == nil || cachedETag != etag {
		t.Fatalf("body 缓存应已填充且 ETag 与响应一致: %q vs %q", cachedETag, etag)
	}

	s.invalidateCatalog()

	if p := catalogDataPtr(t, s); p != nil {
		t.Fatal("失效后目录缓存应为 nil")
	}
	assertBodyCleared(t, s)
}

// TestAppsWithEtag_BodyCache_06313 body 缓存行为：304 重校验 / 命中字节一致 /
// 有 source 参数不走也不覆盖缓存 / 失效后下次全量请求重建。
func TestAppsWithEtag_BodyCache_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", s.appsWithEtag)
	get := func(url, ifNoneMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", url, nil).WithContext(t.Context())
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec1 := get("/api/apps", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("首次应 200, got %d", rec1.Code)
	}
	etag := rec1.Header().Get("ETag")

	rec2 := get("/api/apps", etag)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match 命中应 304, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatal("304 响应体应为空")
	}

	rec3 := get("/api/apps", "")
	if rec3.Code != http.StatusOK || rec3.Body.String() != rec1.Body.String() {
		t.Fatalf("body 缓存命中应返回同一字节, got %d", rec3.Code)
	}

	// 有 source 参数：现算过滤，不读也不覆盖 body 缓存
	s.catalogBodyMu.Lock()
	bodyBefore := string(s.catalogBody)
	s.catalogBodyMu.Unlock()
	rec4 := get("/api/apps?source=tst", "")
	if rec4.Code != http.StatusOK {
		t.Fatalf("?source= 应 200, got %d", rec4.Code)
	}
	var resp4 struct {
		Apps []AppInfo `json:"apps"`
	}
	if err := json.Unmarshal(rec4.Body.Bytes(), &resp4); err != nil {
		t.Fatalf("解析 ?source= 响应: %v", err)
	}
	if len(resp4.Apps) == 0 {
		t.Fatal("?source=tst 应返回至少一条")
	}
	for _, a := range resp4.Apps {
		if a.Source != "tst" {
			t.Fatalf("?source= 过滤泄漏: %q", a.Source)
		}
	}
	s.catalogBodyMu.Lock()
	bodyAfter := string(s.catalogBody)
	s.catalogBodyMu.Unlock()
	if bodyAfter != bodyBefore {
		t.Fatal("带 source 参数的请求不得覆盖 body 缓存")
	}

	// 失效 → body 清空；下次全量请求重建
	s.invalidateCatalog()
	assertBodyCleared(t, s)
	rec5 := get("/api/apps", "")
	if rec5.Code != http.StatusOK {
		t.Fatal("失效后全量请求应 200")
	}
	s.catalogBodyMu.Lock()
	repop := s.catalogBody != nil
	s.catalogBodyMu.Unlock()
	if !repop {
		t.Fatal("失效后下次全量请求应重建 body 缓存")
	}
}

// TestCatalog_SWR_StaleReturn_06313 F11：过期且有旧数据 → 立即返回旧数据
//（不阻塞），后台重建随后原子换入新切片。
func TestCatalog_SWR_StaleReturn_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	c1 := s.cachedCatalog("zh-CN")
	oldPtr := &c1[findDemo(t, c1)]

	// 强制过期（TTL 1ms + 睡过）
	s.catalogTTL = time.Millisecond
	time.Sleep(10 * time.Millisecond)

	start := time.Now()
	c2 := s.cachedCatalog("zh-CN")
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("SWR 应立即返回旧数据，实际耗时 %s", elapsed)
	}
	if i := indexDemo(c2); i < 0 || &c2[i] != oldPtr {
		t.Fatal("过期且有旧数据时应立即返回旧数据（同切片）")
	}

	// 后台重建应在 5s 内完成（原子换入新切片）
	var c5 []AppInfo
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c5 = s.cachedCatalog("zh-CN")
		if i := indexDemo(c5); i >= 0 && &c5[i] != oldPtr {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if i := indexDemo(c5); i < 0 || &c5[i] == oldPtr {
		t.Fatal("后台重建完成后应换入新切片")
	}
}

// TestCatalog_SWR_NilSyncRebuild_06313 F11：刚失效（catalogData=nil，无旧
// 数据）→ 同步重建（保安装/更新/档位切换反馈即时），返回新切片。
func TestCatalog_SWR_NilSyncRebuild_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	c1 := s.cachedCatalog("zh-CN")
	oldPtr := &c1[findDemo(t, c1)]

	s.invalidateCatalog()
	start := time.Now()
	c2 := s.cachedCatalog("zh-CN")
	i2 := findDemo(t, c2)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("同步重建不应异常慢: %s", time.Since(start))
	}
	if &c2[i2] == oldPtr {
		t.Fatal("失效后（无旧数据）应同步重建返回新切片")
	}
	c3 := s.cachedCatalog("zh-CN")
	if &c3[findDemo(t, c3)] != &c2[i2] {
		t.Fatal("同步重建后二次读取应命中缓存（同切片）")
	}
}

// TestCatalog_SWR_ConcurrentSingleflight_06313 F11：并发两请求同时撞过期
// → 都拿到有效目录（一个立即给旧数据、一个在 done 上等待），无死锁、
// 完成后 building 标志回静（singleflight 原子标志 + done channel）。
func TestCatalog_SWR_ConcurrentSingleflight_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	c1 := s.cachedCatalog("zh-CN")
	_ = c1
	s.catalogTTL = time.Millisecond
	time.Sleep(10 * time.Millisecond)

	const n = 2
	results := make([]int, n)
	var wg sync.WaitGroup
	for g := 0; g < n; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			c := s.cachedCatalog("zh-CN")
			results[g] = indexDemo(c)
		}(g)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("并发 cachedCatalog 疑似死锁（>10s 未完成）")
	}
	for g, i := range results {
		if i < 0 {
			t.Fatalf("并发调用方 %d 应拿到含 demo 的有效目录", g)
		}
	}
	s.catalogMu.Lock()
	building := s.catalogBuilding
	s.catalogMu.Unlock()
	if building {
		t.Fatal("并发读完成后 building 标志应回静")
	}
}

// TestCatalog_SWR_DoneTimeout_06313 F11：done channel 2s 等待上限——重建
// 「永不完成」时超时返回旧数据，不阻塞请求。
func TestCatalog_SWR_DoneTimeout_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	c1 := s.cachedCatalog("zh-CN")
	oldPtr := &c1[findDemo(t, c1)]

	// 构造极端态：已过期 + 重建进行中但 done 永不关闭
	s.catalogMu.Lock()
	s.catalogAt = time.Time{}
	s.catalogBuilding = true
	s.catalogRebuildDone = make(chan struct{}) // 永不 close
	s.catalogMu.Unlock()

	start := time.Now()
	c2 := s.cachedCatalog("zh-CN")
	elapsed := time.Since(start)
	if i := indexDemo(c2); i < 0 || &c2[i] != oldPtr {
		t.Fatal("等待超时应返回旧数据")
	}
	if elapsed < 1500*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("等待超时应约 2s（catalogRebuildWait），实际 %s", elapsed)
	}

	// 清理极端态（后续读取走正常同步重建）
	s.invalidateCatalog()
	if i := indexDemo(s.cachedCatalog("zh-CN")); i < 0 {
		t.Fatal("清理后应能正常同步重建")
	}
}

// TestCatalog_TTLFromEnv_06313 env MOO_CATALOG_TTL：duration 解析 / 非法值
// 回退 60s / 显式 catalogTTL 优先（测试逃生阀）。
func TestCatalog_TTLFromEnv_06313(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 60 * time.Second},
		{"90s", 90 * time.Second},
		{"1m", time.Minute},
		{"abc", 60 * time.Second},  // 非法 → 回退默认
		{"-5s", 60 * time.Second}, // 负值非法 → 回退默认
	}
	for _, c := range cases {
		t.Setenv("MOO_CATALOG_TTL", c.env)
		s := newCatalogTestServer(t)
		s.catalogMu.Lock()
		got := s.catalogTTLValueLocked()
		s.catalogMu.Unlock()
		if got != c.want {
			t.Fatalf("MOO_CATALOG_TTL=%q 应生效 %s，实际 %s", c.env, c.want, got)
		}
	}
	// 显式设置优先于 env
	t.Setenv("MOO_CATALOG_TTL", "5s")
	s := newCatalogTestServer(t)
	s.catalogTTL = 2 * time.Second
	s.catalogMu.Lock()
	got := s.catalogTTLValueLocked()
	s.catalogMu.Unlock()
	if got != 2*time.Second {
		t.Fatalf("显式 catalogTTL 应优先于 env，实际 %s", got)
	}
}

// TestInvalidateMatrix_WriteHandlers_06313 失效矩阵：17 个 invalidateCatalog
// 调用点共享同一函数（语义由 TestInvalidateCatalog_ClearsBothCaches 覆盖）；
// 这里实际驱动 4 条不同形态的写入口 handler → 断言目录 + body 缓存失效：
// 设置保存（update_policy 变更）/ 源改名 / 检查更新（全量刷新）/ 删源。
func TestInvalidateMatrix_WriteHandlers_06313(t *testing.T) {
	s := newCatalogTestServer(t)
	s.catalogTTL = time.Hour
	t.Setenv("MOO_DATA", t.TempDir()) // Cfg.Save 落盘走临时目录

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("POST /api/sources/{id}/rename", s.renameSource)
	mux.HandleFunc("POST /api/check", s.checkUpdates)
	mux.HandleFunc("DELETE /api/sources/{id}", s.removeSource)
	adminDo := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r := httptest.NewRequest(method, target, rd)
		r.Header.Set("X-Moo-Admin", "1")
		r = r.WithContext(withUserCtx(r.Context(), User{Username: "t", UserID: "t", IsAdmin: true, Trusted: true}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}
	step := func(name, method, target, body string) {
		t.Helper()
		seedAppsBody(t, s) // 每步先预热两个缓存
		oldPtr := catalogDataPtr(t, s)
		rec := adminDo(method, target, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("[%s] 应 200, got %d: %s", name, rec.Code, rec.Body.String())
		}
		assertBodyCleared(t, s)
		p := catalogDataPtr(t, s)
		// 目录缓存要么仍为失效态（nil），要么已被后续读取重建为新切片
		if oldPtr != nil && p != nil && p == oldPtr {
			t.Fatalf("[%s] 写入口后目录缓存应失效（或重建为新切片）", name)
		}
	}

	step("设置保存(update_policy)", "PUT", "/api/settings", `{"update_policy":"origin"}`)
	step("源改名", "POST", "/api/sources/tst/rename", `{"name":"tst2"}`)
	step("检查更新(全量刷新)", "POST", "/api/check", "")
	step("删源", "DELETE", "/api/sources/tst2", "")
}
