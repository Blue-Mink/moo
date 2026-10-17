package api

// fpk_icon_sf_test.go —— 0.6.326 终测低风险项定向单测（2026-10-17）
//
//	#3 fpkIconFallback 跨请求 single-flight：同一应用并发 N 次只应下 1 次包；
//	#4 README 响应 Content-Type 归一到 text/markdown（上游 raw 给 text/plain）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/source"
)

// TestFpkIconFallbackSingleFlight_326LF 同一应用 8 个并发请求只发 1 次下载。
func TestFpkIconFallbackSingleFlight_326LF(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(150 * time.Millisecond)     // 拉住在飞窗口，保证并发调用都赶上
		_, _ = io.WriteString(w, "not-an-fpk") // 内容无效：解包必失败，只验下载次数
	}))
	defer ts.Close()

	s := &Server{}
	a := &source.App{
		Source:      "sf-src",
		Name:        "sf-app",
		Version:     "9.9.9",
		IconURL:     "http://example.invalid/icon.png",
		DownloadURL: ts.URL,
		SizeBytes:   1024,
	}

	const n = 8
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		oks []bool
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, ok := s.fpkIconFallback(context.Background(), a)
			mu.Lock()
			oks = append(oks, ok)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("并发 %d 次应只下 1 次包，实测 %d 次", n, got)
	}
	for i, ok := range oks {
		if ok {
			t.Fatalf("第 %d 次调用期望失败（假包无图标），却报成功", i)
		}
	}
}

// TestNormalizeReadmeCT_326LF 上游 text/plain 归一为 markdown；html 保持原样。
func TestNormalizeReadmeCT_326LF(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "text/markdown; charset=utf-8"},
		{"text/plain", "text/markdown; charset=utf-8"},
		{"text/plain; charset=utf-8", "text/markdown; charset=utf-8"},
		{"application/octet-stream", "text/markdown; charset=utf-8"},
		{"text/html; charset=utf-8", "text/html; charset=utf-8"},
		{"text/markdown; charset=utf-8", "text/markdown; charset=utf-8"},
	}
	for _, c := range cases {
		if got := normalizeReadmeCT(c.in); got != c.want {
			t.Errorf("normalizeReadmeCT(%q)=%q want %q", c.in, got, c.want)
		}
	}
	// serveReadme 端到端：body 原样、头已归一。
	rec := httptest.NewRecorder()
	serveReadme(rec, []byte("# t"), "text/plain", "")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("serveReadme Content-Type=%q，期望 text/markdown", ct)
	}
	if rec.Body.String() != "# t" {
		t.Fatalf("body 被改动：%q", rec.Body.String())
	}
}
