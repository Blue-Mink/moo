package operation

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// detailErr 模拟带结构化细节的失败（升级向导缺项等）。
type detailErr struct{ kind string }

func (e *detailErr) Error() string { return "需要先完成升级确认" }
func (e *detailErr) ErrorDetail() map[string]any {
	return map[string]any{"kind": e.kind}
}

// plainErr 是不带细节的普通失败（不得凭空造出 detail）。
type plainErr struct{}

func (e *plainErr) Error() string { return "普通失败" }

func waitTerminal(t *testing.T, q *Queue, id string) View {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := q.Status(id); v != nil && (v.State == StateDone || v.State == StateError) {
			return *v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("操作未在 3s 内到达终态")
	return View{}
}

// 0.6.5xx：Detailer 错误的细节必须带到 View.Detail，且被包装后仍能取到。
func TestQueueErrorDetailReachesView(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		kind  string
		wantD bool
	}{
		{"直接细节错误", &detailErr{kind: "wizard_required"}, "wizard_required", true},
		{"被包装的细节错误", fmt.Errorf("依赖 x 安装失败: %w", &detailErr{kind: "wizard_required"}), "wizard_required", true},
		{"普通错误", &plainErr{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := NewQueue()
			v, err := q.Start("update", "motrix", func(ctx context.Context, progress func(string, float64)) error {
				return c.err
			})
			if err != nil {
				t.Fatalf("Start 失败: %v", err)
			}
			got := waitTerminal(t, q, v.ID)
			if got.State != StateError {
				t.Fatalf("state = %q，期望 error", got.State)
			}
			if c.wantD {
				if got.Detail == nil {
					t.Fatalf("Detail 为空：细节丢失")
				}
				if got.Detail["kind"] != c.kind {
					t.Fatalf("Detail.kind = %v，期望 %q", got.Detail["kind"], c.kind)
				}
				return
			}
			if got.Detail != nil {
				t.Fatalf("普通错误不该有 Detail，得到 %v", got.Detail)
			}
		})
	}
}

// StartSync 路径（自动更新批处理）同样要拿到细节。
func TestStartSyncErrorDetail(t *testing.T) {
	q := NewQueue()
	v, err := q.StartSync(context.Background(), "update", "motrix",
		func(ctx context.Context, progress func(string, float64)) error {
			return &detailErr{kind: "wizard_required"}
		})
	if err == nil {
		t.Fatalf("期望返回错误")
	}
	if v.Detail == nil || v.Detail["kind"] != "wizard_required" {
		t.Fatalf("StartSync 视图缺 Detail: %+v", v)
	}
}
