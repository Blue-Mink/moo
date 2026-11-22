package source

// P1-4（2026-11-21，FnDepot#24）：源解析落库前的 base 规范化。
//
// 背景：索引（moo.json/fnpack.json）常经镜像前缀拉到，胜出地址形如
// "https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/moo.json"。
// 旧实现直接把它当 base，相对资源（download_url/icon/readme/preview）就被
// 拼成 "https://gh-proxy.com/<raw…>" 落进用户数据——镜像域名一下线，
// 存量条目全变死链（#24 就是 cdn.gh-proxy.org Fastly unknown domain）。
// 修复后：base 一律规范化成内层 raw 直链，镜像只在下游下载/抓取时按需展开。

import (
	"strings"
	"testing"
)

func TestCanonicalMirrorURL_24(t *testing.T) {
	cases := map[string]string{
		"https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/moo.json":  "https://raw.githubusercontent.com/o/r/main/moo.json",
		"https://ghproxy.net/http://raw.githubusercontent.com/o/r/main/fnpack.json": "http://raw.githubusercontent.com/o/r/main/fnpack.json",
		"https://gh-proxy.com/https://github.com/o/r":                               "https://github.com/o/r",
		"https://gh-proxy.com/https://codeload.github.com/o/r/tar.gz/main":          "https://codeload.github.com/o/r/tar.gz/main",
		// 已是直链 / 非镜像壳 / 内层非 GitHub 主机 → 原样
		"https://raw.githubusercontent.com/o/r/main/moo.json":    "https://raw.githubusercontent.com/o/r/main/moo.json",
		"https://cdn.jsdelivr.net/gh/o/r@main/moo.json":          "https://cdn.jsdelivr.net/gh/o/r@main/moo.json",
		"https://myproxy.example.com/https://example.org/x.json": "https://myproxy.example.com/https://example.org/x.json",
		"http://192.0.2.15:3033/bluemink/moo/moo.json":           "http://192.0.2.15:3033/bluemink/moo/moo.json",
		"": "",
	}
	for in, want := range cases {
		if got := canonicalMirrorURL(in); got != want {
			t.Errorf("canonicalMirrorURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIndexBaseForWinner_24(t *testing.T) {
	cases := map[string]string{
		"https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/moo.json":     "https://raw.githubusercontent.com/o/r/main",
		"https://ghproxy.net/https://raw.githubusercontent.com/o/r/master/fnpack.json": "https://raw.githubusercontent.com/o/r/master",
		"https://raw.githubusercontent.com/o/r/main/moo.json":                          "https://raw.githubusercontent.com/o/r/main",
	}
	for in, want := range cases {
		if got := indexBaseForWinner(in); got != want {
			t.Errorf("indexBaseForWinner(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranslateEntryBaseIsRaw_24(t *testing.T) {
	base := indexBaseForWinner("https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/moo.json")
	m := map[string]any{
		"version":      "1.0.0",
		"download_url": "app/app.fpk",
		"icon_url":     "app/ICON.PNG",
		"readme_url":   "app/README.md",
		"preview_urls": []any{"app/Preview/01.png"},
	}
	a := translateEntry("app", m, "s", "https://gh-proxy.com/https://github.com/o/r", base)
	got := []string{a.DownloadURL, a.IconURL, a.ReadmeURL}
	got = append(got, a.PreviewURLs...)
	for _, u := range got {
		if !strings.HasPrefix(u, "https://raw.githubusercontent.com/o/r/main/") {
			t.Errorf("相对资源未按 raw base 补全: %q", u)
		}
		if strings.Contains(u, "gh-proxy") {
			t.Errorf("镜像域名被写进落库地址: %q", u)
		}
	}
}

func TestTranslateEntryMirrorRepoFallback_24(t *testing.T) {
	// base 缺失（索引直链/目录形态）时，回退分支也必须用去壳后的仓库地址，
	// 否则会拼出 "raw.githubusercontent.com/https:/github.com/o/r/…" 的乱码。
	m := map[string]any{"version": "1.0.0", "icon_url": "app/ICON.PNG"}
	a := translateEntry("app", m, "s", "https://gh-proxy.com/https://github.com/o/r", "")
	if want := "https://raw.githubusercontent.com/o/r/main/app/app.fpk"; a.DownloadURL != want {
		t.Errorf("DownloadURL = %q, want %q", a.DownloadURL, want)
	}
	if want := "https://raw.githubusercontent.com/o/r/main/app/ICON.PNG"; a.IconURL != want {
		t.Errorf("IconURL = %q, want %q", a.IconURL, want)
	}
}
