package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 0.2.77：自托管 README 地址必须按路径段转义（应用名可含空格 / 中文 / 斜杠）。
func TestReadmeSelfURL(t *testing.T) {
	const base = "http://127.0.0.1:38108"
	cases := []struct{ app, want string }{
		{"demo", base + "/moo/demo/README.md"},
		{"my app", base + "/moo/my%20app/README.md"},
		{"a/b", base + "/moo/a%2Fb/README.md"},
	}
	for _, cs := range cases {
		got := readmeSelfURL(base, cs.app)
		if got != cs.want {
			t.Fatalf("readmeSelfURL(%q) = %q, want %q", cs.app, got, cs.want)
		}
		// 斜杠未转义会多切出路径段：3 个分隔符 + scheme 的 2 个 = 5。
		if n := strings.Count(got, "/"); n != 5 {
			t.Fatalf("应用名 %q 产生了多余路径段：%q", cs.app, got)
		}
	}
	got := readmeSelfURL(base, "中文应用")
	if strings.Contains(got, "中文") {
		t.Fatalf("中文未转义：%q", got)
	}
	u, err := url.Parse(got)
	if err != nil || u.Path != "/moo/中文应用/README.md" {
		t.Fatalf("转义后不可回解：%q err=%v path=%q", got, err, u.Path)
	}
}

func newReadmeTestCompanion(t *testing.T, src string) *companion {
	t.Helper()
	dir := t.TempDir()
	ix := newIndex(filepath.Join(dir, "index.json"))
	ix.doc.Apps["demo"] = &indexedApp{Appname: "demo", Source: src}
	return &companion{idx: ix, bootDir: dir, base: "http://127.0.0.1:38108"}
}

func getReadme(c *companion) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c.serveReadme(rec, httptest.NewRequest(http.MethodGet, "/moo/demo/README.md", nil), "demo")
	return rec
}

// 上游失败 → 负缓存命中期间不得再打上游；TTL 过期后重新尝试；成功即清缓存。
func TestServeReadmeNegativeCacheSuppressesUpstream(t *testing.T) {
	var calls int32
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("# Demo\n"))
	}))
	defer srv.Close()
	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()

	c := newReadmeTestCompanion(t, "https://github.com/acme/demo")
	c.httpc = srv.Client()

	if rec := getReadme(c); rec.Code != http.StatusNotFound {
		t.Fatalf("首次拉取失败应 404：%d", rec.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("首次应请求一次上游：%d", n)
	}
	if rec := getReadme(c); rec.Code != http.StatusNotFound {
		t.Fatalf("负缓存命中应 404：%d", rec.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("负缓存命中不得再打上游，实际 %d 次", n)
	}

	c.readmeNeg.mu.Lock()
	c.readmeNeg.at["demo"] = time.Now().Add(-readmeNegTTL - time.Second)
	c.readmeNeg.mu.Unlock()
	fail.Store(false)

	rec := getReadme(c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "# Demo") {
		t.Fatalf("TTL 过期后应重新拉取成功：%d %q", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("TTL 过期后应重试一次：%d", n)
	}
	if c.readmeNeg.hit("demo") {
		t.Fatalf("拉取成功后应清除负缓存")
	}
	if rec := getReadme(c); rec.Code != http.StatusOK {
		t.Fatalf("已落盘应 200：%d", rec.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("新鲜副本不得再打上游：%d", n)
	}
}

// 上游失败但有旧副本：负缓存命中时仍出旧副本（不能退化成 404），且不打上游。
func TestServeReadmeNegativeCacheKeepsStaleCopy(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	old := ghAPIBase
	ghAPIBase = srv.URL
	defer func() { ghAPIBase = old }()

	c := newReadmeTestCompanion(t, "https://github.com/acme/demo")
	c.httpc = srv.Client()
	if err := c.saveReadme("demo", []byte("# Old\n"), "README.md"); err != nil {
		t.Fatalf("saveReadme: %v", err)
	}
	stale := time.Now().Add(-readmeTTL - time.Hour)
	if err := os.Chtimes(c.readmePath("demo"), stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	for i := 0; i < 2; i++ {
		rec := getReadme(c)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "# Old") {
			t.Fatalf("第 %d 次请求应回落旧副本：%d %q", i+1, rec.Code, rec.Body.String())
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("旧副本可用时失败也只应打一次上游，实际 %d 次", n)
	}
}
