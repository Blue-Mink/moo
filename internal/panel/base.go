package panel

// 面板本机基址解析（0.6.349，issue moo#7）。
//
// 背景：Moo 与飞牛面板同机，OAuth 换 token / 刷新 / 业务代理一律走本机
// 回环。历史实现写死 http://127.0.0.1:5666，用户把面板端口改成自定义值后
// 授权页能打开、code 也拿得到，但换 token 打空 → 授权永远走不完。
//
// 解析优先级（越靠前越权威）：
//  1. 环境变量 MOO_PANEL_BASE（排障/特殊部署）
//  2. 调用方显式传入（存量的 panel_base_url 配置，已通过回环校验）
//  3. /usr/trim/etc/network_gateway_setting.conf 的 schema.http.port
//     （force_https=true 时用 schema.https.port；可用 MOO_PANEL_CONF 覆盖路径）
//  4. 回环探测 5666(http) → 5667(https)
//  5. 兜底 http://127.0.0.1:5666
//
// 只接受回环地址：面板通道永远只走本机，非回环一律拒绝并回落（安全策略
// 与 0.6.255 的 panel_base_url 校验一致）。

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultPanelPort 面板 http 默认端口。
	DefaultPanelPort = 5666
	// DefaultPanelPortTLS 面板 https 默认端口。
	DefaultPanelPortTLS = 5667
	// BaseTTL 解析结果缓存时长（改端口后最迟多久自动跟上）。
	BaseTTL = 5 * time.Minute
	// DefaultGatewayConf fnOS 网关端口配置文件。
	DefaultGatewayConf = "/usr/trim/etc/network_gateway_setting.conf"
)

var (
	baseEnvName  = "MOO_PANEL_BASE"
	confEnvName  = "MOO_PANEL_CONF"
	gatewayConf  = DefaultGatewayConf
	probeTimeout = 350 * time.Millisecond
	probeDial    = func(addr string, d time.Duration) bool {
		c, err := net.DialTimeout("tcp", addr, d)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}
)

// BaseInfo 面板本机基址解析结果。
type BaseInfo struct {
	URL    string
	Source string // env / explicit / gateway-conf / probe / default
}

var (
	baseMu     sync.Mutex
	baseCache  BaseInfo
	baseAt     time.Time
	baseEnvKey string // 缓存时的 MOO_PANEL_BASE 值（env 变化即失效）
)

// ResolveBaseCached 返回缓存后的解析结果（显式地址不缓存）。
func ResolveBaseCached(explicit string) BaseInfo {
	if strings.TrimSpace(explicit) != "" {
		return resolveBase(explicit)
	}
	env := strings.TrimSpace(os.Getenv(baseEnvName))
	baseMu.Lock()
	defer baseMu.Unlock()
	if baseCache.URL != "" && env == baseEnvKey && time.Since(baseAt) < BaseTTL {
		return baseCache
	}
	info := resolveBase("")
	baseCache, baseAt, baseEnvKey = info, time.Now(), env
	return info
}

// InvalidateBaseCache 丢弃缓存（调用方发现连接失败时触发重解析）。
func InvalidateBaseCache() {
	baseMu.Lock()
	baseCache, baseAt, baseEnvKey = BaseInfo{}, time.Time{}, ""
	baseMu.Unlock()
}

func resolveBase(explicit string) BaseInfo {
	if raw := strings.TrimSpace(os.Getenv(baseEnvName)); raw != "" {
		if u, ok := normalizePanelBase(raw); ok {
			return BaseInfo{u, baseEnvName}
		}
		log.Printf("[panel] 忽略非法地址 %q（%s 只接受本机回环）", raw, baseEnvName)
	}
	if u, ok := normalizePanelBase(explicit); ok {
		return BaseInfo{u, "explicit"}
	} else if strings.TrimSpace(explicit) != "" {
		log.Printf("[panel] 忽略非本机回环地址 %q，回落自动解析", explicit)
	}
	if port, useTLS, ok := readGatewayPort(); ok && port > 0 {
		cand := baseFromPort(port, useTLS)
		if probeOK(cand) {
			return BaseInfo{cand, "gateway-conf"}
		}
		log.Printf("[panel] 网关配置端口 %d 无响应，继续探测默认端口", port)
	}
	for _, c := range []struct {
		port int
		tls  bool
	}{{DefaultPanelPort, false}, {DefaultPanelPortTLS, true}} {
		if cand := baseFromPort(c.port, c.tls); probeOK(cand) {
			return BaseInfo{cand, "probe"}
		}
	}
	return BaseInfo{DefaultBaseURL, "default"}
}

type gatewaySetting struct {
	Schema struct {
		HTTP  struct{ Port int } `json:"http"`
		HTTPS struct{ Port int } `json:"https"`
	} `json:"schema"`
	ForceHTTPS bool `json:"force_https"`
}

func readGatewayPort() (port int, useTLS bool, ok bool) {
	path := gatewayConf
	if v := strings.TrimSpace(os.Getenv(confEnvName)); v != "" {
		path = v
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false, false
	}
	var s gatewaySetting
	if err := json.Unmarshal(b, &s); err != nil {
		log.Printf("[panel] 网关配置解析失败（%s）: %v", path, err)
		return 0, false, false
	}
	if s.ForceHTTPS {
		if s.Schema.HTTPS.Port > 0 {
			return s.Schema.HTTPS.Port, true, true
		}
	}
	if s.Schema.HTTP.Port > 0 {
		return s.Schema.HTTP.Port, false, true
	}
	return 0, false, false
}

func baseFromPort(port int, useTLS bool) string {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	return scheme + "://127.0.0.1:" + strconv.Itoa(port)
}

func probeOK(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return false
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), map[bool]string{true: "443", false: "80"}[u.Scheme == "https"])
	}
	return probeDial(addr, probeTimeout)
}

func normalizePanelBase(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	if !isLoopbackHost(u.Hostname()) {
		return "", false
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + net.JoinHostPort(u.Hostname(), port), true
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// HTTPClientForBase 为面板通道构造 http.Client：https + 回环时放宽证书校验
// （面板本机证书多为自签，回环链路的中间人风险由本机权限边界兜住）。
func HTTPClientForBase(base string, timeout time.Duration) *http.Client {
	tr := &http.Transport{}
	if u, err := url.Parse(base); err == nil && u.Scheme == "https" && isLoopbackHost(u.Hostname()) {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // 仅本机回环
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}
