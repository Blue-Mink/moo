package source

import (
	"strings"
	"testing"
)

// 0.6.350 全量测试 WS1：版本比较的**独立表驱动矩阵**。
// 与 version_330_test.go 互为交叉校验：同一语义若有分叉，两处必有一处红。
// 每行同时断言 CompareVersion 值、反对称性、以及 VersionLess/VersionEqual/IsNewer 三助手一致性。
type verCase350 struct {
	name string
	a, b string
	want int
}

func runVerMatrix350(t *testing.T, cases []verCase350) {
	t.Helper()
	for _, c := range cases {
		got := CompareVersion(c.a, c.b)
		if got != c.want {
			t.Errorf("%s: CompareVersion(%q,%q)=%d want %d", c.name, c.a, c.b, got, c.want)
		}
		if rev := CompareVersion(c.b, c.a); rev != -c.want {
			t.Errorf("%s: 反对称性破坏 CompareVersion(%q,%q)=%d want %d", c.name, c.b, c.a, rev, -c.want)
		}
		if self := CompareVersion(c.a, c.a); self != 0 {
			t.Errorf("%s: 自反性破坏 CompareVersion(%q,%q)=%d want 0", c.name, c.a, c.a, self)
		}
		if VersionLess(c.a, c.b) != (c.want < 0) {
			t.Errorf("%s: VersionLess(%q,%q)=%v want %v", c.name, c.a, c.b, VersionLess(c.a, c.b), c.want < 0)
		}
		// VersionEqual 对空串有特判（空 == 空，空 != 任何非空），与 CompareVersion 的
		// 「不可解析返回 0」不同源；这里两套契约分别钉死。
		if c.a != "" && c.b != "" {
			if VersionEqual(c.a, c.b) != (c.want == 0) {
				t.Errorf("%s: VersionEqual(%q,%q)=%v want %v", c.name, c.a, c.b, VersionEqual(c.a, c.b), c.want == 0)
			}
		} else if VersionEqual(c.a, c.b) != (c.a == c.b) {
			t.Errorf("%s: 空串 VersionEqual 契约破坏 (%q,%q)", c.name, c.a, c.b)
		}
		if IsNewer(c.a, c.b) != (c.want > 0) {
			t.Errorf("%s: IsNewer(%q,%q)=%v want %v", c.name, c.a, c.b, IsNewer(c.a, c.b), c.want > 0)
		}
	}
}

// A. 数字核心（缺段按 0 补齐）+ 数字修订号
func TestVersionMatrix350Core(t *testing.T) {
	runVerMatrix350(t, []verCase350{
		{"缺段等同", "1.0", "1.0.0", 0},
		{"缺段等同反向", "1.0.0", "1.0", 0},
		{"补丁位递增", "1.0.0", "1.0.1", -1},
		{"补丁位递减", "1.0.1", "1.0.0", 1},
		{"数值非字典序", "1.9.0", "1.10.0", -1},
		{"主版本优先", "2.0", "1.99.99", 1},
		{"多核心段", "4.1.3.4", "4.1.3", 1},
		{"前导零", "01.2", "1.2", 0},

		{"修订号更高", "1.13.0", "1.13.0-1", -1},
		{"修订号反向", "1.13.0-1", "1.13.0", 1},
		{"相同修订号", "1.13.0-1", "1.13.0-1", 0},
		{"两位修订号数值比较", "1.13.0-9", "1.13.0-10", -1},
		{"两位修订号反向", "1.13.0-10", "1.13.0-9", 1},
		{"修订号跨核心", "1.13.0-1", "1.12.9", 1},
		{"修订号零等同", "1.0.0-0", "1.0.0", 0},
		{"官方包形态", "3.12.4-11", "3.12.4-9", 1},
		{"官方包形态二", "22.18.0-1", "22.18.0", 1},
	})
}

