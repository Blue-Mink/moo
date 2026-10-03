package source

import (
	"strings"
	"testing"

	"moo/internal/config"
)

func careBool(b bool) *bool { return &b }

// careManager 造一个 3 源（a/b/c）管理器。
func careManager(t *testing.T, off bool) (*Manager, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		SourceAutoCareOff: off,
		Sources: []config.SourceRef{
			{Name: "a", URL: "https://a.example/fnpack.json"},
			{Name: "b", URL: "https://b.example/fnpack.json"},
			{Name: "c", URL: "https://c.example/fnpack.json"},
		},
	}
	return NewManager(cfg), cfg
}

// careRound 按 counts 造一轮全量刷新状态（err 里的源带错误）。
func careRound(counts map[string]int, err map[string]string) []SourceStatus {
	var sts []SourceStatus
	for _, n := range []string{"a", "b", "c"} {
		st := SourceStatus{Name: n}
		if e, ok := err[n]; ok {
			st.Error = e
		} else if c, ok := counts[n]; ok {
			st.Count = c
		}
		sts = append(sts, st)
	}
	return sts
}

func careSource(cfg *config.Config, name string) *config.SourceRef {
	for i := range cfg.Sources {
		if cfg.Sources[i].Name == name {
			return &cfg.Sources[i]
		}
	}
	return nil
}

// TestAutoCarePassDisablesAndSinks 连续 5 轮空 → 自动停用；空源当轮即沉底。
func TestAutoCarePassDisablesAndSinks(t *testing.T) {
	m, cfg := careManager(t, false)
	counts := map[string]int{"a": 0, "b": 2, "c": 0}

	// 第 1 轮：无停用（未达阈值），但 a/c 沉底
	acts := m.AutoCarePass(careRound(counts, nil))
	if len(acts) == 0 {
		t.Fatal("第 1 轮应有空源沉底动作")
	}
	if got := [3]string{cfg.Sources[0].Name, cfg.Sources[1].Name, cfg.Sources[2].Name}; got != [3]string{"b", "a", "c"} {
		t.Errorf("空源应沉底保序 [b a c], got %v", got)
	}
	if careSource(cfg, "a").IsEnabled() == false || careSource(cfg, "c").IsEnabled() == false {
		t.Error("1 轮不应停用源")
	}

	// 第 2-4 轮：无新动作（顺序已稳定、计数未达阈值）
	for i := 2; i <= 4; i++ {
		if acts := m.AutoCarePass(careRound(counts, nil)); acts != nil {
			t.Errorf("第 %d 轮应无动作, got %v", i, acts)
		}
	}

	// 第 5 轮：a/c 达到阈值 → 停用
	acts = m.AutoCarePass(careRound(counts, nil))
	if len(acts) == 0 {
		t.Fatal("第 5 轮应触发自动停用")
	}
	for _, n := range []string{"a", "c"} {
		if careSource(cfg, n).IsEnabled() {
			t.Errorf("源 %s 应被自动停用", n)
		}
	}
	if !careSource(cfg, "b").IsEnabled() {
		t.Error("有应用的源 b 不应被停用")
	}

	// 停用后计数清零：再来一轮空也不重复停用/动作
	if acts := m.AutoCarePass(careRound(counts, nil)); acts != nil {
		t.Errorf("停用后应无重复动作, got %v", acts)
	}
}

// TestAutoCarePassResetOnApps 有应用的一轮清零计数，4+1+4 不应停用。
func TestAutoCarePassResetOnApps(t *testing.T) {
	m, cfg := careManager(t, false)
	empty := map[string]int{"a": 0, "b": 2, "c": 1}
	full := map[string]int{"a": 3, "b": 2, "c": 1}

	for i := 0; i < 4; i++ {
		m.AutoCarePass(careRound(empty, nil))
	}
	m.AutoCarePass(careRound(full, nil)) // 有应用 → 清零
	for i := 0; i < 4; i++ {
		m.AutoCarePass(careRound(empty, nil))
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Error("4+1+4 未连续 5 轮空，不应停用")
	}
}

// TestAutoCarePassErrorNotCounted 刷新失败不计空源（网络故障≠空源）、不沉底。
func TestAutoCarePassErrorNotCounted(t *testing.T) {
	m, cfg := careManager(t, false)
	for i := 0; i < 5; i++ {
		acts := m.AutoCarePass(careRound(map[string]int{"b": 2, "c": 1}, map[string]string{"a": "timeout"}))
		if acts != nil {
			t.Fatalf("失败源不应触发任何动作（第 %d 轮）, got %v", i+1, acts)
		}
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Error("连续刷新失败不应停用源")
	}
	if cfg.Sources[0].Name != "a" {
		t.Error("失败源不应沉底")
	}
}

// TestAutoCarePassDisabled 策略关闭时完全 no-op。
func TestAutoCarePassDisabled(t *testing.T) {
	m, cfg := careManager(t, true)
	for i := 0; i < 6; i++ {
		acts := m.AutoCarePass(careRound(map[string]int{"a": 0, "b": 2, "c": 0}, nil))
		if acts != nil {
			t.Fatalf("策略关闭应无动作, got %v", acts)
		}
	}
	if !careSource(cfg, "a").IsEnabled() || !careSource(cfg, "c").IsEnabled() {
		t.Error("策略关闭不应停用源")
	}
	if cfg.Sources[0].Name != "a" {
		t.Error("策略关闭不应重排源")
	}
}

// TestAutoCarePassManualDisabledUntouched 用户手动停用的源保留原位、不重复处理。
func TestAutoCarePassManualDisabledUntouched(t *testing.T) {
	cfg := &config.Config{
		Sources: []config.SourceRef{
			{Name: "a", URL: "https://a.example/fnpack.json"},
			{Name: "b", URL: "https://b.example/fnpack.json"},
			{Name: "c", URL: "https://c.example/fnpack.json", Enabled: careBool(false)},
		},
	}
	m := NewManager(cfg)
	// c 用户手动停用：全量刷新轮不含它（RefreshAllConcurrent 只刷启用源），
	// 本轮 sts 只有 a/b → c 不参与计数；a 空 → 当轮沉底（预期动作）
	acts := m.AutoCarePass([]SourceStatus{{Name: "a", Count: 0}, {Name: "b", Count: 2}})
	if len(acts) != 1 || !strings.Contains(acts[0], "空源沉底") {
		t.Fatalf("应仅空源沉底动作, got %v", acts)
	}
	// a 空 → 沉到底；c 未参与本轮刷新（停用），在非沉底组内保持相对序（[b c a]）
	if got := [3]string{cfg.Sources[0].Name, cfg.Sources[1].Name, cfg.Sources[2].Name}; got != [3]string{"b", "c", "a"} {
		t.Errorf("顺序应 [b c a], got %v", got)
	}
	if careSource(cfg, "c").Enabled == nil || *careSource(cfg, "c").Enabled {
		t.Error("停用源 c 的 Enabled 不应被改写")
	}
}
