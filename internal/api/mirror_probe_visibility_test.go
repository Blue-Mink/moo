package api

// 2026-11-26 B-MIRROR-PROBE-VISIBILITY：测速摘要日志（P1-1）与
// health 到期时刻/降频标识（P1-2）的护栏用例。零外网（纯构造，不发请求）。

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"moo/internal/config"
)

// TestMirrorProbeSummaryOneLine 摘要行格式：含计数、优选、耗时，且单行。
func TestMirrorProbeSummaryOneLine(t *testing.T) {
	stats := []MirrorStat{
		{Key: "cdn-ghproxy", Label: "CDN", Status: "ok", SpeedBPS: 900000},
		{Key: "direct", Status: "fail", Error: "连接超时"},
		{Key: "conversun", Status: "fail", Error: "HTTP 403"},
		{Key: "gh-tbap", Status: "limited", RetryAfterS: 42},
	}
	line := mirrorProbeSummary("gh", stats, "cdn-ghproxy", 8200*time.Millisecond, mirrorProbeSummaryLineMax)
	if strings.Contains(line, "\n") {
		t.Fatalf("摘要必须单行: %q", line)
	}
	for _, want := range []string{"[mirror] gh 探测 4 源", "ok=1", "fail=2", "limited=1",
		"优选=cdn-ghproxy", "耗时=8.2s", "direct=连接超时", "conversun=HTTP 403"} {
		if !strings.Contains(line, want) {
			t.Errorf("摘要应含 %q, got %q", want, line)
		}
	}
}

// TestMirrorProbeSummaryTruncated 失败源很多时也只用一行、≤200 字（不逐源刷屏）。
func TestMirrorProbeSummaryTruncated(t *testing.T) {
	stats := make([]MirrorStat, 0, 40)
	for i := 0; i < 40; i++ {
		stats = append(stats, MirrorStat{
			Key: fmt.Sprintf("mirror-%02d", i), Status: "fail",
			Error: "dial tcp: connect: connection refused（模拟很长的错误信息）",
		})
	}
	line := mirrorProbeSummary("gh", stats, "direct", 30*time.Second, mirrorProbeSummaryLineMax)
	if strings.Contains(line, "\n") {
		t.Fatal("摘要必须单行")
	}
	if n := utf8.RuneCountInString(line); n > mirrorProbeSummaryLineMax {
		t.Fatalf("摘要应 ≤%d 字, got %d: %q", mirrorProbeSummaryLineMax, n, line)
	}
	if !strings.Contains(line, "fail=40") || !strings.Contains(line, "（异常：") {
		t.Fatalf("摘要应含计数与异常段, got %q", line)
	}
}

// TestMirrorHealthNextProbe 到期时刻 == last_probe + interval_s（配置口径），
// 并附带不活跃门控标识（P1-2：降频不再隐形）。
func TestMirrorHealthNextProbe(t *testing.T) {
	s := &Server{Cfg: &config.Config{GhProbeMinutes: 5}, Mirrors: newMirrorMonitor()}
	base := time.Now().Add(-90 * time.Second).Truncate(time.Second)
	s.Mirrors.mu.Lock()
	s.Mirrors.lastProbeGh = base
	s.Mirrors.mu.Unlock()

	h := s.healthOf("auto", "gh", nil)
	if h.IntervalS != 300 {
		t.Fatalf("interval_s 应为 300, got %d", h.IntervalS)
	}
	want := base.Add(5 * time.Minute).Format(time.RFC3339)
	if h.NextProbe != want {
		t.Fatalf("next_probe 应为 last_probe+interval_s=%s, got %q", want, h.NextProbe)
	}
	if h.IdleGapS != int(mirrorProbeIdleGap/time.Second) {
		t.Fatalf("idle_gap_s 应为 %d, got %d", int(mirrorProbeIdleGap/time.Second), h.IdleGapS)
	}
	if h.IdleThrottled {
		t.Fatal("StartedAt 为零（刚启动）不应判不活跃")
	}
	// 30 分钟无 UI 访问 → 标识为真（门控阈值本身不改，仅可见化）
	s.StartedAt = time.Now().Add(-2 * time.Hour)
	if !s.healthOf("auto", "gh", nil).IdleThrottled {
		t.Fatal(">30min 无 UI 访问应 idle_throttled=true")
	}
}

// TestLogMirrorRoundOneLinePerCall logMirrorRound 每次调用恰一行（每轮恰一行）。
func TestLogMirrorRoundOneLinePerCall(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	s.logMirrorRound("gh", time.Now(), []MirrorStat{{Key: "a", Status: "ok"}}, "a")
	s.logMirrorRound("dk", time.Now(), []MirrorStat{{Key: "b", Status: "fail", Error: "x"}}, "b")

	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("两次调用应恰 2 行, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "[mirror] gh 探测") || !strings.Contains(lines[1], "[mirror] dk 探测") {
		t.Fatalf("每行应按组打头, got %q", lines)
	}
}

// 批次 1 F4（N8）：截断断点不得落在 '/' 上——否则摘要常被切成
// 「…≈ 4KB/…」这种把单位劈开的半句。（真实日志里速度/路径到处都是 '/'）
// 反向验证：把 '/' 加回 truncateAtBoundary 的断点集合 → 本用例 FAIL。
func TestMirrorProbeTruncateNotAtSlash(t *testing.T) {
	// 构造：截断点正好压在 '/' 上（旧实现会把「4KB/」留在结尾）。
	prefix := strings.Repeat("a", 30) + "4KB/"
	s := prefix + strings.Repeat("b", 100)
	max := utf8.RuneCountInString(prefix) // cut = max-1 = '/' 的下标
	got := truncateAtBoundary(s, max)
	if strings.Contains(got, "KB/") {
		t.Errorf("截断结果不得含被劈开的「KB/」, got %q", got)
	}
	if strings.HasSuffix(got, "/…") {
		t.Errorf("截断结果不得以「/…」结尾, got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("超长内容应带省略号, got %q", got)
	}
	if n := utf8.RuneCountInString(got); n > max {
		t.Errorf("截断后应 ≤%d 字, got %d", max, n)
	}

	// 真实摘要：长异常段被截断后同样不得以「/…」结尾（'/' 不再是断点）。
	stats := make([]MirrorStat, 0, 40)
	for i := 0; i < 40; i++ {
		stats = append(stats, MirrorStat{
			Key: fmt.Sprintf("mirror-%02d", i), Status: "fail",
			Error: "读取 4KB/秒失败：dial tcp 127.0.0.1:1: connect: connection refused",
		})
	}
	line := mirrorProbeSummary("gh", stats, "direct", 30*time.Second, mirrorProbeSummaryLineMax)
	if strings.HasSuffix(line, "/…") || strings.Contains(line, "KB/…") {
		t.Errorf("摘要不得截在 '/' 上, got %q", line)
	}
}
