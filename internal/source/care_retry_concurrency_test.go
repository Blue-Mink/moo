package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// ⑤ 重试必须留在复核既有的并发闸门内：不新开 goroutine、不突破
// careProbeConcurrency（≤2）。同时证明重试真的发生了（首轮 502 → 重试 200）。
func TestCareRetryRespectsProbeConcurrency(t *testing.T) {
	oldDelay := careProbeRetryDelay
	careProbeRetryDelay = 20 * time.Millisecond
	t.Cleanup(func() { careProbeRetryDelay = oldDelay })

	var inflight, peak int32
	var hits sync.Map // 源名 → *int32（该源已请求次数）
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inflight, 1)
		defer atomic.AddInt32(&inflight, -1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		v, _ := hits.LoadOrStore(r.URL.Query().Get("s"), new(int32))
		if atomic.AddInt32(v.(*int32), 1) == 1 { // 每个源的首次请求 → 瞬时 502
			time.Sleep(5 * time.Millisecond)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK) // 重试 → 200
	}))
	defer ts.Close()

	const n = 6
	refs := make([]config.SourceRef, 0, n)
	for i := 0; i < n; i++ {
		refs = append(refs, config.SourceRef{Name: fmt.Sprintf("s%d", i), URL: ts.URL})
	}
	m := NewManager(&config.Config{Sources: refs})
	m.CareProbe = func(name, url string) CareVerdict {
		g := probeGroup{Name: "direct", URLs: []string{ts.URL + "/moo.json?s=" + name}}
		if probeGroupStatusRetry(testProbeClient(2*time.Second), g, time.Time{}) == probeOK {
			return CareAlive
		}
		return CareUncertain
	}

	out := make([]CareVerdict, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = m.probeCareSafe(fmt.Sprintf("s%d", i), refs[i].URL)
		}(i)
	}
	wg.Wait()

	if p := atomic.LoadInt32(&peak); p > int32(careProbeConcurrency) {
		t.Fatalf("重试不得突破并发上限 %d（实测峰值 %d）", careProbeConcurrency, p)
	}
	for i, v := range out {
		if v != CareAlive {
			t.Fatalf("源 s%d：首次 502 + 重试 200 应判存活, got %v", i, v)
		}
	}
}
