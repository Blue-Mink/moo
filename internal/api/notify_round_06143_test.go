package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// injectCatalog 注入内存目录（测试免真实抓源）。
func injectCatalog(t *testing.T, s *Server, apps []AppInfo) {
	t.Helper()
	s.catalogMu.Lock()
	s.catalogData = apps
	s.catalogAt = time.Now()
	s.catalogMu.Unlock()
}

// TestBuildSourceSummary（0.6.143）：文案「个应用」、总数含多源重复、
// 紧凑版无逐源明细、完整版含明细。
// 0.6.312 B3/F5 起签名 = (sts 本轮刷新, all 全量当前状态, …)：旧全量轮
// 语义下 all 与 sts 同源（失败源在状态视图里无 Error、按旧计数展示为 0）。
func TestBuildSourceSummary(t *testing.T) {
	sts := []source.SourceStatus{
		{Name: "big", Count: 100},
		{Name: "mid", Count: 50},
		{Name: "tiny", Count: 3},
		{Name: "broken", Count: 0, Error: "boom"},
	}
	all := []source.SourceStatus{
		{Name: "big", Count: 100},
		{Name: "mid", Count: 50},
		{Name: "tiny", Count: 3},
		{Name: "broken", Count: 0},
	}
	compact, full := buildSourceSummary(sts, all, 153, 2, 1)
	for _, want := range []string{
		"- big：100 个应用", "- tiny：3 个应用", "- broken：同步失败",
		"应用总数 153（含多源重复）· 关注源 1 · 收藏 2 个",
	} {
		if !strings.Contains(full, want) {
			t.Errorf("完整版缺 %q：\n%s", want, full)
		}
	}
	if strings.Contains(full, "条") {
		t.Error("文案应为「个应用」，不应残留「条」")
	}
	if !strings.Contains(compact, "源同步：3 源成功 / 1 源失败") {
		t.Errorf("紧凑版缺概况：\n%s", compact)
	}
	if !strings.Contains(compact, "应用数最多的源：\n- big：100\n- mid：50\n- tiny：3") {
		t.Errorf("紧凑版缺 Top（列表式，0.6.144）：\n%s", compact)
	}
	if strings.Contains(compact, "本轮源同步：") || strings.Contains(compact, "个应用") {
		t.Error("紧凑版不应含逐源明细（刷屏）")
	}
	if !strings.Contains(compact, "失败源：broken") {
		t.Errorf("紧凑版缺失败源：\n%s", compact)
	}
}

// TestFavoriteSourcePass（0.6.143）：首次关注记基线不推；新增应用推（带版本）；
// 已推过的不重复推。
func TestFavoriteSourcePass(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{
		FavoriteSources: []string{"s1"},
		Sources:         []config.SourceRef{{Name: "s1", URL: "https://x"}},
	}}
	injectCatalog(t, s, []AppInfo{
		{Key: "a@x", AppName: "a", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "b@x", AppName: "b", Source: "other", LatestVersion: "1.0.0"},
	})
	// 第 1 轮：记基线，不推
	s.favoriteSourcePass()
	if len(s.Cfg.NotifyLog) != 0 {
		t.Fatalf("首次关注应记基线不推: %+v", s.Cfg.NotifyLog)
	}
	// 第 2 轮：无变化，不推
	s.favoriteSourcePass()
	if len(s.Cfg.NotifyLog) != 0 {
		t.Fatalf("无变化不应推: %+v", s.Cfg.NotifyLog)
	}
	// 第 3 轮：新增应用，推（带版本）
	injectCatalog(t, s, []AppInfo{
		{Key: "a@x", AppName: "a", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "c@x", AppName: "c", DisplayName: "应用 C", Source: "s1", LatestVersion: "2.0.0"},
	})
	s.favoriteSourcePass()
	if len(s.Cfg.NotifyLog) != 1 {
		t.Fatalf("新增应用应推一次: %+v", s.Cfg.NotifyLog)
	}
	e := s.Cfg.NotifyLog[0]
	if e.Event != "favorite_source_apps" {
		t.Fatalf("event=%s", e.Event)
	}
	if !strings.Contains(e.Content, "关注源「s1」新增 1 个应用") || !strings.Contains(e.Content, "应用 C（2.0.0）") {
		t.Errorf("通知内容不对: %s", e.Content)
	}
	// 第 4 轮：同集合，不重复推
	s.favoriteSourcePass()
	if len(s.Cfg.NotifyLog) != 1 {
		t.Fatalf("已推过的新增不应重复推: %+v", s.Cfg.NotifyLog)
	}
}

// doAdmin 可信通道（网关 socket 模拟：trust=true + X-Trim-* 管理员头 +
// X-Moo-Admin 变更头（0.6.207-panel D2））。
func doAdmin(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, GatewayPrefix+path, strings.NewReader(body)).WithContext(t.Context())
	r.Header.Set("X-Trim-Username", "admin")
	r.Header.Set("X-Trim-Isadmin", "true")
	r.Header.Set("X-Moo-Admin", "1")
	rec := httptest.NewRecorder()
	s.Handler(true).ServeHTTP(rec, r)
	return rec
}

// TestFavoriteSourceAPI（0.6.143）：星标切换幂等语义；不存在的源 404。
func TestFavoriteSourceAPI(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{
		Sources: []config.SourceRef{{Name: "s1", URL: "https://x"}},
	}
	s := &Server{Cfg: cfg}
	rec := doAdmin(t, s, "POST", "/api/sources/nope/favorite", `{"favorite":true}`)
	if rec.Code != 404 {
		t.Fatalf("不存在源应 404, got %d (%s)", rec.Code, rec.Body.String())
	}
	rec = doAdmin(t, s, "POST", "/api/sources/s1/favorite", `{"favorite":true}`)
	if rec.Code != 200 {
		t.Fatalf("关注应 200: %s", rec.Body.String())
	}
	if len(cfg.FavoriteSources) != 1 || cfg.FavoriteSources[0] != "s1" {
		t.Fatalf("favorite_sources 未落: %+v", cfg.FavoriteSources)
	}
	// 已关注再点 on：幂等不重复
	rec = doAdmin(t, s, "POST", "/api/sources/s1/favorite", `{"favorite":true}`)
	if rec.Code != 200 || len(cfg.FavoriteSources) != 1 {
		t.Fatalf("重复 on 应幂等: %d %+v", rec.Code, cfg.FavoriteSources)
	}
	rec = doAdmin(t, s, "POST", "/api/sources/s1/favorite", `{"favorite":false}`)
	if rec.Code != 200 || len(cfg.FavoriteSources) != 0 {
		t.Fatalf("取消关注应清空: %d %+v", rec.Code, cfg.FavoriteSources)
	}
}
