// Package netguard 限制源数据驱动的出站抓取：只放行公共地址。
//
// 背景（2026-09-27 代码审核）：目录条目里的 readme_url / preview_urls /
// icon_url / download_url 均由源提供（155+ 源，任何发布者可控）。旧实现
// 直接 http.DefaultClient 抓取并把响应透传给浏览器，构成 SSRF——恶意源
// 可让 root 进程探测内网地址（路由器管理页/元数据服务/其它 NAS 服务）
// 并把响应展示给受害者。aria2 下载候选同理，且候选值以 - 开头时还会被
// aria2c 解析为选项（--exec=… → root 命令执行）。
//
// 信任线：管理员显式配置的地址（应用源 URL、自建镜像、源列表地址）进
// 豁免清单（rebuilder 按 30s TTL 自动从配置重建，源增删无需逐处挂钩）；
// 源数据里的任意 URL 一律过 Public() 校验（scheme 限定 http/https +
// 主机解析后不得为环回/私网/链路本地/未指定地址，DNS 解析失败=拒绝）。
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const exemptTTL = 30 * time.Second

var (
	mu          sync.Mutex
	exempt      map[string]bool
	rebuilder   func() []string
	lastRebuild time.Time
)

// SetRebuilder 注册豁免主机重建函数（启动时调用一次）。
// 抓取时若距上次重建超过 TTL 则自动重建——应用源增删改即时（≤30s）生效。
func SetRebuilder(f func() []string) {
	mu.Lock()
	rebuilder = f
	lastRebuild = time.Time{}
	exempt = map[string]bool{}
	mu.Unlock()
}

// ensureExempt 返回当前豁免表；TTL 过期时先解锁调 rebuilder（避免持锁
// 读配置），重建后落表。
func ensureExempt() map[string]bool {
	mu.Lock()
	need := rebuilder != nil && time.Since(lastRebuild) > exemptTTL
	fn := rebuilder
	mu.Unlock()
	if need && fn != nil {
		hosts := fn()
		m := make(map[string]bool, len(hosts))
		for _, h := range hosts {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				m[h] = true
			}
		}
		mu.Lock()
		exempt = m
		lastRebuild = time.Now()
		mu.Unlock()
	}
	mu.Lock()
	defer mu.Unlock()
	return exempt
}

func isExempt(host string) bool {
	return ensureExempt()[strings.ToLower(host)]
}

func ipOK(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsMulticast() &&
		!ip.IsUnspecified() && !ip.IsInterfaceLocalMulticast()
}

// IsLoopback 主机是否为本地回环（127.0.0.0/8、::1、localhost）。
// 面板通道专用：面板客户端会把面板口令放进 WS 登录帧，目标地址必须
// 限定本机，防止 panel_base_url 被配置成外网地址导致口令外泄。
func IsLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Public 校验 rawURL 是否允许抓取：
//   - scheme 必须 http/https（顺带挡住以 - 开头的 aria2 选项注入值）
//   - 主机非空；IP 字面量直接判；域名解析后全部地址须为公共地址
//   - 豁免清单内的主机（管理员显式配置）直接放行
//
// 返回主机名（日志用）。
func Public(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("URL 解析失败: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("禁止的协议 %q（仅 http/https）", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("缺少主机")
	}
	if isExempt(host) {
		return host, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ipOK(ip) {
			return host, fmt.Errorf("禁止抓取内网/保留地址 %s", host)
		}
		return host, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		return host, fmt.Errorf("主机解析失败（拒绝抓取）: %v", err)
	}
	for _, a := range addrs {
		if !ipOK(a.IP) {
			return host, fmt.Errorf("主机 %s 解析到内网/保留地址 %s", host, a.IP)
		}
	}
	return host, nil
}
