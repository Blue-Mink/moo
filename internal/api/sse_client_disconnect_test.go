package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/operation"
)

// 复现（用户侧报障 2026-11-21）：SSE 客户端在操作中途断开。
// 期望观察到两件事同时成立：
//  1. sseRunOp 立刻返回（写 SSE 的请求 goroutine 结束，客户端只收到开头那条事件）；
//  2. 操作本身在后台继续跑完并落进 history=done —— 于是「提示失败、实际更新成功」。
func TestSSERunOpClientDisconnectKeepsOperationRunning(t *testing.T) {
	s := &Server{Ops: operation.NewQueue()}
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/api/apps/fnmonitor/update", nil).WithContext(ctx)

	started := make(chan struct{})
	release := make(chan struct{})
	fnReturned := make(chan struct{})
	fn := func(_ context.Context, progress func(string, float64)) error {
		close(started)
		progress("安装中…", 50)
		<-release
		close(fnReturned)
		return nil
	}

	sseReturned := make(chan struct{})
	go func() {
		defer close(sseReturned)
		s.sseRunOp(req, rec, rec, "update", "fnmonitor", fn)
	}()

	<-started
	cancel() // 模拟手机切网 / WebView 挂起：请求上下文被取消
	select {
	case <-sseReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("客户端断开后 sseRunOp 未返回（请求 goroutine 泄漏）")
	}

	body := rec.Body.String()
	if strings.Contains(body, `"step":"done"`) {
		t.Fatalf("客户端断开后不应再收到终态事件，实际 SSE:\n%s", body)
	}

	// 客户端已经不在了，但操作必须还能自己跑完
	close(release)
	select {
	case <-fnReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("操作未在后台继续执行")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range s.Ops.History() {
			if v.Target == "fnmonitor" && v.State == operation.StateDone {
				return // 后台跑完并记为 done
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("后台操作未记入 history=done，当前 history=%+v", s.Ops.History())
}
