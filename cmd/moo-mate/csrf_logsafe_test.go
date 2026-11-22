package main

// 0.2.76（审计 #14 / P2-4）单测：CSRF 拒绝日志的来源归一 + 头部脱敏/截断。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestOriginSiteKeyNormalizes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://Evil.Example:8443/some/path?q=1#f", "https://evil.example:8443"},
		{"http://user:pass@evil.example/x", "http://evil.example"},
		{"null", "null"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := originSiteKey(c.in); got != c.want {
			t.Fatalf("originSiteKey(%q) = %q，want %q", c.in, got, c.want)
		}
	}
}

func TestCSRFOriginKeyUsesSiteOnly(t *testing.T) {
	r := httptest.NewRequest("POST", "http://127.0.0.1:38108/api/sync", nil)
	r.Header.Set("Origin", "https://evil.example/a?b=1")
	if got := csrfOriginKey(r); got != "https://evil.example" {
		t.Fatalf("Origin 应归一为 scheme://host，实得 %q", got)
	}
	r.Header.Del("Origin")
	r.Header.Set("Referer", "https://evil.example/page?x=1")
	if got := csrfOriginKey(r); got != "https://evil.example" {
		t.Fatalf("Referer 应归一为 scheme://host，实得 %q", got)
	}
	r.Header.Del("Referer")
	if got := csrfOriginKey(r); got != "no-origin" {
		t.Fatalf("都缺时应为 no-origin，实得 %q", got)
	}
}

func TestLogHeaderValueRedactsAndTruncates(t *testing.T) {
	long := "https://user:s3cr3t@" + strings.Repeat("h", 400) + ".example/path"
	got := logHeaderValue(long, logHeaderMaxRunes)
	if strings.Contains(got, "s3cr3t") || strings.Contains(got, "user:") {
		t.Fatalf("userinfo 未脱敏：%q", got)
	}
	if n := utf8.RuneCountInString(got); n > logHeaderMaxRunes {
		t.Fatalf("超长头应截到 ≤%d rune，实得 %d", logHeaderMaxRunes, n)
	}
	if !strings.HasPrefix(got, "https://") {
		t.Fatalf("应保留 scheme：%q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长头应带省略号：%q", got)
	}
}

func TestCSRFDenyLogStaysShortAndRedacted(t *testing.T) {
	buf := captureLog(t)
	old := csrfThrottle
	csrfThrottle = newThrottle(10 * time.Minute)
	t.Cleanup(func() { csrfThrottle = old })

	huge := "https://user:s3cr3t@" + strings.Repeat("e", 3000) + ".example/path?token=abc"
	r := httptest.NewRequest("POST", "http://127.0.0.1:38108/api/config", strings.NewReader("{}"))
	r.Header.Set("Origin", huge)
	r.Header.Set("Referer", "https://user:s3cr3t@evil.example/page")
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	csrfDeny(w, r, csrfPolicy{name: "tcp"}, http.StatusForbidden, "测试拒绝")

	if w.Code != http.StatusForbidden {
		t.Fatalf("响应码应保持不变：%d", w.Code)
	}
	out := buf.String()
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Fatalf("同来源应只打 1 行，实得 %d 行：%q", len(lines), out)
	}
	if strings.Contains(out, "s3cr3t") {
		t.Fatalf("日志泄漏了 Origin/Referer 里的凭证：%q", lines[0])
	}
	if n := utf8.RuneCountInString(lines[0]); n > 800 {
		t.Fatalf("日志行被超长头放大：%d rune", n)
	}
	if !strings.Contains(lines[0], "https://") {
		t.Fatalf("归一后的来源应可读：%q", lines[0])
	}
}
