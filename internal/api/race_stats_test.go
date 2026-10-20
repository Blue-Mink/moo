package api

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// raceLog 收集聚合行（替代 log.Printf，便于断言「恰好几行」）。
type raceLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *raceLog) emit(s string) {
	r.mu.Lock()
	r.lines = append(r.lines, s)
	r.mu.Unlock()
}

func (r *raceLog) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// 0.6.332 F2：一个 60s 窗口内 3 次竞速 → 结算恰好 1 行；空窗口不再打印。
func TestRaceWindowAggregatesToSingleLine(t *testing.T) {
	col := &raceLog{}
	w := newRaceWindow()
	w.emit = col.emit
	defer w.stop()

	for i := 0; i < 3; i++ {
		w.begin()
	}
	w.cand(300*time.Millisecond, "https://a.example.com/x.png")
	w.cand(3200*time.Millisecond, "https://b.example.com/x.png")
	w.hit()
	w.hit()
	w.fail()

	w.tick()
	lines := col.snapshot()
	if len(lines) != 1 {
		t.Fatalf("窗口内应恰好 1 行聚合日志，实际 %d 行: %v", len(lines), lines)
	}
	got := lines[0]
	for _, want := range []string{
		"[race] 60s:",
		"3 次竞速",
		"命中 2",
		"全败 1",
		"均 1.8s",
		"最慢 3.2s",
		"https://b.example.com/x.png",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("聚合行缺少 %q：%s", want, got)
		}
	}

	// 清空后的空窗口不得再打印（否则会刷「0 次竞速」的空行）
	w.tick()
	if n := len(col.snapshot()); n != 1 {
		t.Fatalf("空窗口应无输出，累计行数变成 %d: %v", n, col.snapshot())
	}
}

// 无任何活动的窗口：一次也不打印。
func TestRaceWindowIdleSilent(t *testing.T) {
	col := &raceLog{}
	w := newRaceWindow()
	w.emit = col.emit
	defer w.stop()

	w.tick()
	w.tick()
	if n := len(col.snapshot()); n != 0 {
		t.Fatalf("无活动窗口不应打印，实际 %d 行: %v", n, col.snapshot())
	}
}

// 慢命中（≥3s）保留单行告警；普通命中不额外打印。
func TestRaceWindowSlowHitOnly(t *testing.T) {
	col := &raceLog{}
	w := newRaceWindow()
	w.emit = col.emit
	defer w.stop()

	w.slowHit(800*time.Millisecond, "https://fast.example.com/x.png")
	if n := len(col.snapshot()); n != 0 {
		t.Fatalf("亚秒命中不应告警，实际 %v", col.snapshot())
	}

	w.slowHit(3400*time.Millisecond, "https://slow.example.com/x.png")
	lines := col.snapshot()
	if len(lines) != 1 {
		t.Fatalf("慢命中应打 1 行，实际 %d 行: %v", len(lines), lines)
	}
	for _, want := range []string{"慢命中", "https://slow.example.com/x.png", "3.4s"} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("慢命中行缺少 %q：%s", want, lines[0])
		}
	}
}

// 耗时格式化：≥1s 保留一位小数，否则毫秒。
func TestRaceDurFormat(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{320 * time.Millisecond, "320ms"},
		{1750 * time.Millisecond, "1.8s"},
		{3200 * time.Millisecond, "3.2s"},
	}
	for _, c := range cases {
		if got := raceDur(c.in); got != c.want {
			t.Fatalf("raceDur(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
