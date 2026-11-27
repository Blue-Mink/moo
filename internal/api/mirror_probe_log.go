package api

// 2026-11-26 B-MIRROR-PROBE-VISIBILITY P1-1：加速源测速的每轮一行摘要。
//
// 背景：mirrors.go 里此前 log.Printf 数量为 0，用户/运维只能靠 UI 或
// /api/mirrors/health 判断「是否在测速」。本文件的纪律：
//   - **每轮恰一行**（gh / dk 各自一组），不逐源刷屏；
//   - 单行 ≤200 字，失败源名+原因截断合并进同一行；
//   - 不改测速算法、不改门控阈值，只加可观测性。

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"
)

// mirrorProbeSummaryLineMax 摘要单行上限（rune）。
const mirrorProbeSummaryLineMax = 200

// truncateRunes 按 rune 截断并加省略号（中文安全）。
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// truncateAtBoundary 按 rune 截断并尽量落在分隔符上（不要留「≈ 4KB/s < …」这种半句）。
// 2026-11-26（批次 C）：摘要是给人看的，截在逗号/空格比截在 `<`、`(` 更易读。
func truncateAtBoundary(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	cut := max - 1
	floor := cut - max/3
	if floor < 0 {
		floor = 0
	}
	for i := cut; i >= floor; i-- {
		// 批次 1 F4（N8）：断点集合**不含 '/'**。旧集合里有它，摘要常被切成
		// 「…≈ 4KB/…」这种把单位劈开的半句（速度/路径里 '/' 太常见）。
		switch r[i] {
		case '，', '、', '；', ' ':
			return string(r[:i+1]) + "…"
		}
	}
	return string(r[:cut]) + "…"
}

// mirrorProbeReason 失败/限流的短原因（有 Error 用 Error，429 带等待时长）。
func mirrorProbeReason(st MirrorStat) string {
	reason := strings.TrimSpace(st.Error)
	if reason == "" {
		if st.RetryAfterS > 0 {
			return fmt.Sprintf("429限流(等%ds)", st.RetryAfterS)
		}
		return "失败"
	}
	// 2026-11-28 门 2 审计 SHOULD-1：失败原因可能带镜像 URL（含 ?token=…）→ 先脱敏再截断。
	return sanitizeLogText(reason, 24)
}

// mirrorProbeSummary 一组测速的一行摘要（纯函数，单测直接断言）：
//
//	[mirror] gh 探测 12 源 ok=5 fail=7 limited=0 优选=cdn-ghproxy 耗时=8.2s（异常：direct=连接超时，conversun=HTTP 403）
func mirrorProbeSummary(group string, stats []MirrorStat, active string, dur time.Duration, maxRunes int) string {
	ok, limited, fail := 0, 0, 0
	bad := make([]string, 0, len(stats))
	for _, st := range stats {
		switch st.Status {
		case "ok":
			ok++
		case "limited":
			limited++
			bad = append(bad, st.Key+"="+mirrorProbeReason(st))
		case "fail":
			fail++
			bad = append(bad, st.Key+"="+mirrorProbeReason(st))
		}
	}
	if active == "" {
		active = "-"
	}
	line := fmt.Sprintf("[mirror] %s 探测 %d 源 ok=%d fail=%d limited=%d 优选=%s 耗时=%s",
		group, len(stats), ok, fail, limited, active, dur.Round(100*time.Millisecond))
	if len(bad) > 0 {
		line += "（异常：" + strings.Join(bad, "，") + "）"
	}
	return truncateAtBoundary(line, maxRunes)
}

// logMirrorRound 记一行本组摘要（每轮恰一次调用 = 恰一行）。
func (s *Server) logMirrorRound(group string, start time.Time, stats []MirrorStat, active string) {
	log.Print(mirrorProbeSummary(group, stats, active, time.Since(start), mirrorProbeSummaryLineMax))
}
