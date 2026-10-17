package api

// lineage_p23_326i_test.go —— 0.6.326i 溯源口径统一（P2）+ fork parent 兜底（P3）单测。
//
// P2：上游包在「上游作者的打包仓」里、owner 的索引仓**已收录**时，
//     除说明文字外还要把 UpstreamSource/UpstreamVersion 一并设上。
// P3：包地址拿不到（download_url 空/非 GitHub）时，用「本源是 fork 搬运源 +
//     发布者≠本源 owner + **fork parent 源里有同名卡**」三条做兜底推断。

import (
	"strings"
	"testing"

	"moo/internal/platform"
	"moo/internal/source"
)

// P2：owner 索引仓已收录 → UpstreamSource/UpstreamVersion 也要设（旧实现只写 note）。
func TestApplyUpstreamLineage_OwnerIndexSourceSetsUpstream(t *testing.T) {
	s := newLineageServer(t)
	s.AddProbes = source.NewAddProbe("")
	// 包在 yuexps 的打包仓（不是索引仓）；yuexps/FnDepot 就是已收录的源「搬运源」。
	s.AddProbes.SetForTest("yuexps/pack-only", "", "yuexps/FnDepot")
	out := []AppInfo{mkLineageCard("app-a@上游源", "app-a", "上游源", "2.5.0", "yuexps/pack-only", false)}
	s.applyUpstreamLineage(out)
	got := out[0]
	if got.UpstreamRepo != "yuexps/pack-only" {
		t.Fatalf("UpstreamRepo = %q（应保留包实际所在的打包仓）", got.UpstreamRepo)
	}
	if got.UpstreamSource != "搬运源" {
		t.Fatalf("P2 失效：UpstreamSource = %q，want 搬运源", got.UpstreamSource)
	}
	if got.UpstreamVersion != "1.9.0" {
		t.Fatalf("UpstreamVersion = %q，want 1.9.0（取上游索引源同名卡）", got.UpstreamVersion)
	}
	if !strings.Contains(got.UpstreamAddNote, "已由已添加的源") {
		t.Fatalf("说明文字应保留，实际 %q", got.UpstreamAddNote)
	}
}

// P3 正例：包地址为空 + 本源是 fork + 发布者≠本源 owner + parent 源里有同名卡 → 补上。
func TestApplyForkParentFallback_Positive(t *testing.T) {
	s := newLineageServer(t)
	s.Forks = source.NewGHForks("")
	s.Forks.SetForTest("yuexps/fndepot", "alice/awesome-apps")
	c := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "", false) // 无 package 地址
	c.Distributor = "第三方打包者"
	out := []AppInfo{c}
	s.applyUpstreamLineage(out)
	got := out[0]
	if got.UpstreamRepo != "alice/awesome-apps" || got.UpstreamSource != "上游源" || got.UpstreamVersion != "2.5.0" {
		t.Fatalf("P3 正例未补上血缘：%+v", got)
	}
	if got.UpstreamURL != "https://github.com/alice/awesome-apps" {
		t.Fatalf("UpstreamURL = %q", got.UpstreamURL)
	}
}

// P3 负例（三种）：任一硬条件不成立都**不得**设值。
func TestApplyForkParentFallback_Negatives(t *testing.T) {
	mk := func(t *testing.T, parent, dist string, forkKnown bool) AppInfo {
		t.Helper()
		s := newLineageServer(t)
		s.Forks = source.NewGHForks("")
		if forkKnown {
			s.Forks.SetForTest("yuexps/fndepot", parent)
		}
		c := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "", false)
		c.Distributor = dist
		out := []AppInfo{c}
		s.applyUpstreamLineage(out)
		return out[0]
	}
	// ① parent 源里没有同名卡（自建源里只有 app-a？→ 该用例用 bob/mine 无同名卡）
	if got := mk(t, "bob/mine", "第三方打包者", true); got.UpstreamRepo != "" {
		t.Fatalf("parent 源无同名卡时不得兜底，实际 %+v", got)
	}
	// ② 发布者就是本源 owner（作者自建仓）
	if got := mk(t, "alice/awesome-apps", "yuexps", true); got.UpstreamRepo != "" {
		t.Fatalf("发布者==本源 owner 时不得兜底，实际 %+v", got)
	}
	// ③ fork 关系未知（未拉过）
	if got := mk(t, "alice/awesome-apps", "第三方打包者", false); got.UpstreamRepo != "" {
		t.Fatalf("fork 关系未知时不得兜底，实际 %+v", got)
	}
	// ④ parent 未收录为源
	if got := mk(t, "carol/whatever", "第三方打包者", true); got.UpstreamRepo != "" {
		t.Fatalf("parent 未收录为源时不得兜底，实际 %+v", got)
	}
}

// P2 的下游收益：自动纠正（auto_correct）在开之前对这类卡必然是空转，开之后能生效。
func TestApplyUpstreamCorrection_P2CardBecomesCorrectable(t *testing.T) {
	s := newLineageServer(t)
	s.AddProbes = source.NewAddProbe("")
	s.AddProbes.SetForTest("yuexps/pack-only", "", "yuexps/FnDepot")
	out := []AppInfo{
		mkLineageCard("app-a@自建源", "app-a", "自建源", "1.0.0", "yuexps/pack-only", true),
		mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "yuexps/FnDepot", false),
	}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource != "搬运源" {
		t.Fatalf("前置条件不成立：UpstreamSource = %q", out[0].UpstreamSource)
	}
	byName := map[string]platform.InstalledApp{}
	applyUpstreamCorrection(out[:1], byName, false) // 开关关：不动
	if out[0].HasUpdate {
		t.Fatalf("auto_correct 关闭时不应改更新目标")
	}
	out[0].HasUpdate = false
	out[0].AvailableVersion = ""
	out[0].UpdateFromSource = ""
	applyUpstreamCorrection(out, byName, true) // 开关开：改指上游源
	if !out[0].HasUpdate || out[0].AvailableVersion != "1.9.0" || out[0].UpdateFromSource != "搬运源" {
		t.Fatalf("auto_correct 开启后应改指上游：%+v", out[0])
	}
}
