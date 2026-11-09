package main

// MM-02：Host / 网关 / 回环别名等边界用例（与 csrf_test.go 同属一道防线）。

import (
	"net/http"
	"testing"
)

func TestCSRFAllowsMultipartUpload(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/backup/restore-upload", "----boundary--")
	r.Header.Set("Content-Type", "multipart/form-data; boundary=--boundary")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("multipart 上传应放行，得 %d", code)
	}
}

func TestCSRFBlocksMultipartOutsideWhitelist(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "----boundary--")
	r.Header.Set("Content-Type", "multipart/form-data; boundary=--boundary")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r); code != http.StatusUnsupportedMediaType {
		t.Fatalf("非白名单 multipart 应 415，得 %d", code)
	}
}

func TestCSRFRejectsForeignHostOnLoopbackChannel(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "evil.example:38108" // DNS rebinding
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("非回环 Host 应 403，得 %d", code)
	}
}

func TestCSRFGatewayChannelAllowsPanelOrigin(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(false)) // 网关通道不卡 Host
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "127.0.0.1:5666"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://192.0.2.15:5666")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("面板同源请求应放行，得 %d", code)
	}
}

// 0.2.57：面板 nginx 转发会剥掉 Host 的端口（实测 Host=192.0.2.15，
// Origin=http://192.0.2.15:5666），此时应视为同源放行，否则面板内写操作全 403。
func TestCSRFGatewayAllowsOriginPortWhenHostHasNoPort(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(false))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "192.0.2.15"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://192.0.2.15:5666")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("网关剥端口时应放行同主机 Origin，得 %d", code)
	}
}

// 反例：主机名不同必须仍然 403。
func TestCSRFGatewayBlocksDifferentHostWithPort(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(false))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "192.0.2.15"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://evil.example:5666")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("不同主机必须 403，得 %d", code)
	}
}

func TestCSRFGatewayChannelBlocksCrossSite(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(false))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "127.0.0.1:5666"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("网关跨站应 403，得 %d", code)
	}
}

func TestCSRFBlocksForeignReferer(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Referer", "http://evil.example/page")
	if code := csrfDo(t, h, r); code != http.StatusForbidden {
		t.Fatalf("跨源 Referer 应 403，得 %d", code)
	}
}

func TestCSRFAllowsLoopbackAlias(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Host = "localhost:38108"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:38108")
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("回环别名应视为同源，得 %d", code)
	}
}

func TestCSRFSafeMethodsPassThrough(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))
	r := csrfReq(http.MethodGet, "/api/status", "")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", "http://evil.example")
	// 0.2.53（A1）：Host 校验已覆盖读方法，故这里用回环 Host ——
	// 本条只断言「读方法不做 CSRF 判据」；恶意 Host 的读请求由
	// TestHostGuardAppliesToSafeMethods 覆盖。
	r.Host = "127.0.0.1:38108"
	if code := csrfDo(t, h, r); code != http.StatusOK {
		t.Fatalf("GET 应放行（只读，不变更状态），得 %d", code)
	}
}
