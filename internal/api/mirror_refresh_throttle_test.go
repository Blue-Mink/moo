package api

// 2026-11-27 盲点补测批次 4b（FIXPLAN 盲点 #2 / #8）：手动测速的**边界**。
//
// 用户触发路径（GET /api/mirrors/health?refresh=1 与 docker/health 同构，
// 面板「立即测速」）此前每次请求都无条件跑一轮全量探测（ensureFresh(true)）：
// 并发 N 个请求 = N 轮探测，连点/多标签/脚本会瞬间把出口带宽打满。
// 本文件钉住三条边界：
//   1) single-flight —— 并发 5 个 refresh=1 只跑一轮探测；
//   2) 5s 最小间隔 —— <5s 的第二次强制请求不新增探测，且响应 throttled=true；
//   3) 超 5s 后允许再探（间隔不是单向死锁）。
// 另加一条回归护栏：内部调用方用的 ensureFresh(true) 语义不变（每次各探一次）。
//
// 零外网：config.SetRemoteMirrorOptions 把两组清单换成「本地 httptest 单条目」
// （gh 指向本地 200；dk 只有 URL 空的 auto 项 → 不可探测，不出站）。
// 每轮探测的 HTTP 请求数不硬编码（ghProbeOne 可能先测延迟再测吞吐），
// 统一以「单轮基线」为参照做倍率比较。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// probeFixture 搭本地探测器夹具，返回命中计数与恢复函数（defer 调用）。
func probeFixture(t *testing.T, delay time.Duration) (*int32, func()) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write(make([]byte, 2*1024*1024)) // ≥ ghProbeMinBytes → 判 ok
	}))
	origGH, origDK := config.RemoteMirrorOptions()
	config.SetRemoteMirrorOptions(
		[]config.MirrorOption{{Key: "local-test", Label: "本地镜像", URL: srv.URL + "/"}},
		[]config.MirrorOption{{Key: "auto", Label: "智能 · 自动选最快"}}, // URL 空 → 跳过
	)
	return &hits, func() { srv.Close(); config.SetRemoteMirrorOptions(origGH, origDK) }
}

func newProbeServer() *Server {
	return &Server{Cfg: &config.Config{GhProbeMinutes: 5}, Mirrors: newMirrorMonitor()}
}

// probeHealth 非致命版（可在 goroutine 里用）：返回响应体解析结果。
func probeHealth(s *Server, rawQuery string) (MirrorHealth, error) {
	rec := httptest.NewRecorder()
	s.mirrorHealth(rec, httptest.NewRequest(http.MethodGet, "/api/mirrors/health"+rawQuery, nil))
	if rec.Code != http.StatusOK {
		return MirrorHealth{}, fmt.Errorf("status %d", rec.Code)
	}
	var h MirrorHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		return MirrorHealth{}, err
	}
	return h, nil
}

func doRefresh(t *testing.T, s *Server, rawQuery string) MirrorHealth {
	t.Helper()
	h, err := probeHealth(s, rawQuery)
	if err != nil {
		t.Fatalf("mirrorHealth%s 失败: %v", rawQuery, err)
	}
	return h
}

// TestMirrorRefreshConcurrentSingleFlight 并发 5 个 refresh=1 → 只跑一轮探测。
func TestMirrorRefreshConcurrentSingleFlight(t *testing.T) {
	hits, restore := probeFixture(t, 250*time.Millisecond)
	defer restore()

	// 基线：单次 refresh=1 一轮探测发出的请求数
	atomic.StoreInt32(hits, 0)
	doRefresh(t, newProbeServer(), "?refresh=1")
	perRound := atomic.LoadInt32(hits)
	if perRound == 0 {
		t.Fatal("基线：单次 refresh=1 必须真探测（hits=0 说明夹具/入口不通）")
	}

	// 并发 5 个：全部用同一 Server（同一 monitor = 同一闸门）
	atomic.StoreInt32(hits, 0)
	s := newProbeServer()
	const n = 5
	res := make([]MirrorHealth, n)
	errs := make([]error, n)
	elapsed := make([]time.Duration, n)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate // 齐发，尽量让 5 个请求同时进入窗口
			t0 := time.Now()
			res[i], errs[i] = probeHealth(s, "?refresh=1")
			elapsed[i] = time.Since(t0)
		}(i)
	}
	close(gate)
	wg.Wait()

	got := atomic.LoadInt32(hits)
	if got != perRound {
		t.Fatalf("并发 %d 个 refresh=1 应只跑一轮探测（%d 次请求），实际 %d 次", n, perRound, got)
	}
	throttled := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("并发请求 #%d 失败: %v", i, errs[i])
		}
		if res[i].Throttled {
			throttled++
		}
	}
	if throttled != n-1 {
		t.Fatalf("并发去重后应有且只有 1 个请求真探测、其余 %d 个 throttled=true，实际 %d 个", n-1, throttled)
	}
	// 单飞的关键语义：等待者必须**等这一轮探测跑完**（拿到新数据）再返回，
	// 而不是立刻返回陈旧数据。夹具每轮 ≥250ms，因此除真探测者之外至少还要
	// 有 1 个请求耗时 ≥100ms（只靠 5s 间隔挡回的话等待者会立刻返回 ~0ms）。
	slow := 0
	for _, d := range elapsed {
		if d >= 100*time.Millisecond {
			slow++
		}
	}
	if slow < 2 {
		t.Fatalf("仅 %d 个并发请求等待了进行中的那一轮（≥100ms），应 ≥2 —— 其余请求没有等 in-flight 探测就返回（single-flight 失效）", slow)
	}
}

