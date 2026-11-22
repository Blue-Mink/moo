package operation

// P0-2：操作队列的可落盘能力（RestoreHistory / Snapshot / OnChange）。
// 借鉴 FnDepot：下载与安装状态持久化，刷新/重启后仍能如实展示。

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// 重启恢复：上一条 running/queued 必须变成 error（写明重启中断），终态原样保留。
func TestRestoreHistoryMarksInterrupted(t *testing.T) {
	q := NewQueue()
	start := time.Now().Add(-time.Hour)
	n := q.RestoreHistory([]View{
		{ID: "a", Kind: "install", Target: "x", State: StateRunning, StartedAt: start},
		{ID: "b", Kind: "update", Target: "y", State: StateQueued, StartedAt: start},
		{ID: "c", Kind: "uninstall", Target: "z", State: StateDone, StartedAt: start},
	})
	if n != 2 {
		t.Fatalf("interrupted = %d，期望 2", n)
	}
	h := q.History()
	if len(h) != 3 {
		t.Fatalf("history = %d 条，期望 3", len(h))
	}
	for _, v := range h {
		switch v.ID {
		case "a", "b":
			if v.State != StateError || !strings.Contains(v.Err, "重启") {
				t.Fatalf("%s 应标记重启中断，实际 state=%q err=%q", v.ID, v.State, v.Err)
			}
			if v.FinishedAt.IsZero() {
				t.Fatalf("%s 缺少 finished_at", v.ID)
			}
		case "c":
			if v.State != StateDone {
				t.Fatalf("终态条目被误改: %+v", v)
			}
		}
	}
}

// OnChange：入队与终态都要触发快照回调（落盘时机）；Snapshot 含历史。
func TestOnChangeFiresOnStartAndFinish(t *testing.T) {
	q := NewQueue()
	var mu sync.Mutex
	var last []View
	fired := 0
	q.SetOnChange(func(vs []View) {
		mu.Lock()
		last = vs
		fired++
		mu.Unlock()
	})

	release := make(chan struct{})
	if _, err := q.Start("install", "demo", func(_ context.Context, progress func(string, float64)) error {
		progress("安装中…", 50)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return fired >= 1 })
	close(release)
	waitFor(t, func() bool {
		h := q.History()
		return len(h) == 1 && h[0].State == StateDone
	})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return fired >= 2 })
	mu.Lock()
	snap := append([]View(nil), last...)
	mu.Unlock()
	if len(snap) == 0 || snap[len(snap)-1].Target != "demo" {
		t.Fatalf("最后一次快照缺历史条目: %+v", snap)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("条件未在 3s 内满足")
}
