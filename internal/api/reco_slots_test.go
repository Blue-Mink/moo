package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/reco"
	"moo/internal/source"
)

// newRecoTestServer 固定推荐测试环境：1 个假源（源名 tst）提供
// fnpack.json（body 可控）；daemon 隔离（SetDaemonSocketForTest 指向不存在
// 的路径，装有 daemon 的机器上也不会把真实已装应用并入目录）→ 目录仅含源条目。
func newRecoTestServer(t *testing.T, fnpackBody string) *Server {
	t.Helper()
	t.Cleanup(func() { platform.SetDaemonSocketForTest("/var/run/com.trim.app.center.sock") })
	platform.SetDaemonSocketForTest(filepath.Join(t.TempDir(), "no-such-sock"))
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fnpackBody))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	cfg := &config.Config{}
	m := source.NewManager(cfg)
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return &Server{Src: m, Cfg: cfg}
}

// getRecommended 调 /api/recommended 返回 apps 列表。
func getRecommended(t *testing.T, s *Server, query string) []AppInfo {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/recommended", s.recommended)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/recommended"+query, nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/recommended 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []AppInfo `json:"apps"`
	}
	if err := jsonUnmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("recommended 响应解析失败: %v", err)
	}
	return resp.Apps
}

var fixedOrder = []string{"fnos-apps-store", "fndepot", "fn-knock"}

// TestRecommendedFixedOrderStable 0.6.314 C①：前三固定顺序、不随机
// （连调 10 次逐位一致）；目录里没有这三个应用时全部回落快照。
func TestRecommendedFixedOrderStable(t *testing.T) {
	s := newRecoTestServer(t, `{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`)
	s.catalogTTL = 0 // 每次现算，验证「每请求判定」而非缓存粘性
	for i := 0; i < 10; i++ {
		apps := getRecommended(t, s, "")
		if len(apps) < 3 {
			t.Fatalf("推荐应含 3 个固定位，实际 %d", len(apps))
		}
		for j, want := range fixedOrder {
			if apps[j].AppName != want {
				t.Fatalf("第 %d 次请求第 %d 位 = %q，期望固定 %q", i+1, j+1, apps[j].AppName, want)
			}
		}
	}
	// 回落快照的字段形态：fn-knock 位=快照完整元数据
	apps := getRecommended(t, s, "")
	k := apps[2]
	if k.AppName != "fn-knock" || k.LatestVersion == "" || k.Description == "" {
		t.Fatalf("fn-knock 位应为快照条目，实际 %+v", k)
	}
	if k.License != "MIT" {
		t.Errorf("快照 fn-knock 应带 license=MIT，实际 %q", k.License)
	}
	if !k.HasReadme {
		t.Error("快照 fn-knock 应 has_readme=true（内嵌全文）")
	}
	// 分类与实时卡同口径：精选表（fnos-apps-store=devtools）+ 有效 13 键
	if apps[0].Category != "devtools" {
		t.Errorf("快照 fnos-apps-store 分类应为精选表 devtools，实际 %q", apps[0].Category)
	}
	if k.Category == "" || !validCategoryKeys[k.Category] {
		t.Errorf("快照 fn-knock 分类应为有效 13 键，实际 %q", k.Category)
	}
	if !strings.HasPrefix(k.ReleaseURL, "https://github.com/kci-lnk/") {
		t.Errorf("快照 fn-knock 下载链接应为 GitHub release 直链，实际 %q", k.ReleaseURL)
	}
}

// TestRecommendedLiveOverridesSnapshot 0.6.314 C②③：源在目录里=实时条目
// （覆盖快照）；源恢复后自动切回实时（本测试直接构造「源在」的目录，
// 等价于恢复后的判定路径）。
func TestRecommendedLiveOverridesSnapshot(t *testing.T) {
	// 假源（tst）提供 fndepot 条目，版本与快照不同（9.9.9）→ 实时优先
	s := newRecoTestServer(t, `{"fndepot":{"version":"9.9.9","display_name":"FnDepot实时","desc":"实时数据","download_url":"https://example.com/fndepot.fpk"}}`)
	s.catalogTTL = 0
	apps := getRecommended(t, s, "")
	if apps[1].AppName != "fndepot" {
		t.Fatalf("第 2 位应为 fndepot，实际 %q", apps[1].AppName)
	}
	if apps[1].LatestVersion != "9.9.9" || apps[1].DisplayName != "FnDepot实时" {
		t.Fatalf("fndepot 位应为实时条目（源在目录里），实际 v%s %q", apps[1].LatestVersion, apps[1].DisplayName)
	}
	if apps[1].Source != "tst" {
		t.Errorf("实时条目源名应为 tst，实际 %q", apps[1].Source)
	}
}

