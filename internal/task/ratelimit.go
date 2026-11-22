// 429 限流表 + Retry-After 解析（2026-11-22 P2-1）。
//
// 背景：候选逐个试错时，同一条线路（host）被 429 后，后面的候选若仍指向同一
// host（同镜像的多个地址，或用户手工重试）都会再撞一次限流——既浪费额度，
// 也让用户看到一串重复的 429。这里按 host 记「解除时刻」，Download 循环遇到
// 仍在限流窗内的 host 直接跳过、不发请求。
package task

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxRetryAfter 单次限流窗口上限：防止源返回离谱的 Retry-After（如 86400）
// 把一条线路整天拉黑。
const maxRetryAfter = time.Hour

// parseRetryAfter 解析 Retry-After 头（秒数或 HTTP-date，RFC 7231 §7.1.3）。
// 非法值 / 过去时刻 → 0；超过 maxRetryAfter → 截断。now 由调用方注入便于测试。
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n <= 0 {
			return 0
		}
		if n >= int64(maxRetryAfter/time.Second) {
			return maxRetryAfter // 先挡溢出再乘
		}
		return time.Duration(n) * time.Second
	}
	for _, layout := range []string{http.TimeFormat, time.RFC1123, time.RFC1123Z, time.RFC850, time.ANSIC} {
		t, err := time.Parse(layout, v)
		if err != nil {
			continue
		}
		switch d := t.Sub(now); {
		case d <= 0:
			return 0
		case d > maxRetryAfter:
			return maxRetryAfter
		default:
			return d
		}
	}
	return 0
}

// rateLimitTable 按 host 记录限流解除时刻（并发安全）。
type rateLimitTable struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newRateLimitTable() *rateLimitTable {
	return &rateLimitTable{until: map[string]time.Time{}}
}

// rateLimitMaxHosts 限流表容量上限：host 数≈镜像+源站（几十），4096 足够；
// 只防「长跑 + 恶意/异常候选 URL」把 map 撑到无界。
const rateLimitMaxHosts = 4096

// mark 登记 host 的限流窗口；多次登记取较晚者（不缩短已有窗口）。
// 顺带清理过期项并在超容量时淘汰最早到期的一条（P2，2026-11-22）。
func (t *rateLimitTable) mark(host string, d time.Duration) {
	if host == "" || d <= 0 {
		return
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	now := time.Now()
	u := now.Add(d)
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.until {
		if !v.After(now) {
			delete(t.until, k)
		}
	}
	// 容量已满且是本表没有的新 host：先淘汰最早到期的一条，再登记，
	// 避免「新登记的短窗口刚写入就被自己挤掉」。
	if _, exists := t.until[host]; !exists && len(t.until) >= rateLimitMaxHosts {
		var evict string
		var evictUntil time.Time
		first := true
		for k, v := range t.until {
			if first || v.Before(evictUntil) {
				evict, evictUntil, first = k, v, false
			}
		}
		delete(t.until, evict)
	}
	if cur, ok := t.until[host]; !ok || u.After(cur) {
		t.until[host] = u
	}
}

// remaining 返回 host 剩余限流时长（0 = 未限流；过期条目顺手清掉）。
func (t *rateLimitTable) remaining(host string) time.Duration {
	if host == "" {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	u, ok := t.until[host]
	if !ok {
		return 0
	}
	d := time.Until(u)
	if d <= 0 {
		delete(t.until, host)
		return 0
	}
	return d
}

func (t *rateLimitTable) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until = map[string]time.Time{}
}

// downloadLimiter 引擎共享的限流表（包级）。
var downloadLimiter = newRateLimitTable()

// ResetRateLimitsForTest 清空限流表（仅测试用）。
func ResetRateLimitsForTest() { downloadLimiter.reset() }

// urlHost 取 URL 的 host[:port]（小写；解析失败返回空串）。
func urlHost(u string) string {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Host)
}

// lineLabel 给候选地址打线路标签：用户只看一串 URL 分不清问题出在镜像还是源站。
func lineLabel(u string) string {
	if isMirrorWrappedURL(u) {
		return "[加速镜像]"
	}
	return "[直连/源站]"
}

// remainingText 剩余时长写成短文本（向上取整秒，60s / 1m30s / 1h）。
func remainingText(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	s := int64((d + time.Second - 1) / time.Second)
	switch {
	case s <= 60: // 60s 保持「60s」而不是「1m」（用户更容易对上 Retry-After 头）
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600 && s%60 == 0:
		return strconv.FormatInt(s/60, 10) + "m"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m" + strconv.FormatInt(s%60, 10) + "s"
	case s%3600 == 0:
		return strconv.FormatInt(s/3600, 10) + "h"
	}
	return strconv.FormatInt(s/3600, 10) + "h" + strconv.FormatInt((s%3600)/60, 10) + "m"
}
