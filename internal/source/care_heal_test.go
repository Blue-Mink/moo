package source

import (
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 2026-11-26 CARE（批次 1b）：自愈通道（P1-1b）的用例。
// 探测全部走注入的 CareHealProbe 假实现，绝不访问网络。

// healManager 造 3 源管理器 + 可控探测计数。
func healManager(t *testing.T, counts map[string]int) (*Manager, *config.Config, *int) {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "a", URL: "https://a.example/fnpack.json"},
		{Name: "b", URL: "https://b.example/fnpack.json"},
		{Name: "c", URL: "https://c.example/fnpack.json"},
	}}
	m := NewManager(cfg)
	m.CareProbe = func(name, url string) CareVerdict { return CareUncertain }
	calls := 0
	m.CareHealProbe = func(name, url string) int {
		calls++
		if n, ok := counts[name]; ok {
			return n
		}
		return careHealUnknown
	}
	return m, cfg, &calls
}

// healDisable 把某源置为「策略停用」状态（等价 1a 失败通道停用后的落盘态）。
func healDisable(m *Manager, cfg *config.Config, name string) {
	for i := range cfg.Sources {
		if cfg.Sources[i].Name != name {
			continue
		}
		off := false
		cfg.Sources[i].Enabled = &off
		m.careMu.Lock()
		m.careRecs[name] = careRec{URL: cfg.Sources[i].URL, FailStreak: 5,
			FirstFailAt: time.Now().Add(-30 * time.Hour), LastError: "仓库不存在（HTTP 404）",
			DisabledBy: careDisabledByAuto}
		m.careMu.Unlock()
	}
}

// healAge 把「上次探测时间」往前拨，跨过 6h 低频间隔（不模拟长跑）。
func healAge(m *Manager, name string, d time.Duration) {
	m.careMu.Lock()
	defer m.careMu.Unlock()
	rec := m.careRecs[name]
	rec.LastHealAt = rec.LastHealAt.Add(-d)
	m.careRecs[name] = rec
}

func healEnabled(cfg *config.Config, name string) bool {
	for i := range cfg.Sources {
		if cfg.Sources[i].Name == name {
			return cfg.Sources[i].IsEnabled()
		}
	}
	return false
}

// 正向：连续 2 次探测到有应用 → 恢复启用 + 一条恢复事件（Notify=true）。
// 仅 1 次不恢复（第二次失败 = 复活条件不成立）。
func TestCareHealNeedsTwoProbes(t *testing.T) {
	m, cfg, calls := healManager(t, map[string]int{"a": 3})
	healDisable(m, cfg, "a")

	// 第 1 次：只到 1 次，不恢复
	if acts := m.AutoCareHealPass(); len(acts) != 0 {
		t.Fatalf("仅 1 次探测成功不得恢复: %v", acts)
	}
	if healEnabled(cfg, "a") {
		t.Fatal("仅 1 次探测成功不得恢复启用")
	}
	if ev := m.DrainCareEvents(); len(ev) != 0 {
		t.Fatalf("未恢复不得产生通知事件: %+v", ev)
	}
	m.careMu.Lock()
	streak := m.careRecs["a"].HealStreak
	m.careMu.Unlock()
	if streak != 1 {
		t.Fatalf("第 1 次成功后 heal_streak 应为 1, got %d", streak)
	}

	// 6h 内再跑一轮：低频节流命中，不探测
	if acts := m.AutoCareHealPass(); len(acts) != 0 || *calls != 1 {
		t.Fatalf("6h 内不得重复探测（calls=%d, acts=%v）", *calls, acts)
	}

	// 拨过 6h：第 2 次成功 → 恢复
	healAge(m, "a", AutoCareHealInterval+time.Hour)
	acts := m.AutoCareHealPass()
	if len(acts) != 1 || !strings.Contains(acts[0], "已自动恢复启用") {
		t.Fatalf("连续 2 次成功应恢复启用, got %v", acts)
	}
	if !healEnabled(cfg, "a") {
		t.Fatal("恢复后源应启用")
	}
	ev := m.DrainCareEvents()
	if len(ev) != 1 || ev[0].Kind != CareEventRecovered || !ev[0].Notify || ev[0].Count != 3 {
		t.Fatalf("应产生一条恢复通知事件, got %+v", ev)
	}
	if again := m.DrainCareEvents(); again != nil {
		t.Fatalf("事件只能取走一次（幂等）, got %+v", again)
	}
}

// 探测不确定（超时/404/解析失败）→ 不恢复、不推进、也不清零。
func TestCareHealUnknownNoAdvance(t *testing.T) {
	m, cfg, calls := healManager(t, map[string]int{"a": careHealUnknown})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	m.careRecs["a"] = func() careRec { r := m.careRecs["a"]; r.HealStreak = 1; return r }()
	m.careMu.Unlock()

	for i := 0; i < 2; i++ {
		if acts := m.AutoCareHealPass(); len(acts) != 0 {
			t.Fatalf("探测不确定不得恢复（第 %d 轮）: %v", i+1, acts)
		}
		healAge(m, "a", AutoCareHealInterval+time.Minute)
	}
	m.careMu.Lock()
	rec := m.careRecs["a"]
	m.careMu.Unlock()
	if rec.HealStreak != 1 {
		t.Fatalf("不确定的探测不得推进/清零 heal_streak, got %d", rec.HealStreak)
	}
	if healEnabled(cfg, "a") {
		t.Fatal("探测不确定不得恢复启用")
	}
	// 6h 节流：LastHealAt 每轮推进（否则会每轮打爆网络）
	if rec.LastHealAt.IsZero() {
		t.Fatal("不确定的探测也要推进上次探测时间（低频约束）")
	}
	if *calls != 2 {
		t.Fatalf("应探测 2 次, got %d", *calls)
	}
}

// 索引能拉到但 0 应用 → 连续计数中断（不恢复）。
func TestCareHealZeroAppsResets(t *testing.T) {
	m, cfg, _ := healManager(t, map[string]int{"a": 0})
	healDisable(m, cfg, "a")
	m.careMu.Lock()
	rec := m.careRecs["a"]
	rec.HealStreak = 1
	m.careRecs["a"] = rec
	m.careMu.Unlock()

	m.AutoCareHealPass()
	m.careMu.Lock()
	streak := m.careRecs["a"].HealStreak
	m.careMu.Unlock()
	if streak != 0 {
		t.Fatalf("0 应用应把连续计数清零, got %d", streak)
	}
	if healEnabled(cfg, "a") {
		t.Fatal("0 应用不得恢复")
	}
}

// 用户手动停用的源永不被自愈复活（也绝不探测，省一次无效请求）。
func TestCareHealManualDisabledNever(t *testing.T) {
	m, cfg, calls := healManager(t, map[string]int{"a": 5, "b": 5})
	// a：用户手动停用（无 auto-care 标记）
	off := false
	cfg.Sources[0].Enabled = &off
	// 只有 a 被申请探测时才计数；若实现漏判就会探测到 a 并复活它
	m.CareHealProbe = func(name, url string) int { *calls++; return 5 }

	for i := 0; i < 3; i++ {
		if acts := m.AutoCareHealPass(); len(acts) != 0 {
			t.Fatalf("用户手动停用的源不得被自愈（第 %d 轮）: %v", i+1, acts)
		}
	}
	if healEnabled(cfg, "a") {
		t.Fatal("用户手动停用的源永不被自愈复活")
	}
	if *calls != 0 {
		t.Fatalf("不该为用户停用源发起探测, calls=%d", *calls)
	}
}
