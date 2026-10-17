package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// 0.6.326z4：恢复（启用）源时顺带后台重拉——此前 ToggleSource 清缓存后要等
// 下一次刷新轮次（实测约 20 分钟）卡片才回来（用户报「恢复fork源后应用不回来」）。
// 断言：禁用不重拉；启用会建一次后台重拉（用本地假源计数，不触外网）。
func TestToggleEnableKicksSourceRefresh(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	var hits int32
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示",` +
			`"download_url":"https://example.com/demo.fpk"}}`))
	}))
	t.Cleanup(idx.Close)

	m := source.NewManager(config.Default())
	s := &Server{Cfg: config.Default(), Src: m}
	if _, err := m.AddSource("tst", idx.URL+"/fnpack.json"); err != nil {
		t.Fatalf("建源失败: %v", err)
	}
	base := atomic.LoadInt32(&hits)
	if base == 0 {
		t.Fatal("AddSource 应已拉取过一次")
	}

	toggle := func(enabled bool) int {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		req := httptest.NewRequest("POST", "/api/sources/tst/toggle", strings.NewReader(body))
		req.SetPathValue("id", "tst")
		rec := httptest.NewRecorder()
		s.toggleSource(rec, req)
		return rec.Code
	}

	if code := toggle(false); code != http.StatusOK {
		t.Fatalf("禁用 rc=%d", code)
	}
	time.Sleep(400 * time.Millisecond)
	if n := atomic.LoadInt32(&hits); n != base {
		t.Fatalf("禁用不该触发重拉：hits %d → %d", base, n)
	}

	if code := toggle(true); code != http.StatusOK {
		t.Fatalf("启用 rc=%d", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&hits) <= base && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&hits); n <= base {
		t.Fatalf("启用应触发后台重拉，hits 仍为 %d", n)
	}
}

// 0.6.326z4：addprobe 轮次有产出后必须失效 /api/apps 体缓存（cachedAppsBody），
// 否则列表 JSON 里的 upstream_add_note/url 会滞后数十分钟。
func TestAddProbeRoundInvalidatesAppsBody(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	ap := source.NewAddProbe("")
	ap.SetProbeForTest(func(string) string { return "moo.json" })
	ap.EnqueueForTest("acme/tool")
	s := &Server{Cfg: config.Default(), Src: source.NewManager(config.Default()), AddProbes: ap}

	s.catalogBodyMu.Lock()
	s.catalogBody = []byte(`{"apps":[]}`)
	s.catalogBodyETag = `"stale"`
	s.catalogBodyMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.StartAddProbeRefresh(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for {
		s.catalogBodyMu.Lock()
		stale := s.catalogBody != nil
		s.catalogBodyMu.Unlock()
		if !stale {
			return // 体缓存已失效 ✓
		}
		if time.Now().After(deadline) {
			t.Fatal("探测轮次有产出后未失效 /api/apps 体缓存")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
