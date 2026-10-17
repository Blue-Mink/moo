package source

import (
	"context"
	"strings"
	"testing"
)

// ── 0.6.326k（盲点 B6）addprobe「owner 索引仓」候选名单扩展 ──────────────────
//
// 覆盖三类断言：①名单覆盖 B6 点名的现网仓名变体（数据断言）；②探测顺序 = 名单
// 顺序，且名单外的名字永不被试；③命中即止（计数桩验证请求数，注入 a.probe，
// 不触网）。证据：LINEAGE_BLINDSPOT_326K.md §1/B6 + default_sources_legacy.txt。

// addProbe326KBaseline = 0.6.326e 引入的原 5 个名字，必须原样保留、相对顺序不变。
var addProbe326KBaseline = []string{"FnDepot", "fndepot", "FnDepot1", "fnos-apps", "fn-apps"}

// addProbe326KB6Variants = B6/§1 点名的白名单缺口变体（缺一个 = 一个假死按钮）。
var addProbe326KB6Variants = []string{
	"FnDepot-", "-FnDepot", "FnDepot-arm", "FnDepot-1",
	"RROrg-fn-apps", "fn_fpk_packages", "fn-knock-turborepo",
}

// addProbe326KExcluded = 判定为应用名（非索引仓）而刻意未收录的历史清单名。
var addProbe326KExcluded = []string{"fntermx", "fnos-lxc", "LitePan-fpk", "fn"}

func stringSliceContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// probeRecorder 记录被探测的仓库（顺序敏感）。hit 里的 "owner/name" 返回索引名。
type probeRecorder struct {
	calls []string
	hit   map[string]bool
}

func (p *probeRecorder) fn(u string) string {
	p.calls = append(p.calls, u)
	if p.hit[strings.TrimPrefix(u, "https://github.com/")] {
		return "fnpack.json"
	}
	return ""
}

// repoSeq 把探测过的 URL 还原成 owner/repo 序列。
func (p *probeRecorder) repoSeq() []string {
	out := make([]string, 0, len(p.calls))
	for _, u := range p.calls {
		out = append(out, strings.TrimPrefix(u, "https://github.com/"))
	}
	return out
}

func assertSeq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("探测序列不符\n got=%v\nwant=%v", got, want)
	}
}

// ①数据断言：名单长度/去重/原 5 个保留/B6 变体全覆盖/应用名不入候选。
func TestAddProbe326K_CandidateListCoversVariants(t *testing.T) {
	if n := len(addProbeOwnerRepos); n != 12 {
		t.Fatalf("候选数应为 12（上限 ~12），实际 %d: %v", n, addProbeOwnerRepos)
	}
	seen := map[string]bool{}
	for _, nm := range addProbeOwnerRepos {
		if nm == "" || strings.TrimSpace(nm) != nm {
			t.Fatalf("候选名不合法：%q", nm)
		}
		if strings.ContainsAny(nm, "/ ") {
			t.Fatalf("候选名不应含路径/空格：%q", nm)
		}
		if seen[nm] {
			t.Fatalf("候选重复：%q", nm)
		}
		seen[nm] = true
	}
	// 原 5 个全部保留且相对顺序不变（生产行为不回退）。
	var gotBaseline []string
	for _, nm := range addProbeOwnerRepos {
		if stringSliceContains(addProbe326KBaseline, nm) {
			gotBaseline = append(gotBaseline, nm)
		}
	}
	assertSeq(t, gotBaseline, addProbe326KBaseline)
	// B6 点名的 7 个变体必须全覆盖。
	for _, nm := range addProbe326KB6Variants {
		if !stringSliceContains(addProbeOwnerRepos, nm) {
			t.Fatalf("B6 变体缺失：%q（该 owner 会得到假「加不上」）", nm)
		}
	}
	// 应用名不得混入候选（每多一个无关候选 = 每个未知 owner 多一次请求）。
	for _, nm := range addProbe326KExcluded {
		if stringSliceContains(addProbeOwnerRepos, nm) {
			t.Fatalf("应用名不应入候选：%q", nm)
		}
	}
	// 头两位 = FnDepot / fndepot（140/157 历史源 + 326c 现网混写实锤）。
	if addProbeOwnerRepos[0] != "FnDepot" || addProbeOwnerRepos[1] != "fndepot" {
		t.Fatalf("前两位应为 FnDepot/fndepot，实际 %v", addProbeOwnerRepos[:2])
	}
}

