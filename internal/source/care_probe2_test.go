package source

import (
	"strings"
	"testing"
)

// 通道分组：GitHub 仓库 → 直连 + 多镜像，且剔除 jsDelivr（陈旧副本不能作证）；
// 非 GitHub 主机 → 只有直连，无镜像可查（必然不确定 = 宁可漏停）。
func TestCareProbeGroups(t *testing.T) {
	gs := careProbeGroups("https://github.com/owner/repo")
	if len(gs) < 2 {
		t.Fatalf("GitHub 源应至少分直连 + 1 镜像, got %d", len(gs))
	}
	if gs[0].Name != "direct" {
		t.Fatalf("首组应为直连, got %s", gs[0].Name)
	}
	for _, g := range gs {
		for _, u := range g.URLs {
			if strings.Contains(u, "cdn.jsdelivr.net") {
				t.Fatalf("jsDelivr 必须剔除（边缘缓存非权威）: %s", u)
			}
		}
	}
	if gs2 := careProbeGroups("https://example.com/some/repo"); len(gs2) != 1 || gs2[0].Name != "direct" {
		t.Fatalf("非 GitHub 主机应只有直连组, got %+v", gs2)
	}
}
