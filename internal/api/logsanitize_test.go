package api

// 2026-11-28 门 2 审计 SHOULD-1/SHOULD-2 的回归用例：
// 日志里不得出现 URL 凭据（userinfo / ?token=…），且必须守住单行上限。

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// SHOULD-1/2 基础：userinfo 与 query 都要去掉，路径保留。
func TestSanitizeLogTextStripsUserinfoAndQuery(t *testing.T) {
	in := "GET https://user:s3cr3t@mirror.example.com/moo.fpk?token=ghp_ABCDEFGHIJK&sig=1 失败"
	got := sanitizeLogText(in, 200)
	for _, bad := range []string{"s3cr3t", "user:", "token=", "ghp_", "sig=1", "?"} {
		if strings.Contains(got, bad) {
			t.Errorf("脱敏后不应含 %q, got %q", bad, got)
		}
	}
	if !strings.Contains(got, "https://mirror.example.com/moo.fpk") {
		t.Errorf("应保留 scheme+host+path, got %q", got)
	}
}

// 压平空白：远端错误带换行时不得把一行日志拆成多行。
func TestSanitizeLogTextCollapsesNewlines(t *testing.T) {
	got := sanitizeLogText("a\nb\tc\r\nd", 200)
	if strings.ContainsAny(got, "\n\t\r") {
		t.Fatalf("应压平所有空白, got %q", got)
	}
	if got != "a b c d" {
		t.Fatalf("期望 \"a b c d\", got %q", got)
	}
}

// 截断：按 rune，不切半个汉字；<=0 表示不截断。
func TestSanitizeLogTextTruncate(t *testing.T) {
	long := strings.Repeat("中", 30)
	got := sanitizeLogText(long, 10)
	if n := utf8.RuneCountInString(got); n > 10 {
		t.Fatalf("截断后应 <=10 rune, got %d (%q)", n, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断应带省略号, got %q", got)
	}
	if all := sanitizeLogText(long, 0); all != long {
		t.Fatalf("maxRunes=0 应不截断, got %q", all)
	}
}

// SHOULD-1 回归：测速摘要里的失败原因不得泄露镜像 URL 的 token。
func TestMirrorProbeSummarySanitizesReason(t *testing.T) {
	stats := []MirrorStat{{
		Key:    "custom",
		Status: "fail",
		Error:  "Get https://u:pw@m.example.com/a?token=ghp_SECRET123456 : timeout",
	}}
	line := mirrorProbeSummary("gh", stats, "direct", 820*time.Millisecond, mirrorProbeSummaryLineMax)
	for _, bad := range []string{"ghp_", "token=", "pw@", "u:pw"} {
		if strings.Contains(line, bad) {
			t.Errorf("摘要行不应含 %q, got %q", bad, line)
		}
	}
	if !strings.Contains(line, "custom=") {
		t.Errorf("应保留失败源 key, got %q", line)
	}
}

// SHOULD-2 回归：失败跳过行的 URL 脱敏 + 单行 <=200 rune。
func TestListSyncSkipLineSanitized(t *testing.T) {
	badURL := "https://user:p%40ss@lists.example.com/" + strings.Repeat("a", 300) + "?token=ghp_SECRET123456"
	reason := "Get https://m.example.com/x?token=ghp_OTHER123456: connect: connection refused\n(第二次)"
	out := captureLog(t, func() {
		logListSyncRound(sourceListSyncResult{Fetched: 1, Failed: 1}, map[string]string{badURL: reason}, 12*time.Millisecond)
	})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("应 1 摘要 + 1 跳过 = 2 行, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if n := utf8.RuneCountInString(l); n > 200 {
			t.Errorf("日志行超 200 rune: %d (%q)", n, l)
		}
		for _, bad := range []string{"ghp_", "token=", "p%40ss", "user:"} {
			if strings.Contains(l, bad) {
				t.Errorf("日志行不应含 %q: %q", bad, l)
			}
		}
	}
	if !strings.Contains(lines[1], "[list-sync] 跳过 https://lists.example.com/") {
		t.Errorf("应保留脱敏后的 URL 前缀, got %q", lines[1])
	}
}
