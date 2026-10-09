package source

import (
	"strings"
	"testing"
)

// TestBundledDefaultSources 0.6.247：内置默认源集完整且规范——
// 0.6.316 起 20 条精选源（此前 0.6.314 起 157 条）、全部带协议、
// 去重后唯一、无回环/空地址、含 fn-knock 官方源（kci-lnk/fn-knock-turborepo）。
func TestBundledDefaultSources(t *testing.T) {
	urls := BundledDefaultSources()
	if len(urls) != 20 {
		t.Fatalf("内置默认源应为 20 条，实际 %d", len(urls))
	}
	hasKnock := false
	for _, u := range urls {
		if u == "https://github.com/kci-lnk/fn-knock-turborepo" {
			hasKnock = true
		}
	}
	if !hasKnock {
		t.Fatal("内置默认源应含 fn-knock 官方源（kci-lnk/fn-knock-turborepo）")
	}
	seen := map[string]bool{}
	for _, u := range urls {
		if u == "" {
			t.Fatal("存在空地址")
		}
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			t.Fatalf("地址未带协议: %q", u)
		}
		if strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") {
			t.Fatalf("内置默认源不得含回环地址: %q", u)
		}
		k := NormalizeSourceURL(u)
		if seen[k] {
			t.Fatalf("内置默认源存在重复: %q", u)
		}
		seen[k] = true
	}
}

// TestSourceNameFromURL 0.6.247：命名规则（从 api 层迁入，锚定行为）。
func TestSourceNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Blue-Mink/FnDepot": "Blue-Mink",
		"github.com/Blue-Mink/FnDepot":         "Blue-Mink",
		"https://www.github.com/a/b.git":       "a",
		// 0.6.271：GitHub 系直链也取作者（保留原始大小写），不再叫 "moo.json"
		"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json":                  "Blue-Mink",
		"https://cdn.jsdelivr.net/gh/Blue-Mink/FnDepot/moo.json":                            "Blue-Mink",
		"https://gh-proxy.com/https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json": "Blue-Mink",
		// 0.6.271：Gitea raw 分支直链取仓库名（先剥 /raw/branch/<分支>/ 与索引文件名）
		"http://192.0.2.15:3033/bluemink/moo/raw/branch/main/moo.json": "moo",
		// 0.6.271：非 GitHub 剥索引文件名后取最后一段
		"https://example.com/myapps/fnpack.json": "myapps",
	}
	for in, want := range cases {
		if got := SourceNameFromURL(in); got != want {
			t.Errorf("SourceNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SourceNameFromURL("https://github.com/conversun/fnos-apps"); got != "fnos-store" {
		t.Errorf("conversun 应固定为 fnos-store，got %q", got)
	}
}

// TestNormalizeSourceURLSameRepo 0.6.271：同一仓库的各种形态归一化为同一
// 身份（旧实现只剥 /fnpack.json：moo.json 直链与仓库根被判成两个源，
// 同仓库重复入库、应用列两遍）。
func TestNormalizeSourceURLSameRepo(t *testing.T) {
	base := NormalizeSourceURL("https://github.com/Blue-Mink/FnDepot")
	if base != "github.com/blue-mink/fndepot" {
		t.Fatalf("仓库根应归一为 github.com/owner/repo，实际 %q", base)
	}
	for _, u := range []string{
		"https://github.com/Blue-Mink/FnDepot/",
		"https://github.com/Blue-Mink/FnDepot/tree/main",
		"https://github.com/Blue-Mink/FnDepot.git",
		"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json",
		"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json",
		"https://cdn.jsdelivr.net/gh/Blue-Mink/FnDepot/moo.json",
		"https://gh-proxy.com/https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json",
	} {
		if got := NormalizeSourceURL(u); got != base {
			t.Errorf("同仓库形态 %q → %q，期望 %q", u, got, base)
		}
	}
	if NormalizeSourceURL("https://github.com/Blue-Mink/New-Store") == base {
		t.Error("不同仓库不应同身份")
	}
	// Gitea：仓库根与 raw 分支直链同身份（0.6.271 剥 /raw/branch/<分支>/ 段）
	g1 := NormalizeSourceURL("http://192.0.2.15:3033/bluemink/moo")
	g2 := NormalizeSourceURL("http://192.0.2.15:3033/bluemink/moo/raw/branch/main/moo.json")
	if g1 != g2 {
		t.Errorf("Gitea 仓库根 %q vs raw 直链 %q 应同身份", g1, g2)
	}
	if g1 != "192.0.2.15:3033/bluemink/moo" {
		t.Errorf("Gitea 归一结果异常: %q", g1)
	}
}
