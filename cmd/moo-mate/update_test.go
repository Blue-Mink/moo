package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.31", "0.2.32", true},
		{"0.2.32", "0.2.31", false},
		{"0.2.32", "0.2.32", false},
		{"v0.2.32", "0.3.0", true},
		{"0.2", "0.2.0", false},
		{"1.0.0", "0.9.9", false},
		{"0.2.32", "1.0.0-rc1", true},
	}
	for _, c := range cases {
		if got := verLess(c.a, c.b); got != c.want {
			t.Errorf("verLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPickMateRelease(t *testing.T) {
	rels := []mateGhRelease{
		{TagName: "v0.6.377", HTMLURL: "moo-page"}, // Moo 自己的版本必须忽略
		{TagName: "mate-v0.2.31", Body: "old", HTMLURL: "p31"},
		{
			TagName: "mate-v0.2.33",
			Body:    "new",
			HTMLURL: "p33",
			Assets: []mateReleaseAsset{
				{Name: "moo-mate_0.2.33_arm.fpk", URL: "arm-url"},
				{Name: "moo-mate_0.2.33_x86.fpk", URL: "x86-url"},
			},
		},
		{TagName: "mate-v0.2.32", Body: "mid", HTMLURL: "p32"},
	}
	ver, notes, page, dl, ok := pickMateRelease(rels)
	if !ok {
		t.Fatal("pickMateRelease 应命中 mate-v*")
	}
	if ver != "0.2.33" || notes != "new" || page != "p33" || dl != "x86-url" {
		t.Fatalf("pickMateRelease 结果不对: ver=%q notes=%q page=%q dl=%q", ver, notes, page, dl)
	}
}

func TestPickMateReleaseNone(t *testing.T) {
	rels := []mateGhRelease{{TagName: "v0.6.377"}}
	if _, _, _, _, ok := pickMateRelease(rels); ok {
		t.Fatal("没有 mate-v* 时不应命中")
	}
}

// ── 0.2.33 追加：atom 兜底通道 / token 只走官方通道 / 失败短缓存 ──

const atomSample = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry><title>Moo v0.6.377</title><link rel="alternate" href="https://github.com/Blue-Mink/moo/releases/tag/v0.6.377"/></entry>
  <entry><title>Moo Mate v0.2.31</title><content type="html">&lt;p&gt;old&lt;/p&gt;</content><link rel="alternate" href="https://github.com/Blue-Mink/moo/releases/tag/mate-v0.2.31"/></entry>
  <entry><title>Moo Mate v0.2.33</title><content type="html">&lt;p&gt;· 新增版本检测&lt;/p&gt;</content><link rel="alternate" href="https://github.com/Blue-Mink/moo/releases/tag/mate-v0.2.33"/></entry>
</feed>`

func TestFetchMateAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, atomSample)
	}))
	defer srv.Close()
	ver, page, notes, ok, err := fetchMateAtom(&http.Client{Timeout: 3 * time.Second}, srv.URL)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if ver != "0.2.33" {
		t.Fatalf("ver=%q want 0.2.33", ver)
	}
	if !strings.HasSuffix(page, "/releases/tag/mate-v0.2.33") {
		t.Fatalf("page=%q", page)
	}
	if !strings.Contains(notes, "新增版本检测") {
		t.Fatalf("notes=%q", notes)
	}
}

// 关键安全断言：token 只能发给官方 API 通道，第三方镜像必须匿名。
func TestProbeTokenOnlyOnOfficialChannel(t *testing.T) {
	var apiAuth, proxyAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiAuth = r.Header.Get("Authorization")
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer api.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyAuth = r.Header.Get("Authorization")
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer proxy.Close()
	atom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, atomSample)
	}))
	defer atom.Close()

	old := mateUpdateProviders
	mateUpdateProviders = []mateUpdateProvider{
		{name: "api", url: api.URL, auth: true},
		{name: "gh-proxy", url: proxy.URL, auth: false},
		{name: "atom", url: atom.URL, auth: false},
	}
	defer func() { mateUpdateProviders = old }()

	info := probeMateUpdate("0.2.32", "SECRET-TOKEN")
	if info.LastError != "" {
		t.Fatalf("atom 兜底应成功，实际 last_error=%q", info.LastError)
	}
	if info.AvailableVersion != "0.2.33" || !info.HasUpdate {
		t.Fatalf("ver=%q has_update=%v", info.AvailableVersion, info.HasUpdate)
	}
	if apiAuth != "Bearer SECRET-TOKEN" {
		t.Fatalf("官方通道应带 token，实际 %q", apiAuth)
	}
	if proxyAuth != "" {
		t.Fatalf("第三方镜像绝不能带 token，实际 %q", proxyAuth)
	}
}

// 生产通道配置的硬约束：token 只能给官方 api.github.com；
// 第三方镜像与 atom 订阅源必须匿名。改默认配置就会红。
func TestProviderAuthFlags(t *testing.T) {
	if len(mateUpdateProviders) < 3 {
		t.Fatalf("探测通道少于 3 条：%d（api / 镜像 / atom）", len(mateUpdateProviders))
	}
	for _, p := range mateUpdateProviders {
		official := strings.HasPrefix(p.url, "https://api.github.com/")
		if p.auth && !official {
			t.Fatalf("通道 %s 不是官方域名却允许带 token：%s", p.name, p.url)
		}
		if official && !p.auth {
			t.Fatalf("官方通道 %s 应允许带 token", p.name)
		}
	}
}

func TestMateCacheTTL(t *testing.T) {
	if got := mateCacheTTL(mateUpdateInfo{AvailableVersion: "0.2.33"}); got != mateUpdateTTL {
		t.Fatalf("成功结果 TTL=%v want %v", got, mateUpdateTTL)
	}
	if got := mateCacheTTL(mateUpdateInfo{LastError: "HTTP 403"}); got != mateUpdateErrTTL {
		t.Fatalf("失败结果 TTL=%v want %v", got, mateUpdateErrTTL)
	}
}
