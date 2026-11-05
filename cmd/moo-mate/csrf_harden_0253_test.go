package main

// 0.2.53（A1/A2/A3）纯收益加固单测：
//   A1 Host 白名单覆盖读方法（DNS rebinding 可读接口）
//   A2 统一请求体上限（默认 1MB，上传备份 256MB）
//   A3 HTTP 服务器基础超时 + 头大小

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func readAllHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(status)
	})
}

// A1：读方法也必须过 Host 白名单。修复前 GET 直接 return，恶意 Host 可读全部只读接口。
func TestHostGuardAppliesToSafeMethods(t *testing.T) {
	h, hits := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodGet, "/api/index", "")
	r.Host = "evil.example:38108"
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("恶意 Host 的 GET 应 403，得 %d", code)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatal("被拒请求不应到达 handler")
	}
}

func TestHostGuardAllowsLoopbackRead(t *testing.T) {
	h, hits := csrfTestHandler(csrfTestPolicy(true))
	for _, host := range []string{"127.0.0.1:38108", "localhost:38108", "[::1]:38108"} {
		r := csrfReq(http.MethodGet, "/api/index", "")
		r.Host = host
		if code := csrfDo(t, h, r); code != http.StatusOK {
			t.Fatalf("回环 Host %s 的 GET 应放行，得 %d", host, code)
		}
	}
	if atomic.LoadInt32(hits) != 3 {
		t.Fatalf("三条回环请求都应到达 handler，得 %d", atomic.LoadInt32(hits))
	}
}

// 网关通道（面板 nginx 决定 Host）不能限制 Host，否则面板入口会被打断。
func TestGatewayPolicyDoesNotRestrictHost(t *testing.T) {
	p := csrfTestPolicy(false)
	h := mustHandler(p)
	r := csrfReq(http.MethodGet, "/api/index", "")
	r.Host = "nas.example.com"
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("网关通道 GET 不应因 Host 被拒，得 %d", code)
	}
	r2 := csrfReq(http.MethodPut, "/api/config", `{"log_lines":200}`)
	r2.Host = "nas.example.com"
	r2.Header.Set("Content-Type", "application/json")
	r2.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r2); code != http.StatusOK {
		t.Fatalf("网关通道同源写不应因 Host 被拒，得 %d", code)
	}
}

func mustHandler(p csrfPolicy) http.Handler {
	h, _ := csrfTestHandler(p)
	return h
}

// A2：默认路径超过 1MB 必须被截断（handler 读体报错 → 413）。
func TestBodyLimitRejectsOversizeJSON(t *testing.T) {
	tcp, _ := newCSRFPolicies("127.0.0.1:38108", "http://127.0.0.1:38108")
	h := csrfGuard(readAllHandler(http.StatusOK), tcp)
	body := `{"url":"` + strings.Repeat("x", 2<<20) + `"}`
	r := csrfReq(http.MethodPost, "/api/probe", body)
	r.Header.Set("Content-Type", "application/json")
	if code := csrfDo(t, h, r); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("2MB 请求体应被上限拒绝，得 %d", code)
	}
}

func TestBodyLimitAllowsNormalJSON(t *testing.T) {
	tcp, _ := newCSRFPolicies("127.0.0.1:38108", "http://127.0.0.1:38108")
	h := csrfGuard(readAllHandler(http.StatusOK), tcp)
	body := `{"url":"` + strings.Repeat("x", 64<<10) + `"}`
	r := csrfReq(http.MethodPost, "/api/probe", body)
	r.Header.Set("Content-Type", "application/json")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("64KB 请求体应放行，得 %d", code)
	}
}

// 上传备份路径保持 256MB，2MB 正常通过。
func TestBodyLimitAllowsUploadPath(t *testing.T) {
	p := csrfTestPolicy(true)
	p.bodyLimits = map[string]int64{"/api/backup/restore-upload": maxUploadBodyBytes}
	h := csrfGuard(readAllHandler(http.StatusOK), p)
	body := strings.Repeat("y", 2<<20)
	r := csrfReq(http.MethodPost, "/api/backup/restore-upload", body)
	r.Header.Set("Content-Type", "multipart/form-data; boundary=t")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("上传路径 2MB 应放行，得 %d", code)
	}
}

// 生产策略必须把两项都配上（防止以后改 newCSRFPolicies 漏掉）。
func TestNewCSRFPoliciesSetBodyLimits(t *testing.T) {
	tcp, gw := newCSRFPolicies("127.0.0.1:38108", "http://127.0.0.1:38108")
	for _, p := range []csrfPolicy{tcp, gw} {
		if p.maxBodyBytes != maxAPIBodyBytes {
			t.Fatalf("%s 通道默认体上限应为 1MB，得 %d", p.name, p.maxBodyBytes)
		}
		if p.bodyLimits["/api/backup/restore-upload"] != maxUploadBodyBytes {
			t.Fatalf("%s 通道上传上限应为 256MB，得 %d", p.name, p.bodyLimits["/api/backup/restore-upload"])
		}
	}
	if !tcp.loopbackOnly || gw.loopbackOnly {
		t.Fatal("TCP 通道应限制 Host，网关通道不应限制")
	}
}

// A3：基础防护参数；不设 WriteTimeout（长响应不能被误杀）。
func TestNewHTTPServerHardening(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout != httpReadHeaderTimeout || srv.ReadHeaderTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout 应为 %v，得 %v", httpReadHeaderTimeout, srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != httpIdleTimeout || srv.IdleTimeout <= 0 {
		t.Fatalf("IdleTimeout 应为 %v，得 %v", httpIdleTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("MaxHeaderBytes 应为 %d，得 %d", httpMaxHeaderBytes, srv.MaxHeaderBytes)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("不得设置 WriteTimeout（会打断长响应），得 %v", srv.WriteTimeout)
	}
	_ = httptest.NewRecorder() // 保持 import 一致性
}
