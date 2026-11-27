package source

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 2026-11-27 FIXPLAN 盲点 #3 · C1：探测瞬时失败重试 1 次（抖动消抖）。
// 全部 httptest 假服务，绝不出网；断言同时覆盖「重试生效」与三条红线
// （404/410 不重试、预算不足不重试、只重试一次）。

// retrySrv 前 failFirst 次请求按 failStatus/failSleep 回应，之后回 okStatus；
// 返回请求计数指针。
func retrySrv(t *testing.T, failFirst int32, failStatus int, failSleep time.Duration, okStatus int) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= failFirst {
			if failSleep > 0 {
				time.Sleep(failSleep)
			}
			w.WriteHeader(failStatus)
			return
		}
		w.WriteHeader(okStatus)
	}))
	t.Cleanup(ts.Close)
	return ts, &n
}

// ① 首次超时、重试成功 → 存活（消抖的直接目标）。
func TestCareRetryFirstTimeoutThenAlive(t *testing.T) {
	// 首次请求睡 300ms（客户端 120ms 超时）→ 瞬时失败；第 2 次立即 200。
	ts, n := retrySrv(t, 1, http.StatusOK, 300*time.Millisecond, http.StatusOK)
	groups := []probeGroup{{Name: "direct", URLs: []string{ts.URL + "/moo.json"}}}
	v, timedOut := probeCareVerdictDeadline(testProbeClient(120*time.Millisecond), groups, time.Time{})
	if v != CareAlive || timedOut {
		t.Fatalf("首次超时 + 重试成功应判存活, got %v timedOut=%v", v, timedOut)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("应探测 2 次（首次失败 + 重试 1 次）, got %d", got)
	}
}

// ② 两次都超时 → 与既有结论一致（不确定），且只重试一次。
func TestCareRetryBothTimeoutUncertain(t *testing.T) {
	ts, n := retrySrv(t, 2, http.StatusOK, 300*time.Millisecond, http.StatusOK)
	groups := []probeGroup{{Name: "direct", URLs: []string{ts.URL + "/moo.json"}}}
	v, timedOut := probeCareVerdictDeadline(testProbeClient(120*time.Millisecond), groups, time.Time{})
	if v != CareUncertain || timedOut {
		t.Fatalf("两次都超时应为不确定（既有结论不变）, got %v timedOut=%v", v, timedOut)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Fatalf("只允许重试 1 次（共 2 次请求）, got %d", got)
	}
}

// ③ 404/410 是确定性结论 → 绝不重试（每个通道恰好 1 次请求），判定不变。
func TestCareRetryNotForGone(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusGone} {
		direct, dn := retrySrv(t, 1<<30, code, 0, code)
		mirror, mn := retrySrv(t, 1<<30, code, 0, code)
		groups := []probeGroup{
			{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
			{Name: "mirror:m1", URLs: []string{mirror.URL + "/moo.json"}},
		}
		if v, _ := probeCareVerdictDeadline(testProbeClient(2*time.Second), groups, time.Time{}); v != CarePermanentlyDead {
			t.Fatalf("%d：双通道 gone 仍应判确定性失效, got %v", code, v)
		}
		if d, m := atomic.LoadInt32(dn), atomic.LoadInt32(mn); d != 1 || m != 1 {
			t.Fatalf("%d：404/410 不得重试，各应 1 次请求, got direct=%d mirror=%d", code, d, m)
		}
	}
}

// ④ 单源预算不足时放弃重试，按首次结论（不确定）走，且不拖过预算。
// 断言两条：请求只有 1 次（重试未发出）+ 耗时里没有多出来的重试间隔
// （把 delay 放大到 400ms，只要重试发生，耗时必然 ≈ 预算 200ms + 400ms）。
func TestCareRetrySkippedWhenBudgetShort(t *testing.T) {
	oldDelay := careProbeRetryDelay
	careProbeRetryDelay = 400 * time.Millisecond
	t.Cleanup(func() { careProbeRetryDelay = oldDelay })

	ts, n := retrySrv(t, 1<<30, http.StatusOK, 500*time.Millisecond, http.StatusOK) // 本次请求永不按时回包
	groups := []probeGroup{{Name: "direct", URLs: []string{ts.URL + "/moo.json"}}}
	start := time.Now()
	v, timedOut := probeCareVerdictDeadline(testProbeClient(5*time.Second), groups, time.Now().Add(200*time.Millisecond))
	el := time.Since(start)
	if v != CareUncertain || !timedOut {
		t.Fatalf("预算用尽应记不确定且 timedOut, got %v timedOut=%v", v, timedOut)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Fatalf("预算不足不得重试（应只有 1 次请求）, got %d", got)
	}
	if el > 400*time.Millisecond {
		t.Fatalf("预算不足时不得重试（重试会额外花 %s）：耗时 %s（应 ≈200ms）", careProbeRetryDelay, el)
	}
}
