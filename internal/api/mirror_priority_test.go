package api

import (
	"testing"

	"moo/internal/config"
)

// setGhStats 测试辅助：直接写入模拟健康数据。
func setGhStats(t *testing.T, m *mirrorMonitor, stats []MirrorStat) {
	t.Helper()
	m.mu.Lock()
	m.ghStats = stats
	m.mu.Unlock()
}

// 手选源未失效 → 手选优先，其后按吞吐序补位。
func TestGhOrderedKeys_ManualPriority(t *testing.T) {
	m := NewMirrorMonitor()
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "ok", SpeedBPS: 50},
		{Key: "c", Status: "ok", SpeedBPS: 10},
	})
	s := &Server{Mirrors: m, Cfg: &config.Config{Mirror: "b"}}
	keys := s.ghOrderedKeys()
	if len(keys) != 3 || keys[0] != "b" || keys[1] != "a" || keys[2] != "c" {
		t.Fatalf("手选应优先: %v", keys)
	}
}

// 手选源最近测速 fail → 剔除手选，纯智能优选；恢复后自动回首位（同一函数按数据驱动）。
func TestGhOrderedKeys_ManualFailedFallback(t *testing.T) {
	m := NewMirrorMonitor()
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "fail", ConsecFails: 3},
		{Key: "c", Status: "ok", SpeedBPS: 10},
	})
	s := &Server{Mirrors: m, Cfg: &config.Config{Mirror: "b"}}
	keys := s.ghOrderedKeys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "c" {
		t.Fatalf("手选失效应纯智能优选: %v", keys)
	}
	// b 恢复 ok → 自动回到首位
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "ok", SpeedBPS: 50},
		{Key: "c", Status: "ok", SpeedBPS: 10},
	})
	keys = s.ghOrderedKeys()
	if keys[0] != "b" {
		t.Fatalf("手选恢复应回到首位: %v", keys)
	}
}

// auto = 吞吐序；direct = 空；custom 排首位。
func TestGhOrderedKeys_AutoDirectCustom(t *testing.T) {
	m := NewMirrorMonitor()
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 1},
		{Key: "b", Status: "ok", SpeedBPS: 9},
	})
	s := &Server{Mirrors: m, Cfg: &config.Config{Mirror: "auto"}}
	keys := s.ghOrderedKeys()
	if len(keys) != 2 || keys[0] != "b" {
		t.Fatalf("auto 应吞吐序: %v", keys)
	}
	s.Cfg.Mirror = "direct"
	if keys := s.ghOrderedKeys(); keys != nil {
		t.Fatalf("direct 应空: %v", keys)
	}
	s.Cfg.Mirror = "custom"
	s.Cfg.CustomGitHubMirror = "https://my.proxy/"
	keys = s.ghOrderedKeys()
	if len(keys) != 3 || keys[0] != "custom" || keys[1] != "b" {
		t.Fatalf("custom 应排首位: %v", keys)
	}
}

// healthOf：手选失效 → active 切最快源 + 切换原因可见；手选未失效 → active=手选。
func TestHealthOf_ManualFailedSwitchVisible(t *testing.T) {
	m := NewMirrorMonitor()
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "fail", ConsecFails: 2},
	})
	s := &Server{Mirrors: m, Cfg: &config.Config{Mirror: "b"}}
	// 初始态：手选 b 未失效 → active=b（初始值不记切换）
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "ok", SpeedBPS: 10},
	})
	h0 := s.healthOf("b", "gh", m.gh())
	if h0.Active != "b" {
		t.Fatalf("手选未失效应优先: %v", h0.Active)
	}
	if h0.LastSwitch != nil {
		t.Fatalf("初始值不应记切换: %+v", h0.LastSwitch)
	}
	// b 失效 → active 切 a + 切换可见
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "fail", ConsecFails: 2},
	})
	h := s.healthOf("b", "gh", m.gh())
	if h.Active != "a" {
		t.Fatalf("手选失效 active 应切最快源: %v", h.Active)
	}
	if h.LastSwitch == nil || h.LastSwitch.From != "b" || h.LastSwitch.To != "a" ||
		h.LastSwitch.Reason != "手选源已失效，自动切换智能优选" {
		t.Fatalf("切换记录错误: %+v", h.LastSwitch)
	}
	setGhStats(t, m, []MirrorStat{
		{Key: "a", Status: "ok", SpeedBPS: 100},
		{Key: "b", Status: "ok", SpeedBPS: 10},
	})
	h2 := s.healthOf("b", "gh", m.gh())
	if h2.Active != "b" {
		t.Fatalf("手选未失效应优先: %v", h2.Active)
	}
}
