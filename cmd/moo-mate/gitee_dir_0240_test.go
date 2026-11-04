package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 0.2.40：文件名版本解析（软仓 server-data/apps 目录形态，无 tag 可依）。
func TestVersionFromFilename(t *testing.T) {
	cases := map[string]string{
		"9router-0.5.55.fpk":           "0.5.55",
		"CHzip-2.8.fpk":                "2.8",
		"FanControlServer-1.3.7.1.fpk": "1.3.7.1",
		"FnEnhance-1.0.3-x86.fpk":      "1.0.3",
		"python312-3.12.13.fpk":        "3.12.13",
		"note-2.fpk":                   "", // 单段数字不算版本
		"no-version.fpk":               "",
		"UPPER-1.2.3.FPK":              "1.2.3", // 大写扩展名也要剥掉
	}
	for in, want := range cases {
		if got := versionFromFilename(in); got != want {
			t.Errorf("versionFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// 0.2.40：目录扫描只收顶层 .fpk，非 file 类型与子目录忽略。
func TestDiscoverGiteeAppsDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/hhxs2025/fn-appstores/contents/server-data/apps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"type":"file","name":"9router-0.5.55.fpk","path":"server-data/apps/9router-0.5.55.fpk","download_url":"https://gitee.com/hhxs2025/fn-appstores/raw/master/server-data/apps/9router-0.5.55.fpk"},
			{"type":"file","name":"NoURL-1.0.0.fpk","path":"server-data/apps/NoURL-1.0.0.fpk","download_url":""},
			{"type":"dir","name":"sub","path":"server-data/apps/sub","download_url":""},
			{"type":"file","name":"readme.md","path":"server-data/apps/readme.md","download_url":"https://x/readme.md"}
		]`))
	}))
	defer srv.Close()

	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	got := discoverGiteeAppsDir(context.Background(), "hhxs2025", "fn-appstores", http.DefaultClient)
	if len(got) != 2 {
		t.Fatalf("目录候选数 = %d, want 2（2 个 fpk，忽略 dir/md）: %+v", len(got), got)
	}
	byName := map[string]releaseFPK{}
	for _, it := range got {
		byName[it.name] = it
	}
	a, ok := byName["9router-0.5.55.fpk"]
	if !ok || a.ver != "0.5.55" || a.url == "" {
		t.Errorf("9router 解析错误: %+v", a)
	}
	// download_url 为空时回落 raw/master 拼接
	b, ok := byName["NoURL-1.0.0.fpk"]
	if !ok || b.url != "https://gitee.com/hhxs2025/fn-appstores/raw/master/server-data/apps/NoURL-1.0.0.fpk" {
		t.Errorf("download_url 回落错误: %+v", b)
	}
}

// 0.2.40：目录不存在（404）时静默返回空，不报错。
func TestDiscoverGiteeAppsDirMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	if got := discoverGiteeAppsDir(context.Background(), "o", "r", http.DefaultClient); len(got) != 0 {
		t.Fatalf("404 目录应返回空, got %d", len(got))
	}
}
