package main

// 0.2.76（审计 P2-4）单测：日志折叠表 key 限长 + 表上限。

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBoundLogKeyTruncatesAndSanitizes(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := boundLogKey(long)
	if n := utf8.RuneCountInString(got); n != logKeyMaxRunes {
		t.Fatalf("超长 key 应截到 %d rune，实得 %d", logKeyMaxRunes, n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断后应带省略号：%q", got[max(0, len(got)-8):])
	}
	// 换行/制表必须被压掉，否则外部头能伪造日志行。
	multi := boundLogKey("x\r\n2026-11-22 00:00:00 fake line\ty")
	if strings.ContainsAny(multi, "\r\n\t") {
		t.Fatalf("控制字符未清理：%q", multi)
	}
	if multi != "x 2026-11-22 00:00:00 fake line y" {
		t.Fatalf("控制字符应压成空格：%q", multi)
	}
	if boundLogKey("   ") != "-" {
		t.Fatalf("空 key 应归一为 \"-\"，实得 %q", boundLogKey("   "))
	}
	if boundLogKey("  busy ") != "busy" {
		t.Fatalf("普通 key 应去首尾空白并原样保留：%q", boundLogKey("  busy "))
	}
}

func TestThrottleKeyTableBounded(t *testing.T) {
	captureLog(t)
	th := newThrottle(time.Hour)
	cur := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	th.now = func() time.Time { return cur }
	for i := 0; i < 10000; i++ {
		cur = cur.Add(time.Millisecond)
		th.Logf(fmt.Sprintf("https://h%d.example", i), catHTTP, "拒绝")
	}
	if len(th.last) > logTableMaxKeys {
		t.Fatalf("throttle.last 超上限：%d > %d", len(th.last), logTableMaxKeys)
	}
	if len(th.dropped) > logTableMaxKeys {
		t.Fatalf("throttle.dropped 超上限：%d > %d", len(th.dropped), logTableMaxKeys)
	}
	last := fmt.Sprintf("https://h%d.example", 9999)
	if _, ok := th.last[last]; !ok {
		t.Fatalf("最新 key 不应被淘汰：%s", last)
	}
	if _, ok := th.last["https://h0.example"]; ok {
		t.Fatalf("最旧 key 应被淘汰，但仍在表中")
	}
	if len(th.dropped) != len(th.last) {
		t.Fatalf("两张表应同步淘汰：last=%d dropped=%d", len(th.last), len(th.dropped))
	}
}

func TestFailStreakKeyTableBounded(t *testing.T) {
	captureLog(t)
	fs := newFailStreak()
	cur := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	fs.now = func() time.Time { return cur }
	for i := 0; i < 10000; i++ {
		cur = cur.Add(time.Millisecond)
		fs.Fail(fmt.Sprintf("k%d", i), catSync, "失败")
	}
	if len(fs.n) > logTableMaxKeys || len(fs.last) > logTableMaxKeys || len(fs.seen) > logTableMaxKeys {
		t.Fatalf("failStreak 表超上限：n=%d last=%d seen=%d", len(fs.n), len(fs.last), len(fs.seen))
	}
	if n := fs.Count("k9999"); n != 1 {
		t.Fatalf("最新 key 应保留且计数为 1，实得 %d", n)
	}
	if n := fs.Count("k0"); n != 0 {
		t.Fatalf("最旧 key 应被淘汰（计数 0），实得 %d", n)
	}
}
