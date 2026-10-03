package api

import (
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// TestMirrorsAllFailedDk（0.6.188）：dk 组全挂监测——全挂推 1 /
// 冷却内不重推 / 恢复对偶推 / 与 gh 组状态互不串扰。
func TestMirrorsAllFailedDk(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	mirrorAllFailedState.mu.Lock()
	mirrorAllFailedState.at = map[string]time.Time{}
	mirrorAllFailedState.down = map[string]bool{}
	mirrorAllFailedState.mu.Unlock()

	s := &Server{Cfg: &config.Config{}, Mirrors: NewMirrorMonitor()}
	setDkStats := func(stats ...MirrorStat) {
		s.Mirrors.mu.Lock()
		s.Mirrors.dkStats = stats
		s.Mirrors.mu.Unlock()
	}
	count := func(ev string) int {
		n := 0
		for _, e := range s.Cfg.NotifyLog {
			if e.Event == ev {
				n++
			}
		}
		return n
	}

	// 1) 远程全 fail + 本地 kspeeder ok → 仍推（0.6.189：本地回环源不计入判定）
	setDkStats(
		MirrorStat{Key: "a", Status: "fail"},
		MirrorStat{Key: "b", Status: "fail"},
		MirrorStat{Key: "kspeeder", Status: "ok", SpeedBPS: 9999},
	)
	s.mirrorsAllFailedCheckOf("dk")
	if n := count("docker_mirrors_all_failed"); n != 1 {
		t.Fatalf("远程全挂（kspeeder 活着）应推 1 条, got %d", n)
	}
	if got := s.Cfg.NotifyLog[len(s.Cfg.NotifyLog)-1].Content; !strings.Contains(got, "本地 KSpeeder 缓存不计入") {
		t.Errorf("全挂通知应注明本地源不计入, got %s", got)
	}

	// 2) 仍全挂（30min 冷却内）→ 不重推
	s.mirrorsAllFailedCheckOf("dk")
	if n := count("docker_mirrors_all_failed"); n != 1 {
		t.Fatalf("冷却内不应重推, got %d", n)
	}

	// 3) 一个远程源恢复（a 可用且最快）→ 推 docker_mirrors_recovered，内容带优选源
	setDkStats(
		MirrorStat{Key: "a", Status: "ok", SpeedBPS: 9999},
		MirrorStat{Key: "b", Status: "fail"},
		MirrorStat{Key: "kspeeder", Status: "ok", SpeedBPS: 100},
	)
	s.mirrorsAllFailedCheckOf("dk")
	if n := count("docker_mirrors_recovered"); n != 1 {
		t.Fatalf("恢复应推 1 条, got %d", n)
	}
	if got := s.Cfg.NotifyLog[len(s.Cfg.NotifyLog)-1].Content; !strings.Contains(got, "a") {
		t.Errorf("恢复通知应带当前优选源 a, got %s", got)
	}

	// 4) gh 组状态不受 dk 操作影响
	if n := count("mirrors_all_failed"); n != 0 {
		t.Fatalf("不应波及 gh 组全挂, got %d", n)
	}
	if n := count("mirrors_recovered"); n != 0 {
		t.Fatalf("不应波及 gh 组恢复, got %d", n)
	}
}

// TestMirrorsAllFailedGhStillWorks 旧 gh 组行为保持：全挂推 mirrors_all_failed。
func TestMirrorsAllFailedGhStillWorks(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	mirrorAllFailedState.mu.Lock()
	mirrorAllFailedState.at = map[string]time.Time{}
	mirrorAllFailedState.down = map[string]bool{}
	mirrorAllFailedState.mu.Unlock()

	s := &Server{Cfg: &config.Config{}, Mirrors: NewMirrorMonitor()}
	s.Mirrors.mu.Lock()
	s.Mirrors.ghStats = []MirrorStat{{Key: "x", Status: "fail"}}
	s.Mirrors.mu.Unlock()
	s.mirrorsAllFailedCheck() // 原入口（gh）
	n := 0
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "mirrors_all_failed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("gh 全挂应推 1 条, got %d", n)
	}
}
