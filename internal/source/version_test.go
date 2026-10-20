package source

import "testing"

func TestVersionHelpers(t *testing.T) {
	cases := []struct {
		a, b     string
		less, eq bool
	}{
		{"1.0.0", "1.0.0", false, true},
		{"v1.0.0", "1.0.0", false, true}, // v 前缀忽略
		{"1.0.0", "1.0.1", true, false},
		{"1.10.0", "1.9.0", false, false}, // 数字段比较（非字典序）
		{"0.8.0", "0.7.16", false, false},
		{"2.4.0", "2.5.5", true, false},
		// 0.6.330 语义变更：预发布 < 同底数正式版（旧实现按后缀字典序把 rc 判更高，
		// 会允许「1.2.3 已装 → 1.2.3-rc1 更新」这种降级；wb2api 事故同源）。
		{"1.2.3-rc1", "1.2.3", true, false},
	}
	for _, c := range cases {
		if got := VersionLess(c.a, c.b); got != c.less {
			t.Errorf("VersionLess(%q,%q) = %v, want %v", c.a, c.b, got, c.less)
		}
		if got := VersionEqual(c.a, c.b); got != c.eq {
			t.Errorf("VersionEqual(%q,%q) = %v, want %v", c.a, c.b, got, c.eq)
		}
	}
	if !IsNewer("2.5.5", "2.4.0") {
		t.Error("IsNewer(2.5.5, 2.4.0) = false, want true")
	}
	if IsNewer("2.4.0", "2.4.0") {
		t.Error("IsNewer 同版本应为 false")
	}
	if IsNewer("", "1.0.0") || IsNewer("1.0.0", "") {
		t.Error("IsNewer 空版本应为 false")
	}
}
