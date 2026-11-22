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
	// 2026-11-21：判据从「必须出现 gh-proxy 字样」放宽为「必须出现镜像前缀形态」。
	// 原断言隐含依赖清单前两条是 hk/cdn.gh-proxy.org；两个域名停服下线后，
	// 前 N 个镜像可能是 gh-tbap / ghproxy-cxkpro，语义（有镜像变体）不变。
	const rawPath = "https://raw.githubusercontent.com/Wyf841015/FnDepot/master/fnpack.json"
	var hasRaw, hasMirror, hasJsDelivr bool
	for _, c := range cands {
		if c == rawPath {
			hasRaw = true
		}
		if c != rawPath && strings.HasSuffix(c, rawPath) {
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
		if strings.Contains(c, "/https://") {
			t.Error("非 GitHub 源不应出现镜像候选: " + c)
		}
	}
}
