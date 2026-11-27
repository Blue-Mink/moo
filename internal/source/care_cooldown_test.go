package source

import (
	"strings"
	"testing"
	"time"
)

// 2026-11-26 CARE 冷却批次：空源通道的分因/通知、72h 冷却窗、抖动上限
// （30 天 2 次）、触顶降频、手动开关清零。全程零网络（探测钩子注入）。

// 空源停用必须与失败通道同待遇：标记 auto-care（进自愈）、留 0 自愈进度、
// 发一条 Empty=true 的停用事件（否则源成死胡同：界面当手动关闭、无人通知、
// 永远等不到自动恢复）。
func TestCareEmptyDisableMarksAutoAndEmits(t *testing.T) {
	m, cfg := careManager(t, false)
	counts := map[string]int{"a": 0, "b": 2, "c": 1}
	for i := 0; i < AutoCareThreshold; i++ {
		m.AutoCarePass(careRound(counts, nil))
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Fatal("连续 5 轮空应停用源 a")
	}
	m.careMu.Lock()
	rec := m.careRecs["a"]
	m.careMu.Unlock()
	if rec.DisabledBy != careDisabledByAuto {
		t.Fatalf("空源停用也必须标记 disabled_by=auto-care（否则不进自愈）, got %q", rec.DisabledBy)
	}
	if rec.HealStreak != 0 || rec.HealCapped {
		t.Fatalf("新一轮停用：自愈进度与上限标记应归零, got %+v", rec)
	}
	ev := m.DrainCareEvents()
	if len(ev) != 1 {
		t.Fatalf("空源停用应发一条通知事件, got %+v", ev)
	}
	if ev[0].Kind != CareEventDisabled || !ev[0].Empty || !ev[0].Notify {
		t.Fatalf("事件应为首轮放行的空源停用事件, got %+v", ev[0])
	}
	if ev[0].Name != "a" || ev[0].Streak != AutoCareThreshold {
		t.Fatalf("事件内容不符: %+v", ev[0])
	}
	if !strings.Contains(ev[0].Reason, "刷新无应用") {
		t.Fatalf("空源事件原因应写「刷新无应用」而不是刷新失败, got %q", ev[0].Reason)
	}
}

// 72h 冷却窗（仅空源通道）：刚被自愈恢复的源在窗口内不因「还是空」被再停；
// 计数继续累积（不 delete careEmpty），到期后下一轮立即停用，不重新数 5 轮。
func TestCareEmptyCooldown72h(t *testing.T) {
	m, cfg := careManager(t, false)
	now := time.Now()
	m.careMu.Lock()
	m.careRecs["a"] = careRec{URL: cfg.Sources[0].URL, LastRecoveredAt: now.Add(-71 * time.Hour)}
	m.careMu.Unlock()
	counts := map[string]int{"a": 0, "b": 2, "c": 1}

	for i := 0; i < AutoCareThreshold; i++ {
		m.AutoCarePass(careRound(counts, nil))
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("71h < 72h 冷却窗：不得因空源停用")
	}
	m.careMu.Lock()
	streak := m.careEmpty["a"]
	m.careMu.Unlock()
	if streak < AutoCareThreshold {
		t.Fatalf("冷却期内计数应继续累积（不许 delete）, got %d", streak)
	}

	// 冷却到期：计数继续沿用（不重新起算），补到「放宽后的阈值」即停用。
	//
	// 2026-11-27（F11/N12）：曾自愈过的源空源阈值改为
	// AutoCareEmptyThresholdHealed（10，原 5）——本用例是「自愈过的源」，所以
	// 冷却到期后还要再空几轮才到 10，不再是「下一轮立即停用」。
	m.careMu.Lock()
	rec := m.careRecs["a"]
	rec.LastRecoveredAt = now.Add(-73 * time.Hour)
	m.careRecs["a"] = rec
	m.careMu.Unlock()
	rounds := 0
	for careSource(cfg, "a").IsEnabled() && rounds < 20 {
		m.AutoCarePass(careRound(counts, nil))
		rounds++
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Fatalf("冷却到期且计数累积到 %d 轮后应停用", AutoCareEmptyThresholdHealed)
	}
	if want := AutoCareEmptyThresholdHealed - streak; rounds != want {
		t.Fatalf("应续算到 %d 轮（已有 %d，再空 %d 轮），实际再空 %d 轮",
			AutoCareEmptyThresholdHealed, streak, want, rounds)
	}
}

