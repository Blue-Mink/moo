package main

// 0.6.313 B4 单测：MOO_GOMEMLIMIT 解析（裸字节 / 数字+单位 / 0 / 非法）。

import "testing"

func TestParseMemLimit_06313(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"268435456", 268435456, false}, // 裸字节
		{"256MB", 256 << 20, false},
		{"512mb", 512 << 20, false}, // 单位大小写不敏感
		{"1GB", 1 << 30, false},
		{"4KB", 4 << 10, false},
		{"1024B", 1024, false},
		{" 256mb ", 256 << 20, false}, // 容许首尾空白
		{"0", 0, false},                // 0 = 不设（逃生阀）
		{"-8", 0, false},               // 负数 = 不设
		{"abc", 0, true},
		{"12X", 0, true},
		{"1.5MB", 0, true},
		{"MB", 0, true},
	}
	for _, c := range cases {
		got, err := parseMemLimit(c.in)
		if (err != nil) != c.wantErr {
			t.Fatalf("parseMemLimit(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Fatalf("parseMemLimit(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
