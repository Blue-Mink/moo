package api

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

func TestEtagMatch(t *testing.T) {
	etag := `"abc123"`
	cases := []struct {
		in   string
		want bool
	}{
		{etag, true},
		{`W/"abc123"`, true},
		{`W/"other", "abc123"`, true},
		{`"other"`, false},
		{"", false},
		{"*", true},
	}
	for _, c := range cases {
		if got := etagMatch(c.in, etag); got != c.want {
			t.Errorf("etagMatch(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestGzipSniffWriter(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil).WithContext(t.Context())
	r.Header.Set("Accept-Encoding", "gzip")
	withGzip(next).ServeHTTP(rec, r)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("json 应 gzip, got %q", rec.Header().Get("Content-Encoding"))
	}
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip 解码失败: %v", err)
	}
	out, _ := io.ReadAll(gr)
	if string(out) != `{"hello":"world"}` {
		t.Errorf("gzip 内容 = %q", out)
	}

	// 二进制直通
	rec2 := httptest.NewRecorder()
	next2 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	r2 := httptest.NewRequest("GET", "/", nil).WithContext(t.Context())
	r2.Header.Set("Accept-Encoding", "gzip")
	withGzip(next2).ServeHTTP(rec2, r2)
	if rec2.Header().Get("Content-Encoding") != "" {
		t.Errorf("png 不应 gzip, got %q", rec2.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(rec2.Body.Bytes(), []byte{0x89, 'P', 'N', 'G'}) {
		t.Errorf("png 内容被改写: %v", rec2.Body.Bytes())
	}

	// SSE 直通：text/event-stream 即使声明 gzip 也不得压缩
	rec3 := httptest.NewRecorder()
	next3 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
	})
	r3 := httptest.NewRequest("GET", "/", nil).WithContext(t.Context())
	r3.Header.Set("Accept-Encoding", "gzip")
	withGzip(next3).ServeHTTP(rec3, r3)
	if rec3.Header().Get("Content-Encoding") != "" {
		t.Errorf("SSE 不应 gzip, got %q", rec3.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(rec3.Body.String(), "event: ping") {
		t.Errorf("SSE 内容被改写: %q", rec3.Body.String())
	}
}

// TestAppsWithEtag304 列表 ETag：首次 200+ETag，重校验 304。
func TestAppsWithEtag304(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{Src: source.NewManager(cfg), Cfg: cfg}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context())
	s.appsWithEtag(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次应 200, got %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("首次响应应带 ETag")
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("应带 Cache-Control: no-cache, got %q", rec.Header().Get("Cache-Control"))
	}
	if !strings.Contains(rec.Body.String(), `"apps"`) {
		t.Errorf("响应体应是 apps 列表: %s", rec.Body.String()[:100])
	}

	rec2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context())
	r2.Header.Set("If-None-Match", etag)
	s.appsWithEtag(rec2, r2)
	if rec2.Code != http.StatusNotModified {
		t.Errorf("目录未变应 304, got %d", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 不应带响应体, got %d 字节", rec2.Body.Len())
	}
}