// 抖动上限：30 天内已自动恢复 2 次 → 再满足恢复条件也保持停用，只发一次
// 触顶事件；HealStreak 钉在门槛值（不每轮刷日志/不重复推）。
func TestCareHealCapTwoPer30d(t *testing.T) {
	m, cfg, calls := healManager(t, map[string]int{"a": 5})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	rec := m.careRecs["a"]
	rec.HealCount = AutoCareHealCap
	rec.HealWindowStart = time.Now().Add(-24 * time.Hour)
	m.careRecs["a"] = rec
	m.careMu.Unlock()

	if acts := m.AutoCareHealPass(); len(acts) != 0 { // 第 1 次探测：只到 1 次
		t.Fatalf("首次探测成功不得触顶/恢复, got %v", acts)
	}
	healAge(m, "a", AutoCareHealInterval+time.Hour)
	acts := m.AutoCareHealPass() // 第 2 次：达恢复条件但已触顶
	if len(acts) != 1 || !strings.Contains(acts[0], "已达上限") {
		t.Fatalf("触顶应只记一次动作且说明上限, got %v", acts)
	}
	if healEnabled(cfg, "a") {
		t.Fatal("30 天窗口内恢复次数用尽：不得自动恢复")
	}
	m.careMu.Lock()
	rec = m.careRecs["a"]
	m.careMu.Unlock()
	if !rec.HealCapped {
		t.Fatal("应标记 heal_capped")
	}
	if rec.HealStreak != AutoCareHealStreak {
		t.Fatalf("触顶后 HealStreak 应钉在 %d, got %d", AutoCareHealStreak, rec.HealStreak)
	}
	ev := m.DrainCareEvents()
	if len(ev) != 1 || ev[0].Kind != CareEventHealCapped || !ev[0].Notify {
		t.Fatalf("应发一条触顶事件, got %+v", ev)
	}
	for _, e := range ev {
		if e.Kind == CareEventRecovered {
			t.Fatalf("触顶时不得发恢复事件: %+v", ev)
		}
	}
	// 再来一轮：不重复动作、不重复事件（触顶后降频到 24h）
	healAge(m, "a", AutoCareHealIntervalCapped+time.Hour)
	if acts := m.AutoCareHealPass(); len(acts) != 0 {
		t.Fatalf("触顶后的轮次不得重复动作, got %v", acts)
	}
	if again := m.DrainCareEvents(); again != nil {
		t.Fatalf("触顶事件只发一次, got %+v", again)
	}
	if *calls != 3 {
		t.Fatalf("应共探测 3 次, got %d", *calls)
	}
}

// 滚动窗口：窗口（30 天）到期先滚（计数归零、上限作废）→ 可再次自动恢复，
// 且新一轮窗口从本次恢复起算、计数为 1。
func TestCareHealCapWindowRolls(t *testing.T) {
	m, cfg, _ := healManager(t, map[string]int{"a": 4})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	rec := m.careRecs["a"]
	rec.HealCount = AutoCareHealCap
	rec.HealWindowStart = time.Now().Add(-(AutoCareHealWindow + 24*time.Hour))
	rec.HealCapped = true
	m.careRecs["a"] = rec
	m.careMu.Unlock()

	m.AutoCareHealPass() // 窗口滚 + 第 1 次成功
	healAge(m, "a", AutoCareHealIntervalCapped+time.Hour)
	if acts := m.AutoCareHealPass(); len(acts) != 1 || !strings.Contains(acts[0], "已自动恢复启用") {
		t.Fatalf("窗口到期后应能再次自动恢复, got %v", acts)
	}
	if !healEnabled(cfg, "a") {
		t.Fatal("窗口滚动后应恢复启用")
	}
	m.careMu.Lock()
	rec = m.careRecs["a"]
	m.careMu.Unlock()
	if rec.HealCount != 1 {
		t.Fatalf("新窗口计数应为 1, got %d", rec.HealCount)
	}
	if rec.HealCapped {
		t.Fatal("窗口滚动后上限标记应作废")
	}
	if rec.HealWindowStart.IsZero() || time.Since(rec.HealWindowStart) > time.Hour {
		t.Fatalf("新窗口起点应为本次恢复时间, got %v", rec.HealWindowStart)
	}
}

