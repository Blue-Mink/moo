package source

import (
	"path/filepath"
	"testing"
	"time"
)

// 恢复后计数从 0 重新计：空源计数删除、失败计数与首次失败时间清零、
// 自愈进度归零、策略标记清除（误判红线 3 的轮数门槛重新起算）。
func TestCareHealRestoreResetsCounters(t *testing.T) {
	m, cfg, _ := healManager(t, map[string]int{"a": 7})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	m.careEmpty["a"] = 4
	rec := m.careRecs["a"]
	rec.HealStreak = 1
	m.careRecs["a"] = rec
	m.careMu.Unlock()

	if acts := m.AutoCareHealPass(); len(acts) != 1 {
		t.Fatalf("应恢复, got %v", acts)
	}
	m.careMu.Lock()
	rec = m.careRecs["a"]
	empty := m.careEmpty["a"]
	m.careMu.Unlock()
	if rec.FailStreak != 0 || !rec.FirstFailAt.IsZero() || rec.HealStreak != 0 || rec.DisabledBy != "" {
		t.Fatalf("恢复后失败计数/首次失败/自愈进度/停用标记都应清空: %+v", rec)
	}
	if empty != 0 {
		t.Fatalf("恢复后 empty_streak 应从 0 重新计, got %d", empty)
	}
}

// 自愈进度跨重启保留（否则「连续 2 次」在换包频繁的机器上永远凑不满）。
func TestCareHealPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, cfg, _ := healManager(t, map[string]int{"a": 3})
	t.Cleanup(m.WaitCareFlush)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	healDisable(m, cfg, "a")
	m.AutoCareHealPass() // 第 1 次成功 → HealStreak=1
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}

	// 「重启」：同一 cfg 换新 Manager
	m2 := NewManager(cfg)
	t.Cleanup(m2.WaitCareFlush)
	m2.CareHealProbe = func(name, url string) int { return 3 }
	if n := m2.SetCarePath(path); n == 0 {
		t.Fatal("重启后应恢复自愈进度")
	}
	m2.careMu.Lock()
	streak := m2.careRecs["a"].HealStreak
	m2.careMu.Unlock()
	if streak != 1 {
		t.Fatalf("重启后 heal_streak 应保留 1, got %d", streak)
	}
	// 重启后第 2 次成功即恢复（证明进度没被清零）；先跨过 6h 低频间隔
	healAge(m2, "a", AutoCareHealInterval+time.Hour)
	if acts := m2.AutoCareHealPass(); len(acts) != 1 {
		t.Fatalf("重启后第 2 次成功应恢复, got %v", acts)
	}
	if !healEnabled(cfg, "a") {
		t.Fatal("重启后应能恢复启用")
	}
}

// 通知节流：同一源同一类通知 24h 内只放行一次（误判红线 6）。
func TestCareNoticeThrottle24h(t *testing.T) {
	now := time.Now()
	var rec careRec
	if !rec.allowNotice(CareEventDisabled, now) {
		t.Fatal("首次应放行")
	}
	if rec.allowNotice(CareEventDisabled, now.Add(23*time.Hour)) {
		t.Fatal("24h 内同类通知必须被节流")
	}
	// 不同类别互不影响（停用与恢复各自 24h）
	if !rec.allowNotice(CareEventRecovered, now.Add(time.Minute)) {
		t.Fatal("恢复类通知不应被停用类节流影响")
	}
	if !rec.allowNotice(CareEventDisabled, now.Add(25*time.Hour)) {
		t.Fatal("超过 24h 应放行")
	}
}

// 停用事件：双门槛达成停用时入队一条（Notify=true），且 Drain 只取一次。
func TestCareDisableEmitsOneEvent(t *testing.T) {
	m, _ := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 5; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	careAge(m, "a", 30*time.Hour)
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); len(acts) == 0 {
		t.Fatal("应停用")
	}
	ev := m.DrainCareEvents()
	if len(ev) != 1 || ev[0].Kind != CareEventDisabled || !ev[0].Notify {
		t.Fatalf("停用应产生一条通知事件, got %+v", ev)
	}
	if ev[0].Name != "a" || ev[0].Streak != 6 || ev[0].Reason == "" {
		t.Fatalf("事件内容不符: %+v", ev[0])
	}
	// 已停用源在后续轮次不得重复产生事件
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	if again := m.DrainCareEvents(); again != nil {
		t.Fatalf("停用后不得重复告警, got %+v", again)
	}
}

// /api/sources 的 disabled_by 来源：Sources() 必须回填策略停用标记。
func TestCareSourcesExposeDisabledBy(t *testing.T) {
	m, cfg, _ := healManager(t, map[string]int{})
	healDisable(m, cfg, "b")
	seen := map[string]string{}
	for _, st := range m.Sources() {
		seen[st.Name] = st.DisabledBy
	}
	if seen["b"] != careDisabledByAuto {
		t.Fatalf("策略停用源应回填 disabled_by=auto-care, got %q", seen["b"])
	}
	if seen["a"] != "" {
		t.Fatalf("正常源不应带 disabled_by, got %q", seen["a"])
	}
}
