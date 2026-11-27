package api

// 2026-11-27 反向验证补课（578 手动测速 force · 后端缺口补齐）：
// 「立即测速」= GET /api/mirrors/health?refresh=1 必须**真发一次探测**，
// 不得被 5 分钟缓存（mirrorCacheTTL）挡回；服务整体处于不活跃态
// （uiInactive，后台循环会把探针降到 15min 一次）时同样不得挡。
//
// 反向验证（已实测）：把 mirrorHealth 里的
//   s.Mirrors.ensureFresh(r.URL.Query().Get("refresh") == "1")
// 改成 s.Mirrors.ensureFresh(false) → 本用例必 FAIL。
//
// 零外网：config.SetRemoteMirrorOptions 把两组清单替换成「本地 httptest 单条目」
// （gh 指向本地 200；dk 只有 URL 空的 auto 项 → 不可探测，不发出站请求）。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

func TestMirrorHealthRefreshForcesProbe(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(make([]byte, 2*1024*1024)) // ≥ ghProbeMinBytes，判 ok
	}))
	defer srv.Close()

	origGH, origDK := config.RemoteMirrorOptions()
	config.SetRemoteMirrorOptions(
		[]config.MirrorOption{{Key: "local-test", Label: "本地镜像", URL: srv.URL + "/"}},
		[]config.MirrorOption{{Key: "auto", Label: "智能 · 自动选最快"}}, // URL 空 → 跳过
	)
	defer config.SetRemoteMirrorOptions(origGH, origDK)

	s := &Server{Cfg: &config.Config{GhProbeMinutes: 5}, Mirrors: newMirrorMonitor()}
	// 两组都「刚测过」= 缓存新鲜：不带 refresh 的请求必须零探测
	fresh := time.Now()
	s.Mirrors.mu.Lock()
	s.Mirrors.lastProbeGh = fresh
	s.Mirrors.lastProbeDk = fresh
	s.Mirrors.mu.Unlock()

	s.mirrorHealth(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/mirrors/health", nil))
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("缓存新鲜时普通 GET 不应探测, hits=%d", got)
	}
	s.Mirrors.mu.Lock()
	unchanged := s.Mirrors.lastProbeGh
	s.Mirrors.mu.Unlock()
	if !unchanged.Equal(fresh) {
		t.Fatalf("缓存新鲜时普通 GET 不应刷新 last_probe, %v → %v", fresh, unchanged)
	}

	// 不活跃态（>30min 无 UI）：后台循环降频，但用户显式点「立即测速」仍必须探
	s.StartedAt = time.Now().Add(-2 * time.Hour)
	s.mirrorHealth(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/mirrors/health?refresh=1", nil))
	if got := atomic.LoadInt32(&hits); got == 0 {
		t.Fatal("refresh=1 必须立即真探测一次（不得被 5 分钟缓存或 idle 降频挡回）")
	}
	s.Mirrors.mu.Lock()
	after := s.Mirrors.lastProbeGh
	s.Mirrors.mu.Unlock()
	if !after.After(fresh) {
		t.Fatalf("refresh=1 后 last_probe 应推进, %v → %v", fresh, after)
	}
}
