package main

// 本机加速复用（2026-10-31 用户令：Dog-dev 的加速也要给 Mate 用上）。
//
// Dog-dev 在本机提供一个 GitHub 加速代理（默认 0.0.0.0:37710，HTTP 正向代理，
// 自带镜像轮换、hosts 优选与直连熔断）。Mate 此前只有「Moo 下发的加速前缀」，
// 直连 GitHub 的请求（release 资产、raw 索引、api）在国内经常 0 字节。
//
// 这里给 Mate 的外部请求加一条「本机代理通道」：
//   - 只对 GitHub 系域名走代理，其它域名（FnDepot、自建源）保持直连，
//     不给用户的环境增加未知跳数；
//   - 回环地址一律直连（Moo API、appcenter socket 不能被代理）；
//   - 探测结果缓存 5 分钟：Dog-dev 没装/没运行/装了没开代理时行为与旧版一致；
//   - 用户可用 MOO_MATE_NO_ACCEL=1 显式关闭（排障逃生门）。

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	accelMu  sync.Mutex
	accelURL *url.URL
	accelAt  time.Time

	// 可被单测替换的探测函数（真实探测依赖本机 Dog-dev）。
	accelProxyProvider = dogdevProxyURL
)

// mateGitHubHosts 是「值得走本机加速」的 GitHub 系域名。
var mateGitHubHosts = map[string]bool{
	"github.com":                            true,
	"api.github.com":                        true,
	"codeload.github.com":                   true,
	"raw.githubusercontent.com":             true,
	"objects.githubusercontent.com":         true,
	"gist.githubusercontent.com":            true,
	"user-images.githubusercontent.com":     true,
	"avatars.githubusercontent.com":         true,
	"github-releases.githubusercontent.com": true,
	"release-assets.githubusercontent.com":  true,
}

// isMateAccelHost 判断该域名是否应走本机加速代理。
func isMateAccelHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return false
	}
	if mateGitHubHosts[h] {
		return true
	}
	return strings.HasSuffix(h, ".githubusercontent.com") || strings.HasSuffix(h, ".github.com")
}

// mateProxyFunc 给 http.Transport 用的代理选择器。
func mateProxyFunc(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	host := req.URL.Hostname()
	// 回环/链路本地一律直连。
	if host == "" || host == "localhost" || host == "::1" ||
		strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "[::1]") {
		return nil, nil
	}
	if !isMateAccelHost(host) {
		return nil, nil
	}
	if os.Getenv("MOO_MATE_NO_ACCEL") == "1" {
		return nil, nil
	}
	return accelProxyProvider(), nil
}

var (
	accelTrOnce sync.Once
	accelTr     *http.Transport
)

// mateAccelTransport 是 Mate 访问外部网络的共享 transport（连接复用）。
func mateAccelTransport() *http.Transport {
	accelTrOnce.Do(func() {
		// 只替换代理选择器，其余超时保持 Go 默认值：各调用点自带
		// Client.Timeout，这里再叠一层会改变既有失败模式（慢源会被误杀）。
		accelTr = &http.Transport{
			Proxy:               mateProxyFunc,
			DialContext:         (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     60 * time.Second,
			MaxIdleConnsPerHost: 4,
		}
	})
	return accelTr
}

// mateExternalClient 返回带本机加速通道的 HTTP 客户端。
func mateExternalClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: mateAccelTransport()}
}

// dogdevProxyURL 探测本机 Dog-dev 加速代理（结果缓存 5 分钟）。
func dogdevProxyURL() *url.URL {
	accelMu.Lock()
	defer accelMu.Unlock()
	if !accelAt.IsZero() && time.Since(accelAt) < 5*time.Minute {
		return accelURL
	}
	accelAt = time.Now()
	accelURL = probeDogdevProxy()
	return accelURL
}

var listenPortRe = regexp.MustCompile(`"listen"\s*:\s*"[^"]*:(\d+)"`)

// probeDogdevProxy 真正探一次：先问控制台「加速服务在不在跑」，再确认代理端口可连。
func probeDogdevProxy() *url.URL {
	port := "37710"
	hc := &http.Client{Timeout: 2 * time.Second}
	if resp, err := hc.Get("http://127.0.0.1:37717/api/health"); err == nil {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"running":true`)) {
			return nil // 装了但服务没在跑
		}
		// 用户可能改过代理端口，按配置读一次。
		if cfgResp, err := hc.Get("http://127.0.0.1:37717/api/config"); err == nil {
			cfgData, _ := io.ReadAll(io.LimitReader(cfgResp.Body, 1<<18))
			cfgResp.Body.Close()
			if m := listenPortRe.FindSubmatch(cfgData); len(m) == 2 {
				port = string(m[1])
			}
		}
	} else if conn, derr := net.DialTimeout("tcp", "127.0.0.1:37710", 800*time.Millisecond); derr == nil {
		// 控制台不可达（可能只开了代理），退回端口探测。
		conn.Close()
	} else {
		return nil
	}
	// 代理端口必须真的能建连，否则等于把请求丢进黑洞。
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 800*time.Millisecond)
	if err != nil {
		return nil
	}
	conn.Close()
	return &url.URL{Scheme: "http", Host: "127.0.0.1:" + port}
}
