package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestResolveSoftstoreURL(t *testing.T) {
	base := "http://nas.example:5660"
	cases := []struct{ in, want string }{
		{"./cloud-printer/cloud-printer.fpk", base + "/cloud-printer/cloud-printer.fpk"},
		{"usbrsync/usbrsync.fpk", base + "/usbrsync/usbrsync.fpk"},
		{"/abs/x.fpk", base + "/abs/x.fpk"},
		{"https://github.com/o/r/x.fpk", "https://github.com/o/r/x.fpk"},
		{"", ""},
	}
	for _, c := range cases {
		if got := resolveSoftstoreURL(base, c.in); got != c.want {
			t.Errorf("resolveSoftstoreURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDiscoverFromSoftstoreResolvesRelativeURLs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": []map[string]any{{
				"id": "rel-app", "name": "Rel", "version": "1.0.0",
				"platform": "x86", "type": "原生", "size": 10,
				"download_url":  "./rel/rel.fpk",
				"download_urls": []string{"./rel/rel.fpk", "https://github.com/o/r/rel.fpk"},
			}},
		})
	}))
	defer srv.Close()

	apps, err := discoverFromSoftstore(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	a := apps["rel-app"]
	if a == nil {
		t.Fatal("缺 rel-app")
	}
	want := srv.URL + "/rel/rel.fpk"
	if a.DownloadURL != want {
		t.Errorf("顶层 download_url = %q, want %q", a.DownloadURL, want)
	}
	if a.Packages["x86"] == nil || a.Packages["x86"].DownloadURL != want {
		t.Errorf("x86 包 download_url 未补绝对: %+v", a.Packages["x86"])
	}
	if len(a.DownloadURLs) != 1 || a.DownloadURLs[0] != want {
		t.Errorf("候选链未补绝对: %#v", a.DownloadURLs)
	}
}

func TestNormalizeDownloadsMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	ix := newIndex(path)
	if err := ix.upsert(&indexedApp{
		appEntry: appEntry{DownloadURL: "./pkg/a.fpk"},
		Appname:  "a", Source: "http://nas.example:5660", Publisher: "softstore", Synced: true,
		Packages:     map[string]*appPackage{"x86": {DownloadURL: "pkg/a.fpk"}},
		DownloadURLs: []string{"./pkg/a.fpk", "https://github.com/o/r/a.fpk"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := ix.upsert(&indexedApp{
		appEntry: appEntry{DownloadURL: "./keep/relative.fpk"},
		Appname:  "github-app", Source: "https://github.com/o/r", Publisher: "o",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := ix.normalizeDownloads()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("修正条数 = %d, want 1", n)
	}
	a := ix.get("a")
	if a.DownloadURL != "http://nas.example:5660/pkg/a.fpk" {
		t.Errorf("顶层地址未修正: %q", a.DownloadURL)
	}
	if a.Packages["x86"].DownloadURL != "http://nas.example:5660/pkg/a.fpk" {
		t.Errorf("包地址未修正: %q", a.Packages["x86"].DownloadURL)
	}
	if a.DownloadURLs[0] != "http://nas.example:5660/pkg/a.fpk" {
		t.Errorf("候选链未修正: %#v", a.DownloadURLs)
	}
	if got := ix.get("github-app").DownloadURL; got != "./keep/relative.fpk" {
		t.Errorf("非软仓条目不应改动: %q", got)
	}
	if n2, _ := ix.normalizeDownloads(); n2 != 0 {
		t.Errorf("幂等失败，第二次修正 %d 条", n2)
	}

	// 重启后仍保持绝对地址。
	if got := newIndex(path).get("a").DownloadURL; got != "http://nas.example:5660/pkg/a.fpk" {
		t.Errorf("落盘后未保持: %q", got)
	}
}

func TestPruneDeadSoftstoreApps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dead.fpk":
			http.NotFound(w, r)
		case "/alive.fpk":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	for _, a := range []*indexedApp{
		{appEntry: appEntry{DownloadURL: srv.URL + "/dead.fpk"}, Appname: "dead", Source: srv.URL, Publisher: "softstore", Synced: true},
		{appEntry: appEntry{DownloadURL: srv.URL + "/alive.fpk"}, Appname: "alive", Source: srv.URL, Publisher: "softstore", Synced: true},
		{appEntry: appEntry{DownloadURL: "https://github.com/o/r/x.fpk"}, Appname: "external", Source: srv.URL, Publisher: "softstore", Synced: true},
	} {
		a.SyncedName = mateSourceName
		if err := ix.upsert(a); err != nil {
			t.Fatal(err)
		}
	}

	c := &companion{idx: ix, httpc: srv.Client()}
	n, err := c.pruneDeadSoftstoreApps(context.Background(), srv.URL, map[string]*indexedApp{"other": {}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("清理条数 = %d, want 1", n)
	}
	if ix.get("dead").Synced {
		t.Error("dead 应标记为未同步")
	}
	if !ix.get("alive").Synced {
		t.Error("alive 不应被清理")
	}
	if !ix.get("external").Synced {
		t.Error("外链应用不应被探测/清理")
	}
}
