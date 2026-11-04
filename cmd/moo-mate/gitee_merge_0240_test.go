package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newGiteeMock 构造 Gitee API mock：releases 返回 relJSON（空串=404），
// contents 返回 dirJSON（空串=404），其余 404。
func newGiteeMock(t *testing.T, relJSON, dirJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r/releases":
			if relJSON == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(relJSON))
		case r.URL.Path == "/repos/o/r/contents/server-data/apps":
			if dirJSON == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(dirJSON))
		default:
			http.NotFound(w, r)
		}
	}))
}

// 0.2.40：Gitee 发现合并 release + server-data/apps，同名以目录版为准。
func TestDiscoverFromRepoGiteeMergesDir(t *testing.T) {
	rel := `[{"tag_name":"20260924","assets":[
		{"name":"clash-for-fnos-1.2.2.fpk","browser_download_url":"https://gitee.com/o/r/releases/download/20260924/clash-for-fnos-1.2.2.fpk"},
		{"name":"OldOnly-1.0.0.fpk","browser_download_url":"https://gitee.com/o/r/releases/download/20260924/OldOnly-1.0.0.fpk"}]}]`
	dir := `[
		{"type":"file","name":"clash-for-fnos-1.2.2.fpk","path":"server-data/apps/clash-for-fnos-1.2.2.fpk","download_url":"https://gitee.com/o/r/raw/master/server-data/apps/clash-for-fnos-1.2.2.fpk"},
		{"type":"file","name":"NewApp-9.9.9.fpk","path":"server-data/apps/NewApp-9.9.9.fpk","download_url":"https://gitee.com/o/r/raw/master/server-data/apps/NewApp-9.9.9.fpk"}]`
	srv := newGiteeMock(t, rel, dir)
	defer srv.Close()
	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	got, err := discoverFromRepo(context.Background(), "https://gitee.com/o/r", "", http.DefaultClient)
	if err != nil {
		t.Fatalf("discoverFromRepo 出错: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("候选数 = %d, want 3（目录 2 + release 独有 1）: %+v", len(got), got)
	}
	byName := map[string]discoveredApp{}
	for _, a := range got {
		byName[a.Name] = a
	}
	if a := byName["clash-for-fnos-1.2.2.fpk"]; a.DownloadURL == "" ||
		a.DownloadURL[:len("https://gitee.com/o/r/raw")] != "https://gitee.com/o/r/raw" {
		t.Errorf("同名应以目录版 URL 为准, got %q", a.DownloadURL)
	}
	if a, ok := byName["NewApp-9.9.9.fpk"]; !ok || a.Version != "9.9.9" {
		t.Errorf("目录新包缺失或版本错误: %+v", a)
	}
	if _, ok := byName["OldOnly-1.0.0.fpk"]; !ok {
		t.Error("release 独有包应保留")
	}
}

// 0.2.40：release 接口挂掉但目录可用 → 不报错，返回目录候选。
func TestDiscoverFromRepoGiteeReleaseDownDirOK(t *testing.T) {
	dir := `[{"type":"file","name":"X-1.2.3.fpk","path":"server-data/apps/X-1.2.3.fpk","download_url":"https://gitee.com/o/r/raw/master/server-data/apps/X-1.2.3.fpk"}]`
	srv := newGiteeMock(t, "", dir)
	defer srv.Close()
	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	got, err := discoverFromRepo(context.Background(), "https://gitee.com/o/r", "", http.DefaultClient)
	if err != nil {
		t.Fatalf("release 挂但目录可用时不应报错: %v", err)
	}
	if len(got) != 1 || got[0].Name != "X-1.2.3.fpk" {
		t.Fatalf("应返回目录候选, got %+v", got)
	}
}

// 0.2.40：release 与目录都不可用 → 保留 release 的原始错误。
func TestDiscoverFromRepoGiteeBothDown(t *testing.T) {
	srv := newGiteeMock(t, "", "")
	defer srv.Close()
	old := giteeAPIBase
	giteeAPIBase = srv.URL
	defer func() { giteeAPIBase = old }()

	if _, err := discoverFromRepo(context.Background(), "https://gitee.com/o/r", "", http.DefaultClient); err == nil {
		t.Fatal("两者都不可用时应返回错误")
	}
}
