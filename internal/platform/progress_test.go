package platform

import (
	"math"
	"testing"
)

// TestNormalizeProgress 表驱动：daemon 两种量纲（百分数 0~100 / 千分比
// 如 2110=21.1%）+ 脏值（负数、超大）全收敛到 [0,1]。
// 期望值即 0.6.320 installDownloadSSE 既有内联归一的行为（H4 抽取前后
// 行为不变，仅补下界 0 钳制）。
func TestNormalizeProgress(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
		desc string
	}{
		{0.5, 0.005, "百分数 0.5% → 0.005"},
		{65, 0.65, "百分数 65% → 0.65"},
		{2110, 0.211, "千分比 2110（=21.1%）→ 0.211"},
		{100, 1.0, "百分数上界 100 → 1"},
		{1500, 0.15, "千分比 1500（=15%）→ 0.15"},
		{-1, 0, "负脏值钳到 0"},
		{99999, 1, "超界脏值钳到 1"},
	}
	for _, c := range cases {
		got := NormalizeProgress(c.in)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("NormalizeProgress(%v) = %v, 期望 %v（%s）", c.in, got, c.want, c.desc)
		}
	}
}
