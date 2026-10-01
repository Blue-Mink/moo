package operation

import (
	"fmt"
	"context"
	"testing"
	"time"
)

// 回归：操作完成后 Queue 清空 current → Current() 返回 nil。
// appStartStop 曾在此处直接 `*s.Ops.Current()` 解引用 nil panic，
// 掐断 HTTP 连接：停用/启动操作实际已执行，客户端却收到空响应
// 并误报「启动/停用失败」。该测试锚定「nil 即完成、终态可从
// history 按 ID 取回」的恢复语义。
func TestQueue_CompletedOpCurrentNilHistoryRecoverable(t *testing.T) {
	q := NewQueue()
	v, err := q.Start("stop", "global-radio", func(ctx context.Context, progress func(string, float64)) error {
		return nil // 快速完成
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != StateRunning {
		t.Fatalf("Start 应返回 running 视图，实际 %q", v.State)
	}

	// 等待后台 goroutine 完成（setDone + 清空 current + 写 history）
	deadline := time.Now().Add(3 * time.Second)
	for q.Current() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.Current() != nil {
		t.Fatal("完成操作后 Current() 应返回 nil（current 被清空）")
	}

	hist := q.History()
	if len(hist) != 1 {
		t.Fatalf("history 应有 1 条，实际 %d", len(hist))
	}
	if hist[0].ID != v.ID {
		t.Errorf("history 头部 ID 应为本次操作 %q，实际 %q", v.ID, hist[0].ID)
	}
	if hist[0].State != StateDone {
		t.Errorf("终态应为 done，实际 %q", hist[0].State)
	}

	// 完成的操作不阻塞后续操作
	if _, err := q.Start("start", "global-radio", func(ctx context.Context, progress func(string, float64)) error {
		return nil
	}); err != nil {
		t.Fatalf("完成后应立即允许新操作: %v", err)
	}
}

// 回归（2026-09-25 kspeeder/fnlogpush 假「完成」）：失败操作结束后
// Current()==nil，SSE 慢轮询若只看 nil 会误报成功——终态必须能从
// history 按 ID 取回且保留 error 状态。
func TestQueue_FindErrorState(t *testing.T) {
	q := NewQueue()
	v, err := q.Start("update", "kspeeder", func(ctx context.Context, progress func(string, float64)) error {
		return fmt.Errorf("无法确认操作结果: app center 已不再持有该升级任务")
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for q.Current() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.Current() != nil {
		t.Fatal("结束操作后 Current() 应为 nil")
	}
	h := q.Find(v.ID)
	if h == nil {
		t.Fatal("Find 应能在 history 里找到已结束操作")
	}
	if h.State != StateError {
		t.Fatalf("终态应为 error，实际 %q", h.State)
	}
	if h.Err == "" {
		t.Error("错误信息应保留")
	}
	if q.Find("no-such-id") != nil {
		t.Error("不存在的 ID 应返回 nil")
	}
}
