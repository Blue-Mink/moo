package source

import (
	"strings"
	"testing"
	"time"
)

// 误判侧②：暂态错误（超时/连接重置/5xx/429）各跑 10 轮 → 不停用、不沉底、
// 计数恒 0。复核结论在暂态场景下必然是 CareUncertain（见 care_probe_test）——
// 这里同时验证「无论复核给 uncertain 还是 alive，都不动源」。
func TestCareFailTransientNeverCounts(t *testing.T) {
	errs := []string{
		"所有候选地址均失败: x: context deadline exceeded",
		"所有候选地址均失败: x: read tcp: connection reset by peer",
		"所有候选地址均失败: x: HTTP 503",
		"所有候选地址均失败: x: HTTP 429",
		"所有候选地址均失败: x: tls: handshake failure",
	}
	for _, verdict := range []CareVerdict{CareUncertain, CareAlive} {
		for _, e := range errs {
			m, cfg := careFailManager(t, verdict)
			for i := 0; i < 10; i++ {
				if acts := m.AutoCarePass(careFailRound([]string{"a"}, e, 2)); acts != nil {
					t.Fatalf("暂态错误不应有动作（%q, 第 %d 轮）: %v", e, i+1, acts)
				}
			}
			if !careSource(cfg, "a").IsEnabled() {
				t.Fatalf("暂态错误（%q）不得停用源", e)
			}
			if cfg.Sources[0].Name != "a" {
				t.Fatalf("暂态错误（%q）不得沉底", e)
			}
			if got := careFailCount(m, "a"); got != 0 {
				t.Fatalf("暂态错误（%q）fail_streak 应为 0, got %d", e, got)
			}
			if m.careEmpty["a"] != 0 {
				t.Fatalf("暂态错误（%q）不得计入空源计数", e)
			}
		}
	}
}

// 误判侧②补充：错误文本里同时出现 404 与 5xx（混合通道信号）也不能当永久失效
// ——判定只看复核结论，不看错误文本。
func TestCareFailMixedTextNotPermanent(t *testing.T) {
	m, cfg := careFailManager(t, CareUncertain)
	msg := "所有候选地址均失败: https://mirror.example/x/moo.json: HTTP 404 / https://raw.example/x/moo.json: HTTP 502"
	for i := 0; i < 8; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, msg, 2))
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("混合信号（404+5xx）必须视为不确定，不得停用")
	}
	if got := careFailCount(m, "a"); got != 0 {
		t.Fatalf("不确定不得推进计数, got %d", got)
	}
}

// 误判侧⑥：已达双门槛，但停用前的强制全量复核返回「不确定」（超时）→
// 不停用、计数不推进。
func TestCareFailVerifyUncertainBlocksDisable(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	for i := 0; i < 5; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	careAge(m, "a", 30*time.Hour)
	before := careFailCount(m, "a")
	// 复核改判为不确定（模拟复核超时）
	m.CareProbe = func(name, url string) CareVerdict { return CareUncertain }
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
		t.Fatalf("复核不确定时不得停用, got %v", acts)
	}
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("复核不确定时源应保持启用")
	}
	if got := careFailCount(m, "a"); got != before {
		t.Fatalf("复核不确定时计数不得推进: before=%d after=%d", before, got)
	}
}

// 误判侧⑧：用户手动停用的源即使被判死 → 不重复处理、不告警、不改 Enabled。
func TestCareFailUserDisabledUntouched(t *testing.T) {
	m, cfg := careFailManager(t, CarePermanentlyDead)
	off := false
	cfg.Sources[0].Enabled = &off // 用户手动停用 a
	m.careMu.Lock()
	m.careRecs["a"] = careRec{FailStreak: 9, FirstFailAt: time.Now().Add(-72 * time.Hour),
		LastError: "仓库不存在（HTTP 404）", URL: cfg.Sources[0].URL}
	m.careMu.Unlock()
	acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	for _, a := range acts {
		if strings.Contains(a, "已自动停用") {
			t.Fatalf("用户手动停用的源不得被策略重复处理: %v", acts)
		}
	}
	if careSource(cfg, "a").Enabled == nil || *careSource(cfg, "a").Enabled {
		t.Error("用户停用状态不应被改写")
	}
	m.careMu.Lock()
	dby := m.careRecs["a"].DisabledBy
	m.careMu.Unlock()
	if dby == "auto-care" {
		t.Error("用户停用的源不得被标成 auto-care（1b 会据此复活）")
	}
}
