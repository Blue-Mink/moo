package api

// lineage_srcfork_test.go —— 0.6.326f「禁用fork源」派生字段单测（Agent H）。
//
// 覆盖：fork 源 → SourceIsFork=true／非 fork 源 → false／fork 关系未知 →
// false（保守，不显示按钮，排队后台拉）／禁用源 → SourceDisabled=true／
// 非 GitHub（Gitea）源 → false／无源卡（官方源、系统自装）两字段都不设／
// 与上游判定独立（ReleaseURL 为空的卡照样算）。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

func TestApplySourceLineage(t *testing.T) {
	off := false
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "fork源", URL: "https://github.com/alice/fndepot"},
		{Name: "普通源", URL: "https://github.com/bob/mine"},
		{Name: "禁用fork源", URL: "https://github.com/carol/pack", Enabled: &off},
		{Name: "gitea源", URL: "http://192.0.2.15:3033/bluemink/moo"},
	}}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.Forks = source.NewGHForks("")
	s.Forks.SetForTest("alice/fndepot", "upstream/root") // 已知：是 fork
	s.Forks.SetForTest("bob/mine", "")                   // 已知：不是 fork
	s.Forks.SetForTest("carol/pack", "upstream/root")    // 已知：是 fork（但源已禁用）
	// gitea 源未注入 → 未知（保守 false + 入队）。

	out := []AppInfo{
		{Key: "a@fork源", AppName: "a", Source: "fork源"},
		// 与上游判定独立：这张卡的 ReleaseURL 为空（不走搬运血缘分支），
		// SourceIsFork 照样算得出来。
		{Key: "a2@fork源", AppName: "a2", Source: "fork源"},
		{Key: "b@普通源", AppName: "b", Source: "普通源"},
		{Key: "c@禁用fork源", AppName: "c", Source: "禁用fork源"},
		{Key: "d@gitea源", AppName: "d", Source: "gitea源"},
		{Key: "e", AppName: "e"},                                        // 无源
		{Key: "f@fnos-official", AppName: "f", Source: "fnos-official"}, // 官方源（不在配置）
	}
	s.applySourceLineage(out)

	if !out[0].SourceIsFork {
		t.Fatalf("fork 源卡应置 SourceIsFork，实际 false（card=%+v）", out[0])
	}
	if out[0].SourceDisabled {
		t.Fatalf("启用的 fork 源不应置 SourceDisabled")
	}
	if !out[1].SourceIsFork {
		t.Fatalf("ReleaseURL 为空的卡也应算 SourceIsFork（独立于上游判定）")
	}
	if out[2].SourceIsFork {
		t.Fatalf("非 fork 源不应置 SourceIsFork")
	}
	if !out[3].SourceIsFork || !out[3].SourceDisabled {
		t.Fatalf("禁用的 fork 源应同时置 SourceIsFork + SourceDisabled，实际 fork=%v disabled=%v",
			out[3].SourceIsFork, out[3].SourceDisabled)
	}
	if out[4].SourceIsFork || out[4].SourceDisabled {
		t.Fatalf("fork 关系未知（Gitea 源）应保守不置位，实际 %+v", out[4])
	}
	for _, i := range []int{5, 6} {
		if out[i].SourceIsFork || out[i].SourceDisabled {
			t.Fatalf("无源卡（官方源/系统自装）两字段都不应设，实际 %+v", out[i])
		}
	}
}

// 边界：未接线 fork 查询器 / 空切片 / 空 Server 都不应 panic。
func TestApplySourceLineageNilSafe(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceRef{{Name: "fork源", URL: "https://github.com/alice/fndepot"}}}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)} // Forks 未接线
	out := []AppInfo{{Key: "a@fork源", AppName: "a", Source: "fork源"}}
	s.applySourceLineage(out)
	if out[0].SourceIsFork {
		t.Fatalf("Forks 未接线时不应置位")
	}
	s.applySourceLineage(nil)
	(&Server{}).applySourceLineage(out)
}