// B. 预发布 / 噪声后缀 / 前导 v / 异常输入
func TestVersionMatrix350Suffix(t *testing.T) {
	runVerMatrix350(t, []verCase350{
		{"预发布低于正式", "1.0.0-rc.1", "1.0.0", -1},
		{"正式高于预发布", "1.0.0", "1.0.0-rc.1", 1},
		{"rc 递增", "1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"rc 两位数值", "1.0.0-rc.9", "1.0.0-rc.10", -1},
		{"rc 同号", "1.0.0-rc.37", "1.0.0-rc.37", 0},
		{"beta 低于 rc", "1.0.0-beta", "1.0.0-rc", -1},
		{"alpha 低于 beta", "1.0.0-alpha", "1.0.0-beta", -1},
		{"rc 无点号形式", "2.0.0-rc1", "2.0.0-rc2", -1},
		{"预发布低于数字修订", "1.0.0-rc.1", "1.0.0-1", -1},
		{"下划线预发布", "1.0.0_rc3", "1.0.0", -1},

		{"加号元数据忽略", "1.0.0+x86", "1.0.0", 0},
		{"stable 尾巴忽略", "1.0.0_stable_1", "1.0.0", 0},
		{"架构后缀忽略", "1.0.0-x86", "1.0.0", 0},
		{"长 stable 尾巴忽略", "2.4.0.1_stable_2026-10-07", "2.4.0.1", 0},
		{"latest 标签不可比", "latest-2026.10.17", "4.1.3.4", 0},
		{"release 标签不可比", "release-2026.10.17", "2026.10.17", 0},
		{"latest 自比也返回 0", "latest-2026.10.17", "latest-2026.10.17", 0},

		{"v 前缀等同", "v1.2.3", "1.2.3", 0},
		{"大写 V 前缀", "V2.0.0", "2.0.0", 0},
		{"v 前缀数值比较", "v10.0.0", "2.0.0", 1},
		{"空串对非空", "", "1.0.0", 0},
		{"双空串", "", "", 0},
		{"畸形双点", "1..2", "1.2", 0},
		{"尾点", "1.2.", "1.2", 0},
		{"非数字核心", "abc", "1.0.0", 0},
		{"核心含字母", "1.2.x", "1.2", 0},
		{"超长数字核心", strings.Repeat("9", 30), "1.0.0", 0},
		{"超长修订号", "1.0.0-" + strings.Repeat("9", 20), "1.0.0", 0},
	})
}

// C. 可解析性口径 + 空值/不可比一律不判更新
func TestVersionParseAndGuard350(t *testing.T) {
	ok := []string{"1", "0", "1.9.0", "v2.5.0", "1.0.0-1", "1.9.0-rc1", "1.8.0_rc", "1.8.0_492", "1.0.0-x86", "1.0.0+build5"}
	bad := []string{"", "   ", "abc", "1.2.x", "1..2", "1.2.", "latest-2026.10.17", "stable-1.2.3", strings.Repeat("9", 30)}
	for _, v := range ok {
		if !ParseVersion(v).OK {
			t.Errorf("ParseVersion(%q).OK=false want true", v)
		}
	}
	for _, v := range bad {
		if ParseVersion(v).OK {
			t.Errorf("ParseVersion(%q).OK=true want false", v)
		}
	}
	// 空值与不可比串永不判更新（升级守卫依赖这条，绝不能出假更新）
	for _, v := range []string{"", "latest-2026.10.17", "abc"} {
		if IsNewer(v, "1.0.0") || IsNewer("1.0.0", v) {
			t.Errorf("IsNewer 对不可比值 %q 误判为更新", v)
		}
	}
	// 无 panic 保证：任意怪串两两比较结果必须落在 {-1,0,1}
	weird := []string{"", "-", ".", "..", "v", "v.", "1-", "1.", "-1", "1.0.0--1", "1.0.0-rc", "１.０.０", "1 .0", "\x00", "1.0.0\x00", strings.Repeat("1.", 200)}
	for _, a := range weird {
		for _, b := range weird {
			if got := CompareVersion(a, b); got < -1 || got > 1 {
				t.Fatalf("CompareVersion(%q,%q)=%d 越界", a, b, got)
			}
		}
	}
}

// D. 排序一致性：小集合全序 + 传递性
func TestVersionOrdering350(t *testing.T) {
	asc := []string{"1.0.0-alpha", "1.0.0-beta", "1.0.0-rc.1", "1.0.0-rc.2", "1.0.0", "1.0.0-1", "1.0.0-10", "1.0.1"}
	for i := 0; i < len(asc); i++ {
		for j := 0; j < len(asc); j++ {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := CompareVersion(asc[i], asc[j]); got != want {
				t.Fatalf("全序破坏：%q vs %q = %d want %d", asc[i], asc[j], got, want)
			}
		}
	}
	for i := range asc {
		for j := range asc {
			for k := range asc {
				if CompareVersion(asc[i], asc[j]) <= 0 && CompareVersion(asc[j], asc[k]) <= 0 {
					if CompareVersion(asc[i], asc[k]) > 0 {
						t.Fatalf("传递性破坏：%q<=%q<=%q 但比较反转", asc[i], asc[j], asc[k])
					}
				}
			}
		}
	}
}
