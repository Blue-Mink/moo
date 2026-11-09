package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 0.2.58：GitHub 仓库文件树里的 .fpk 也要能发现（release 不是唯一来源）。
func TestDiscoverRepoFilesGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/app":
			_, _ = w.Write([]byte(`{"default_branch":"main","private":false}`))
		case "/repos/acme/app/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v1.2.0","assets":[
				{"name":"rel.fpk","browser_download_url":"https://example.com/rel.fpk","size":10},
				{"name":"notes.txt","browser_download_url":"https://example.com/notes.txt","size":3}]}]`))
		case "/repos/acme/app/git/trees/main":
			_, _ = w.Write([]byte(`{"truncated":false,"tree":[
				{"path":"dist/a.fpk","type":"blob","size":20},
				{"path":"README.md","type":"blob","size":3},
				{"path":"dist/b.txt","type":"blob","size":2}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()

	apps, err := discoverFromRepo(context.Background(), "https://github.com/acme/app", "", srv.Client())
	if err != nil {
		t.Fatalf("discoverFromRepo: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("候选数 = %d, want 2: %+v", len(apps), apps)
	}
	byName := map[string]discoveredApp{}
	for _, a := range apps {
		byName[a.Name] = a
	}
	rel, ok := byName["rel.fpk"]
	if !ok || rel.Version != "1.2.0" || rel.DownloadURL != "https://example.com/rel.fpk" {
		t.Fatalf("release 候选错误: %+v", rel)
	}
	repoFile, ok := byName["a.fpk"]
	if !ok {
		t.Fatalf("缺少仓库文件候选: %+v", apps)
	}
	if repoFile.DownloadURL != "https://github.com/acme/app/raw/main/dist/a.fpk" {
		t.Fatalf("仓库文件下载地址 = %q", repoFile.DownloadURL)
	}
	if repoFile.Size != 20 {
		t.Fatalf("仓库文件 size = %d, want 20", repoFile.Size)
	}
	// 输出按文件名排序，保证进度日志顺序稳定
	if apps[0].Name != "a.fpk" || apps[1].Name != "rel.fpk" {
		t.Fatalf("顺序 = %s,%s，want a.fpk,rel.fpk", apps[0].Name, apps[1].Name)
	}
}

// 0.2.58：私有仓库不扫文件树（raw 链接对外不可下载）。
func TestScanRepoFilesGitHubPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/acme/priv" {
			_, _ = w.Write([]byte(`{"default_branch":"main","private":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()

	_, err := scanRepoFilesGitHub(context.Background(), "acme", "priv", "", srv.Client(), nil)
	if err == nil || !strings.Contains(err.Error(), "私有仓库") {
		t.Fatalf("私有仓库应当跳过，err = %v", err)
	}
}

// 0.2.58：Gitee 仓库文件树 + release 合并；同版本目录版优先（release 多为停更旧包）。
func TestDiscoverRepoFilesGitee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/o/r":
			_, _ = w.Write([]byte(`{"default_branch":"master"}`))
		case "/repos/o/r/releases":
			_, _ = w.Write([]byte(`[{"tag_name":"v2.0.0","assets":[
				{"name":"same.fpk","browser_download_url":"https://gitee.com/o/r/releases/download/v2.0.0/same.fpk"}]}]`))
		case "/repos/o/r/git/trees/master":
			_, _ = w.Write([]byte(`{"tree":[
				{"path":"server-data/apps/same.fpk","type":"blob","size":5},
				{"path":"server-data/apps/x-1.0.0.fpk","type":"blob","size":6},
				{"path":"docs/a.md","type":"blob","size":1}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	apps, err := discoverFromRepo(context.Background(), "https://gitee.com/o/r", "", srv.Client())
	if err != nil {
		t.Fatalf("discoverFromRepo: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("候选数 = %d, want 2: %+v", len(apps), apps)
	}
	byName := map[string]discoveredApp{}
	for _, a := range apps {
		byName[a.Name] = a
	}
	same := byName["same.fpk"]
	if !strings.Contains(same.DownloadURL, "/raw/master/server-data/apps/same.fpk") {
		t.Fatalf("同版本应优先仓库文件，得到 %q", same.DownloadURL)
	}
	if x := byName["x-1.0.0.fpk"]; x.Version != "1.0.0" {
		t.Fatalf("文件名版本解析 = %q, want 1.0.0", x.Version)
	}
}

// 0.2.58：按应用键剔除历史版本，只留最高版（多架构同版都保留）。
func TestPruneByAppKey(t *testing.T) {
	items := []releaseFPK{
		{name: "alist_3.64.0_x86.fpk", ver: "3.64.0"},
		{name: "alist_3.65.0_arm.fpk", ver: "3.65.0"},
		{name: "alist_3.65.0_x86.fpk", ver: "3.65.0"},
		{name: "foo-1.0.0.fpk", ver: "1.0.0"},
		{name: "bar.fpk", ver: ""},
	}
	got := pruneByAppKey(items)
	if len(got) != 4 {
		t.Fatalf("聚合后 = %d, want 4: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, it := range got {
		names[it.name] = true
	}
	for _, want := range []string{"alist_3.65.0_arm.fpk", "alist_3.65.0_x86.fpk", "foo-1.0.0.fpk", "bar.fpk"} {
		if !names[want] {
			t.Fatalf("缺少 %s: %+v", want, got)
		}
	}
	if names["alist_3.64.0_x86.fpk"] {
		t.Fatal("旧版本应被剔除")
	}
	if k := appKeyFromFilename("transmission_latest-2026.11.08_arm.fpk"); k != "transmission_latest" {
		t.Fatalf("appKey = %q, want transmission_latest", k)
	}
	if k := appKeyFromFilename("FanControlServer-1.3.7.1.fpk"); k != "fancontrolserver" {
		t.Fatalf("appKey = %q, want fancontrolserver", k)
	}
}

// 0.2.58：单应用更新检查按文件名过滤候选；全不匹配时保持全量。
func TestFilterCandidatesForApp(t *testing.T) {
	items := []discoveredApp{
		{Name: "fnmusic-ext-2.12.3-0.fpk"},
		{Name: "other-1.0.0.fpk"},
	}
	got := filterCandidatesForApp(items, "fnmusic-ext")
	if len(got) != 1 || got[0].Name != "fnmusic-ext-2.12.3-0.fpk" {
		t.Fatalf("过滤结果 = %+v", got)
	}
	if all := filterCandidatesForApp(items, "completely-different"); len(all) != 2 {
		t.Fatalf("无匹配时应全量返回，得到 %d", len(all))
	}
}

// 0.2.58：超预算时按应用分组、小包优先（尽量多覆盖应用）。
func TestOrderByAppBreadth(t *testing.T) {
	items := []releaseFPK{
		{name: "big_1.0.0_x86.fpk", ver: "1.0.0", size: 100},
		{name: "small_1.0.0_x86.fpk", ver: "1.0.0", size: 10},
		{name: "small_1.0.0_arm.fpk", ver: "1.0.0", size: 10},
	}
	got := orderByAppBreadth(items, nil)
	if len(got) != 3 {
		t.Fatalf("数量 = %d, want 3", len(got))
	}
	if got[0].name != "small_1.0.0_x86.fpk" || got[1].name != "small_1.0.0_arm.fpk" || got[2].name != "big_1.0.0_x86.fpk" {
		t.Fatalf("顺序 = %s,%s,%s", got[0].name, got[1].name, got[2].name)
	}
}

// 0.2.58：合并规则——高版本优先；同版本按来源偏好（GitHub release / Gitee 仓库文件）。
func TestMergeFPKsPreference(t *testing.T) {
	rel := []releaseFPK{{name: "a.fpk", ver: "1.0.0", url: "release"}}
	repo := []releaseFPK{{name: "a.fpk", ver: "1.0.0", url: "repo", fromRepo: true}}
	if got := mergeFPKs(rel, repo, false); len(got) != 1 || got[0].url != "release" {
		t.Fatalf("GitHub 同版本应偏好 release，得到 %+v", got)
	}
	if got := mergeFPKs(rel, repo, true); len(got) != 1 || got[0].url != "repo" {
		t.Fatalf("Gitee 同版本应偏好仓库文件，得到 %+v", got)
	}
	repoNew := []releaseFPK{{name: "a.fpk", ver: "1.1.0", url: "repo", fromRepo: true}}
	if got := mergeFPKs(rel, repoNew, false); len(got) != 1 || got[0].url != "repo" {
		t.Fatalf("更高版本应胜出，得到 %+v", got)
	}
}
