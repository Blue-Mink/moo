package api

// 0.6.326z5 单测（后端两条修复）：
//
//	① WP1 /api/recommended 固定位：实时卡 Hidden/UserHidden ⇒ 整位跳过且
//	   不回落快照（回落=隐藏位照样出现，等于禁用/去重对该位置无效）。
//	② WP2 /api/apps 体缓存（cachedAppsBody）：体缓存必须与目录**同代**——
//	   marshal 在锁外，落盘前复核代际号；目录前进（invalidateCatalog /
//	   finishCatalogRebuild 换入新切片）一并作废体缓存。旧实现的漏判会让
//	   基于旧代目录的响应体长期驻留（源启用瞬间漏卡 >150s 的根因）。
//
// 纯本地装置：假源缓存注入（source.Manager.SeedCacheForTest）+ 预置目录
// 切片；不触外网、不依赖 daemon（ListInstalled 失败被忽略=仅源条目）。

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/reco"
	"moo/internal/source"
)

// ───────────────────────── 装置 ─────────────────────────

// z5PoolCards 造 n 张可见池卡（pool01..poolNN，带图标=随机补足池的
// 「优先有图标」桶），用于验证固定位跳过后由随机池补齐。
func z5PoolCards(n int) []AppInfo {
	out := make([]AppInfo, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("pool%02d", i)
		out = append(out, AppInfo{
			Key:           name + "@z5",
			AppName:       name,
			Source:        "z5",
			IconURL:       "https://example.com/icons/" + name + ".png",
			LatestVersion: "1.0.0",
		})
	}
	return out
}

// z5FixedCard 造某个固定推荐位的实时卡（Source 用内嵌快照的规范源名 →
// fixedSlotLive 走「首选源精确命中」分支；快照缺失则用 z5src，仍走精确
// 匹配分支，判定结果一致）。
func z5FixedCard(appName string) AppInfo {
	src := "z5src"
	if snap := reco.Lookup(appName); snap != nil && snap.Source != "" {
		src = snap.Source
	}
	return AppInfo{
		Key:           appName + "@" + src,
		AppName:       appName,
		Source:        src,
		IconURL:       "https://example.com/icons/" + appName + ".png",
		LatestVersion: "9.9.9",
	}
}

// z5ServerWithCatalog 预置目录切片（新鲜）→ recommended / cachedAppsBody
// 不再触发重建；catalogData 非 nil（空目录传空切片而非 nil）。
func z5ServerWithCatalog(t *testing.T, catalog []AppInfo) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	if catalog == nil {
		catalog = []AppInfo{}
	}
	s.catalogData = catalog
	s.catalogAt = time.Now()
	s.catalogTTL = time.Hour
	return s
}

