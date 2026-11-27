package source

import (
	"strings"
	"testing"
	"time"
)

// PATCH_0575 A（2026-11-27）：死源判定缺口——直连组全部候选 404/410，
// 镜像组全部「不可达」（超时/5xx/403/429，非 404）时，不再永远停在
// CareUncertain，而是计入失败轮（但仍受 5 轮 + 24h 双门槛约束）。

// 直连 404 + 镜像 5xx/超时 → 计入失败轮。
func TestProbeVerdictDirectGoneMirrorsUnreachable(t *testing.T) {
	direct := probeSrv(t, 404, 0)
	m1 := probeSrv(t, 502, 0)
	m2 := probeSrv(t, 200, 400*time.Millisecond) // 客户端 120ms → 超时
	groups := []probeGroup{
		{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{m1.URL + "/moo.json"}},
		{Name: "mirror:m2", URLs: []string{m2.URL + "/moo.json"}},
	}
	got := probeCareVerdictWith(testProbeClient(120*time.Millisecond), groups)
	if got != CareDirectGoneMirrorsUnreachable {
		t.Fatalf("直连 404 + 镜像全不可达应计入失败轮, got %v", got)
	}
}

// 误判护栏②：镜像同样 404/410 → 走原确定性判死路径（更快）。
func TestProbeVerdictDirectGoneMirrorGoneStillDead(t *testing.T) {
	direct := probeSrv(t, 410, 0)
	m := probeSrv(t, 404, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{m.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CarePermanentlyDead {
		t.Fatalf("双通道确定性失效应直接判死, got %v", got)
	}
}

// 误判护栏①：镜像任一 200 → 立刻健康（即使直连 404）。
func TestProbeVerdictDirectGoneMirrorOKAlive(t *testing.T) {
	direct := probeSrv(t, 404, 0)
	m := probeSrv(t, 200, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{m.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CareAlive {
		t.Fatalf("镜像可达应判健康, got %v", got)
	}
}

// 误判护栏⑤：无镜像组 → 只有直连 404 仍不能构成新规则（保持不确定）。
func TestProbeVerdictDirectGoneNoMirrorsUncertain(t *testing.T) {
	direct := probeSrv(t, 404, 0)
	groups := []probeGroup{{Name: "direct", URLs: []string{direct.URL + "/moo.json"}}}
	if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CareUncertain {
		t.Fatalf("无镜像组时应保持不确定, got %v", got)
	}
}

// 误判护栏⑤：直连不可达（超时）+ 镜像不可达 → 不确定（不能单独判死）。
func TestProbeVerdictDirectBadMirrorsBadUncertain(t *testing.T) {
	slow := probeSrv(t, 200, 400*time.Millisecond)
	m := probeSrv(t, 502, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{slow.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{m.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(120*time.Millisecond), groups); got != CareUncertain {
		t.Fatalf("直连未确认 404/410 时应不确定, got %v", got)
	}
}

// 端到端：连续 5 轮（跨 24h）该结论 → 停用 + 沉底 + disabled_by=auto-care。
func TestCareDirectGoneMirrorsUnreachableDisables(t *testing.T) {
	m, cfg := careFailManager(t, CareDirectGoneMirrorsUnreachable)
	for i := 1; i <= 4; i++ {
		if acts := m.AutoCarePass(careFailRound([]string{"a"}, "所有候选地址均失败: x: HTTP 502", 2)); acts != nil {
			t.Fatalf("第 %d 轮不应有动作, got %v", i, acts)
		}
	}
	if got := careFailCount(m, "a"); got != 4 {
		t.Fatalf("4 轮后 fail_streak 应为 4, got %d", got)
	}
	// 第 5 轮：未满 24h → 不动手。
	if acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2)); acts != nil {
		t.Fatalf("不足 24h 不应停用, got %v", acts)
	}
	careAge(m, "a", 25*time.Hour)
	acts := m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))
	if !strings.Contains(strings.Join(acts, " | "), "已自动停用") {
		t.Fatalf("第 5 轮 + 跨 24h 应自动停用, got %v", acts)
	}
	if careSource(cfg, "a").IsEnabled() {
		t.Error("源 a 应被自动停用")
	}
	if cfg.Sources[2].Name != "a" {
		t.Errorf("死源应沉底（末位）, got %q", []string{cfg.Sources[0].Name, cfg.Sources[1].Name, cfg.Sources[2].Name})
	}
	m.careMu.Lock()
	dby := m.careRecs["a"].DisabledBy
	m.careMu.Unlock()
	if dby != "auto-care" {
		t.Errorf("应标记 disabled_by=auto-care, got %q", dby)
	}
}

// 误判护栏③：第 4 轮不动；中途一轮 CareAlive → 清零、重新计数。
func TestCareDirectGoneMirrorsUnreachableResetByAlive(t *testing.T) {
	m, cfg := careFailManager(t, CareDirectGoneMirrorsUnreachable)
	for i := 0; i < 4; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))
	}
	m.CareProbe = func(name, url string) CareVerdict { return CareAlive }
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))
	if got := careFailCount(m, "a"); got != 0 {
		t.Fatalf("中途镜像 200 应清零, got %d", got)
	}
	careAge(m, "a", 48*time.Hour)
	m.CareProbe = func(name, url string) CareVerdict { return CareDirectGoneMirrorsUnreachable }
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))
	if !careSource(cfg, "a").IsEnabled() {
		t.Fatal("清零后只 1 轮，不得停用")
	}
}
