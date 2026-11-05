package main

// MM-02（0.2.52）CSRF 防线单测：跨站/跨源必须拒绝，同源与命令行客户端必须放行。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func csrfTestPolicy(loopbackOnly bool) csrfPolicy {
	return csrfPolicy{
		name:           "test",
		loopbackOnly:   loopbackOnly,
		allowedHosts:   []string{"127.0.0.1:38108"},
		multipartPaths: map[string]bool{"/api/backup/restore-upload": true},
	}
}

func csrfTestHandler(p csrfPolicy) (http.Handler, *int32) {
	var hits int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return csrfGuard(next, p), &hits
}

func csrfReq(method, path, body string) *http.Request {
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	var r *http.Request
	if rd != nil {
		r = httptest.NewRequest(method, "http://127.0.0.1:38108"+path, rd)
	} else {
		r = httptest.NewRequest(method, "http://127.0.0.1:38108"+path, nil)
	}
	r.Host = "127.0.0.1:38108"
	return r
}

func csrfDo(t *testing.T, h http.Handler, r *http.Request) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestCSRFBlocksCrossSitePost(t *testing.T) {
	h, hits := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", "http://evil.example")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("跨站 POST 应 403，得 %d", code)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("被拒请求不应到达 handler")
	}
}

func TestCSRFBlocksForeignOriginWithoutSecFetch(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/datadir", `{"path":"/vol9"}`)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://evil.example")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("跨源 Origin 应 403，得 %d", code)
	}
}

func TestCSRFBlocksNullOrigin(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "null") // data: / 沙箱 iframe
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("Origin: null 应 403，得 %d", code)
	}
}

func TestCSRFBlocksTextPlainBody(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", `{}`)
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r); code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain 体应 415，得 %d", code)
	}
}

func TestCSRFAllowsSameOriginJSON(t *testing.T) {
	h, hits := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPut, "/api/config", `{"log_lines":200}`)
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	r.Header.Set("Origin", "http://127.0.0.1:38108")
	if code := csrfDo(t, h, r); code != http.StatusOK || atomic.LoadInt32(hits) != 1 {
		t.Fatalf("同源 JSON 应放行，得 %d hits=%d", code, atomic.LoadInt32(hits))
	}
}

func TestCSRFAllowsNonBrowserClient(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	// curl / 脚本：既无 Sec-Fetch-* 也无 Origin。
	r := csrfReq(http.MethodPost, "/api/softstore-sync", `{}`)
	r.Header.Set("Content-Type", "application/json")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("无 Origin 的 JSON POST 应放行，得 %d", code)
	}
	empty := csrfReq(http.MethodPost, "/api/logs/refresh", "")
	if code := csrfDo(t, h, empty); code != http.StatusOK {
		t.Fatalf("空体 POST 应放行，得 %d", code)
	}
}