// TestRecommendedRemainingExcludesFixed 0.6.314 C④：总位数 >3 时其余位置
// 维持随机逻辑，且不得重复出现前三 key（appname 归一不重复）。
func TestRecommendedRemainingExcludesFixed(t *testing.T) {
	body := `{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/a.fpk",` +
		`"icon_url":"https://example.com/a.png"},` +
		`"demo2":{"version":"2.0.0","display_name":"演示二","download_url":"https://example.com/b.fpk"},` +
		`"demo3":{"version":"3.0.0","display_name":"演示三","download_url":"https://example.com/c.fpk"}}`
	s := newRecoTestServer(t, body)
	s.catalogTTL = 0
	apps := getRecommended(t, s, "?count=12")
	if len(apps) < 4 {
		t.Fatalf("count=12 时应返回 3 固定 + ≥1 随机，实际 %d", len(apps))
	}
	seen := map[string]bool{}
	for j, want := range fixedOrder {
		if apps[j].AppName != want {
			t.Fatalf("第 %d 位 = %q，期望 %q", j+1, apps[j].AppName, want)
		}
		seen[reco.NormAppName(want)] = true
	}
	for _, a := range apps[3:] {
		n := reco.NormAppName(a.AppName)
		if seen[n] {
			t.Fatalf("随机位重复出现前三 key: %q", a.AppName)
		}
		seen[n] = true
	}
}

// TestAppDetailSnapshotFallback 0.6.314 C⑤：源删后详情路由回落快照——
// 裸 key 与旧 @源名 深链形态均可达，Key 回显请求原样。
func TestAppDetailSnapshotFallback(t *testing.T) {
	s := newRecoTestServer(t, `{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`)
	s.catalogTTL = 0
	get := func(key string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/apps/{key}", s.appDetail)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps/"+key, nil).WithContext(t.Context()))
		return rec
	}
	for _, key := range []string{"fn-knock", "fn-knock@kci-lnk", "FN-KNOCK"} {
		rec := get(key)
		if rec.Code != http.StatusOK {
			t.Fatalf("详情 %q 应 200（快照回落），实际 %d: %s", key, rec.Code, rec.Body.String())
		}
		var ai AppInfo
		if err := jsonUnmarshal(rec.Body.Bytes(), &ai); err != nil {
			t.Fatalf("详情解析失败: %v", err)
		}
		if ai.AppName != "fn-knock" || ai.Key != key {
			t.Errorf("详情 %q：AppName=%q Key=%q（应回显请求 key）", key, ai.AppName, ai.Key)
		}
		if ai.LatestVersion == "" || !ai.HasReadme {
			t.Errorf("快照详情缺版本/README 标记: %+v", ai)
		}
	}
	// 非固定推荐 key 仍诚实 404
	if rec := get("not-exist-anywhere"); rec.Code != http.StatusNotFound {
		t.Errorf("未知 key 应 404，实际 %d", rec.Code)
	}
}

// TestAssetSnapshotFallback 0.6.314 C⑥：源删后资产路由回落——图标=内嵌
// base64（PNG 魔数）、readme=内嵌全文。用 fn-knock（本机无已装目录，
// 不经过 localAppIcon 本地兜底分支）。
func TestAssetSnapshotFallback(t *testing.T) {
	s := newRecoTestServer(t, `{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`)
	s.catalogTTL = 0
	get := func(path string) *httptest.ResponseRecorder {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/apps/{key}/asset", s.appAsset)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil).WithContext(t.Context()))
		return rec
	}
	rec := get("/api/apps/fn-knock/asset?type=icon")
	if rec.Code != http.StatusOK {
		t.Fatalf("快照图标应 200，实际 %d", rec.Code)
	}
	body := rec.Body.Bytes()
	if len(body) < 8 || body[0] != 0x89 || body[1] != 'P' || body[2] != 'N' || body[3] != 'G' {
		t.Fatalf("快照图标非 PNG 魔数（%d 字节）", len(body))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("图标 Content-Type = %q", ct)
	}
	rec = get("/api/apps/fn-knock/asset?type=readme")
	if rec.Code != http.StatusOK {
		t.Fatalf("快照 README 应 200，实际 %d", rec.Code)
	}
	md := rec.Body.String()
	if !regexp.MustCompile(`(?s).{50,}`).MatchString(md) {
		t.Errorf("快照 README 全文过短（%d 字节）", len(md))
	}
	if !strings.Contains(md, "knock") && !strings.Contains(md, "敲门") {
		t.Errorf("README 内容疑似不对: %q", md[:min(60, len(md))])
	}
}
