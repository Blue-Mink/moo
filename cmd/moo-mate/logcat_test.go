package main

// 批次⑦单测：日志归类与降噪（throttle 折叠 / failStreak 连续失败 / syncLogMode）。

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// captureLog 把标准 logger 重定向到缓冲区并在测试结束还原。
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})
	return &buf
}

// nonEmptyLines 返回非空行（跳过尾部空串）。
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestCatLogfPrefix(t *testing.T) {
	buf := captureLog(t)
	catLogf(catBoot, "Moo Mate 启动：%s", "127.0.0.1:38108")
	got := strings.TrimSpace(buf.String())
	if got != "[boot] Moo Mate 启动：127.0.0.1:38108" {
		t.Fatalf("分类前缀不符：%q", got)
	}
}

func TestThrottleFoldsWithinWindowAndReportsCount(t *testing.T) {
	buf := captureLog(t)
	cur := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	th := newThrottle(time.Hour)
	th.now = func() time.Time { return cur }
	// 窗口内连打 4 次 → 只应留 1 条（后 3 条折叠）。
	for i := 0; i < 4; i++ {
		th.Logf("busy", catSync, "同步进行中，跳过本轮唤醒")
		cur = cur.Add(time.Minute)
	}
	if n := strings.Count(buf.String(), catSync); n != 1 {
		t.Fatalf("窗口内 4 次应只打 1 条，实得 %d 条：%q", n, buf.String())
	}
	if strings.Contains(buf.String(), "已折叠") {
		t.Fatalf("窗口内首条不应带折叠后缀：%q", buf.String())
	}
	// 窗口到期后再打一条：应带「期间同类 3 条已折叠」。
	buf.Reset()
	cur = cur.Add(2 * time.Hour)
	th.Logf("busy", catSync, "同步进行中，跳过本轮唤醒")
	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("窗口到期后应恰好 1 条，实得 %d 行：%q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "期间同类 3 条已折叠") {
		t.Fatalf("窗口到期应报告折叠 3 条，实得 %q", lines[0])
	}
}

func TestThrottleKeysIndependent(t *testing.T) {
	buf := captureLog(t)
	th := newThrottle(time.Hour)
	th.Logf("a", catSync, "A")
	th.Logf("a", catSync, "A")
	th.Logf("b", catSync, "B")
	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("不同 key 互不影响（应 2 行），实得 %d 行：%q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "A") || !strings.Contains(lines[1], "B") {
		t.Fatalf("key 顺序/内容不符：%q", buf.String())
	}
}

func TestFailStreakFirstAndTenth(t *testing.T) {
	buf := captureLog(t)
	fs := newFailStreak()
	for i := 0; i < 12; i++ {
		fs.Fail("sync", catSync, "同步失败: %v", "boom")
	}
	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("连续 12 次失败应恰好打第 1、10 次（2 行），实得 %d 行：%q", len(lines), buf.String())
	}
	if lines[0] != "[sync] 同步失败: boom" {
		t.Fatalf("第 1 次应为原文无后缀，实得 %q", lines[0])
	}
	if !strings.Contains(lines[1], "（连续第 10 次）") {
		t.Fatalf("第 10 次应带「（连续第 10 次）」，实得 %q", lines[1])
	}
	if n := fs.Count("sync"); n != 12 {
		t.Fatalf("计数应为 12，实得 %d", n)
	}
}

func TestFailStreakOKResets(t *testing.T) {
	buf := captureLog(t)
	fs := newFailStreak()
	for i := 0; i < 3; i++ {
		fs.Fail("sync", catSync, "同步失败: %v", "boom")
	}
	if n := len(nonEmptyLines(buf.String())); n != 1 {
		t.Fatalf("连续 3 次失败应只打 1 行，实得 %d 行：%q", n, buf.String())
	}
	buf.Reset()
	fs.OK("sync", catSync, "同步已恢复")
	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 || lines[0] != "[sync] 同步已恢复（此前连续失败 3 次）" {
		t.Fatalf("恢复行不符：%q", buf.String())
	}
	if n := fs.Count("sync"); n != 0 {
		t.Fatalf("OK 后计数应清零，实得 %d", n)
	}
	// 无失败时的 OK 不制造噪音。
	buf.Reset()
	fs.OK("sync", catSync, "同步已恢复")
	if buf.Len() != 0 {
		t.Fatalf("无失败时 OK 不应打印，实得 %q", buf.String())
	}
	// 再次失败从 1 重新计，不打「连续第」后缀。
	fs.Fail("sync", catSync, "同步失败: %v", "boom")
	lines = nonEmptyLines(buf.String())
	if len(lines) != 1 || strings.Contains(lines[0], "连续第") {
		t.Fatalf("恢复后再失败应重新从 1 计：%q", buf.String())
	}
}

func TestSyncLogMode(t *testing.T) {
	cases := []struct {
		name    string
		updated int
		since   time.Duration
		want    string
	}{
		{"有更新立即打", 1, 0, "changed"},
		{"有更新且久未打", 3, 7 * time.Hour, "changed"},
		{"无更新且刚打过", 0, 5 * time.Hour, "silent"},
		{"无更新满 6h", 0, 6 * time.Hour, "heartbeat"},
		{"无更新 7h", 0, 7 * time.Hour, "heartbeat"},
		{"无更新从未打过", 0, syncHeartbeatWindow * 2, "heartbeat"},
	}
	for _, c := range cases {
		if got := syncLogMode(c.updated, c.since); got != c.want {
			t.Fatalf("%s：syncLogMode(%d, %v) = %q，want %q", c.name, c.updated, c.since, got, c.want)
		}
	}
}
