package api

import (
	"os"
	"testing"
	"time"
)

// 2026-11-26（批次 C）：会话加密未生效提醒的 24h 节流（落盘状态，重启不重置）。
func TestOfficialEncryptWarnThrottle(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	s := &Server{}
	if s.warnedRecently(officialEncryptWarnKey, 24*time.Hour) {
		t.Fatal("首次不应命中节流")
	}
	s.markWarned(officialEncryptWarnKey)
	if !s.warnedRecently(officialEncryptWarnKey, 24*time.Hour) {
		t.Fatal("标记后 24h 内应命中节流")
	}
	if s.warnedRecently(officialEncryptWarnKey, 0) {
		t.Fatal("gap=0 时不应命中（等价关闭节流）")
	}
	fi, err := os.Stat(s.officialEncryptWarnPath())
	if err != nil {
		t.Fatalf("节流状态应落盘: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("节流状态文件应为 0600，实际 %o", fi.Mode().Perm())
	}
	// 新进程（新 Server）读同一文件 → 仍然节流（重启不重置）。
	s2 := &Server{}
	if !s2.warnedRecently(officialEncryptWarnKey, 24*time.Hour) {
		t.Fatal("重启后应仍命中节流")
	}
}
