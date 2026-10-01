package api

import (
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.187：优选切换评估（evaluateMirror）行为测试——后台探测循环与
// healthOf 视图共用同一判定：首调记基线不推 / 生效源变化推 / 同 active
// 不重推 / 30min 冷却内不重推 / gh、dk 两组互不串扰。
func newMirrorSwitchTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	// 包级冷却状态跨测试复位（与生产同构的 30min 防抖变量）
	mirrorSwitchNotified.Lock()
	mirrorSwitchNotified.at = map[string]time.Time{}
	mirrorSwitchNotified.Unlock()
	return &Server{Cfg: &config.Config{}, Mirrors: NewMirrorMonitor()}
}

func mkStat(key, status string, speed int64) MirrorStat {
	return MirrorStat{Key: key, Label: key, Status: status, SpeedBPS: speed, LatencyMS: 100}
}

func countSwitchNotifies(s *Server, group string) int {
	n := 0
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "mirror_switched" && strings.Contains(e.Content, groupLabelOf(group)) {
			n++
		}
	}
	return n
}

func groupLabelOf(group string) string {
	if group == "dk" {
		return "Docker"
	}
	return "GitHub"
}

// TestEvaluateMirrorBaseline 首调（prevActive 为空）只记基线，不推。
func TestEvaluateMirrorBaseline(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	stats := []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)}
	active, _ := s.evaluateMirror("auto", "gh", stats)
	if active != "b" {
		t.Fatalf("吞吐最高应为 b, got %s", active)
	}
	if got := countSwitchNotifies(s, "gh"); got != 0 {
		t.Fatalf("首调不应推送, got %d 条", got)
	}
	if s.Mirrors.peekActive("gh") != "b" {
		t.Fatalf("应记基线 b, got %q", s.Mirrors.peekActive("gh"))
	}
}

// TestEvaluateMirrorSwitchPushes 生效源真实变化 → 推一条，文案带旧→新与原因。
func TestEvaluateMirrorSwitchPushes(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	// 基线
	s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)})
	// a 变最快 → 生效源 b → a
	active, reason := s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 300), mkStat("b", "ok", 200)})
	if active != "a" {
		t.Fatalf("应切到 a, got %s", active)
	}
	if reason != "按吞吐测速自动优选" {
		t.Errorf("auto 原因应为吞吐测速, got %s", reason)
	}
	if got := countSwitchNotifies(s, "gh"); got != 1 {
		t.Fatalf("应推 1 条, got %d", got)
	}
	entry := s.Cfg.NotifyLog[len(s.Cfg.NotifyLog)-1]
	if entry.Event != "mirror_switched" || !strings.Contains(entry.Content, "b → a") {
		t.Errorf("通知内容不符: event=%s content=%s", entry.Event, entry.Content)
	}
	sw := s.Mirrors.lastSwitchOf("gh")
	if sw == nil || sw.From != "b" || sw.To != "a" {
		t.Errorf("lastSwitch 应记 b→a, got %+v", sw)
	}
}

// TestEvaluateMirrorNoReshoot 同 active 重复评估不重推（幂等）。
func TestEvaluateMirrorNoReshoot(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	stats := []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)}
	s.evaluateMirror("auto", "gh", stats)
	s.evaluateMirror("auto", "gh", stats)
	s.evaluateMirror("auto", "gh", stats)
	if got := countSwitchNotifies(s, "gh"); got != 0 {
		t.Fatalf("同 active 重复评估不应推, got %d", got)
	}
}

// TestEvaluateMirrorCooldown 30min 冷却内连续两次变化只推一条。
func TestEvaluateMirrorCooldown(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	// 基线 a 最快
	s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 300), mkStat("b", "ok", 200)})
	// 变化 1：b 变最快 → 推
	s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)})
	// 变化 2（冷却内）：a 又变最快 → 不推
	s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 300), mkStat("b", "ok", 200)})
	if got := countSwitchNotifies(s, "gh"); got != 1 {
		t.Fatalf("冷却内应只推 1 条, got %d", got)
	}
	// 但 prev 已跟随到 a（横幅/基线不错位）
	if s.Mirrors.peekActive("gh") != "a" {
		t.Fatalf("prev 应跟随到 a, got %q", s.Mirrors.peekActive("gh"))
	}
}

// TestEvaluateMirrorGroupsIndependent gh 基线不影响 dk 组（dk 首调仍不推）。
func TestEvaluateMirrorGroupsIndependent(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	s.evaluateMirror("auto", "gh", []MirrorStat{mkStat("a", "ok", 300)})
	s.evaluateMirror("auto", "dk", []MirrorStat{mkStat("x", "ok", 300)})
	if s.Mirrors.peekActive("dk") != "x" {
		t.Fatalf("dk 应独立记基线 x, got %q", s.Mirrors.peekActive("dk"))
	}
	if got := countSwitchNotifies(s, "dk"); got != 0 {
		t.Fatalf("dk 首调不应推, got %d", got)
	}
	if got := countSwitchNotifies(s, "gh"); got != 0 {
		t.Fatalf("gh 不应推, got %d", got)
	}
}

// TestEvaluateMirrorManualFailFallback 手选源失效 → 切智能优选，
// 原因文案为「手选源已失效」。
func TestEvaluateMirrorManualFailFallback(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	// 基线：手选 a（ok）
	s.evaluateMirror("a", "gh", []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)})
	// a 失效 → 切 b
	active, reason := s.evaluateMirror("a", "gh", []MirrorStat{mkStat("a", "fail", 0), mkStat("b", "ok", 200)})
	if active != "b" || reason != "手选源已失效，自动切换智能优选" {
		t.Fatalf("应回落 b 并带手选失效原因, got %s/%s", active, reason)
	}
	if got := countSwitchNotifies(s, "gh"); got != 1 {
		t.Fatalf("应推 1 条, got %d", got)
	}
}

// TestHealthOfUsesEvaluate healthOf 复用同一判定：不重复推、Active 一致。
func TestHealthOfUsesEvaluate(t *testing.T) {
	s := newMirrorSwitchTestServer(t)
	stats := []MirrorStat{mkStat("a", "ok", 100), mkStat("b", "ok", 200)}
	h := s.healthOf("auto", "gh", stats) // 首调基线
	if h.Active != "b" {
		t.Fatalf("healthOf.Active 应为 b, got %s", h.Active)
	}
	// 再读一次（同 active）：视图不触发推送
	h2 := s.healthOf("auto", "gh", stats)
	if h2.Active != "b" || countSwitchNotifies(s, "gh") != 0 {
		t.Fatalf("同 active 复读不应推: active=%s n=%d", h2.Active, countSwitchNotifies(s, "gh"))
	}
}
