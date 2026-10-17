package api

// lineage_326z_test.go —— 0.6.326z：溯源「二创打包」误判修复单测。
//
// 用户报 GenOffice（及飞牛便签）溯源上游有误：它们都是「作者 fork 了
// EWEDLCM/FnDepot 索引框架、往里加自己打包的应用」的二创项目，却被整仓 fork
// 判定当成了「搬运 EWEDLCM」。
//
// 修复：主路径整仓 fork 分支补上「交叉印证」——fork parent 已收录时，源里
// 必须有同名卡才判搬运（与 applyForkParentFallback 同口径）。parent 里没有
// 同名卡 = fork 者自加的二创，不判搬运。
//
// 覆盖：二创不判搬运（GenOffice / 飞牛便签）/ 真搬运仍判搬运（picoclaw 回归）。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// GenOffice：tingwensuojian（听闻）fork 了 EWEDLCM/FnDepot 索引框架，往里加
// 自己打包的 GenOffice（maintainer=原作者 Genspark、distributor=打包者听闻）。
// parent（EWEDLCM/FnDepot）里没有 GenOffice → 二创，不判搬运。
func TestLineage326Z_SecondCreationNotUpstream(t *testing.T) {
	s := newForkLineageServer(t)
	out := []AppInfo{mkLineageCard("genoffice@Fork搬运源", "genoffice", "Fork搬运源", "0.10.63", "Chordi0124/FnDepot", false)}
	out[0].Distributor = "听闻" // fork 者自己的中文署名（≠ 英文 owner "chordi0124"）
	out[0].Maintainer = "Genspark"
	out[0].MaintainerURL = "https://github.com/genspark-ai/genoffice"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" {
		t.Fatalf("二创项目不应判搬运（parent 里无同名卡）：%#v", out[0])
	}
}

// 真搬运回归：picoclaw 在 parent（EWEDLCM/FnDepot）里有同名卡 → 仍判搬运，
// 上游 = fork parent。
func TestLineage326Z_RealForkMoveStillUpstream(t *testing.T) {
	s := newForkLineageServer(t)
	out := []AppInfo{mkLineageCard("picoclaw@Fork搬运源", "picoclaw", "Fork搬运源", "0.2.8", "Chordi0124/FnDepot", false)}
	out[0].Distributor = "EWEDL"
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "ewedlcm/fndepot" {
		t.Fatalf("真搬运应溯源到 fork parent，UpstreamRepo = %q", m.UpstreamRepo)
	}
	if m.UpstreamSource != "上游源" || m.UpstreamVersion != "0.3.1" {
		t.Fatalf("parent 是 Moo 的源时应填 UpstreamSource/Version：%#v", m)
	}
}

// 飞牛便签：cliii-one/FnDepot fork 自 EWEDLCM/FnDepot，但 EWEDLCM 里没有
// fnnas.notes（cliii-one 自己加的）→ 二创，不判搬运。
func TestLineage326Z_FeiniNotesSecondCreation(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "EWEDLCM", URL: "https://github.com/EWEDLCM/FnDepot"},
		{Name: "cliii-one", URL: "https://github.com/cliii-one/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"EWEDLCM": {"picoclaw": {Name: "picoclaw", Source: "EWEDLCM", Version: "0.3.1"}},
	})
	f := source.NewGHForks("")
	f.SetForTest("cliii-one/fndepot", "EWEDLCM/FnDepot")
	s := &Server{Cfg: cfg, Src: m, Forks: f}
	out := []AppInfo{mkLineageCard("fnnas.notes@cliii-one", "fnnas.notes", "cliii-one", "1.0.4", "cliii-one/FnDepot", false)}
	out[0].Distributor = "cliii_one"
	out[0].Maintainer = "cliii_one"
	out[0].MaintainerURL = "https://github.com/cliii-one/FnStore"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" {
		t.Fatalf("飞牛便签是二创不应判搬运（parent 里无同名卡）：%#v", out[0])
	}
}
