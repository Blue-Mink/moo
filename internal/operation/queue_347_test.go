package operation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestGateSerializesAndReportsPosition 验证闸门容量 1、先进先出、
// 等待方拿到「前面还有几个」的位次。
func TestGateSerializesAndReportsPosition(t *testing.T) {
	g := NewGate()
	ctx := context.Background()
	release1, err := g.Acquire(ctx, nil)
	if err != nil {
		t.Fatalf("首个 Acquire 失败: %v", err)
	}

	var pos int32
	got := make(chan struct{})
	go func() {
		r2, err := g.Acquire(ctx, func(p int) {
			atomic.StoreInt32(&pos, int32(p))
			close(got)
		})
		if err != nil {
			t.Errorf("第二个 Acquire 失败: %v", err)
			return
		}
		r2()
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("等待方应立刻收到位次回调")
	}
	if p := atomic.LoadInt32(&pos); p != 1 {
		t.Fatalf("位次应为 1（前面还有 1 个），实际 %d", p)
	}
	release1()
	// 释放后第二个应当拿到闸门（Waiting 归零是最终态）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g.Waiting() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("释放后等待计数未归零: %d", g.Waiting())
}

// TestGateCancelWhileWaiting ctx 取消时等待方直接返回错误、不占位次。
func TestGateCancelWhileWaiting(t *testing.T) {
	g := NewGate()
	release, err := g.Acquire(context.Background(), nil)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx, nil); err == nil {
		t.Fatal("ctx 超时应当返回错误")
	}
	if g.Waiting() != 1 {
		t.Fatalf("取消后应只剩持有者 1 个，实际 %d", g.Waiting())
	}
}

// TestQueueParallelLimit 验证 SetLimit(2) 时两个操作可同时执行。
func TestQueueParallelLimit(t *testing.T) {
	q := NewQueue()
	q.SetLimit(2)
	var started int32
	block := make(chan struct{})
	fn := func(ctx context.Context, progress func(string, float64)) error {
		atomic.AddInt32(&started, 1)
		<-block
		return nil
	}
	v1, _ := q.Start("install", "a", fn)
	v2, _ := q.Start("install", "b", fn)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&started) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&started); got != 2 {
		t.Fatalf("limit=2 时应同时开始 2 个，实际 %d", got)
	}
	close(block)
	for _, id := range []string{v1.ID, v2.ID} {
		waitState(t, q, id, StateDone)
	}
}

// TestQueueDetachFreesSlot 验证 Detach 让出额度后，排在后面的操作立刻开始
// （下载并行、安装串行的关键机制）。
func TestQueueDetachFreesSlot(t *testing.T) {
	q := NewQueue() // limit=1（严格串行）
	firstDetached := make(chan struct{})
	unblockFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	var secondDone int32

	q.Start("install", "a", func(ctx context.Context, progress func(string, float64)) error {
		Detach(ctx) // 模拟「下载完成、等安装闸门」
		close(firstDetached)
		<-unblockFirst
		return nil
	})
	q.Start("install", "b", func(ctx context.Context, progress func(string, float64)) error {
		close(secondStarted)
		atomic.StoreInt32(&secondDone, 1)
		return nil
	})
	<-firstDetached
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("前一个操作 Detach 后，后一个操作应立即开始")
	}
	if atomic.LoadInt32(&secondDone) != 1 {
		t.Fatal("第二个操作应当已执行")
	}
	close(unblockFirst)
}

func waitState(t *testing.T, q *Queue, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := q.Status(id); v != nil && v.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("操作 %s 未到达状态 %s", id, want)
}
