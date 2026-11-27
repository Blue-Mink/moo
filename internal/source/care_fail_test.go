package source

import (
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 2026-11-26 CARE（批次 1a）：失败通道的误判侧用例。
// 每个用例对应 BACKLOG §B-SRC-CARE-DEADFEED「误判红线」的一条，缺一不可。
// 全部用注入的 CareProbe 假实现，绝不访问网络。

// careFailManager 造 3 源管理器；verdict 为「复核结论」固定值。
func careFailManager(t *testing.T, verdict CareVerdict) (*Manager, *config.Config) {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "a", URL: "https://a.example/fnpack.json"},
		{Name: "b", URL: "https://b.example/fnpack.json"},
		{Name: "c", URL: "https://c.example/fnpack.json"},
	}}
	m := NewManager(cfg)
	m.CareProbe = func(name, url string) CareVerdict { return verdict }
	return m, cfg
}

// careFailRound 造一轮：bad 里的源刷新失败（errText），其余成功。
func careFailRound(bad []string, errText string, count int) []SourceStatus {
	var sts []SourceStatus
	for _, n := range []string{"a", "b", "c"} {
		st := SourceStatus{Name: n, Count: count}
		for _, b := range bad {
			if b == n {
				st.Error = errText
				st.Count = 0
			}
		}
		sts = append(sts, st)
	}
	return sts
}

func careFailCount(m *Manager, name string) int {
	for _, st := range m.Sources() {
		if st.Name == name {
			return st.FailStreak
		}
	}
	return -1
}

// careAge 把某源的「首次失败时间」往前拨，跨过 24h 双门槛（不模拟长跑）。
func careAge(m *Manager, name string, d time.Duration) {
	m.careMu.Lock()
	defer m.careMu.Unlock()
	rec := m.careRecs[name]
	rec.FirstFailAt = rec.FirstFailAt.Add(-d)
	m.careRecs[name] = rec
}

// 正向：连续 5 轮确定性 404（且已跨 24h）→ 第 5 轮停用 + 沉底 + 动作文案。
func TestCareFailDisablesAfterThreshold(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 1; i <= 4; i++ {
		if acts := m.AutoCarePass(careFailRound([]string{"a"}, "所有候选地址均失败: x: HTTP 404", 2)); acts != nil {
			t.Fatalf("第 %d 轮不应有动作, got %v", i, acts)
		}
	}
	if got := careFailCount(m, "a"); got != 4 {
		t.Fatalf("4 轮后 fail_streak 应为 4, got %d", got)
	}
	// 第 5 轮：未满 24h → 双门槛未满足，不动手
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
		t.Fatalf("不足 24h 不应停用, got %v", acts)
	}
	if careSource(cfg, "a").IsEnabled() == false {
		t.Fatal("不足 24h 时源仍应启用")
	}
	careAge(m, "a", 25*time.Hour)
	acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	if len(acts) == 0 {
		t.Fatal("第 5 轮 + 跨 24h 应触发自动停用")
	}
	joined := strings.Join(acts, " | ")
	if !strings.Contains(joined, "已自动停用") || !strings.Contains(joined, "a") {
		t.Fatalf("动作文案应说明已自动停用: %v", acts)
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Error("源 a 应被自动停用")
	}
	if careSource(cfg, "a").URL != "https://a.example/fnpack.json" {
		t.Error("停用是可逆标记：必须保留原始 URL")
	}
	if got := careFailCount(m, "a"); got < AutoCareFailThreshold {
		t.Errorf("停用必须保留历史计数（红线 5）, got %d", got)
	}
	if cfg.Sources[2].Name != "a" {
		t.Errorf("死源应沉底（末位）, got 顺序 %v", []string{cfg.Sources[0].Name, cfg.Sources[1].Name, cfg.Sources[2].Name})
	}
	m.careMu.Lock()
	dby := m.careRecs["a"].DisabledBy
	m.careMu.Unlock()
	if dby != "auto-care" {
		t.Errorf("应标记 disabled_by=auto-care（1b 自愈需区分用户停用）, got %q", dby)
	}
}

// 误判侧①：4 轮失败 + 第 5 轮成功 → 计数清零、不停用。
func TestCareFailResetOnSuccess(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 4; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	careAge(m, "a", 48*time.Hour) // 即使跨过 24h
	m.AutoCarePass(careFailRound(nil, "", 3))
	if got := careFailCount(m, "a"); got != 0 {
		t.Fatalf("失败后成功一轮应清零, got %d", got)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Error("计数已清零，不应停用")
	}
}

// 误判侧⑤：5 轮确定性失败但距首次失败不足 24h → 不停用（双门槛）。
func TestCareFailNeeds24hWindow(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 8; i++ {
		if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
			t.Fatalf("第 %d 轮（<24h）不应停用, got %v", i+1, acts)
		}
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Error("不足 24h 不得停用")
	}
	if got := careFailCount(m, "a"); got != 8 {
		t.Errorf("计数应继续累积（不停用不等于不计数）, got %d", got)
	}
}