// 触顶降频：HealCapped 时探测间隔从 6h 拉到 24h（源还探，但不再频繁）。
func TestCareHealCappedProbeInterval(t *testing.T) {
	m, cfg, calls := healManager(t, map[string]int{"a": 5})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	rec := m.careRecs["a"]
	rec.HealCapped = true
	rec.HealCount = AutoCareHealCap
	rec.HealWindowStart = time.Now().Add(-24 * time.Hour)
	rec.LastHealAt = time.Now().Add(-7 * time.Hour)
	m.careRecs["a"] = rec
	m.careMu.Unlock()

	if acts := m.AutoCareHealPass(); len(acts) != 0 {
		t.Fatalf("触顶降频轮不应有动作, got %v", acts)
	}
	if *calls != 0 {
		t.Fatalf("触顶后 24h 内不得探测（6h 间隔已不适用）, got %d", *calls)
	}

	m.careMu.Lock()
	rec = m.careRecs["a"]
	rec.LastHealAt = time.Now().Add(-25 * time.Hour)
	m.careRecs["a"] = rec
	m.careMu.Unlock()
	m.AutoCareHealPass()
	if *calls != 1 {
		t.Fatalf("超过 24h 应探测一次, got %d", *calls)
	}
	if healEnabled(cfg, "a") {
		t.Fatal("触顶状态下探测成功也不得自动启用")
	}
}

// 用户手动开启 = 我确认要跑：冷却窗、恢复计数、窗口起点、上限与触顶通知
// 节流一并清零（否则用户刚开就被旧上限立刻再停用）。
func TestCareToggleManualClearsCapAndCooldown(t *testing.T) {
	m, cfg := careManager(t, false)
	now := time.Now()
	off := false
	cfg.Sources[0].Enabled = &off
	m.careMu.Lock()
	m.careRecs["a"] = careRec{URL: cfg.Sources[0].URL, DisabledBy: careDisabledByAuto,
		HealStreak: AutoCareHealStreak, HealCount: AutoCareHealCap, HealCapped: true,
		HealWindowStart: now.Add(-24 * time.Hour), LastRecoveredAt: now.Add(-48 * time.Hour),
		CappedNoticeAt: now.Add(-2 * time.Hour)}
	m.careMu.Unlock()

	if err := m.ToggleSource("a", true); err != nil {
		t.Fatalf("ToggleSource: %v", err)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("手动开启后源应启用")
	}
	m.careMu.Lock()
	rec := m.careRecs["a"]
	m.careMu.Unlock()
	if !rec.LastRecoveredAt.IsZero() {
		t.Fatalf("应清空 LastRecoveredAt（冷却窗作废）, got %v", rec.LastRecoveredAt)
	}
	if rec.HealCount != 0 {
		t.Fatalf("应清空 HealCount, got %d", rec.HealCount)
	}
	if !rec.HealWindowStart.IsZero() {
		t.Fatalf("应清空 HealWindowStart, got %v", rec.HealWindowStart)
	}
	if rec.HealCapped {
		t.Fatal("应清掉 HealCapped")
	}
	if !rec.CappedNoticeAt.IsZero() {
		t.Fatalf("应清空 CappedNoticeAt, got %v", rec.CappedNoticeAt)
	}
	if rec.DisabledBy != "" {
		t.Fatalf("应清掉策略停用标记, got %q", rec.DisabledBy)
	}
}
