package source

// 2026-11-27（F11 / 审计 N12）：空源通道的两条「宁可漏停」加固。
//
//  1. 解析旁证：本轮 0 条若来自「索引没被识别/无结构」（ParseFailed），
//     不得计入空源轮次，也不得沉底——否则上游一改格式，209 个源会在
//     5 轮（≈17h）内排队被停用。
//  2. 曾自愈过的源阈值放宽到 10 轮（AutoCareEmptyThresholdHealed）。

import (
	"strings"
	"testing"
	"time"
)

// 索引结构未识别的 0 条：连跑 10 轮也不得推进空源计数、不得停用、不得沉底。
func TestCareEmptyParseFailedNeverCounts(t *testing.T) {
	m, cfg := careManager(t, false)
	for i := 0; i < 10; i++ {
		acts := m.AutoCarePass([]SourceStatus{
			{Name: "a", Count: 0, ParseFailed: true},
			{Name: "b", Count: 2},
			{Name: "c", Count: 2},
		})
		if len(acts) != 0 {
			t.Fatalf("第 %d 轮：解析失败的空轮不得产生动作: %v", i+1, acts)
		}
	}
	if got := m.careEmpty["a"]; got != 0 {
		t.Fatalf("解析失败的轮次不得计入空源计数, got %d", got)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("解析失败不得停用源")
	}
	if cfg.Sources[0].Name != "a" {
		t.Fatalf("解析失败不得沉底: %v", cfg.Sources[0].Name)
	}
}

// 解析失败也不得把已有计数清零（与失败通道的「不确定」同口径：不增不减）。
func TestCareEmptyParseFailedKeepsExistingStreak(t *testing.T) {
	m, _ := careManager(t, false)
	m.careEmpty["a"] = 3
	m.AutoCarePass([]SourceStatus{{Name: "a", Count: 0, ParseFailed: true}, {Name: "b", Count: 1}, {Name: "c", Count: 1}})
	if got := m.careEmpty["a"]; got != 3 {
		t.Fatalf("解析失败既不加也不清零，应保持 3，实际 %d", got)
	}
}

// 曾自愈过的源：9 轮不到阈值（旧实现第 5 轮就停用），第 10 轮才停用。
func TestCareEmptyHealedSourceThresholdRelaxed(t *testing.T) {
	m, cfg := careManager(t, false)
	// 曾自愈过（HealCount=1），且已过 72h 冷却窗（否则冷却窗会先挡住）。
	rec := m.careRecordLocked("a")
	rec.HealCount = 1
	rec.LastRecoveredAt = time.Now().Add(-80 * time.Hour)
	m.setCareRecordLocked("a", rec)

	round := []SourceStatus{{Name: "a", Count: 0}, {Name: "b", Count: 2}, {Name: "c", Count: 2}}
	for i := 0; i < AutoCareEmptyThresholdHealed-1; i++ {
		m.AutoCarePass(round) // 首轮会有「空源沉底」动作，只看是否停用
		if !careSource(cfg, "a").IsEnabled() {
			t.Fatalf("第 %d 轮就被停用了（阈值没放宽到 %d）", i+1, AutoCareEmptyThresholdHealed)
		}
	}
	acts := m.AutoCarePass(round)
	if len(acts) == 0 {
		t.Fatalf("第 %d 轮应停用", AutoCareEmptyThresholdHealed)
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Fatal("达到放宽后的阈值仍应停用")
	}
	if !strings.Contains(strings.Join(acts, " "), "刷新无应用") {
		t.Fatalf("动作文案应说明空源原因: %v", acts)
	}
}
