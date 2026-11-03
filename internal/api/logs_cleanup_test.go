package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func logLine(at time.Time, msg string) string {
	return at.Format(logLineTimeLayout) + ".000000 [test] " + msg + "\n"
}

// 0.6.500：按时间过滤——保留最近行 + 无时间戳行（脚本输出）
func TestFilterLogLinesKeepsRecentAndUntimed(t *testing.T) {
	now := time.Now()
	old := logLine(now.AddDate(0, 0, -10), "old")
	recent := logLine(now.AddDate(0, 0, -3), "recent")
	today := logLine(now, "today")
	untimed := "Starting moo-server...\n"
	got := string(filterLogLines([]byte(old+recent+untimed+today), now.AddDate(0, 0, -7)))
	if strings.Contains(got, "old") {
		t.Fatalf("10 天前的行未被删: %q", got)
	}
	for _, want := range []string{"recent", "today", "Starting moo-server"} {
		if !strings.Contains(got, want) {
			t.Fatalf("应保留 %q，实际 %q", want, got)
		}
	}
}

// 0.6.500：保留 7 天——当前文件过滤、全旧归档删除、新归档保留
func TestCleanupLogFilesKeep7Days(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "moo.log")
	now := time.Now()
	oldLine := logLine(now.AddDate(0, 0, -10), "old-line")
	newLine := logLine(now.AddDate(0, 0, -1), "new-line")
	if err := os.WriteFile(logPath, []byte(oldLine+newLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".1", []byte(oldLine+oldLine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".2", []byte(newLine), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := cleanupLogFiles(logPath, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedFiles != 1 {
		t.Fatalf("应删除 1 个全旧归档，实际 %d", res.RemovedFiles)
	}
	if res.FreedBytes <= 0 || res.KeptBytes <= 0 {
		t.Fatalf("字节统计异常: %+v", res)
	}
	cur, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("当前日志必须保留: %v", err)
	}
	if strings.Contains(string(cur), "old-line") || !strings.Contains(string(cur), "new-line") {
		t.Fatalf("当前日志过滤结果不对: %q", cur)
	}
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Fatalf("全旧归档 .1 应被删除")
	}
	if b, err := os.ReadFile(logPath + ".2"); err != nil || !strings.Contains(string(b), "new-line") {
		t.Fatalf(".2 应保留且内容不变: %v %q", err, b)
	}
}

// 0.6.500：keep_days=0 全部清空——当前文件截断但保留，归档全删
func TestCleanupLogFilesAll(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "moo.log")
	now := time.Now()
	if err := os.WriteFile(logPath, []byte(logLine(now, "x")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".1", []byte("anything\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := cleanupLogFiles(logPath, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedFiles != 1 {
		t.Fatalf("归档应全删，实际 %d", res.RemovedFiles)
	}
	fi, err := os.Stat(logPath)
	if err != nil || fi.Size() != 0 {
		t.Fatalf("当前日志应被截断为 0: %v %v", err, fi)
	}
	if res.KeptBytes != 0 {
		t.Fatalf("全部清空后 kept 应为 0: %+v", res)
	}
}

// 0.6.500：只有无时间戳行的文件不能被误删
func TestCleanupLogLinesUntimedOnlyKept(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "moo.log")
	content := "post-install script output\nno timestamp here\n"
	if err := os.WriteFile(logPath+".1", []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := cleanupLogFiles(logPath, 7, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.RemovedFiles != 0 {
		t.Fatalf("无时间戳归档不应删除: %+v", res)
	}
	if b, err := os.ReadFile(logPath + ".1"); err != nil || string(b) != content {
		t.Fatalf("内容应原样保留: %v %q", err, b)
	}
}
