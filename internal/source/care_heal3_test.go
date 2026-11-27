package source

import (
	"path/filepath"
	"testing"
	"time"
)

// 只剩「自愈进度」的记录也必须落盘并被加载：HealStreak 若被持久化层过滤掉，
// 「连续 2 次」在频繁换包的机器上永远凑不满（等价 1a 的计数不落盘坑）。
// 同时覆盖 24h 通知节流时间戳的同样要求（否则重启即重复骚扰用户）。
func TestCareHealProgressOnlyRecordPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, cfg, _ := healManager(t, map[string]int{})
	t.Cleanup(m.WaitCareFlush)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	now := time.Now()
	m.careMu.Lock()
	// a：只剩自愈进度（HealStreak/LastHealAt）；b：只剩通知节流时间戳。
	m.careRecs["a"] = careRec{URL: cfg.Sources[0].URL, HealStreak: 1, LastHealAt: now}
	m.careRecs["b"] = careRec{URL: cfg.Sources[1].URL, DisableNoticeAt: now}
	m.careMu.Unlock()
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}

	m2 := NewManager(cfg)
	t.Cleanup(m2.WaitCareFlush)
	if n := m2.SetCarePath(path); n != 2 {
		t.Fatalf("只含自愈进度/节流时间的记录也应恢复（应 2 条）, got %d", n)
	}
	m2.careMu.Lock()
	a, b := m2.careRecs["a"], m2.careRecs["b"]
	m2.careMu.Unlock()
	if a.HealStreak != 1 || a.LastHealAt.IsZero() {
		t.Fatalf("自愈进度应逐字恢复: %+v", a)
	}
	if b.DisableNoticeAt.IsZero() {
		t.Fatalf("通知节流时间应逐字恢复: %+v", b)
	}
}
