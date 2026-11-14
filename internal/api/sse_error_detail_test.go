package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/operation"
)

// sseDetailErr 模拟升级向导缺项（operation.Detailer）。
type sseDetailErr struct{}

func (e *sseDetailErr) Error() string {
	return "motrix v1.6.11 需要先完成升级确认（必填：下载目录）"
}

func (e *sseDetailErr) ErrorDetail() map[string]any {
	return map[string]any{
		"kind":    "wizard_required",
		"appname": "motrix",
		"missing": []string{"下载目录"},
		"wizard":  map[string]any{"appname": "motrix", "has_wizard": true},
	}
}

// runSSEOp 跑一次 sseRunOp 并返回 SSE 全文。
func runSSEOp(t *testing.T, opErr error) string {
	t.Helper()
	s := &Server{Ops: operation.NewQueue()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/apps/motrix/update", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sseRunOp(req, rec, rec, "update", "motrix",
			func(ctx context.Context, progress func(string, float64)) error { return opErr })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("sseRunOp 5s 未结束")
	}
	return rec.Body.String()
}

// 0.6.5xx：向导缺项必须随 error 事件下发 detail，前端才能弹窗收集参数重试。
func TestSSERunOpErrorCarriesDetail(t *testing.T) {
	body := runSSEOp(t, &sseDetailErr{})
	for _, want := range []string{`"step":"error"`, `"error":`, `"detail"`, `"wizard_required"`, `"下载目录"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE 缺 %s\n全文: %s", want, body)
		}
	}
}

// 普通失败不得凭空带 detail（避免前端误判成向导类失败）。
func TestSSERunOpPlainErrorHasNoDetail(t *testing.T) {
	body := runSSEOp(t, errPlain)
	if strings.Contains(body, `"detail"`) {
		t.Fatalf("普通失败不该带 detail\n全文: %s", body)
	}
	if !strings.Contains(body, `"step":"error"`) {
		t.Fatalf("缺少 error 事件: %s", body)
	}
}

type plainOpErr struct{}

func (e *plainOpErr) Error() string { return "下载失败：连接超时" }

var errPlain = &plainOpErr{}
