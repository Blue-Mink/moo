package source

import (
	"strings"
	"testing"
)

// TestBundledDefaultSources 0.6.247：内置默认源集完整且规范——
// 156 条、全部带协议、去重后唯一、无回环/空地址。
func TestBundledDefaultSources(t *testing.T) {
	urls := BundledDefaultSources()
	if len(urls) != 156 {
		t.Fatalf("内置默认源应为 156 条，实际 %d", len(urls))
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
