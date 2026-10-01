package source

import "testing"

// TestTranslateEntryBaseResolve 相对 download_url 按 fnpack.json 实测目录
// （base）补全——社区源普遍用仓库内相对路径（"fnlogpush/fnlogpush.fpk"），
// base 为空时下载引擎直接拒收（unsupported protocol scheme ""）。
func TestTranslateEntryBaseResolve(t *testing.T) {
	base := "https://raw.githubusercontent.com/Wyf841015/FnDepot/master"
	// 1) 顶层相对 download_url（V1 平铺）
	m1 := map[string]any{
		"version":      "1.5.2",
		"download_url": "fnlogpush/fnlogpush.fpk",
		"icon_url":     "fnlogpush/ICON_256.PNG",
	}
	a := translateEntry("fnlogpush", m1, "s", "https://github.com/Wyf841015/FnDepot", base)
	if a.DownloadURL != base+"/fnlogpush/fnlogpush.fpk" {
		t.Errorf("download_url = %q", a.DownloadURL)
	}
	if a.IconURL != base+"/fnlogpush/ICON_256.PNG" {
		t.Errorf("icon_url = %q", a.IconURL)
	}
	// 2) V2 releases 相对 download_url（bestRelease 选出后同样要补全）
	m2 := map[string]any{
		"releases": map[string]any{
			"1.5.2": map[string]any{
				"packages": map[string]any{
					"all": map[string]any{
						"download_url": "fnlogpush/fnlogpush.fpk",
						"size":         1001199,
					},
				},
			},
			"1.3.0": map[string]any{
				"packages": map[string]any{
					"all": map[string]any{"download_url": "fnlogpush/fnlogpush.fpk"},
				},
			},
		},
	}
	b := translateEntry("fnlogpush", m2, "s", "https://github.com/Wyf841015/FnDepot", base)
	if b.Version != "1.5.2" {
		t.Fatalf("version = %q, want 1.5.2", b.Version)
	}
	if b.DownloadURL != base+"/fnlogpush/fnlogpush.fpk" {
		t.Errorf("releases download_url = %q", b.DownloadURL)
	}
	// 3) 绝对 URL 不受 base 影响
	m3 := map[string]any{
		"version":      "0.8.0",
		"download_url": "https://github.com/Blue-Mink/FnDepot/releases/download/v0.8.0/kspeeder.fpk",
	}
	c := translateEntry("kspeeder", m3, "s", "https://github.com/Blue-Mink/FnDepot", base)
	if c.DownloadURL != "https://github.com/Blue-Mink/FnDepot/releases/download/v0.8.0/kspeeder.fpk" {
		t.Errorf("绝对 URL 被改写: %q", c.DownloadURL)
	}
	// 4) base 为空 → 回退仓库 URL 拼接（非 GitHub）
	d := translateEntry("x", map[string]any{
		"version":      "1.0",
		"download_url": "x/x.fpk",
	}, "s", "https://fndepot.imcq.top", "")
	if d.DownloadURL != "https://fndepot.imcq.top/x/x.fpk" {
		t.Errorf("回退拼接 = %q", d.DownloadURL)
	}
	// 5) base 为空 + GitHub 仓库 → raw main 猜测（既有行为）
	e := translateEntry("x", map[string]any{
		"version":      "1.0",
		"download_url": "x/x.fpk",
	}, "s", "https://github.com/o/r", "")
	if e.DownloadURL != "https://raw.githubusercontent.com/o/r/main/x/x.fpk" {
		t.Errorf("github 回退 = %q", e.DownloadURL)
	}
}
