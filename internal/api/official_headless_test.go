package api

// 0.6.254：无头授权 + 面板前端支持探测测试。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/official"
)

func TestExtractOAuthCodeShapes(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
		ok   bool
	}{
		{`{"code":0,"msg":"","data":{"code":"A1B2C3D4E5"}}`, "A1B2C3D4E5", true},
		{`{"code":0,"msg":"","data":{"access_code":" X9Y8Z7 "}}`, "X9Y8Z7", true},
		{`{"code":0,"msg":"","data":{"token_code":"T1T2T3"}}`, "T1T2T3", true},
		{`{"code":0,"msg":"","data":"S0L0C0D3"}`, "S0L0C0D3", true},
		{`{"code":0,"msg":"","data":{"other":"nope"}}`, "", false},
		{`{"code":11001,"msg":"invalid request","data":null}`, "", false},
		{`not json at all`, "", false},
	} {
		code, _, ok := extractOAuthCode([]byte(tc.body))
		if ok != tc.ok || code != tc.want {
			t.Errorf("body=%s: got(%q,%v) want(%q,%v)", tc.body, code, ok, tc.want, tc.ok)
		}
	}
}

// newFakePanelFrontend 模拟面板静态站：/signin + assets（支持懒加载 chunk 二层探测）。
func newFakePanelFrontend(t *testing.T, markerInLazy bool) *httptest.Server {
	t.Helper()
	lazyChunk := "/* lazy */ import('/assets/lazy-abc123.js')"
	if markerInLazy {
		lazyChunk += " code_challenge YJNMPJUGA9"
	}
	plain := "/* top chunk, no marker */"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/signin":
			fmt.Fprint(w, `<html><script src="/assets/index-top.js"></script></html>`)
		case "/assets/index-top.js":
			fmt.Fprint(w, plain+lazyChunk)
		case "/assets/lazy-abc123.js":
			if markerInLazy {
				fmt.Fprint(w, lazyChunk)
			} else {
				fmt.Fprint(w, "/* no marker here either */")
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestProbeUISupport_DirectHit(t *testing.T) {
	ts := newFakePanelFrontend(t, true)
	defer ts.Close()
	if !official.ProbeUISupport(context.Background(), ts.URL) {
		t.Fatal("含 marker 的面板前端应判定为支持")
	}
}

func TestProbeUISupport_NoMarker(t *testing.T) {
	ts := newFakePanelFrontend(t, false)
	defer ts.Close()
	if official.ProbeUISupport(context.Background(), ts.URL) {
		t.Fatal("无 marker 的旧版面板前端应判定为不支持")
	}
}

func TestProbeUISupport_PanelDown(t *testing.T) {
	if official.ProbeUISupport(context.Background(), "http://127.0.0.1:1") {
		t.Fatal("面板不可达应按不支持处理")
	}
}
