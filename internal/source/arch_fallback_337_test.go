package source

import "testing"

// TestArchFromAssetName（0.6.337）：详情页「架构」兜底——从 Release 资产文件名
// 推断架构；识别不出的、以及「含 arm 的非架构词」都不误判。
func TestArchFromAssetName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"1panel_1.0_x86.fpk", "x86"},
		{"1panel_1.0_arm.fpk", "arm"},
		{"foo_amd64.fpk", "x86"},
		{"bar_arm64.fpk", "arm"},
		{"baz_x86_64.fpk", "x86"},
		{"qux_aarch64.fpk", "arm"},
		{"app-x64.fpk", "x86"},
		{"app-armv7.fpk", "arm"},
		{"app-armhf.fpk", "arm"},
		// 非架构 / 含 arm 的词不误判
		{"harmony.fpk", ""},
		{"alarm.fpk", ""},
		{"smart-home.fpk", ""},
		{"plain.fpk", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := ArchFromAssetName(c.name); got != c.want {
			t.Errorf("ArchFromAssetName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}
