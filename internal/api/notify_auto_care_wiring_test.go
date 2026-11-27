package api

// notify_auto_care_wiring_test.go —— 2026-11-26 CARE P1-1c 的接线验证：
// 策略停用/自愈恢复真的会走到通知（不是只有排版函数能画图）。
//
// 手法：把「上一版留下的 source_care.json」写到临时目录再 SetCarePath 加载
// （等价运行了 5 轮 + 跨 24h），全程零网络（探测钩子注入、通知开关关闭）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

type careFixture struct {
	URL       string    `json:"url,omitempty"`
	Fail      int       `json:"fail_streak,omitempty"`
	FirstFail time.Time `json:"first_fail_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Disabled  string    `json:"disabled_by,omitempty"`
	Heal      int       `json:"heal_streak,omitempty"`
	LastHeal  time.Time `json:"last_heal_at,omitempty"`
}

func writeCareFixture(t *testing.T, rec careFixture) string {
	t.Helper()
	f := map[string]any{"saved_at": time.Now().Format(time.RFC3339),
		"records": map[string]any{"a": rec}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source_care.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func careTestConfig(url string, off bool) *config.Config {
	return &config.Config{
		Sources: []config.SourceRef{{Name: "a", URL: url, Enabled: &off}},
	}
}

// 停用 → 通知：双门槛达成 → 一条 source_auto_disabled 记录。
func TestAutoCareNotifyWiringDisabled(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	url := "https://a.example/fnpack.json"
	cfg := careTestConfig(url, true)
	cfg.NotifyEnabled = boolPtr(false) // 只验逻辑：不外发（应用内记录照落）
	m := source.NewManager(cfg)
	t.Cleanup(m.WaitCareFlush)
	m.CareProbe = func(name, u string) source.CareVerdict { return source.CarePermanentlyDead }
	m.SetCarePath(writeCareFixture(t, careFixture{
		URL: url, Fail: 4, FirstFail: time.Now().Add(-30 * time.Hour),
		LastError: "仓库不存在（HTTP 404）",
	}))
	s := &Server{Cfg: cfg, Src: m}

	acts := m.AutoCarePass([]source.SourceStatus{{Name: "a", Error: "x: HTTP 404"}})
	if len(acts) == 0 {
		t.Fatal("双门槛达成应停用")
	}
	if n := countNotifyEvent(cfg, "source_auto_disabled"); n != 0 {
		t.Fatalf("通知轮之前不应有记录: %d", n)
	}
	s.autoCareNotifyPass()
	if n := countNotifyEvent(cfg, "source_auto_disabled"); n != 1 {
		t.Fatalf("停用后应推一条通知, got %d", n)
	}
	last := lastNotifyEvent(cfg, "source_auto_disabled")
	if last.OK {
		t.Fatal("停用属告警，OK 应为 false（渠道配色/图标依赖它）")
	}
	if !strings.HasSuffix(last.Msg, "源自动停用") {
		t.Fatalf("标题应为类别名: %q", last.Msg)
	}
	// 再取一次事件队列：空（幂等，不会重复推）
	s.autoCareNotifyPass()
	if n := countNotifyEvent(cfg, "source_auto_disabled"); n != 1 {
		t.Fatalf("事件只能消费一次, got %d", n)
	}
}

// 自愈恢复 → 通知：连续 2 次探测到有应用 → 一条 source_auto_recovered。
func TestAutoCareNotifyWiringRecovered(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	url := "https://a.example/fnpack.json"
	cfg := careTestConfig(url, false) // 源已停用（enabled=false）
	cfg.NotifyEnabled = boolPtr(false)
	m := source.NewManager(cfg)
	t.Cleanup(m.WaitCareFlush)
	probes := 0
	m.CareHealProbe = func(name, u string) int { probes++; return 4 }
	m.SetCarePath(writeCareFixture(t, careFixture{
		URL: url, Fail: 5, FirstFail: time.Now().Add(-48 * time.Hour),
		LastError: "仓库不存在（HTTP 404）", Disabled: "auto-care",
		Heal: 1, LastHeal: time.Now().Add(-8 * time.Hour),
	}))
	s := &Server{Cfg: cfg, Src: m}

	if acts := m.AutoCareHealPass(); len(acts) != 1 {
		t.Fatalf("连续 2 次探测到有应用应恢复, got %v", acts)
	}
	if !cfg.Sources[0].IsEnabled() {
		t.Fatal("恢复后源应启用")
	}
	s.autoCareNotifyPass()
	if n := countNotifyEvent(cfg, "source_auto_recovered"); n != 1 {
		t.Fatalf("恢复后应推一条通知, got %d", n)
	}
	if last := lastNotifyEvent(cfg, "source_auto_recovered"); !last.OK || !strings.HasSuffix(last.Msg, "源自动恢复") {
		t.Fatalf("恢复通知应为成功态: %+v", last)
	}
	if probes != 1 {
		t.Fatalf("应只探测 1 次（6h 节流前已探过 1 次）, got %d", probes)
	}
}

// /api/sources 的 disabled_by 只读字段（前端分因展示的依据）。
func TestSourceEntryDisabledByJSON(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{Src: source.NewManager(cfg), Cfg: cfg}
	e := s.sourceEntry(source.SourceStatus{Name: "a", DisabledBy: "auto-care"})
	if e.DisabledBy != "auto-care" {
		t.Fatalf("应透出 disabled_by, got %q", e.DisabledBy)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got["disabled_by"]) != "auto-care" {
		t.Fatalf("JSON 应含 disabled_by: %s", raw)
	}
	// 手动停用的源（无标记）不该带这个键
	raw2, _ := json.Marshal(s.sourceEntry(source.SourceStatus{Name: "b"}))
	var got2 map[string]any
	_ = json.Unmarshal(raw2, &got2)
	if _, ok := got2["disabled_by"]; ok {
		t.Fatalf("手动停用源不应带 disabled_by: %s", raw2)
	}
}
