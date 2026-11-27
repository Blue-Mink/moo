package source

import (
	"errors"
	"strings"
	"testing"
)

// P1-3：/api/sources 的 last_error 不得泄露凭据与 query（源地址里可能带
// userinfo 或 token），且要是人话、要截断（界面只放 40 字 + title 全文）。
func TestSanitizeSourceError(t *testing.T) {
	raw := "所有候选地址均失败: https://user:s3cr3t@mirror.example:8443/o/r/main/moo.json?token=abc123&sig=zz: HTTP 502"
	got := sanitizeSourceError(raw)
	for _, bad := range []string{"s3cr3t", "user:", "token=abc123", "?token", "sig=zz"} {
		if strings.Contains(got, bad) {
			t.Fatalf("脱敏后仍含 %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "https://mirror.example:8443/") {
		t.Fatalf("host 与路径应保留（用户要能认出是哪个通道）: %s", got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "  ") {
		t.Fatalf("空白应压平: %q", got)
	}
	// 超长输入按 rune 截断，且不会被砍成半个字符
	long := strings.Repeat("错", 400)
	if n := len([]rune(sanitizeSourceError(long))); n > 201 {
		t.Fatalf("应截断（≤200 rune + 省略号）, got %d", n)
	}
}

// describeFetchError：把失败压成一句人话（判定不看它，看 probe 结论）。
func TestDescribeFetchError(t *testing.T) {
	cases := map[string]string{
		"所有候选地址均失败: x: HTTP 404":                     "仓库不存在（HTTP 404）",
		"所有候选地址均失败: x: 404 Not Found":                "仓库不存在（HTTP 404）",
		"所有候选地址均失败: x: HTTP 410":                     "源地址已失效（HTTP 410）",
		"所有候选地址均失败: x: HTTP 429":                     "被镜像限流（HTTP 429），稍后自动重试",
		"所有候选地址均失败: x: HTTP 503":                     "源站服务端错误（HTTP 5xx），稍后自动重试",
		"所有候选地址均失败: x: context deadline exceeded":    "连接超时",
		"所有候选地址均失败: x: tls: handshake failure":       "TLS/证书错误",
		"所有候选地址均失败: x: dial tcp: connection refused": "网络不可达",
	}
	for in, want := range cases {
		if got := describeFetchError(errors.New(in)); got != want {
			t.Fatalf("%q → %q, want %q", in, got, want)
		}
	}
	if got := describeFetchError(nil); got != "" {
		t.Fatalf("nil 错误应为空串, got %q", got)
	}
}
