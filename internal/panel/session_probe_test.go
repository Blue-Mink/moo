package panel

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDumpSession 临时探针：完整登录链 + ost cookie 有效期 + app-center 目录调用。
// 用于评估「会话复用」改造（0.6.253 候选）：登录一次后 cookie 能用多久。
// 探针性质：无面板（CI/干净机）或假凭据登录失败 → skip，不阻塞 go test 全绿（Release 门依赖）。
func TestDumpSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 探针用假凭据（真实凭据勿入库，安全红线）；本地实跑请临时替换
	c := NewClient("http://127.0.0.1:5666", "probe-user", "probe-fake-password")

	ticket, err := wsLogin(ctx, c.BaseURL, c.Username, c.Password)
	if err != nil {
		t.Skipf("探针跳过（无面板或登录失败，非回归）: %v", err)
	}
	t.Logf("ticket=%s", ticket)

	// 直接看 /app/ticket 的 Set-Cookie 原始头
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/app/ticket",
		strings.NewReader(`{"ticket":"`+ticket+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ticket exchange: %v", err)
	}
	defer resp.Body.Close()
	t.Logf("exchange status=%d", resp.StatusCode)
	for _, ck := range resp.Header["Set-Cookie"] {
		t.Logf("Set-Cookie: %s", ck)
	}
	cookie := ""
	if ck := resp.Cookies(); len(ck) > 0 {
		cookie = ck[0].Name + "=" + ck[0].Value
	}
	if cookie == "" {
		t.Fatalf("no cookie")
	}

	// 用 ost cookie 调 app-center 目录
	req2, _ := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/app-center/v1/app/list", nil)
	req2.Header.Set("Cookie", cookie)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("app/list: %v", err)
	}
	defer resp2.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp2.Body.Read(buf)
	t.Logf("app/list status=%d body_head=%s", resp2.StatusCode, buf[:min(n, 200)])

	// 3) 只用 osrt 刷新 token（不带 ost）→ 能否换新 ost（免账号续期）？
	osrt := ""
	for _, ck := range resp.Cookies() {
		if ck.Name == "osrt" {
			osrt = ck.Name + "=" + ck.Value
		}
	}
	req3, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/app/ticket", strings.NewReader(`{}`))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("Cookie", osrt)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("osrt refresh: %v", err)
	}
	defer resp3.Body.Close()
	t.Logf("osrt-only /app/ticket status=%d", resp3.StatusCode)
	for _, ck := range resp3.Cookies() {
		t.Logf("refresh Set-Cookie: %s =%s (maxage=%d)", ck.Name, ck.Value[:min(len(ck.Value), 12)], ck.MaxAge)
	}
}
