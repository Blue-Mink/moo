package main

// MM-02：Host / Origin 判定的辅助函数（与 csrf.go 同属一道防线）。

import (
	"net"
	"net/url"
	"strings"
)

// 0.2.53（A2）：请求体上限。所有接口默认 1MB（正常载荷 KB 级）；
// 恢复备份上传沿用 256MB（restore.go 内还有一层同名限制）。
const (
	maxAPIBodyBytes    = 1 << 20   // 1MB
	maxUploadBodyBytes = 256 << 20 // 256MB
)

// newCSRFPolicies 生成 TCP 与 unix socket 网关两套策略。
func newCSRFPolicies(listen, selfBase string) (tcp, gw csrfPolicy) {
	uploads := map[string]bool{"/api/backup/restore-upload": true}
	limits := map[string]int64{"/api/backup/restore-upload": maxUploadBodyBytes}
	allowed := allowedHostList(selfBase, listen)
	tcp = csrfPolicy{
		name:           "tcp",
		loopbackOnly:   isLoopbackListen(listen),
		allowedHosts:   allowed,
		multipartPaths: uploads,
		maxBodyBytes:   maxAPIBodyBytes,
		bodyLimits:     limits,
	}
	// 网关通道的 Host 由面板 nginx 决定（常见 127.0.0.1:5666 或面板域名），
	// 不能按回环限制；Origin 同源判定仍生效。
	gw = csrfPolicy{
		name:           "gateway",
		allowedHosts:   allowed,
		multipartPaths: uploads,
		maxBodyBytes:   maxAPIBodyBytes,
		bodyLimits:     limits,
	}
	return
}

// sameOrigin 判断 Origin/Referer 的 host[:port] 是否与请求 Host 同源。
// 回环别名（127.0.0.1 / [::1] / localhost）互相等价；"null"（沙箱 iframe、
// data: 页面）host 为空，一律不匹配。
func sameOrigin(raw, reqHost string, allowed []string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, reqHost) {
		return true
	}
	for _, a := range allowed {
		if a != "" && strings.EqualFold(u.Host, a) {
			return true
		}
	}
	return isLoopbackHostPort(u.Host) && isLoopbackHostPort(reqHost)
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if isLoopbackHostPort(host) {
		return true
	}
	for _, a := range allowed {
		if a != "" && strings.EqualFold(host, a) {
			return true
		}
	}
	return false
}

func isLoopbackHostPort(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackListen(listen string) bool {
	h := listen
	if hh, _, err := net.SplitHostPort(listen); err == nil {
		h = hh
	}
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if h == "" {
		return false
	}
	return isLoopbackHostPort(h)
}

func allowedHostList(selfBase, listen string) []string {
	var out []string
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		for _, e := range out {
			if strings.EqualFold(e, h) {
				return
			}
		}
		out = append(out, h)
	}
	if u, err := url.Parse(strings.TrimSpace(selfBase)); err == nil {
		add(u.Host)
	}
	if host, port, err := net.SplitHostPort(listen); err == nil {
		if !isWildcardHost(host) {
			add(net.JoinHostPort(host, port))
			add(host)
		}
	} else {
		add(listen)
	}
	return out
}

func isWildcardHost(h string) bool {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	return h == "" || h == "0.0.0.0" || h == "::"
}