// TestMirrorRefreshThrottledWithin5s 首次真探、<5s 内第二次不新增探测且可感知。
func TestMirrorRefreshThrottledWithin5s(t *testing.T) {
	hits, restore := probeFixture(t, 0)
	defer restore()

	s := newProbeServer()
	h1 := doRefresh(t, s, "?refresh=1")
	if h1.Throttled {
		t.Fatal("首次 refresh=1 不应被节流")
	}
	first := atomic.LoadInt32(hits)
	if first == 0 {
		t.Fatal("首次 refresh=1 必须真探测")
	}

	h2 := doRefresh(t, s, "?refresh=1") // 紧接第二次（远小于 5s）
	if got := atomic.LoadInt32(hits); got != first {
		t.Fatalf("5s 内第二次强制探测不应新增请求: %d → %d", first, got)
	}
	if !h2.Throttled {
		t.Fatal("被节流的响应必须带 throttled=true（前端/脚本需可感知）")
	}
	if h2.LastProbe != h1.LastProbe {
		t.Fatalf("被节流时 last_probe 不应推进: %q → %q", h1.LastProbe, h2.LastProbe)
	}
}

// TestMirrorRefreshAllowedAfter5s 节流窗口过后必须能再探（不是单向死锁）。
func TestMirrorRefreshAllowedAfter5s(t *testing.T) {
	hits, restore := probeFixture(t, 0)
	defer restore()

	s := newProbeServer()
	doRefresh(t, s, "?refresh=1")
	after1 := atomic.LoadInt32(hits)
	doRefresh(t, s, "?refresh=1") // 节流，不新增
	if got := atomic.LoadInt32(hits); got != after1 {
		t.Fatalf("前置条件：5s 内第二次应被节流, %d → %d", after1, got)
	}

	// 把上次用户触发时刻回拨 6s（> mirrorUserProbeMinGap）
	s.Mirrors.userMu.Lock()
	s.Mirrors.lastUserProbe = time.Now().Add(-6 * time.Second)
	s.Mirrors.userMu.Unlock()

	h3 := doRefresh(t, s, "?refresh=1")
	if h3.Throttled {
		t.Fatal("超过 5s 最小间隔后必须真探，不得继续节流")
	}
	if got := atomic.LoadInt32(hits); got <= after1 {
		t.Fatalf("超过 5s 后应新增一轮探测请求: %d → %d", after1, got)
	}
}

// TestMirrorRefreshInternalCallersUnaffected 内部调用方的 ensureFresh(true)
// 语义未被用户触发闸门污染：连续两次各探一次（后台循环 / mirrors_remote /
// store_ops 三个内部调用方依赖此语义）。
func TestMirrorRefreshInternalCallersUnaffected(t *testing.T) {
	hits, restore := probeFixture(t, 0)
	defer restore()

	m := newMirrorMonitor()
	m.ensureFresh(true)
	n1 := atomic.LoadInt32(hits)
	if n1 == 0 {
		t.Fatal("ensureFresh(true) 必须真探测")
	}
	m.ensureFresh(true) // 立刻再来一次（远小于 5s）
	if n2 := atomic.LoadInt32(hits); n2 <= n1 {
		t.Fatalf("ensureFresh(true) 连续两次必须各探一次（内部语义未变）: %d → %d", n1, n2)
	}
}
