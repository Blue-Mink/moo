package source

// 2026-11-27（F10 / 审计 N6）：复核的并发口径。
//
// 旧实现是全局 probeMu：任一轮里有多个死源同时复核就互相排队（N6 的 77s
// 里也含这段）。新口径：**按源串行**（同一源不并发）+ 全局限并发 2
// （并发再高会撞镜像限流 → 假「不可达」）。

import (
	"sync"
	"testing"
	"time"

	"moo/internal/config"
)

// probeConcurrencyManager 造一个只在内存里跑的 Manager（CareProbe 注入，
// 绝不出网），并统计峰值并发。
func probeConcurrencyManager(t *testing.T, names ...string) (*Manager, func() int) {
	t.Helper()
	srcs := make([]config.SourceRef, 0, len(names))
	for _, n := range names {
		srcs = append(srcs, config.SourceRef{Name: n, URL: "https://" + n + ".example/moo.json"})
	}
	m := NewManager(&config.Config{Sources: srcs})
	var mu sync.Mutex
	inflight, peak := 0, 0
	m.CareProbe = func(name, url string) CareVerdict {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		mu.Unlock()
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()
		return CareUncertain
	}
	return m, func() int {
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

// 不同源并发：峰值并发 = 2（既证明不再全局串行，也证明上限被压住）。
func TestProbeCareSafeConcurrencyCapAcrossSources(t *testing.T) {
	m, peak := probeConcurrencyManager(t, "a", "b", "c", "d")
	var wg sync.WaitGroup
	start := time.Now()
	for _, n := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			m.probeCareSafe(n, "https://"+n+".example/moo.json")
		}(n)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := peak(); got != careProbeConcurrency {
		t.Fatalf("4 个源并发复核的峰值并发应为 %d，实际 %d（旧的全局锁会给 1）",
			careProbeConcurrency, got)
	}
	// 串行需要 4×150ms；限并发 2 需要 2×150ms。
	if elapsed > 3*150*time.Millisecond {
		t.Fatalf("并发没生效：耗时 %s", elapsed)
	}
}

// 同一源并发：必须严格串行（峰值 1）——并发打同一仓库只会自己撞限流。
func TestProbeCareSafeSameSourceSerialized(t *testing.T) {
	m, peak := probeConcurrencyManager(t, "a")
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.probeCareSafe("a", "https://a.example/moo.json")
		}()
	}
	wg.Wait()
	if got := peak(); got != 1 {
		t.Fatalf("同一源必须串行（峰值并发 1），实际 %d", got)
	}
}
