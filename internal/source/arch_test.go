package source

import "testing"

// TestTranslateEntryArch（0.6.303）：releases 多架构包 → Arch=命中架构 +
// Archs=完整清单（展示序 x86→arm→all）；arch_diff 旧格式同款；平铺单链接无架构信息。
func TestTranslateEntryArch(t *testing.T) {
	m1 := map[string]any{
		"releases": map[string]any{
			"2.0": map[string]any{
				"packages": map[string]any{
					"x86": map[string]any{"download_url": "https://x/app-x86.fpk"},
					"arm": map[string]any{"download_url": "https://x/app-arm.fpk"},
				},
			},
		},
	}
	a := translateEntry("app", m1, "s", "https://github.com/x/y", "")
	if a.Arch != currentArch() {
		t.Errorf("arch = %q, want %q", a.Arch, currentArch())
	}
	wantURL := "https://x/app-x86.fpk"
	if currentArch() == "arm" {
		wantURL = "https://x/app-arm.fpk"
	}
	if a.DownloadURL != wantURL {
		t.Errorf("download_url = %q, want %q", a.DownloadURL, wantURL)
	}
	if len(a.Archs) != 2 || a.Archs[0] != "x86" || a.Archs[1] != "arm" {
		t.Errorf("archs = %v, want [x86 arm]", a.Archs)
	}

	// 只有 all 包 → arch=all
	m2 := map[string]any{
		"releases": map[string]any{
			"1.0": map[string]any{
				"packages": map[string]any{
					"all": map[string]any{"download_url": "https://x/app-all.fpk"},
				},
			},
		},
	}
	b := translateEntry("app", m2, "s", "https://github.com/x/y", "")
	if b.Arch != "all" || len(b.Archs) != 1 || b.Archs[0] != "all" {
		t.Errorf("b = arch %q archs %v", b.Arch, b.Archs)
	}

	// 本机架构包缺失 → 回退 all（诚实标记 arch=all）
	m3 := map[string]any{
		"releases": map[string]any{
			"1.0": map[string]any{
				"packages": map[string]any{
					"arm": map[string]any{"download_url": "https://x/app-arm.fpk"},
					"all": map[string]any{"download_url": "https://x/app-all.fpk"},
				},
			},
		},
	}
	c := translateEntry("app", m3, "s", "https://github.com/x/y", "")
	if c.Arch != "all" || c.DownloadURL != "https://x/app-all.fpk" {
		t.Errorf("c = arch %q url %q", c.Arch, c.DownloadURL)
	}
	if len(c.Archs) != 2 || c.Archs[0] != "arm" || c.Archs[1] != "all" {
		t.Errorf("c.archs = %v, want [arm all]", c.Archs)
	}

	// arch_diff 旧格式
	m4 := map[string]any{
		"arch_diff": map[string]any{
			"x86": map[string]any{"download_url": "https://x/a-x86.fpk"},
			"arm": map[string]any{"download_url": "https://x/a-arm.fpk"},
		},
	}
	d := translateEntry("app", m4, "s", "https://github.com/x/y", "")
	if d.Arch != currentArch() || len(d.Archs) != 2 {
		t.Errorf("d = arch %q archs %v", d.Arch, d.Archs)
	}

	// 平铺单链接（无架构信息）→ 空（详情页不显示架构行）
	e := translateEntry("app", map[string]any{
		"version":      "1.0",
		"download_url": "https://x/a.fpk",
	}, "s", "https://github.com/x/y", "")
	if e.Arch != "" || len(e.Archs) != 0 {
		t.Errorf("e = arch %q archs %v, want empty", e.Arch, e.Archs)
	}
}

// TestBestReleaseArchSelection（0.6.303）：命中架构键=安装自动识别依据；
// 本机架构包缺失且无 all = 诚实「无可安装包」（不拿 x86 包糊弄 arm 设备）。
func TestBestReleaseArchSelection(t *testing.T) {
	rel := map[string]any{
		"1.0": map[string]any{
			"packages": map[string]any{
				"x86": map[string]any{"download_url": "u"},
			},
		},
	}
	ver, arch, pkg := bestRelease(rel, "x86")
	if ver != "1.0" || arch != "x86" || pkg == nil {
		t.Errorf("x86 select = %q/%q/%v", ver, arch, pkg)
	}
	ver, arch, pkg = bestRelease(rel, "arm")
	if ver != "" || arch != "" || pkg != nil {
		t.Errorf("arm on x86-only source should be empty, got %q/%q", ver, arch)
	}
}

// TestArchDisplayKeys（0.6.303）：展示序 x86 → arm → all → 其余字母序。
func TestArchDisplayKeys(t *testing.T) {
	got := archDisplayKeys([]string{"all", "x86", "arm", "riscv"})
	want := [4]string{"x86", "arm", "all", "riscv"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order = %v, want %v", got, want[:])
		}
	}
}
