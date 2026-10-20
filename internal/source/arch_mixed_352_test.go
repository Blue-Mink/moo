package source

import "testing"

// TestTranslateEntryMixedArch（0.6.352）：顶层 download_url（单架构便捷链接）
// + releases.packages（多架构）混合形态 → Arch/Archs 也要从 packages 提取，
// 详情页才能显示完整架构清单（修复前 Archs 为空、只显示顶层单架构）。
func TestTranslateEntryMixedArch(t *testing.T) {
	m := map[string]any{
		"version":      "2.0",
		"download_url": "https://x/app-x86.fpk", // 顶层单架构便捷链接
		"releases": map[string]any{
			"2.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://x/app-x86.fpk"},
					"arm": map[string]any{"download_url": "https://x/app-arm.fpk"},
				},
			},
		},
	}
	a := translateEntry("app", m, "s", "https://github.com/x/y", "")
	// 顶层 download_url 保留（便捷链接不被覆盖）
	if a.DownloadURL != "https://x/app-x86.fpk" {
		t.Errorf("download_url = %q, want 顶层便捷链接保留", a.DownloadURL)
	}
	// 修复点：Archs 必须从 packages 提取出双架构
	if len(a.Archs) != 2 || a.Archs[0] != "x86" || a.Archs[1] != "arm" {
		t.Errorf("archs = %v, want [x86 arm]", a.Archs)
	}
	// 顶层 download_url 未声明架构时，Arch 也应由 packages 命中架构补齐
	if a.Arch != currentArch() {
		t.Errorf("arch = %q, want %q", a.Arch, currentArch())
	}
}

// TestTranslateEntryMixedArchNoOverwrite（0.6.352）：混合形态下若 Arch/Archs
// 已有值（理论上不来自顶层，来自其它解析），无条件提取不得覆盖。
func TestTranslateEntryMixedArchNoOverwrite(t *testing.T) {
	m := map[string]any{
		"version":      "2.0",
		"download_url": "https://x/app-x86.fpk",
		"releases": map[string]any{
			"2.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://x/app-x86.fpk"},
					"arm": map[string]any{"download_url": "https://x/app-arm.fpk"},
				},
			},
		},
	}
	a := translateEntry("app", m, "s", "https://github.com/x/y", "")
	// 无二次覆盖：Archs 只提取一次、展示序稳定
	if len(a.Archs) != 2 || a.Archs[0] != "x86" || a.Archs[1] != "arm" {
		t.Errorf("archs = %v", a.Archs)
	}
	// 再次 translate（幂等语义）：结果不变
	b := translateEntry("app", m, "s", "https://github.com/x/y", "")
	if len(b.Archs) != len(a.Archs) {
		t.Errorf("二次解析 archs 漂移: %v vs %v", b.Archs, a.Archs)
	}
}

// TestTranslateEntryMixedArchDiff（0.6.352）：混合 arch_diff 形态
// （顶层 download_url + arch_diff）下，Arch/Archs 也要无条件提取。
func TestTranslateEntryMixedArchDiff(t *testing.T) {
	m := map[string]any{
		"version":      "1.0",
		"download_url": "https://x/app-x86.fpk", // 顶层单架构便捷链接
		"arch_diff": map[string]any{
			"x86": map[string]any{"download_url": "https://x/app-x86.fpk"},
			"arm": map[string]any{"download_url": "https://x/app-arm.fpk"},
		},
	}
	a := translateEntry("app", m, "s", "https://github.com/x/y", "")
	if a.DownloadURL != "https://x/app-x86.fpk" {
		t.Errorf("download_url = %q, want 顶层便捷链接保留", a.DownloadURL)
	}
	if len(a.Archs) != 2 || a.Archs[0] != "x86" || a.Archs[1] != "arm" {
		t.Errorf("混合 arch_diff 形态 archs = %v, want [x86 arm]", a.Archs)
	}
	if a.Arch != currentArch() {
		t.Errorf("arch = %q, want %q", a.Arch, currentArch())
	}
}

// TestMixedArchArmSelect（0.6.352 GAP-1）：arm 设备上，混合形态（顶层 x86
// download_url + releases.packages 双架构）应覆盖为本机 arm 包；x86 设备保留
// 顶层链接。
func TestMixedArchArmSelect(t *testing.T) {
	m := map[string]any{
		"version":      "2.0",
		"download_url": "https://github.com/x/y/releases/download/v2.0/app_2.0_x86.fpk",
		"releases": map[string]any{
			"2.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://github.com/x/y/releases/download/v2.0/app_2.0_x86.fpk"},
					"arm": map[string]any{"download_url": "https://github.com/x/y/releases/download/v2.0/app_2.0_arm.fpk"},
				},
			},
		},
	}

	// arm 设备：应覆盖为 arm 包
	SetArchForTest("arm")
	a := translateEntry("app", m, "s", "https://github.com/x/y", "")
	if a.DownloadURL != "https://github.com/x/y/releases/download/v2.0/app_2.0_arm.fpk" {
		t.Errorf("arm 设备 DownloadURL = %q, want arm 包", a.DownloadURL)
	}
	if a.Arch != "arm" {
		t.Errorf("arm 设备 arch = %q, want arm", a.Arch)
	}

	// x86 设备：保留顶层链接（不覆盖）
	SetArchForTest("x86")
	b := translateEntry("app", m, "s", "https://github.com/x/y", "")
	if b.DownloadURL != "https://github.com/x/y/releases/download/v2.0/app_2.0_x86.fpk" {
		t.Errorf("x86 设备 DownloadURL = %q, want 保留顶层 x86", b.DownloadURL)
	}
	SetArchForTest("")
}

// TestMixedArchNoMismatchOverwrite（0.6.352 GAP-1 风险点）：顶层 download_url
// 为非 GitHub release（加速镜像）时无法解析架构，应保守保留顶层链接，不误覆盖。
func TestMixedArchNoMismatchOverwrite(t *testing.T) {
	m := map[string]any{
		"version":      "2.0",
		"download_url": "https://gh-proxy.org/https://github.com/x/y/releases/download/v2.0/app_2.0_x86.fpk",
		"releases": map[string]any{
			"2.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://github.com/x/y/releases/download/v2.0/app_2.0_x86.fpk"},
					"arm": map[string]any{"download_url": "https://github.com/x/y/releases/download/v2.0/app_2.0_arm.fpk"},
				},
			},
		},
	}
	SetArchForTest("arm")
	defer SetArchForTest("")
	a := translateEntry("app", m, "s", "https://github.com/x/y", "")
	if a.DownloadURL != m["download_url"] {
		t.Errorf("加速镜像 DownloadURL 被覆盖 = %q, want 保守保留", a.DownloadURL)
	}
}