// z5Recommended 打一次 /api/recommended?count=N，返回 apps 列表。
func z5Recommended(t *testing.T, s *Server, count int) []AppInfo {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/recommended", s.recommended)
	rec := httptest.NewRecorder()
	url := fmt.Sprintf("/api/recommended?count=%d", count)
	mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/recommended 应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []AppInfo `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析推荐响应: %v", err)
	}
	return resp.Apps
}

// z5IndexOf 按归一 appname 找下标（-1=未出现）。
func z5IndexOf(apps []AppInfo, appName string) int {
	want := reco.NormAppName(appName)
	for i, a := range apps {
		if reco.NormAppName(a.AppName) == want {
			return i
		}
	}
	return -1
}

// z5Names 便于失败信息阅读。
func z5Names(apps []AppInfo) string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, fmt.Sprintf("%s(hidden=%v,user=%v)", a.AppName, a.Hidden, a.UserHidden))
	}
	return fmt.Sprint(out)
}

// z5AssertUnique 断言无重复 appname（归一口径，与 handler 的 used 同规则）。
func z5AssertUnique(t *testing.T, apps []AppInfo) {
	t.Helper()
	seen := map[string]bool{}
	for _, a := range apps {
		n := reco.NormAppName(a.AppName)
		if seen[n] {
			t.Fatalf("推荐结果出现重复 appname %q: %s", a.AppName, z5Names(apps))
		}
		seen[n] = true
	}
}

// z5SrcApp 一张假源卡（本地假源，不触外网）。
func z5SrcApp(name string) *source.App {
	return &source.App{
		Name:        name,
		Source:      "z5",
		DisplayName: "演示 " + name,
		Version:     "1.0.0",
		DownloadURL: "https://example.com/" + name + ".fpk",
		IconURL:     "https://example.com/icons/" + name + ".png",
	}
}

// z5SrcCards 组一个源的内层缓存（appname → 卡）。
func z5SrcCards(names ...string) map[string]*source.App {
	out := make(map[string]*source.App, len(names))
	for _, n := range names {
		out[n] = z5SrcApp(n)
	}
	return out
}

// z5SrcServer 造 Server：Src = 已注入缓存的假源（buildCatalog 可真实跑，
// ListInstalled 失败被忽略=仅源条目），目录切片预置为 catalog（新鲜，
// TTL 1h）。返回 Manager 供测试**经锁**改种缓存（直接改 map 会与后台重建
// 的读并发 → -race 报数据竞争）。
func z5SrcServer(t *testing.T, catalog []AppInfo, names ...string) (*Server, *source.Manager) {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "z5", URL: "https://example.com/z5/fnpack.json"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{"z5": z5SrcCards(names...)})
	s := &Server{Cfg: cfg, Src: m}
	if catalog == nil {
		catalog = []AppInfo{}
	}
	s.catalogData = catalog
	s.catalogAt = time.Now()
	s.catalogTTL = time.Hour
	return s, m
}

// z5Get 经真实 handler 打一次 GET /api/apps（url 可带 ?source= 等参数；
// 无参数=体缓存热路径）。
func z5Get(t *testing.T, s *Server, url, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", s.appsWithEtag)
	req := httptest.NewRequest("GET", url, nil).WithContext(t.Context())
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// z5CatalogHas 当前目录切片是否含某 appname（锁内读）。
func z5CatalogHas(s *Server, appName string) bool {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	for _, a := range s.catalogData {
		if a.AppName == appName {
			return true
		}
	}
	return false
}

// z5Trunc 失败信息里截断长字节序列。
func z5Trunc(b []byte) string {
	const max = 220
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// ─────────────── WP1：固定位隐藏矩阵 ───────────────

// TestZ5_RecommendedFixedSlotMatrix 矩阵：{live 可见 / live UserHidden /
// live Hidden / 不在目录→快照 / 目录空} × 每个固定位 × count=3 与 5。
// 断言：隐藏位既不 live 也不快照出现；可见位（live / 快照回落）必须在位；
// live 全可见时前三与 FixedOrder 同序；总数 == want（池足够时）；无重复。
func TestZ5_RecommendedFixedSlotMatrix(t *testing.T) {
	fixed := reco.FixedOrder()
	if len(fixed) == 0 {
		t.Skip("内嵌固定推荐快照缺失（reco.FixedOrder 为空）")
	}
	states := []string{"live", "userhidden", "hidden", "absent", "emptydir"}
	for _, target := range fixed {
		for _, state := range states {
			for _, want := range []int{3, 5} {
				name := fmt.Sprintf("%s/%s/count%d", target, state, want)
				t.Run(name, func(t *testing.T) {
					var catalog []AppInfo
					if state != "emptydir" {
						for _, n := range fixed {
							if state == "absent" && n == target {
								continue // 该位不在目录（但内嵌快照有）→ 应回落快照
							}
							c := z5FixedCard(n)
							if n == target {
								switch state {
								case "hidden":
									c.Hidden = true
								case "userhidden":
									c.UserHidden = true
								}
							}
							catalog = append(catalog, c)
						}
						catalog = append(catalog, z5PoolCards(12)...)
					}
					s := z5ServerWithCatalog(t, catalog)
					out := z5Recommended(t, s, want)

					switch state {
					case "hidden", "userhidden":
						// 核心：隐藏位既不得走 live，也不得回落快照。
						if i := z5IndexOf(out, target); i >= 0 {
							t.Fatalf("隐藏固定位 %s（%s）不得出现在推荐里：pos=%d %s",
								target, state, i, z5Names(out))
						}
					case "live", "absent":
						// live 可见 / 目录无该应用 → 都必须在位（后者走快照回落）。
						if i := z5IndexOf(out, target); i < 0 {
							t.Fatalf("固定位 %s（%s）应在位：%s", target, state, z5Names(out))
						}
					}

					// 位序：live 全部可见时，前三应与 FixedOrder 同序（语义不许动）。
					if state == "live" && len(out) >= len(fixed) {
						for i, n := range fixed {
							if reco.NormAppName(out[i].AppName) != reco.NormAppName(n) {
								t.Fatalf("固定位序漂移：pos%d=%q 期望 %q（%s）",
									i, out[i].AppName, n, z5Names(out))
							}
						}
					}

					// 总数：池足够 → want；目录空 → 只有快照回落位（len(fixed)）。
					wantTotal := want
					if state == "emptydir" {
						wantTotal = len(fixed)
					}
					if len(out) != wantTotal {
						t.Fatalf("%s 应 %d 张，实际 %d：%s", name, wantTotal, len(out), z5Names(out))
					}
					z5AssertUnique(t, out)
				})
			}
		}
	}
}

// TestZ5_RecommendedMultiHiddenFixedSlots 盲点用例：同时隐藏多个固定位
// （混用 Hidden / UserHidden），甚至三个全隐藏——空位必须由随机池逐一补齐、
// 总数与 want 一致、无重复，且被隐藏的 appname 一律不得出现。
func TestZ5_RecommendedMultiHiddenFixedSlots(t *testing.T) {
	fixed := reco.FixedOrder()
	if len(fixed) < 3 {
		t.Skipf("固定推荐位不足 3（实际 %d）", len(fixed))
	}
	cases := []struct {
		name    string
		hidden  map[int]string // 位序 → "hidden" / "userhidden"
		want    int
		expects []string // 仍应在位的固定位 appname
	}{
		{"两个隐藏(count3)", map[int]string{1: "hidden", 2: "userhidden"}, 3, fixed[:1]},
		{"两个隐藏(count5)", map[int]string{1: "hidden", 2: "userhidden"}, 5, fixed[:1]},
		{"三个全隐藏(count3)", map[int]string{0: "hidden", 1: "hidden", 2: "userhidden"}, 3, nil},
		{"三个全隐藏(count5)", map[int]string{0: "hidden", 1: "userhidden", 2: "hidden"}, 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var catalog []AppInfo
			for i, n := range fixed {
				c := z5FixedCard(n)
				if kind, ok := tc.hidden[i]; ok {
					if kind == "hidden" {
						c.Hidden = true
					} else {
						c.UserHidden = true
					}
				}
				catalog = append(catalog, c)
			}
			catalog = append(catalog, z5PoolCards(12)...)
			s := z5ServerWithCatalog(t, catalog)
			out := z5Recommended(t, s, tc.want)

			if len(out) != tc.want {
				t.Fatalf("应 %d 张，实际 %d：%s", tc.want, len(out), z5Names(out))
			}
			z5AssertUnique(t, out)
			for i, n := range fixed {
				pos := z5IndexOf(out, n)
				if _, hidden := tc.hidden[i]; hidden {
					if pos >= 0 {
						t.Fatalf("被隐藏固定位 %s 不得出现：%s", n, z5Names(out))
					}
					continue
				}
				if pos < 0 {
					t.Fatalf("未隐藏固定位 %s 应在位：%s", n, z5Names(out))
				}
			}
			for _, n := range tc.expects {
				if z5IndexOf(out, n) < 0 {
					t.Fatalf("%s 应在位：%s", n, z5Names(out))
				}
			}
		})
	}
}

// ───────── WP2：体缓存与目录同代（R1） ─────────

// TestZ5_BodyCacheOldGenerationNotStored —— **机制定位决定性用例**。
//
// 证明的机制（修复前必 FAIL）：cachedAppsBody 取出目录快照 → marshal 期间
// 目录被推进一代（生产里=后台重建/失效把含新卡的切片换入）→ 旧实现把这次
// 「旧代」结果**无条件落盘**，而此后没有任何事件会清掉它 ⇒ 该响应体长期
// 驻留（源启用瞬间并发 GET 漏卡 >150s 的根因）。
//
// 用测试缝 appsBodyHook（生产恒 nil）把交错点钉死在「已 marshal、未落盘」，
// 保证确定性；目录前进走真实生产函数 finishCatalogRebuild（SWR/后台重建
// 收尾），不构造假替代。
func TestZ5_BodyCacheOldGenerationNotStored(t *testing.T) {
	s, m := z5SrcServer(t, []AppInfo{{AppName: "alpha", Source: "z5", Key: "alpha@z5"}}, "alpha")

	var once sync.Once
	s.appsBodyHook = func() {
		once.Do(func() {
			// 源侧抓取完成（经管理器锁注入）：apha + beta
			m.SeedCacheForTest(map[string]map[string]*source.App{"z5": z5SrcCards("alpha", "beta")})
			// 目录前进一代（真实收尾函数：原子换入新切片，含 beta）
			s.finishCatalogRebuild("zh-CN", make(chan struct{}))
		})
	}
	t.Cleanup(func() { s.appsBodyHook = nil })

	body, etag, ok := s.cachedAppsBody(
		httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context()))
	if !ok || etag == "" {
		t.Fatalf("首次未命中应能构建响应体: ok=%v etag=%q", ok, etag)
	}
	if bytes.Contains(body, []byte("beta")) {
		t.Fatalf("装置前提被破坏：本次体应在目录前进前 marshal（不含 beta）：%s", z5Trunc(body))
	}

	// 核心断言①：旧代结果不得落盘（落盘 = 此后所有请求长期拿到缺卡体）
	s.catalogBodyMu.Lock()
	cached, gen := s.catalogBody, s.catalogBodyGen
	s.catalogBodyMu.Unlock()
	if cached != nil {
		t.Fatalf("旧代响应体被落盘（体缓存未与目录同代）：gen=%d body=%s", gen, z5Trunc(cached))
	}

	// 核心断言②：下一个请求必须看到新代（含 beta）
	rec := z5Get(t, s, "/api/apps", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("后续请求应 200, got %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("beta")) {
		t.Fatalf("后续请求仍拿到旧代（缺 beta）：%s", z5Trunc(rec.Body.Bytes()))
	}
}

// TestZ5_BodyCacheSWRRebuildDoesNotKeepStaleBody 真实时序（无测试缝）：
// 目录过期有旧数据 → SWR 立即返回旧代并后台重建；重建换入含新卡的切片后，
// 旧代响应体不得继续驻留（修复前：体缓存不会被重建作废 → rec2 仍是旧代）。
func TestZ5_BodyCacheSWRRebuildDoesNotKeepStaleBody(t *testing.T) {
	s, m := z5SrcServer(t, []AppInfo{{AppName: "alpha", Source: "z5", Key: "alpha@z5"}}, "alpha")

	// 源抓取完成：源侧多出 beta
	m.SeedCacheForTest(map[string]map[string]*source.App{"z5": z5SrcCards("alpha", "beta")})
	// 目录过期（旧代数据仍在）→ 请求走 SWR 分支（立即返回旧数据 + 后台重建）
	s.catalogMu.Lock()
	s.catalogAt = time.Now().Add(-2 * time.Hour)
	s.catalogMu.Unlock()

	rec1 := z5Get(t, s, "/api/apps", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("首请求应 200, got %d", rec1.Code)
	}
	if bytes.Contains(rec1.Body.Bytes(), []byte("beta")) {
		t.Fatalf("SWR 应立即返回旧代数据（不含 beta）：%s", z5Trunc(rec1.Body.Bytes()))
	}

	deadline := time.Now().Add(15 * time.Second)
	for !z5CatalogHas(s, "beta") {
		if time.Now().After(deadline) {
			t.Fatal("后台重建未换入含 beta 的新切片（装置前提未达成）")
		}
		time.Sleep(10 * time.Millisecond)
	}

	rec2 := z5Get(t, s, "/api/apps", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("第二请求应 200, got %d", rec2.Code)
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte("beta")) {
		t.Fatalf("旧代响应体越过目录重建长期驻留：%s", z5Trunc(rec2.Body.Bytes()))
	}
}

// TestZ5_AppsBodyCacheConcurrentGenerationStress 并发压力（-race 下跑）：
// 8 个 goroutine 反复全量 GET + 写方 30 次目录代际前进（invalidateCatalog）。
// 断言：每个响应 200、ETag == md5(body)、体是合法 JSON；静默期（无在途
// 请求）体缓存必属当前代且与最新响应逐字节一致（无旧代残留）。
func TestZ5_AppsBodyCacheConcurrentGenerationStress(t *testing.T) {
	s, _ := z5SrcServer(t, []AppInfo{
		{AppName: "alpha", Source: "z5", Key: "alpha@z5"},
		{AppName: "beta", Source: "z5", Key: "beta@z5"},
	}, "alpha", "beta")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps", s.appsWithEtag)
	ctx := context.Background()
	doGet := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps", nil).WithContext(ctx))
		return rec
	}

	var (
		wg   sync.WaitGroup
		stop = make(chan struct{})
		mu   sync.Mutex
		errs []string
	)
	fail := func(format string, a ...any) {
		mu.Lock()
		if len(errs) < 20 {
			errs = append(errs, fmt.Sprintf(format, a...))
		}
		mu.Unlock()
	}

	wg.Add(1)
	go func() { // 写方：目录代际反复前进（清目录 + 清体缓存 + 代际++）
		defer wg.Done()
		defer close(stop)
		for i := 0; i < 30; i++ {
			s.invalidateCatalog()
			time.Sleep(time.Millisecond)
		}
	}()

	const readers = 8
	for g := 0; g < readers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := doGet()
				if rec.Code != http.StatusOK {
					fail("reader%d: GET /api/apps 应 200, got %d", g, rec.Code)
					return
				}
				body := rec.Body.Bytes()
				want := `"` + fmt.Sprintf("%x", md5.Sum(body)) + `"`
				if got := rec.Header().Get("ETag"); got != want {
					fail("reader%d: ETag 与响应体不一致: %q vs %q", g, got, want)
					return
				}
				var resp struct {
					Apps []AppInfo `json:"apps"`
				}
				if err := json.Unmarshal(body, &resp); err != nil {
					fail("reader%d: 响应体不是合法 JSON: %v", g, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("并发压力下断言失败 %d 条：%v", len(errs), errs)
	}

	// 静默期（无在途请求）：体缓存必属当前代
	s.invalidateCatalog()
	rec1 := doGet()
	rec2 := doGet()
	if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
		t.Fatalf("静默期请求应 200, got %d/%d", rec1.Code, rec2.Code)
	}
	if rec2.Body.String() != rec1.Body.String() {
		t.Fatal("静默期第二次请求应命中同代体缓存（字节应一致）")
	}
	if !bytes.Contains(rec1.Body.Bytes(), []byte("alpha")) || !bytes.Contains(rec1.Body.Bytes(), []byte("beta")) {
		t.Fatalf("静默期响应应含当前目录全部卡：%s", z5Trunc(rec1.Body.Bytes()))
	}
	s.catalogBodyMu.Lock()
	cached := s.catalogBody
	s.catalogBodyMu.Unlock()
	if cached == nil {
		t.Fatal("静默期请求后体缓存应已填充")
	}
	if string(cached) != rec1.Body.String() {
		t.Fatalf("体缓存字节 ≠ 当前代响应（旧代残留）：%s", z5Trunc(cached))
	}
}

// TestZ5_ETagAndSourcePathSemanticsPreserved 硬约束回归：ETag/304 语义、
// Cache-Control、以及「有 ?source= 参数的路径不读也不写体缓存」都不因
// WP2 改动而变。
func TestZ5_ETagAndSourcePathSemanticsPreserved(t *testing.T) {
	s, _ := z5SrcServer(t, []AppInfo{{AppName: "alpha", Source: "z5", Key: "alpha@z5"}}, "alpha")

	rec1 := z5Get(t, s, "/api/apps", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("首次应 200, got %d", rec1.Code)
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatal("全量响应应带 ETag")
	}
	if cc := rec1.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control 应为 no-cache, got %q", cc)
	}
	if want := `"` + fmt.Sprintf("%x", md5.Sum(rec1.Body.Bytes())) + `"`; etag != want {
		t.Fatalf("ETag 应为响应体 md5: %q vs %q", etag, want)
	}

	rec2 := z5Get(t, s, "/api/apps", etag)
	if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
		t.Fatalf("If-None-Match 命中应 304 空体, got %d/%d", rec2.Code, rec2.Body.Len())
	}

	// ?source= 路径：现算过滤，不读也不覆盖体缓存
	s.catalogBodyMu.Lock()
	before := string(s.catalogBody)
	s.catalogBodyMu.Unlock()
	rec3 := z5Get(t, s, "/api/apps?source=z5", "")
	if rec3.Code != http.StatusOK {
		t.Fatalf("?source= 应 200, got %d", rec3.Code)
	}
	s.catalogBodyMu.Lock()
	after := string(s.catalogBody)
	s.catalogBodyMu.Unlock()
	if after != before {
		t.Fatal("带 source 参数的请求不得覆盖体缓存")
	}
}
