package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// hostTestServer 起 mux + companion（hosted 为空 = 模拟「进程刚重启」）。
func hostTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	idx := newIndex(filepath.Join(dir, "index.json"))
	if err := idx.upsert(&indexedApp{
		Appname:   "motrix",
		Source:    "https://github.com/RockerHX/motrix-fnos",
		Publisher: "motrix-fnos",
		appEntry: appEntry{
			DisplayName: "Motrix",
			Version:     "1.9.8",
			Sha256:      "abc",
			DownloadURL: "https://github.com/RockerHX/motrix-fnos/releases/download/v1.9.8/motrix_1.9.8_arm.fpk",
		},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	c := &companion{idx: idx, hosted: map[string][]byte{}, base: "http://127.0.0.1:38108"}
	mux := http.NewServeMux()
	// 与 main.go 同款模式：模式非法会在注册时 panic（回归网）。
	mux.HandleFunc("GET /moo/{appname}", c.handleHost)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// 重启后 hosted map 为空，托管仍须能从持久索引现重建（否则 Moo 周期性
// 再同步已加源会 404）。
func TestHostRebuildsFromIndexAfterRestart(t *testing.T) {
	srv := hostTestServer(t)
	resp, err := http.Get(srv.URL + "/moo/motrix.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"Motrix"`) || !strings.Contains(string(body), "1.9.8") {
		t.Fatalf("托管内容不含预期字段: %s", body)
	}
}

// .json 后缀可选：加源 url 形态 /moo/{app}.json 与直访 /moo/{app} 均可。
func TestHostAcceptsWithAndWithoutJSONSuffix(t *testing.T) {
	srv := hostTestServer(t)
	for _, path := range []string{"/moo/motrix.json", "/moo/motrix"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status = %d", path, resp.StatusCode)
		}
		if !strings.Contains(string(body), "motrix_1.9.8_arm.fpk") {
			t.Fatalf("%s: 内容不含 download_url: %s", path, body)
		}
	}
}

// 索引里没有的应用 404。
func TestHostUnknownApp404(t *testing.T) {
	srv := hostTestServer(t)
	resp, err := http.Get(srv.URL + "/moo/nope.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
