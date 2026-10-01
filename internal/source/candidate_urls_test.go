package source

import (
	"strings"
	"testing"
)

// TestCandidateURLsGitHubMirrors GitHub 仓库源的一级候选必须含 gh-proxy
// 镜像变体（raw main/master 之外）——GitHub 直连在国内 NAS 间歇性超时，
// 无镜像时源同步整体失败（2026-09-25 实测）。
func TestCandidateURLsGitHubMirrors(t *testing.T) {
	f := NewFnDepot("s", "github.com/Wyf841015/FnDepot")
	cands := f.candidateURLs()
	if len(cands) == 0 {
		t.Fatal("无候选")
	}
	var hasRaw, hasMirror, hasJsDelivr bool
	for _, c := range cands {
		if strings.Contains(c, "raw.githubusercontent.com/Wyf841015/FnDepot/master/fnpack.json") &&
			!strings.Contains(c, "gh-proxy") {
			hasRaw = true
		}
		if strings.Contains(c, "gh-proxy") && strings.Contains(c, "FnDepot/master/fnpack.json") {
			hasMirror = true
		}
		if strings.Contains(c, "cdn.jsdelivr.net/gh/Wyf841015/FnDepot") {
			hasJsDelivr = true
		}
	}
	if !hasRaw {
		t.Error("缺少 raw master 直连候选")
	}
	if !hasMirror {
		t.Error("缺少 gh-proxy 镜像候选（master）")
	}
	if !hasJsDelivr {
		t.Error("缺少 jsDelivr 兜底候选")
	}
	// 直连 .json 源不加镜像
	g := NewFnDepot("s", "https://fndepot.example.com/fnpack.json")
	for _, c := range g.candidateURLs() {
		if strings.Contains(c, "gh-proxy") {
			t.Error("非 GitHub 源不应出现镜像候选: " + c)
		}
	}
}
