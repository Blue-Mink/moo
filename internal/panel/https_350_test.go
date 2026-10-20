package panel

// 0.6.349 WS2：https 回环自签证书放宽校验的端到端实证（本地 https stub）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHTTPClientForBaseTLSSelfSigned_350：HTTPClientForBase 对 https+回环
// 放行自签证书；普通 client 因证书校验失败报错（证明放宽确实生效）。
func TestHTTPClientForBaseTLSSelfSigned_350(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	c := HTTPClientForBase(srv.URL, 5*time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("回环自签证书应被放行: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码异常: %d", resp.StatusCode)
	}

	// 负向对照：普通 client 不跳过证书校验 → 必须报错
	if _, err := http.DefaultClient.Get(srv.URL); err == nil {
		t.Fatal("普通 client 不应通过自签证书校验（若通过说明证书非自签，测试失真）")
	}
}
