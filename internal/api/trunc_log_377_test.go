package api

import (
	"context"
	"fmt"
	"io"
	"testing"
)

// 0.6.377：读取失败日志分类。「取消/超时/真截断」必须分开记，否则线上
// 130 条 TRUNC 里绝大多数（父 ctx 取消，甚至 got==want）会把真截断淹没。
func TestTruncLogAction_377(t *testing.T) {
	cases := []struct {
		name string
		err  error
		got  int64
		want int64
		act  string
	}{
		{"父ctx取消-不记", context.Canceled, 846732, 846732, ""},
		{"包装后的取消-不记", fmt.Errorf("读中断: %w", context.Canceled), 512, 512, ""},
		{"自身12s超时-记TIMEOUT", context.DeadlineExceeded, 2048, 40960, "TIMEOUT"},
		{"传输中断-记TRUNC", io.ErrUnexpectedEOF, 1024, 8192, "TRUNC"},
		{"读满但无err-不记", nil, 4096, 4096, ""},
		{"声明长度不足-记TRUNC", nil, 900, 4096, "TRUNC"},
		{"未知长度短响应-记TRUNC", nil, 900, -1, "TRUNC"},
	}
	for _, c := range cases {
		if got := truncLogAction(c.err, c.got, c.want); got != c.act {
			t.Fatalf("%s: truncLogAction(%v,%d,%d)=%q，期望 %q", c.name, c.err, c.got, c.want, got, c.act)
		}
	}
}
