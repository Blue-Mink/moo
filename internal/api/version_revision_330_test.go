package api

import (
	"testing"

	"moo/internal/platform"
	"moo/internal/source"
)

// 0.6.330：版本比较统一到 internal/source.CompareVersion。
// 回归点：修复前 splitVer 把第一个 `-` 起整段丢弃，`1.13.0-1` → `1.13.0`，
// 与已装判等 → has_update=false（wb2api 实测），而安装守卫 source.IsNewer
// 认为它更高 → 「点得到但不给点」。
func TestNumericRevisionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.13.0-1", "1.13.0", 1},     // 修订号更高（本条即 wb2api 场景）
		{"1.13.0", "1.13.0-1", -1},    // 反向
		{"1.13.0-1", "1.13.0-1", 0},   // 同修订
		{"1.13.0-2", "1.13.0-10", -1}, // 数字段比较，不是字符串比较
		{"3.12.4-11", "3.12.4-9", 1},  // 官方包形态
		{"22.18.0-1", "22.18.0", 1},   // 官方包形态
		{"1.13.0-1", "1.12.0-9", 1},   // 主版本优先于修订
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Fatalf("compareVersions(%q,%q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}

	// splitVer 的具体形态（仅供 comparableVersion 使用，便于排障）
	if got := joined(splitVer("1.13.0-1")); got != "1.13.0.1" {
		t.Fatalf("splitVer(\"1.13.0-1\") = %q，期望 1.13.0.1", got)
	}
	// 非数字后缀仍按「忽略」处理，不得制造假更新
	if got := compareVersions("1.0.0-x86", "1.0.0"); got != 0 {
		t.Fatalf("compareVersions(\"1.0.0-x86\",\"1.0.0\") = %d，期望 0（非数字后缀忽略）", got)
	}
}

func joined(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}

// 既有契约（这些断言若红说明改动面越界）。
func TestNumericRevisionKeepsLegacyContracts(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "1.2.3", 0},        // F7：剥前导 v
		{"1.2.2", "v1.2.3", -1},       // F7
		{"v2.0.0", "10.0.0", -1},      // 数值比较不是字符串比较
		{"1.8.0_99", "1.8.0_100", -1}, // G4：_ 当分段符
		{"1.8.0_492", "1.8.0_492", 0},
		{"", "", 0},
		// 0.6.330 语义变更：预发布 < 同底数正式版（旧实现判等/更高）
		{"2.0.0-rc1", "2.0.0", -1},
		{"2.0.0", "2.0.0-rc1", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Fatalf("compareVersions(%q,%q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
	// 可比较性判定对带修订号/预发布的版本仍为 true（自动纠正依赖它）
	for _, v := range []string{"1.13.0-1", "1.9.0-rc1", "1.8.0_rc"} {
		if !comparableVersion(v) {
			t.Fatalf("comparableVersion(%q) 应为 true", v)
		}
	}
	for _, v := range []string{"abc", "1.2.x", "latest-2026.10.17", ""} {
		if comparableVersion(v) {
			t.Fatalf("comparableVersion(%q) 应为 false", v)
		}
	}
}

// api 与 source 两个入口必须是同一口径（防再次分叉）。
func TestCompareVersionsMatchesSource(t *testing.T) {
	pairs := [][2]string{
		{"1.13.0-1", "1.13.0"}, {"1.0.0-10", "1.0.0-9"}, {"1.0.0-rc.38", "1.0.0-rc.37"},
		{"1.0.0-rc.1", "1.0.0"}, {"1.0", "1.0.0"}, {"1.0.0+build5", "1.0.0"},
		{"latest-2026.10.17", "4.1.3.4"}, {"v2.0.0", "10.0.0"}, {"1.8.0_99", "1.8.0_100"},
	}
	for _, p := range pairs {
		if a, b := compareVersions(p[0], p[1]), source.CompareVersion(p[0], p[1]); a != b {
			t.Fatalf("口径分叉：compareVersions(%q,%q)=%d 而 source.CompareVersion=%d", p[0], p[1], a, b)
		}
	}
}

// 端到端：fillInstalled 是「源版本 vs 已装版本」落 HasUpdate 的唯一入口。
func TestFillInstalledVersionScenarios(t *testing.T) {
	cases := []struct {
		name      string
		latest    string
		installed string
		want      bool
	}{
		{"修订号递增（wb2api）", "1.13.0-1", "1.13.0", true},
		{"同修订号", "1.13.0-1", "1.13.0-1", false},
		{"两位修订号", "1.0.0-10", "1.0.0-9", true},
		{"预发布递增", "1.0.0-rc.38", "1.0.0-rc.37", true},
		{"正式版高于已装预发布", "1.0.0", "1.0.0-rc.37", true},
		{"预发布不得顶掉正式版", "1.0.0-rc.38", "1.0.0", false},
		{"段数零填充等同", "1.0.0", "1.0", false},
		{"build 元数据等同", "1.0.0+build5", "1.0.0", false},
		{"标签串不可比", "latest-2026.10.17", "4.1.3.4", false},
		{"降级不亮", "1.12.0", "1.13.0", false},
	}
	for _, c := range cases {
		ai := AppInfo{Key: "zz", AppName: "zz", LatestVersion: c.latest}
		fillInstalled(&ai, platform.InstalledApp{AppName: "zz", Version: c.installed})
		if ai.HasUpdate != c.want {
			t.Fatalf("%s：HasUpdate = %v，期望 %v（latest=%q installed=%q）", c.name, ai.HasUpdate, c.want, c.latest, c.installed)
		}
	}
}
