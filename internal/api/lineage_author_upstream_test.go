package api

// lineage_author_upstream_test.go —— 0.6.326 溯源批次「作者声明上游
// （maintainer_url）」单测。
//
// 覆盖：maintainer_url 指向已收录打包者索引仓且 owner ≠ 本源 owner 时，作者
// 声明优先于 fork 转口（mah1618 非 fork 搬运 RROrg、其 fork ganx2024 停在
// mah1618 转口的链断场景）；源内无同名卡时只填 repo/url/source、不填 version
// （展示层）；未收录开源项目不覆盖 fork parent；同 owner 自建仓 / 主页形态
// 均不判上游。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// newAuthorUpstreamServer 作者声明上游测试环境：
// RROrg（打包者索引仓）← mah1618（非 fork 搬运）← ganx2024（fork 自 mah1618）；
// shuangji66（打包者索引仓）← wabisabi926（fork 自 shuangji66，其 maintainer_url
// 指向未收录的开源项目）。
func newAuthorUpstreamServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "RROrg", URL: "https://github.com/RROrg/fn-apps"},
		{Name: "mah1618", URL: "https://github.com/mah1618/FnDepot"},
		{Name: "ganx2024", URL: "https://github.com/ganx2024/FnDepot"},
		{Name: "shuangji66", URL: "https://github.com/shuangji66/FnDepot"},
		{Name: "wabisabi926", URL: "https://github.com/wabisabi926/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"RROrg": {
			"fn-terminal": {Name: "fn-terminal", Source: "RROrg", Version: "1.1.2"},
		},
		"ganx2024": {
			"fn-terminal": {Name: "fn-terminal", Source: "ganx2024", Version: "1.0.1"},
		},
		"mah1618": {
			"fn-terminal": {Name: "fn-terminal", Source: "mah1618", Version: "1.0.1"},
		},
		"shuangji66": {
			"AdGuard": {Name: "AdGuard", Source: "shuangji66", Version: "1.0.0"},
		},
	})
	f := source.NewGHForks("")
	f.SetForTest("ganx2024/fndepot", "mah1618/FnDepot") // ganx2024 fork 自 mah1618
	f.SetForTest("mah1618/fndepot", "")                 // mah1618 非 fork（搬运镜像）
	f.SetForTest("wabisabi926/fndepot", "shuangji66/FnDepot") // fork 自 shuangji66
	f.SetForTest("shuangji66/fndepot", "")               // shuangji66 非 fork（打包者）
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// ① 作者声明上游命中（覆盖 fork 转口）：ganx2024 卡的 maintainer_url 指向
// RROrg/fn-apps（已收录），owner 不同 → 溯源到 RROrg，而非 fork parent mah1618。
func TestApplyUpstreamLineage_AuthorDeclaredUpstream(t *testing.T) {
	s := newAuthorUpstreamServer(t)
	out := []AppInfo{
		mkLineageCard("fn-terminal@ganx2024", "fn-terminal", "ganx2024", "1.0.1", "ganx2024/FnDepot", false),
	}
	out[0].Maintainer = "RROrg"
	out[0].MaintainerURL = "https://github.com/RROrg/fn-apps"
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "rrorg/fn-apps" {
		t.Fatalf("应溯源到作者声明仓库 RROrg，UpstreamRepo=%q", m.UpstreamRepo)
	}
	if m.UpstreamURL != "https://github.com/rrorg/fn-apps" {
		t.Fatalf("UpstreamURL=%q", m.UpstreamURL)
	}
	if m.UpstreamSource != "RROrg" {
		t.Fatalf("UpstreamSource=%q, want RROrg", m.UpstreamSource)
	}
	if m.UpstreamVersion != "1.1.2" {
		t.Fatalf("UpstreamVersion=%q, want 1.1.2", m.UpstreamVersion)
	}
}

// ② 展示层：maintainer_url 指向已收录源，但该源内无同名卡（上游已下架该应用）
// → 仍填 repo/url/source，version 留空（诚实）。
func TestApplyUpstreamLineage_AuthorDeclaredNoSibling(t *testing.T) {
	s := newAuthorUpstreamServer(t)
	out := []AppInfo{
		mkLineageCard("fn-msedge@mah1618", "fn-msedge", "mah1618", "1.0.1", "mah1618/FnDepot", false),
	}
	out[0].Maintainer = "RROrg"
	out[0].MaintainerURL = "https://github.com/RROrg/fn-apps"
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "rrorg/fn-apps" || m.UpstreamSource != "RROrg" {
		t.Fatalf("无同名卡也应填 repo/source：%#v", m)
	}
	if m.UpstreamVersion != "" {
		t.Fatalf("无同名卡不应填 version：%#v", m)
	}
}

// ③ 未收录开源项目不覆盖 fork parent：wabisabi926 的 maintainer_url 指向
// AdguardTeam/AdGuardHome（未收录为源）→ 作者声明不命中，走整仓 fork 分支
// 溯源到 shuangji66（打包者）。
func TestApplyUpstreamLineage_AuthorDeclaredOpenSourceNotOverridden(t *testing.T) {
	s := newAuthorUpstreamServer(t)
	out := []AppInfo{
		mkLineageCard("AdGuard@wabisabi926", "AdGuard", "wabisabi926", "1.0.0", "wabisabi926/FnDepot", false),
	}
	out[0].Maintainer = "AdguardTeam"
	out[0].MaintainerURL = "https://github.com/AdguardTeam/AdGuardHome"
	out[0].Distributor = "shuangji66"
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "shuangji66/fndepot" {
		t.Fatalf("未收录开源项目应走 fork parent 溯源到 shuangji66，UpstreamRepo=%q", m.UpstreamRepo)
	}
}

// ④ 同 owner 声明（作者自建仓）不判上游。
func TestApplyUpstreamLineage_AuthorDeclaredSameOwner(t *testing.T) {
	s := newAuthorUpstreamServer(t)
	out := []AppInfo{
		mkLineageCard("fn-terminal@RROrg", "fn-terminal", "RROrg", "1.1.2", "RROrg/fn-apps", false),
	}
	out[0].Maintainer = "RROrg"
	out[0].MaintainerURL = "https://github.com/RROrg/fn-apps"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" {
		t.Fatalf("同 owner 作者自建仓不应判上游：%#v", out[0])
	}
}

// ⑤ 主页形态 maintainer_url（无 repo 段）不判上游。
func TestApplyUpstreamLineage_AuthorDeclaredHomepageOnly(t *testing.T) {
	s := newAuthorUpstreamServer(t)
	out := []AppInfo{
		mkLineageCard("x@ganx2024", "x", "ganx2024", "1.0.0", "ganx2024/FnDepot", false),
	}
	out[0].Maintainer = "somebody"
	out[0].MaintainerURL = "https://github.com/somebody"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" {
		t.Fatalf("主页形态 maintainer_url 不应判上游：%#v", out[0])
	}
}
