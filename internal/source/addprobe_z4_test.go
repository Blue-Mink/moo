package source

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// 0.6.326z4：addprobe 首轮并发化（用户令）。
// 断言两件事：①多个仓库确实同时在飞（否则等于没并发）：②单仓内的候选探测
// 仍是串行、命中即止（顺序语义不变）——并发只发生在仓库之间。
func TestAddProbeConcurrentRounds(t *testing.T) {
	a := NewAddProbe("")
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	probed := map[string]int{}
	a.SetProbeForTest(func(u string) string {
		key := strings.TrimPrefix(u, "https://github.com/")
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		probed[key]++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // 放大并发窗口
		mu.Lock()
		inFlight--
		mu.Unlock()
		return "moo.json" // 本仓即命中 → 每个仓库只探 1 次
	})
	repos := []string{"o1/a", "o2/b", "o3/c", "o4/d", "o5/e", "o6/f"}
	for _, r := range repos {
		a.Enqueue(r)
	}
	if n := a.RefreshRound(context.Background(), 64); n != len(repos) {
		t.Fatalf("应探测 %d 个仓库，实际 %d", len(repos), n)
	}
	mu.Lock()
	gotMax := maxInFlight
	mu.Unlock()
	if gotMax < 2 {
		t.Fatalf("应至少 2 个仓库同时在飞（并发未生效），实际 %d", gotMax)
	}
	if gotMax > addProbeWorkers {
		t.Fatalf("并发度 %d 超过 worker 上限 %d", gotMax, addProbeWorkers)
	}
	for _, r := range repos {
		if idx, _, known := a.Lookup(r); !known || idx != "moo.json" {
			t.Fatalf("%s 结果未落: idx=%q known=%v", r, idx, known)
		}
		mu.Lock()
		n := probed[r]
		mu.Unlock()
		if n != 1 {
			t.Fatalf("%s 探测 %d 次，期望 1", r, n)
		}
	}
}

// 单仓内部仍是「候选名单串行 + 命中即止」：首候选命中时不得试后面的候选。
func TestAddProbeSingleRepoCandidatesStable(t *testing.T) {
	a := NewAddProbe("")
	rec := &probeRecorder{hit: map[string]bool{"solo/FnDepot": true}}
	a.probe = rec.fn
	a.Enqueue("solo/pack-only")
	if n := a.RefreshRound(context.Background(), 8); n != 1 {
		t.Fatalf("应探测 1 个仓库，实际 %d", n)
	}
	assertSeq(t, rec.repoSeq(), []string{"solo/pack-only", "solo/FnDepot"})
}

// 本轮上限：maxRepos 截断（多余仓库留待下一轮，不在本轮被清空处理）。
func TestAddProbeRoundLimit(t *testing.T) {
	a := NewAddProbe("")
	a.SetProbeForTest(func(string) string { return "moo.json" })
	repos := []string{"o1/a", "o2/b", "o3/c", "o4/d", "o5/e"}
	for _, r := range repos {
		a.Enqueue(r)
	}
	if n := a.RefreshRound(context.Background(), 2); n != 2 {
		t.Fatalf("maxRepos=2 时应只探 2 个，实际 %d", n)
	}
	known := 0
	for _, r := range repos {
		if _, _, ok := a.Lookup(r); ok {
			known++
		}
	}
	if known != 2 {
		t.Fatalf("应恰好 2 个仓库落结果，实际 %d", known)
	}
}
