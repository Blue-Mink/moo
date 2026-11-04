package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 0.2.38：日志功能单测（路径解析 / 行过滤 / 清理 / 轮转）。

func TestResolveMateLogPath_0238(t *testing.T) {
	dir := t.TempDir()
	if got := resolveMateLogPath(dir); got != filepath.Join(dir, "moo-mate.log") {
		t.Fatalf("默认路径 = %q", got)
	}
	// 相对路径：相对 PKGVAR
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("logs/app.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMateLogPath(dir); got != filepath.Join(dir, "logs", "app.log") {
		t.Fatalf("相对路径 = %q", got)
	}
	// 绝对路径原样
	abs := filepath.Join(dir, "custom.log")
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte(abs), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMateLogPath(dir); got != abs {
		t.Fatalf("绝对路径 = %q", got)
	}
	// 空白 .logpath → 默认
	if err := os.WriteFile(filepath.Join(dir, ".logpath"), []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveMateLogPath(dir); got != filepath.Join(dir, "moo-mate.log") {
		t.Fatalf("空白指针 = %q", got)
	}
}

func TestMateLogLineTS_0238(t *testing.T) {
	ts, ok := mateLogLineTS("2026/11/04 08:57:09 Moo Mate 启动：127.0.0.1:38108")
	if !ok || ts.Year() != 2026 || ts.Month() != time.November || ts.Day() != 4 {
		t.Fatalf("正常时间戳解析失败: %v %v", ts, ok)
	}
	for _, line := range []string{
		"Stopping moo-mate ...",
		"moo-mate started: gateway socket=...",
		"2026/11/04T08:57:09 非 Go log 格式",
		"20261104 085709 无分隔符",
	} {
		if _, ok := mateLogLineTS(line); ok {
			t.Errorf("不应识别为时间戳: %q", line)
		}
	}
}

func TestFilterMateLogLines_0238(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	cutoff := now.AddDate(0, 0, -7)
	data := []byte(strings.Join([]string{
		"2026/10/20 10:00:00 很旧的日志",
		"2026/11/01 10:00:00 七天内的日志",
		"没有时间戳的脚本输出",
		"2026/11/04 09:00:00 今天的日志",
		"",
	}, "\n"))
	out := string(filterMateLogLines(data, cutoff))
	if strings.Contains(out, "很旧的日志") {
		t.Error("旧日志未被过滤")
	}
	for _, keep := range []string{"七天内的日志", "没有时间戳的脚本输出", "今天的日志"} {
		if !strings.Contains(out, keep) {
			t.Errorf("应保留: %q（实际=%q）", keep, out)
		}
	}
}

func TestCleanupMateLogFiles_0238(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "moo-mate.log")
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.Local)
	old := "2026/10/01 00:00:00 旧日志\n"
	fresh := "2026/11/03 00:00:00 新日志\n"
	script := "Stopping moo-mate ...\n"
	if err := os.WriteFile(logPath, []byte(old+fresh+script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".1", []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := cleanupMateLogFiles(logPath, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.FreedBytes <= 0 {
		t.Errorf("应释放字节，实际 %d", res.FreedBytes)
	}
	if res.RemovedFiles != 1 {
		t.Errorf("旧归档应被删除，removed_files=%d", res.RemovedFiles)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Contains(string(b), "旧日志") {
		t.Error("旧日志未被删除")
	}
	if !strings.Contains(string(b), "新日志") || !strings.Contains(string(b), "Stopping moo-mate") {
		t.Errorf("应保留新日志与脚本输出，实际=%q", string(b))
	}
	if _, err := os.Stat(logPath + ".1"); !os.IsNotExist(err) {
		t.Error("空归档应被删除")
	}
	// keep_days=0：全部清空（对齐 Moo 0.6.500：「全部清空」含脚本输出行）
	res2, err := cleanupMateLogFiles(logPath, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(logPath)
	if len(b) != 0 {
		t.Errorf("keep=0 应清空整个当前日志，实际剩余 %q", string(b))
	}
	if res2.KeepDays != 0 {
		t.Errorf("keep_days 回显 = %d", res2.KeepDays)
	}
}

func TestRotateMateLog_0238(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "moo-mate.log")
	oldMax := mateLogRotateMaxSize
	mateLogRotateMaxSize = 64
	defer func() { mateLogRotateMaxSize = oldMax }()
	big := strings.Repeat("x", 200) + "\n"
	if err := os.WriteFile(logPath, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	size, ok := rotateMateLog(logPath)
	if !ok || size == 0 {
		t.Fatalf("应触发轮转: size=%d ok=%v", size, ok)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Size() != 0 {
		t.Errorf("当前日志应清零: %v size=%d", err, fi.Size())
	}
	if b, err := os.ReadFile(logPath + ".1"); err != nil || string(b) != big {
		t.Errorf("归档内容不符: %v", err)
	}
	// 再写一份触发第二次轮转 → .1 顺移到 .2
	if err := os.WriteFile(logPath, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := rotateMateLog(logPath); !ok {
		t.Fatal("第二次轮转失败")
	}
	if _, err := os.Stat(logPath + ".2"); err != nil {
		t.Error("归档应顺移到 .2")
	}
}
