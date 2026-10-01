// Package netx：「科学加速」出网代理（0.6.206，加速源设置末卡片）。
//
// 启用后仅把发往 GitHub 域名（github.com 及其子域：raw/codeload/object
// user-content、githubassets 等）的流量改道本机代理；国内镜像域名
// （gh-proxy 等）与其余流量继续直连——代理挂掉不会打断镜像加速，
// GitHub 直连候选走代理失败时由上层既有镜像回退链接管。
//
// 支持 http/https/socks4/socks5（如 socks5://127.0.0.1:1080）。
// 配置为进程级全局：SetProxy 后所有 netx 客户端即时生效，无需重建。
package netx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

var (
	mu     sync.RWMutex
	proxyU string

	transMu sync.Mutex
	trans   = map[string]*http.Transport{}
)

// SetProxy 设置代理地址（"" = 关闭）。
func SetProxy(u string) {
	mu.Lock()
	proxyU = strings.TrimSpace(u)
	mu.Unlock()
}

// ProxyURL 返回当前生效的代理地址（"" = 关闭）。
func ProxyURL() string {
	mu.RLock()
	defer mu.RUnlock()
	return proxyU
}

// isGitHubHost 判断目标是否 GitHub 域名（仅这些走代理）。
func isGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "github.com" ||
		host == "github.dev" || // 裸域名（开发者门户 CNAME）
		host == "githubassets.com" || // 裸域名（资产 CDN 兜底）
		strings.HasSuffix(host, ".github.com") ||
		strings.HasSuffix(host, ".githubusercontent.com") ||
		strings.HasSuffix(host, ".github.dev") ||
		strings.HasSuffix(host, ".githubassets.com")
}

func directDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}

// baseTransport 基础 transport（调优与 http.DefaultTransport 对齐）。
func baseTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           directDialer().DialContext,
	}
}

// transportFor 取指定代理地址对应的 transport（每地址一份缓存，连接池复用）。
// 地址非法时回退直连 transport，不炸主流程。
func transportFor(u string) *http.Transport {
	if u == "" {
		return http.DefaultTransport.(*http.Transport)
	}
	transMu.Lock()
	defer transMu.Unlock()
	if t, ok := trans[u]; ok {
		return t
	}
	t := baseTransport()
	pu, perr := url.Parse(u)
	ok := false
	if perr == nil && pu.Host != "" {
		switch pu.Scheme {
		case "http", "https":
			// 仅 GitHub 域名走代理 CONNECT；其余直连。
			t.Proxy = func(req *http.Request) (*url.URL, error) {
				if isGitHubHost(req.URL.Hostname()) {
					return pu, nil
				}
				return nil, nil
			}
			ok = true
		case "socks4", "socks5", "socks5h": // socks5h = 远端 DNS 解析（Clash 常用）
			dialer, derr := proxy.FromURL(pu, proxy.Direct)
			if derr == nil {
				direct := directDialer()
				t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					host, _, serr := net.SplitHostPort(addr)
					if serr == nil && isGitHubHost(host) {
						return dialer.Dial(network, addr)
					}
					return direct.DialContext(ctx, network, addr)
				}
				ok = true
			}
		}
	}
	if !ok {
		t = http.DefaultTransport.(*http.Transport)
	} else {
		trans[u] = t
	}
	return t
}

// dynamicTransport 请求时按当前代理配置选 transport：
// 保存新代理后无需重建 client 即生效。
type dynamicTransport struct{}

func (dynamicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transportFor(ProxyURL()).RoundTrip(req)
}

// NewClient 返回带「科学加速」代理的 HTTP client（GitHub 域名走代理、
// 其余直连；代理配置变更即时生效）。timeout<=0 表示不设整体超时
// （由调用方 context 控制）。
func NewClient(timeout time.Duration) *http.Client {
	c := &http.Client{Transport: dynamicTransport{}}
	if timeout > 0 {
		c.Timeout = timeout
	}
	return c
}

// Validate 校验代理地址（scheme 须为 http/https/socks4/socks5 且带 host）。
func Validate(u string) error {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil || pu.Host == "" {
		return errors.New("代理地址不合法")
	}
	switch pu.Scheme {
	case "http", "https", "socks4", "socks5", "socks5h":
		return nil
	}
	return errors.New("仅支持 http/https/socks4/socks5/socks5h 代理")
}

// Test 经指定代理探测 https://github.com（HEAD，独立于当前保存的配置），
// 返回 (耗时, 错误)。
func Test(u string, timeout time.Duration) (time.Duration, error) {
	u = strings.TrimSpace(u)
	c := &http.Client{Timeout: timeout, Transport: transportFor(u)}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, "https://github.com", nil)
	if err != nil {
		return 0, err
	}
	t0 := time.Now()
	resp, err := c.Do(req)
	lat := time.Since(t0)
	if err != nil {
		return lat, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return lat, errors.New("github.com 返回 " + resp.Status)
	}
	return lat, nil
}
