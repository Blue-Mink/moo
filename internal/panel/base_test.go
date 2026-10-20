package panel

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// resetBase 清空缓存并复位可变全局，测试结束自动还原。
func resetBase(t *testing.T) {
	t.Helper()
	InvalidateBaseCache()
	oldConf, oldEnv, oldGateway := gatewayConf, baseEnvName, confEnvName
	t.Cleanup(func() {
		gatewayConf, baseEnvName, confEnvName = oldConf, oldEnv, oldGateway
		InvalidateBaseCache()
	})
}

func writeConf(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gw.conf")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gatewayConf = p
	return p
}

func listenLocal(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// env 覆盖优先于一切；非回环被拒绝。
func TestResolveBaseEnv(t *testing.T) {
	resetBase(t)
	t.Setenv("MOO_PANEL_BASE", "http://127.0.0.1:18080")
	if got := ResolveBaseCached(""); got.URL != "http://127.0.0.1:18080" || got.Source != baseEnvName {
		t.Fatalf("env 覆盖失败: %+v", got)
	}

	t.Setenv("MOO_PANEL_BASE", "http://192.0.2.9:5666")
	writeConf(t, `{"schema":{"http":{"port":5666}}}`)
	if got := ResolveBaseCached(""); got.URL != DefaultBaseURL {
		t.Fatalf("非回环 env 应被忽略，得到 %+v", got)
	}
}

// 显式地址（存量配置）：回环接受，非回环拒绝。
func TestResolveBaseExplicit(t *testing.T) {
	resetBase(t)
	if got := ResolveBaseCached("http://127.0.0.1:18081/"); got.URL != "http://127.0.0.1:18081" || got.Source != "explicit" {
		t.Fatalf("显式回环地址失败: %+v", got)
	}
	writeConf(t, `{"schema":{"http":{"port":5666}}}`)
	if got := ResolveBaseCached("https://nas.example.com:5667"); got.URL != DefaultBaseURL {
		t.Fatalf("非回环显式地址应被忽略，得到 %+v", got)
	}
}

// 网关配置端口（自定义端口场景，issue moo#7）。
func TestResolveBaseGatewayConf(t *testing.T) {
	resetBase(t)
	port := listenLocal(t)
	writeConf(t, `{"schema":{"http":{"port":`+strconv.Itoa(port)+`},"https":{"port":5667}},"force_https":false}`)
	got := ResolveBaseCached("")
	if got.URL != baseFromPort(port, false) || got.Source != "gateway-conf" {
		t.Fatalf("网关配置端口未生效: %+v", got)
	}
}

// force_https → 用 https 端口。
func TestResolveBaseForceHTTPS(t *testing.T) {
	resetBase(t)
	port := listenLocal(t)
	writeConf(t, `{"schema":{"http":{"port":5666},"https":{"port":`+strconv.Itoa(port)+`}},"force_https":true}`)
	got := ResolveBaseCached("")
	if got.URL != baseFromPort(port, true) {
		t.Fatalf("force_https 未走 https: %+v", got)
	}
}

// 配置损坏 → 不 panic，回落探测/默认。
func TestResolveBaseBrokenConf(t *testing.T) {
	resetBase(t)
	writeConf(t, `{not json`)
	if got := ResolveBaseCached(""); got.Source != "default" && got.Source != "probe" {
		t.Fatalf("坏配置应回落: %+v", got)
	}
}

// 缓存：配置变更在 TTL 内不生效，Invalidate 后立即生效。
func TestResolveBaseCache(t *testing.T) {
	resetBase(t)
	p1 := listenLocal(t)
	writeConf(t, `{"schema":{"http":{"port":`+strconv.Itoa(p1)+`}}}`)
	first := ResolveBaseCached("")
	p2 := listenLocal(t)
	writeConf(t, `{"schema":{"http":{"port":`+strconv.Itoa(p2)+`}}}`)
	if got := ResolveBaseCached(""); got.URL != first.URL {
		t.Fatalf("TTL 内不应变化: %+v", got)
	}
	InvalidateBaseCache()
	if got := ResolveBaseCached(""); got.URL != baseFromPort(p2, false) {
		t.Fatalf("失效后应重解析: %+v", got)
	}
}

// https 回环客户端放宽证书校验；http 不设 TLS 配置。
func TestHTTPClientForBase(t *testing.T) {
	c := HTTPClientForBase("https://127.0.0.1:5667", time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("https 回环应放宽证书校验")
	}
	if c2 := HTTPClientForBase("http://127.0.0.1:5666", time.Second); c2.Transport != nil {
		if tr2, ok := c2.Transport.(*http.Transport); ok && tr2.TLSClientConfig != nil {
			t.Fatal("http 不应设置 TLS 配置")
		}
	}
}