// ②探测顺序：按名单顺序试候选；命中即止；名单外的名字永不被试。
func TestAddProbe326K_ProbeOrderAndHitStops(t *testing.T) {
	a := NewAddProbe("")
	rec := &probeRecorder{hit: map[string]bool{"oak/FnDepot-arm": true}} // 第 7 个候选才命中
	a.probe = rec.fn
	a.Enqueue("oak/pack-only")
	if n := a.RefreshRound(context.Background(), 10); n != 1 {
		t.Fatalf("应探测 1 个仓库，实际 %d", n)
	}
	idx, owner, known := a.Lookup("oak/pack-only")
	if !known || idx != "" || owner != "oak/FnDepot-arm" {
		t.Fatalf("Lookup = (%q,%q,%v)，期望 (,\"oak/FnDepot-arm\",true)", idx, owner, known)
	}
	// 序列 = 自身 + 名单前 7 个（含命中的那个），之后不再探测。
	want := []string{"oak/pack-only"}
	for _, nm := range addProbeOwnerRepos {
		want = append(want, "oak/"+nm)
		if nm == "FnDepot-arm" {
			break
		}
	}
	got := rec.repoSeq()
	assertSeq(t, got, want)
	// 名单外的名字永不被试（逐条核对 owner 下的仓名）。
	for _, key := range got {
		repo := strings.TrimPrefix(key, "oak/")
		if repo == "pack-only" || stringSliceContains(addProbeOwnerRepos, repo) {
			continue
		}
		t.Fatalf("试了名单外的名字：%q", key)
	}
}

// ③命中即止：候选里首个命中的被采用，其后的候选（即使也有索引）不再探测。
func TestAddProbe326K_EarliestHitWins(t *testing.T) {
	a := NewAddProbe("")
	rec := &probeRecorder{hit: map[string]bool{
		"zed/FnDepot-":  true, // 第 4 个候选
		"zed/fnos-apps": true, // 第 8 个候选（命中即止 → 不得被采用）
	}}
	a.probe = rec.fn
	a.Enqueue("zed/tool")
	a.RefreshRound(context.Background(), 10)
	idx, owner, known := a.Lookup("zed/tool")
	if !known || idx != "" || owner != "zed/FnDepot-" {
		t.Fatalf("应取首个命中的候选，实际 (%q,%q,%v)", idx, owner, known)
	}
	assertSeq(t, rec.repoSeq(), []string{
		"zed/tool", "zed/FnDepot", "zed/fndepot", "zed/FnDepot1", "zed/FnDepot-"})
}

// ③自身即候选名时只探一次（cand == k 跳过），不得把自己写成「owner 索引仓」。
func TestAddProbe326K_SelfCandidateSkipped(t *testing.T) {
	a := NewAddProbe("")
	rec := &probeRecorder{}
	a.probe = rec.fn
	a.Enqueue("kim/FnDepot") // 自身就是名单第 1 个名字
	a.RefreshRound(context.Background(), 10)
	idx, owner, known := a.Lookup("kim/FnDepot")
	if !known || idx != "" || owner != "" {
		t.Fatalf("期望已知且全空，实际 (%q,%q,%v)", idx, owner, known)
	}
	// 上界 = 1（自身）+ 12（候选）− 1（与被探仓库同名的那条被跳过）= 12。
	if n := len(rec.calls); n != 12 {
		t.Fatalf("应共 12 次探测，实际 %d：%v", n, rec.repoSeq())
	}
	seen := map[string]int{}
	for _, k := range rec.repoSeq() {
		seen[k]++
	}
	if seen["kim/FnDepot"] != 1 {
		t.Fatalf("自身仓库应只探 1 次，实际 %d", seen["kim/FnDepot"])
	}
}

// ③最坏请求数上界：未知 owner 且处处无索引 = 1 + len(候选)，且逐个按序试完。
func TestAddProbe326K_WorstCaseProbeCount(t *testing.T) {
	a := NewAddProbe("")
	rec := &probeRecorder{}
	a.probe = rec.fn
	a.Enqueue("nobody/unknown")
	a.RefreshRound(context.Background(), 10)
	idx, owner, known := a.Lookup("nobody/unknown")
	if !known || idx != "" || owner != "" {
		t.Fatalf("期望已知且全空，实际 (%q,%q,%v)", idx, owner, known)
	}
	if want := 1 + len(addProbeOwnerRepos); len(rec.calls) != want {
		t.Fatalf("最坏探测数应为 %d，实际 %d", want, len(rec.calls))
	}
	got := rec.repoSeq()
	if got[0] != "nobody/unknown" {
		t.Fatalf("首个应探自身仓库，实际 %q", got[0])
	}
	for i, nm := range addProbeOwnerRepos {
		if got[i+1] != "nobody/"+nm {
			t.Fatalf("第 %d 个候选应为 nobody/%s，实际 %s", i+1, nm, got[i+1])
		}
	}
}
