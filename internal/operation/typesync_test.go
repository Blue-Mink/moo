package operation

// 0.6.544：StartSync 必须把原始错误类型带回调用方。
//
// 自动更新批次用 errors.As 区分「升级需先确认向导」（*platform.WizardRequiredError，
// 计「跳过」+ 写 12h 负缓存）与真失败（计「失败」+ 通知）。此前 StartSync 用
// fmt.Errorf("%s", v.Err) 返回，类型恒丢失 → 那个判断在真机上从未成立
// （0.6.542 上线后实测仍计「失败」，0.6.544 修）。

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type typedBoom struct{ missing string }

func (e *typedBoom) Error() string { return "需要先完成升级确认" }

func TestStartSyncPreservesErrorType(t *testing.T) {
	q := NewQueue()
	_, err := q.StartSync(context.Background(), "update", "moo.livetest",
		func(ctx context.Context, p func(string, float64)) error {
			return &typedBoom{missing: "moo_test_note"}
		})
	if err == nil {
		t.Fatalf("期望错误，实际 nil")
	}
	var tb *typedBoom
	if !errors.As(err, &tb) {
		t.Fatalf("错误类型丢失: %T / %v", err, err)
	}
	if tb.missing != "moo_test_note" {
		t.Fatalf("错误字段丢失: %+v", tb)
	}
	if err.Error() != "需要先完成升级确认" {
		t.Fatalf("错误文案被改变: %q", err.Error())
	}
}

func TestStartSyncPreservesWrappedErrorType(t *testing.T) {
	q := NewQueue()
	_, err := q.StartSync(context.Background(), "update", "motrix",
		func(ctx context.Context, p func(string, float64)) error {
			return fmt.Errorf("依赖 X 安装失败: %w", &typedBoom{missing: "m"})
		})
	var tb *typedBoom
	if !errors.As(err, &tb) {
		t.Fatalf("包装后的类型丢失: %T / %v", err, err)
	}
}
