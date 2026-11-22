package task

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

// P2-1：Retry-After 解析（秒数 / HTTP-date / 非法 / 截断）。
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   string
		want time.Duration
	}{
		{"秒数", "60", 60 * time.Second},
		{"零", "0", 0},
		{"负数", "-5", 0},
		{"空", "", 0},
		{"空白", "  ", 0},
		{"乱码", "soon-ish", 0},
		{"溢出不炸", "99999999999999999999", 0},
		{"超上限截断", "7200", time.Hour},
		{"恰好上限", "3600", time.Hour},
		{"HTTP-date GMT 未来", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"HTTP-date RFC1123 未来", now.Add(90 * time.Second).Format(time.RFC1123), 90 * time.Second},
		{"HTTP-date 过去", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"HTTP-date 超上限", now.Add(3 * time.Hour).Format(http.TimeFormat), time.Hour},
	}
	for _, c := range cases {
		if got := parseRetryAfter(c.in, now); got != c.want {
			t.Errorf("%s: parseRetryAfter(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// P2-1：限流表按 host 记窗口；多次登记取较晚者；过期自动清；reset 清空。
func TestRateLimitTableMarkRemaining(t *testing.T) {
	tbl := newRateLimitTable()
	if d := tbl.remaining("example.com"); d != 0 {
		t.Fatalf("空表应为 0, got %v", d)
	}
	tbl.mark("example.com", 80*time.Millisecond)
	if d := tbl.remaining("example.com"); d <= 0 {
		t.Fatalf("登记后应有限流窗口, got %v", d)
	}
	tbl.mark("example.com", 5*time.Millisecond) // 较短的不得缩短已有窗口
	if d := tbl.remaining("example.com"); d < 40*time.Millisecond {
		t.Fatalf("多次登记应取较晚者, got %v", d)
	}
	if d := tbl.remaining("other.example.com"); d != 0 {
		t.Fatalf("其他 host 不应被牵连, got %v", d)
	}
	tbl.reset()
	if d := tbl.remaining("example.com"); d != 0 {
		t.Fatalf("reset 后应为 0, got %v", d)
	}
	tbl.mark("expired.example", time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if d := tbl.remaining("expired.example"); d != 0 {
		t.Fatalf("过期窗口应归零, got %v", d)
	}
}

// P2-1：线路标签 + 剩余时长文案 + host 提取。
func TestLineLabelAndRemainingText(t *testing.T) {
	wrapped := "https://cdn.gh-proxy.org/https://raw.githubusercontent.com/o/r/main/x.fpk"
	if got := lineLabel(wrapped); got != "[加速镜像]" {
		t.Errorf("镜像壳应标 [加速镜像], got %q", got)
	}
	if got := lineLabel("https://raw.githubusercontent.com/o/r/main/x.fpk"); got != "[直连/源站]" {
		t.Errorf("直链应标 [直连/源站], got %q", got)
	}
	if got := urlHost(wrapped); got != "cdn.gh-proxy.org" {
		t.Errorf("urlHost 应取外层的镜像 host, got %q", got)
	}
	if got := urlHost("不是URL"); got != "" {
		t.Errorf("非法 URL 应返回空 host, got %q", got)
	}
	cases := map[time.Duration]string{
		0:                       "0s",
		1500 * time.Millisecond: "2s",
		60 * time.Second:        "60s",
		90 * time.Second:        "1m30s",
		time.Hour:               "1h",
	}
	for in, want := range cases {
		if got := remainingText(in); got != want {
			t.Errorf("remainingText(%v) = %q, want %q", in, got, want)
		}
	}
}

// P2（2026-11-22）：限流表有容量上限，且 mark 时顺手清理过期项。
func TestRateLimitTableCapsAndCleans(t *testing.T) {
	tbl := newRateLimitTable()
	for i := 0; i < rateLimitMaxHosts+50; i++ {
		tbl.mark("h"+strconv.Itoa(i), time.Hour)
	}
	tbl.mu.Lock()
	n := len(tbl.until)
	tbl.until["expired-host"] = time.Now().Add(-time.Minute)
	tbl.mu.Unlock()
	if n > rateLimitMaxHosts {
		t.Fatalf("限流表条数 = %d，超过上限 %d", n, rateLimitMaxHosts)
	}
	tbl.mark("fresh-host", time.Minute)
	tbl.mu.Lock()
	_, hasOld := tbl.until["expired-host"]
	_, hasNew := tbl.until["fresh-host"]
	tbl.mu.Unlock()
	if hasOld {
		t.Fatalf("过期条目应被 mark 清理")
	}
	if !hasNew {
		t.Fatalf("新 host 应被登记")
	}
}
