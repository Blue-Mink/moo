package source

import "testing"

// 相对 readme_url（无 ./ 前缀）必须补全为 raw 直链
// （社区源普遍给 "app/README.md" 这种不带 ./ 的写法）
func TestTranslateEntry_RelativeReadmeNoSlashPrefix(t *testing.T) {
	m := map[string]any{
		"version":    "0.5.86",
		"readme_url": "9router/README.md",
		"icon_url":   "9router/ICON.PNG",
	}
	a := translateEntry("9router", m, "shuangji66的应用源", "https://github.com/shuangji66/Fndepot", "")
	want := "https://raw.githubusercontent.com/shuangji66/Fndepot/main/9router/README.md"
	if a.ReadmeURL != want {
		t.Fatalf("readme_url = %q, want %q", a.ReadmeURL, want)
	}
	if a.IconURL != "https://raw.githubusercontent.com/shuangji66/Fndepot/main/9router/ICON.PNG" {
		t.Fatalf("icon_url = %q", a.IconURL)
	}
}

// 绝对 URL 与 ./ 前缀保持原行为
func TestTranslateEntry_AbsAndDotRel(t *testing.T) {
	m := map[string]any{"readme_url": "./README.md"}
	a := translateEntry("x", m, "s", "https://github.com/o/r", "")
	if a.ReadmeURL != "https://raw.githubusercontent.com/o/r/main/README.md" {
		t.Fatalf("./ 前缀: %q", a.ReadmeURL)
	}
	m2 := map[string]any{"readme_url": "https://example.com/README.md"}
	a2 := translateEntry("x", m2, "s", "https://github.com/o/r", "")
	if a2.ReadmeURL != "https://example.com/README.md" {
		t.Fatalf("绝对 URL 被改写: %q", a2.ReadmeURL)
	}
}
