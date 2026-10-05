package source

import (
	"strings"
	"testing"
)

// TestGHJSONVariantsDirectLink 0.6.283：GitHub 系 .json 直链必须展开
// 镜像前缀 + 规范化 raw + jsDelivr 兜底候选——主 NAS 2026-10-05 事件：
// 用户贴 raw 直链加源，候选仅原 URL 一条，raw 间歇断流一天失败 11 次，
// 其余仓库形态源（有镜像竞速）全部正常。
func TestGHJSONVariantsDirectLink(t *testing.T) {
	cands := buildCandidates("https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json")
	if len(cands) < 5 {
		t.Fatalf("直链候选过少（应含镜像+兜底）: %d", len(cands))
	}
	if cands[0] != "https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json" {
		t.Errorf("第一候选必须是用户原地址: %s", cands[0])
	}
	var hasMirror, hasJD bool
	for _, c := range cands {
		if strings.Contains(c, "raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json") &&
			!strings.HasPrefix(c, "https://raw.githubusercontent.com/") {
			hasMirror = true
		}
		if c == "https://cdn.jsdelivr.net/gh/Blue-Mink/FnDepot@main/moo.json" {
			hasJD = true
		}
	}
	if !hasMirror {
		t.Error("缺少镜像前缀候选")
	}
	if !hasJD {
		t.Error("缺少 jsDelivr 兜底候选（fetchJSON 按域名自动归兜底组）")
	}
}

// TestGHJSONVariantsWrappedForms 镜像包裹 / jsDelivr / github.com raw 路径
// 三种直链形态都要能反推出规范化 raw 并展开。
func TestGHJSONVariantsWrappedForms(t *testing.T) {
	raw := "https://raw.githubusercontent.com/o/r/main/fnpack.json"
	jdNoRef := "https://cdn.jsdelivr.net/gh/o/r/fnpack.json"
	cases := map[string][]string{
		// 镜像包裹直链 → 规范化 raw + @main jsDelivr
		"https://ghfast.top/" + raw: {raw, "cdn.jsdelivr.net/gh/o/r@main/fnpack.json"},
		// jsDelivr @ref → 规范化 raw + 镜像
		"https://cdn.jsdelivr.net/gh/o/r@dev/fnpack.json": {"raw.githubusercontent.com/o/r/dev/fnpack.json"},
		// jsDelivr 无 ref → main/master 双探
		jdNoRef: {"o/r@main/fnpack.json", "o/r@master/fnpack.json"},
		// github.com raw 路径 → 规范化 raw
		"https://github.com/o/r/raw/main/fnpack.json": {raw},
		// github.com blob 路径
		"https://github.com/o/r/blob/main/fnpack.json": {raw},
	}
	for in, wants := range cases {
		cands := buildCandidates(in)
		for _, want := range wants {
			var hit bool
			for _, c := range cands {
				if strings.Contains(c, want) {
					hit = true
					break
				}
			}
			if !hit {
				t.Errorf("%s 缺少候选 %s（得 %v）", in, want, cands)
			}
		}
	}
}

// TestGHJSONVariantsNonGitHubUntouched 非 GitHub 主机直链保持原行为：仅原 URL。
func TestGHJSONVariantsNonGitHubUntouched(t *testing.T) {
	u := "http://gitea.lan:3000/user/repo/raw/branch/main/moo.json"
	cands := buildCandidates(u)
	if len(cands) != 1 || cands[0] != u {
		t.Errorf("非 GitHub 直链应保持单候选, 得 %v", cands)
	}
}
