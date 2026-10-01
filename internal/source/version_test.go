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
		// 既有 versionLess 语义：非数字后缀按字典序比空后缀大
		// （为 -N 构建号服务，rc 场景与之相反但极少见，保持一致）
		{"1.2.3-rc1", "1.2.3", false, false},
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
