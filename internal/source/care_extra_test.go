package source

import (
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 红线 6（告警节流/幂等）：源被策略停用后，后续轮次不得重复「已自动停用」
// 动作与告警（同一源只处理一次）。
func TestCareFailNoRepeatAfterDisable(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 5; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	careAge(m, "a", 25*time.Hour)
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); len(acts) == 0 {
		t.Fatal("首轮应停用")
	}
	for i := 0; i < 3; i++ {
		acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
		for _, a := range acts {
			if strings.Contains(a, "已自动停用") {
				t.Fatalf("停用后不得重复动作（第 %d 轮重放）: %v", i+1, acts)
			}
		}
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Error("源应保持停用（不得被重放复活）")
	}
}

// 误判侧⑥（200 分支）：已达双门槛，但停用前复核显示源可达 → 不停用，且
// 失败计数清零（下一轮重新起算）。
func TestCareFailVerifyAliveResetsStreak(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 5; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	careAge(m, "a", 30*time.Hour)
	m.CareProbe = func(name, url string) CareVerdict { return CareAlive }
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
		t.Fatalf("复核可达时不得停用, got %v", acts)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("复核可达时源应保持启用")
	}
	if got := careFailCount(m, "a"); got != 0 {
		t.Fatalf("复核可达应清零失败计数, got %d", got)
	}
}

// 换了仓库地址 = 换了源：旧计数不得沿用到新地址（否则一次改名即被误杀）。
func TestCareFailURLChangeResetsStreak(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 4; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	if got := careFailCount(m, "a"); got != 4 {
		t.Fatalf("前置：4 轮后应为 4, got %d", got)
	}
	careSource(cfg, "a").URL = "https://a.example/another/fnpack.json"
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	if got := careFailCount(m, "a"); got != 1 {
		t.Fatalf("换地址后计数应重新起算（1）, got %d", got)
	}
}

// 用户手动开关源：启用视为「我确认要跑」→ 计数与策略标记全清（1b 自愈
// 通道只允许复活 auto-care 停用的源）；停用则清掉策略标记，避免被自愈复活。
func TestCareToggleManualResets(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 3; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	m.careMu.Lock()
	m.careEmpty["a"] = 3
	m.careRecs["a"] = careRec{FailStreak: 3, FirstFailAt: time.Now().Add(-30 * time.Hour),
		LastError: "仓库不存在（HTTP 404）", URL: cfg.Sources[0].URL, DisabledBy: "auto-care"}
	m.careMu.Unlock()

	if err := m.ToggleSource("a", false); err != nil {
		t.Fatal(err)
	}
	m.careMu.Lock()
	dby := m.careRecs["a"].DisabledBy
	m.careMu.Unlock()
	if dby != "" {
		t.Fatalf("用户手动停用应清掉 auto-care 标记, got %q", dby)
	}
	if err := m.ToggleSource("a", true); err != nil {
		t.Fatal(err)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("手动启用后源应启用")
	}
	if got := careFailCount(m, "a"); got != 0 {
		t.Fatalf("手动启用应清零失败计数, got %d", got)
	}
	m.careMu.Lock()
	empty := m.careEmpty["a"]
	m.careMu.Unlock()
	if empty != 0 {
		t.Fatalf("手动启用应清零空源计数, got %d", empty)
	}
}

// 红线 3 的「≥5 轮」这一半：跨过 24h 后仍必须凑满 5 轮才动手（否则等于
// 「首次失败 24h 后一次就杀」，误杀面巨大）。
func TestCareFailThresholdCountsRounds(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	careAge(m, "a", 30*time.Hour) // 时间门槛已过，只剩轮数门槛
	for i := 0; i < 3; i++ {
		if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
			t.Fatalf("第 %d 轮（累计 %d < 5）不应停用: %v", i+2, i+2, acts)
		}
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("未满 5 轮不得停用")
	}
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); len(acts) == 0 {
		t.Fatal("满 5 轮且跨 24h 应停用")
	}
}

// P2-5：已停用源不参与目录重建（省无效抓取、也防停用卡片继续出现）。
func TestCareDisabledSourceOutOfCatalogApps(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceRef{{Name: "a", URL: "https://a.example/fnpack.json"}}}
	m := NewManager(cfg)
	m.cache["a"] = map[string]*App{"x": {Name: "x", DisplayName: "X"}}
	if n := len(m.Apps("", "")); n != 1 {
		t.Fatalf("前置：启用源应有 1 条, got %d", n)
	}
	off := false
	cfg.Sources[0].Enabled = &off
	if n := len(m.Apps("", "")); n != 0 {
		t.Fatalf("已停用源不得参与目录重建, got %d 条", n)
	}
}
