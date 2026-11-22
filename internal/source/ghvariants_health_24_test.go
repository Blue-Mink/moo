package source

import (
	"strings"
	"testing"

	"moo/internal/config"
)

// 2026-11-21 FnDepot#24 / BACKLOG P0-3：ghVariants 取前 N 个镜像时必须跳过
// 连续 fail 的源。反向验证：把 ghVariants 回退成 config.GitHubMirrorOptions()
// 时本用例必 FAIL。
func TestGhVariants24_SkipsFailedMirror(t *testing.T) {
	defer config.SetGHMirrorHealth(nil)
	opts := config.GitHubMirrorOptions()
	victim, victimURL := "", ""
	for _, o := range opts {
		if o.URL != "" {
			victim, victimURL = o.Key, o.URL
			break
		}
	}
	if victim == "" {
		t.Skip("清单里没有带 URL 的镜像")
	}
	config.SetGHMirrorHealth(func(k string) string {
		if k == victim {
			return "fail"
		}
		return "ok"
	})
	raw := "https://raw.githubusercontent.com/o/r/main/x.fpk"
	cands := ghVariants("o/r", "main", "x.fpk", raw, 2)
	if len(cands) < 3 {
		t.Fatalf("候选过少：%v", cands)
	}
	dead := strings.TrimRight(victimURL, "/") + "/" + raw
	if cands[0] == dead || cands[1] == dead {
		t.Fatalf("fail 镜像仍进了前 2 个：%v", cands[:2])
	}
	// ghVariants 只取前 N 个镜像（此处 2 个），fail 源被降级后不应出现在候选里；
	// 但直连与 jsDelivr 兜底必须仍在。
	hasRaw := false
	for _, c := range cands {
		if c == raw {
			hasRaw = true
		}
	}
	if !hasRaw {
		t.Errorf("直连兜底丢失：%v", cands)
	}
}

// 未注入健康数据时，前 2 个镜像应与静态清单一致（保持既有行为）。
func TestGhVariants24_NoInjectionKeepsStaticOrder(t *testing.T) {
	config.SetGHMirrorHealth(nil)
	defer config.SetGHMirrorHealth(nil)
	var want []string
	for _, o := range config.GitHubMirrorOptions() {
		if o.URL == "" {
			continue
		}
		want = append(want, strings.TrimRight(o.URL, "/")+"/https://raw.githubusercontent.com/o/r/main/x.fpk")
		if len(want) == 2 {
			break
		}
	}
	if len(want) < 2 {
		t.Skip("清单里镜像不足 2 个")
	}
	cands := ghVariants("o/r", "main", "x.fpk", "https://raw.githubusercontent.com/o/r/main/x.fpk", 2)
	if len(cands) < 2 || cands[0] != want[0] || cands[1] != want[1] {
		t.Fatalf("静态序不符：got %v want %v", cands[:2], want)
	}
}
