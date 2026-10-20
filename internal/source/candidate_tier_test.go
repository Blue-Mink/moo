package source

import (
	"strings"
	"testing"
)

// 0.6.330 WP2：镜像分级。
//   - fast（mirrorLimit=fastMirrorCount）候选必须含 raw 两分支 + jsDelivr + 前 N 个镜像；
//   - jsDelivr 必须带 @<分支>（0.6.331 P0：无 @ref 形态会返回陈旧副本）；
//   - 必须不含第 N+1 个镜像（证明确实收窄，否则「降请求数」是空话）；
//   - full（-1）必须比 fast 多。
func TestBuildCandidatesTiered(t *testing.T) {
	const repo = "https://github.com/Blue-Mink/FnDepot"
	fast := buildCandidatesLimited(repo, fastMirrorCount)
	full := buildCandidatesLimited(repo, -1)

	joined := strings.Join(fast, "\n")
	for _, want := range []string{
		"raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json",
		"raw.githubusercontent.com/Blue-Mink/FnDepot/master/fnpack.json",
		"cdn.jsdelivr.net/gh/Blue-Mink/FnDepot@main/moo.json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fast 候选缺少 %s", want)
		}
	}
	// 0.6.331 P0：无 @ref 的 jsDelivr（/gh/o/r/<文件>）会被边缘缓存钉在旧副本上，
	// 生成器里必须彻底消失。
	if strings.Contains(joined, "cdn.jsdelivr.net/gh/Blue-Mink/FnDepot/moo.json") {
		t.Fatalf("fast 候选仍含无 @ref 的 jsDelivr 形态（陈旧索引来源）")
	}
	if len(fast) >= len(full) {
		t.Fatalf("fast 候选数应小于全量：fast=%d full=%d", len(fast), len(full))
	}
	// 第 3 个及以后的镜像不应出现在 fast 里（用「全量有、fast 没有」的差集验证）
	var extra int
	for _, u := range full {
		if !strings.Contains(joined, u) {
			extra++
		}
	}
	if extra == 0 {
		t.Fatal("fast 与 full 没有差异，镜像分级未生效")
	}
	// 全量里 moo.json 仍早于 fnpack.json（顺序语义不能被分级破坏）
	iMoo, iFn := -1, -1
	for i, u := range fast {
		if iMoo < 0 && strings.HasSuffix(u, "/moo.json") {
			iMoo = i
		}
		if iFn < 0 && strings.HasSuffix(u, "/fnpack.json") {
			iFn = i
		}
	}
	if iMoo < 0 || iFn < 0 || iMoo > iFn {
		t.Fatalf("顺序语义被破坏：moo=%d fnpack=%d", iMoo, iFn)
	}
}

// 记忆命中的索引名必须能过滤候选；过滤为空时回退全量（防记忆写坏后永久失败）。
func TestCandidateURLsPrefFilter(t *testing.T) {
	f := NewFnDepot("s", "https://github.com/Blue-Mink/FnDepot")
	f.withPref("fnpack.json")
	for _, u := range f.candidateURLs() {
		if indexNameOf(u) != "fnpack.json" {
			t.Fatalf("pref 过滤后出现其它索引名：%s", u)
		}
	}
	// 不存在的名字 → 回退全量（候选非空且不全是该名字）
	g := NewFnDepot("s", "https://github.com/Blue-Mink/FnDepot").withPref("nonexistent.json")
	cands := g.candidateURLs()
	if len(cands) == 0 {
		t.Fatal("过滤为空时应回退全量候选")
	}
	var other bool
	for _, u := range cands {
		if indexNameOf(u) != "nonexistent.json" {
			other = true
			break
		}
	}
	if !other {
		t.Fatal("回退逻辑未生效")
	}
}

// 全量模式下候选数应明显多于 fast（现状 190+ vs 个位数~几十）。
func TestFullMirrorsWidens(t *testing.T) {
	f := NewFnDepot("s", "https://github.com/Blue-Mink/FnDepot")
	fast := len(f.candidateURLs())
	f.withFullMirrors()
	full := len(f.candidateURLs())
	if full <= fast*2 {
		t.Fatalf("全量应至少是 fast 的 2 倍：fast=%d full=%d", fast, full)
	}
}
