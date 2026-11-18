package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCleanReleaseNotes(t *testing.T) {
	body := "## 修复\n\n- 修复 A\n<!-- 隐藏注释 -->\n\n" +
		"| 文件 | 说明 |\n|---|---|\n| moo_x86.fpk | x86 |\n\n" +
		"- [下载](https://example.com/a.fpk)\nhttps://example.com/bare\n\n" +
		"**Full Changelog**: https://example.com/compare/v1...v2\n"
	got := cleanReleaseNotes(body)
	if strings.Contains(got, "moo_x86.fpk") || strings.Contains(got, "隐藏注释") {
		t.Fatalf("资产表/注释未清洗: %q", got)
	}
	if strings.Contains(got, "example.com") {
		t.Fatalf("裸链接未清洗: %q", got)
	}
	if !strings.Contains(got, "修复 A") || !strings.Contains(got, "## 修复") {
		t.Fatalf("正文丢失: %q", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("空行未压缩: %q", got)
	}
}

func TestCleanReleaseNotesTruncate(t *testing.T) {
	long := strings.Repeat("很长的日志行\n", 400)
	got := cleanReleaseNotes(long)
	if n := len([]rune(got)); n > maxNotesRunes+1 {
		t.Fatalf("未截断: %d 字符", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("缺少截断标记")
	}
}

func TestSHAFromDigest(t *testing.T) {
	good := strings.Repeat("a1", 32)
	if got := shaFromDigest("sha256:" + good); got != good {
		t.Fatalf("digest 归一失败: %q", got)
	}
	if got := shaFromDigest("md5:" + good); got != "" {
		t.Fatalf("非 sha256 应返回空: %q", got)
	}
	if got := shaFromDigest(""); got != "" {
		t.Fatalf("空 digest 应返回空")
	}
}

func TestPrevByAppKeyPicksSecondHighest(t *testing.T) {
	items := []releaseFPK{
		{name: "demo_1.0.0_x86.fpk", ver: "1.0.0", url: "u100", size: 10, changelog: "c100", updatedAt: "t100"},
		{name: "demo_1.2.0_x86.fpk", ver: "1.2.0", url: "u120", size: 12},
		{name: "demo_1.1.0_x86.fpk", ver: "1.1.0", url: "u110", size: 11},
		{name: "demo_1.1.0_arm.fpk", ver: "1.1.0", url: "u110a", size: 11},
	}
	prev := prevByAppKey(items)
	k := appKeyFromFilename("demo_1.2.0_x86.fpk")
	list, ok := prev[k]
	if !ok || len(list) != 2 {
		t.Fatalf("应挑出次高版本的两个架构: %+v", prev)
	}
	ver, at, notes, pkgs := prevFields(list)
	if ver != "1.1.0" {
		t.Fatalf("次高版本错: %q", ver)
	}
	if at != "" && notes != "" {
		t.Fatalf("无元数据时应为空: %q %q", at, notes)
	}
	if len(pkgs) != 2 {
		t.Fatalf("按架构聚合失败: %+v", pkgs)
	}
}

func TestReadmeFileAndRoundTrip(t *testing.T) {
	if got := readmeFile("../../etc/passwd"); strings.ContainsAny(got, "/\\") {
		t.Fatalf("路径未净化: %q", got)
	}
	c := &companion{bootDir: t.TempDir()}
	if _, _, ok := c.readReadme("demo"); ok {
		t.Fatalf("初始不应有 README")
	}
	if err := c.saveReadme("demo", []byte("# Demo\n"), "README.md"); err != nil {
		t.Fatalf("save: %v", err)
	}
	b, mt, ok := c.readReadme("demo")
	if !ok || string(b) != "# Demo\n" {
		t.Fatalf("roundtrip 失败: %q", b)
	}
	if time.Since(mt) > time.Minute {
		t.Fatalf("mtime 异常")
	}
}

func TestNeedsBackfill(t *testing.T) {
	cases := []struct {
		a    *indexedApp
		want bool
	}{
		{&indexedApp{appEntry: appEntry{ReadmeURL: "u"}, Source: "https://github.com/a/b"}, true},
		{&indexedApp{appEntry: appEntry{ReadmeURL: "u", Changelog: "c"}, Source: "https://github.com/a/b"}, true},
		{&indexedApp{appEntry: appEntry{ReadmeURL: "u", Changelog: "c", Releases: map[string]*appRelease{"1": {}}}, Source: "https://github.com/a/b"}, false},
		{&indexedApp{appEntry: appEntry{ReadmeURL: "u", Changelog: "c", Releases: map[string]*appRelease{"1": {}}}, Source: ""}, false},
	}
	for i, c := range cases {
		if got := needsBackfill(c.a); got != c.want {
			t.Fatalf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

// 0.2.73：release 元数据（body / published_at / digest）与历史版本要一路带到 discoveredApp。
func TestDiscoverFromRepoReleaseMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/demo":
			_, _ = w.Write([]byte(`{"default_branch":"main","private":false}`))
		case "/repos/acme/demo/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[{"path":"README.md","type":"blob"},{"path":"cmd/moo-mate/README.md","type":"blob"},{"path":"moo-mate/README.md","type":"blob"},{"path":"docs/README.md","type":"blob"}]}`))
		case "/repos/acme/demo/releases":
			sha := strings.Repeat("b2", 32)
			_, _ = w.Write([]byte(`[
			 {"tag_name":"v1.2.0","published_at":"2026-11-01T00:00:00Z",
			  "body":"## 修复\n\n- 修复 A\n\n| 文件 | 架构 |\n|---|---|\n| demo_1.2.0_x86.fpk | x86 |\n",
			  "assets":[
			   {"name":"demo_1.2.0_x86.fpk","browser_download_url":"https://example.com/x86.fpk","size":100,"digest":"sha256:` + sha + `"},
			   {"name":"demo_1.2.0_arm.fpk","browser_download_url":"https://example.com/arm.fpk","size":90,"digest":"sha256:` + sha + `"}]},
			 {"tag_name":"v1.1.0","published_at":"2026-10-01T00:00:00Z","body":"- 旧版",
			  "assets":[
			   {"name":"demo_1.1.0_x86.fpk","browser_download_url":"https://example.com/old_x86.fpk","size":80},
			   {"name":"demo_1.1.0_arm.fpk","browser_download_url":"https://example.com/old_arm.fpk","size":70}]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()

	apps, err := discoverFromRepo(context.Background(), "https://github.com/acme/demo", "", srv.Client())
	if err != nil {
		t.Fatalf("discoverFromRepo: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("候选数 = %d, want 2: %+v", len(apps), apps)
	}
	sha := strings.Repeat("b2", 32)
	for _, a := range apps {
		if a.Version != "1.2.0" {
			t.Fatalf("版本 = %q, want 1.2.0", a.Version)
		}
		if !strings.Contains(a.Changelog, "修复 A") || strings.Contains(a.Changelog, "demo_1.2.0_x86.fpk") {
			t.Fatalf("更新日志未清洗: %q", a.Changelog)
		}
		if a.UpdatedAt != "2026-11-01T00:00:00Z" {
			t.Fatalf("发布时间 = %q", a.UpdatedAt)
		}
		if a.Sha256 != sha {
			t.Fatalf("sha256 = %q", a.Sha256)
		}
		if a.PrevVer != "1.1.0" || len(a.PrevPkgs) != 2 {
			t.Fatalf("历史版本 = %q / %+v", a.PrevVer, a.PrevPkgs)
		}
	}
}

func TestVersionFromTagPrefixed(t *testing.T) {
	cases := map[string]string{
		"v1.2.3":        "1.2.3",
		"1.2.3":         "1.2.3",
		"mate-v0.2.71":  "0.2.71",
		"release-1.0.4": "",
		"app/v2.0.0":    "2.0.0",
		"20260317":      "20260317",
		"nightly-build": "",
	}
	for in, want := range cases {
		if got := versionFromTag(in); got != want {
			t.Fatalf("versionFromTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoReadmePathsPrefersAppSubdir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/demo":
			_, _ = w.Write([]byte(`{"default_branch":"main"}`))
		case "/repos/acme/demo/git/trees/main":
			_, _ = w.Write([]byte(`{"tree":[{"path":"README.md","type":"blob"},{"path":"cmd/moo-mate/README.md","type":"blob"},{"path":"moo-mate/README.md","type":"blob"},{"path":"docs/README.md","type":"blob"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()
	c := &companion{httpc: srv.Client()}
	byApp, root := c.repoReadmePaths(context.Background(), "https://github.com/acme/demo")
	if root != "README.md" {
		t.Fatalf("根 README = %q", root)
	}
	if byApp["moo-mate"] != "moo-mate/README.md" {
		t.Fatalf("同仓多应用应取短路径子目录 README: %+v", byApp)
	}
	if byApp["docs"] != "docs/README.md" {
		t.Fatalf("其他子目录 README 应可用: %+v", byApp)
	}
	if _, ok := byApp["cmd"]; ok {
		t.Fatalf("不应把 cmd 目录当作应用名: %+v", byApp)
	}
}
