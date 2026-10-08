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

// 回归（0.6.261 设置页「已下载 FPK」失败行无删除按钮）：官方 cloud 下载
// 走长操作队列，失败行删除入口必须能按 ID 清掉 history 终态条目；
// 运行中操作与未知 ID 必须拒绝/空操作。
func TestQueue_RemoveTerminalOp(t *testing.T) {
	q := NewQueue()
	// 先起一个慢操作占住 current，再等它结束
	v, err := q.Start("download", "1Panel", func(ctx context.Context, progress func(string, float64)) error {
		return fmt.Errorf("下载失败: 面板登录被限流 (errno 131072)")
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

	// 运行中删除必须拒绝
	v2, err := q.Start("install", "global-radio", func(ctx context.Context, progress func(string, float64)) error {
		time.Sleep(150 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if removed, rerr := q.Remove(v2.ID); removed || rerr == nil {
		t.Fatalf("运行中操作应拒绝删除，实际 removed=%v err=%v", removed, rerr)
	}
	for q.Current() != nil {
		time.Sleep(5 * time.Millisecond)
	}

	// 终态条目可删
	removed, rerr := q.Remove(v.ID)
	if rerr != nil {
		t.Fatalf("终态条目应可删除: %v", rerr)
	}
	if !removed {
		t.Fatal("终态条目删除应返回 removed=true")
	}
	if h := q.Find(v.ID); h != nil {
		t.Error("删除后 Find 应返回 nil")
	}
	// 不影响其他 history 条目
	if h := q.Find(v2.ID); h == nil {
		t.Error("其他历史条目不应受影响")
	}
	// 未知 ID 空操作
	if removed, _ = q.Remove("no-such-id"); removed {
		t.Error("未知 ID 应返回 removed=false")
	}
}

// 0.6.312 B1/F1：StartSync 同步语义——①调用返回时操作已终态且 current
// 已清空（阻塞式，供自动更新批次逐条统计）；②运行中拒绝新操作（与
// Start 同互斥）；③失败操作终态 error 入 history 不阻塞下一项。
func TestQueue_StartSyncBlockingAndMutex(t *testing.T) {
	q := NewQueue()
	done := make(chan struct{})
	// 手动操作（异步 Start）先占住队列
	if _, err := q.Start("update", "manual-app", func(ctx context.Context, progress func(string, float64)) error {
		<-done
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// StartSync 应被拒绝（已有活动操作）
	if _, err := q.StartSync(context.Background(), "update", "auto-app", func(ctx context.Context, progress func(string, float64)) error {
		return nil
	}); err == nil {
		t.Fatal("活动操作存在时 StartSync 应拒绝")
	}
	close(done)
	deadline := time.Now().Add(3 * time.Second)
	for q.Current() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if q.Current() != nil {
		t.Fatal("手动操作完成后 Current() 应为 nil")
	}
	// 空闲时 StartSync 阻塞执行：返回时已完成
	started := time.Now()
	v, err := q.StartSync(context.Background(), "update", "auto-app", func(ctx context.Context, progress func(string, float64)) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 40*time.Millisecond {
		t.Fatal("StartSync 应阻塞到操作完成（返回时已终态）")
	}
	if v.State != StateDone {
		t.Fatalf("StartSync 返回视图应为 done，实际 %q", v.State)
	}
	if q.Current() != nil {
		t.Fatal("StartSync 完成后 current 应清空")
	}
	hist := q.History()
	if len(hist) != 2 || hist[0].State != StateDone || hist[0].Target != "auto-app" {
		t.Fatalf("history 应为 [auto-app done, manual-app done]，实际 %+v", hist)
	}
	// 失败项：同步返回错误 + 终态 error 入 history，且不阻塞下一项
	if _, err := q.StartSync(context.Background(), "update", "fail-app", func(ctx context.Context, progress func(string, float64)) error {
		return fmt.Errorf("boom")
	}); err == nil {
		t.Fatal("操作体失败时 StartSync 应返回错误")
	}
	if cur := q.History(); len(cur) < 3 || cur[0].Target != "fail-app" || cur[0].State != StateError {
		t.Fatalf("失败操作应入 history 且状态 error，实际 %+v", cur)
	}
	if _, err := q.StartSync(context.Background(), "update", "next-app", func(ctx context.Context, progress func(string, float64)) error {
		return nil
	}); err != nil {
		t.Fatalf("失败项不应阻塞后续: %v", err)
	}
}
