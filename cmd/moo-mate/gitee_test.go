package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGiteeRepo(t *testing.T) {
	cases := map[string][2]string{
		"https://gitee.com/hhxs2025/fn-appstores":        {"hhxs2025", "fn-appstores"},
		"https://gitee.com/hhxs2025/fn-appstores/":       {"hhxs2025", "fn-appstores"},
		"http://gitee.com/hhxs2025/fn-appstores.git":     {"hhxs2025", "fn-appstores"},
		"gitee.com/ybgs/fn-fpk":                          {"ybgs", "fn-fpk"},
		"https://gitee.com/hhxs2025/fn-appstores-client": {"hhxs2025", "fn-appstores-client"},
	}
	for in, want := range cases {
		o, r, ok := parseGiteeRepo(in)
		if !ok {
			t.Errorf("parseGiteeRepo(%q) 未命中", in)
			continue
		}
		if o != want[0] || r != want[1] {
			t.Errorf("parseGiteeRepo(%q) = (%q,%q), want (%q,%q)", in, o, r, want[0], want[1])
		}
	}
	// 非 Gitee 不命中
	for _, in := range []string{"https://github.com/a/b", "https://example.com/a/b", ""} {
		if _, _, ok := parseGiteeRepo(in); ok {
			t.Errorf("parseGiteeRepo(%q) 不应命中", in)
		}
	}
}

// repoOwner 对 GitHub / Gitee 都返回 owner。
func TestRepoOwner(t *testing.T) {
	if got := repoOwner("https://github.com/Blue-Mink/moo"); got != "Blue-Mink" {
		t.Errorf("repoOwner(github) = %q, want Blue-Mink", got)
	}
	if got := repoOwner("https://gitee.com/hhxs2025/fn-appstores"); got != "hhxs2025" {
		t.Errorf("repoOwner(gitee) = %q, want hhxs2025", got)
	}
	if got := repoOwner("https://example.com/x/y"); got != "" {
		t.Errorf("repoOwner(非仓库) = %q, want 空", got)
	}
}

// versionFromTag 兼容飞牛软仓的日期 tag（20260827 / 2026092101）。
func TestVersionFromTagDate(t *testing.T) {
	cases := map[string]string{
		"20260827":    "20260827",
		"2026092101":  "2026092101",
		"2.9.0":       "2.9.0",
		"v1.5.2":      "1.5.2",
		"3.13.16":     "3.13.16",
		"app/v1.2.3":  "1.2.3",
		"release-abc": "", // 非数字开头 → 空
	}
	for in, want := range cases {
		if got := versionFromTag(in); got != want {
			t.Errorf("versionFromTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// dedupFPKs：Gitee 资产无 size，size 传 0 不影响去重；同名只留最高版。
func TestDedupFPKsGiteeNoSize(t *testing.T) {
	items := []releaseFPK{
		{name: "python312-3.12.13.fpk", url: "https://gitee.com/x/releases/download/20260827/python312-3.12.13.fpk", size: 0, ver: "20260827"},
		{name: "fn-wireguard-0.9.0.fpk", url: "https://gitee.com/x/releases/download/20260921/fn-wireguard-0.9.0.fpk", size: 0, ver: "20260921"},
		{name: "fn-wireguard-0.9.0.fpk", url: "https://gitee.com/x/releases/download/2026092101/fn-wireguard-0.9.0.fpk", size: 0, ver: "2026092101"},
		{name: "note.zip", url: "https://gitee.com/x/releases/download/20260827/note.zip", size: 0, ver: "20260827"}, // 非 fpk 忽略
	}
	got := dedupFPKs(items)
	if len(got) != 2 {
		t.Fatalf("dedupFPKs 结果数 = %d, want 2（python312 + fn-wireguard 去重后 1）", len(got))
	}
	byName := map[string]discoveredApp{}
	for _, a := range got {
		byName[a.Name] = a
	}
	if _, ok := byName["python312-3.12.13.fpk"]; !ok {
		t.Error("缺 python312")
	}
	wg, ok := byName["fn-wireguard-0.9.0.fpk"]
	if !ok {
		t.Fatal("缺 fn-wireguard")
	}
	// 同名去重：保留更高日期 tag 的那条
	if wg.Version != "2026092101" {
		t.Errorf("fn-wireguard 版本 = %q, want 2026092101（更高日期 tag）", wg.Version)
	}
	if wg.Size != 0 {
		t.Errorf("Gitee 资产 size 应为 0, got %d", wg.Size)
	}
}

// giteeReleases：httptest 模拟 Gitee API 分页与资产解析（无 size 字段）。
func TestGiteeReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		if page == "1" || page == "" {
			_, _ = w.Write([]byte(`[
				{"tag_name":"20260827","assets":[{"name":"note-1.2.1.fpk","browser_download_url":"https://gitee.com/hhxs2025/fn-appstores/releases/download/20260827/note-1.2.1.fpk"},{"name":"python312-3.12.13.fpk","browser_download_url":"https://gitee.com/hhxs2025/fn-appstores/releases/download/20260827/python312-3.12.13.fpk"}]},
				{"tag_name":"2026082701","assets":[{"name":"LitePan-0.5.1.fpk","browser_download_url":"https://gitee.com/hhxs2025/fn-appstores/releases/download/2026082701/LitePan-0.5.1.fpk"}]}
			]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	rels, err := giteeReleases(context.Background(), "hhxs2025", "fn-appstores", http.DefaultClient)
	if err != nil {
		t.Fatalf("giteeReleases 出错: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("release 数 = %d, want 2", len(rels))
	}
	if rels[0].TagName != "20260827" || len(rels[0].Assets) != 2 {
		t.Errorf("首条 release 解析错误: %+v", rels[0])
	}
	if rels[0].Assets[0].Name != "note-1.2.1.fpk" || rels[0].Assets[0].URL == "" {
		t.Errorf("资产解析错误: %+v", rels[0].Assets[0])
	}
	// Gitee 资产结构体不含 size（依赖 API 不返回，字段未声明 → 恒 0）
}
