package api

// lineage_multi_upstream_test.go —— 0.6.326 溯源批次「多上游」边界测试。
//
// 用本地伪造的「聚合源」（脚本同步多个打包者）+「双重身份源」（fork + 脚本同步）+
// 「同名撞车」（两个打包者有同名 appname）驱动 applyUpstreamLineage，验证：
//   每张卡独立溯源、不整仓覆盖、不误判。
//
// 场景覆盖（对应用户追问）：
//   ① 脚本同步多个不同开发者 —— 聚合源每卡溯源到各自打包者；
//   ② 脚本同步 + fork 同时存在 —— 双重身份源，fork 应用→parent、脚本应用→各自打包者；
//   ③ 同名撞车 —— 同名 appname 不同打包者，按 maintainer_url 溯源到正确打包者；
//   ④ 开源项目未收录 —— 脚本应用 mu 指向未收录开源项目，整仓 fork 交叉印证防覆盖；
//   ⑤ releaseOrigin 转口 —— download_url 指向非原作者，作者声明优先修正。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// newMultiUpstreamServer 伪造三个打包者索引仓（RROrg / shuangji66 / EWEDLCM）+
// 聚合源 foo（非 fork，脚本同步三家的应用）+ 双重身份源 bar（fork 自 RROrg，又脚本
// 同步 shuangji66）。RROrg 与 shuangji66 各有一个同名 appname "terminal"（不同应用）。
func newMultiUpstreamServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "RROrg", URL: "https://github.com/RROrg/fn-apps"},
		{Name: "shuangji66", URL: "https://github.com/shuangji66/FnDepot"},
		{Name: "EWEDLCM", URL: "https://github.com/EWEDLCM/FnDepot"},
		{Name: "聚合源", URL: "https://github.com/foo/FnDepot"},
		{Name: "双重身份源", URL: "https://github.com/bar/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"RROrg": {
			"terminal": {Name: "terminal", Source: "RROrg", DisplayName: "RROrg-Terminal", Version: "1.0.0"},
			"vm-tools": {Name: "vm-tools", Source: "RROrg", DisplayName: "vm-tools", Version: "1.1.0"},
		},
		"shuangji66": {
			"terminal": {Name: "terminal", Source: "shuangji66", DisplayName: "shuangji66-Terminal", Version: "2.0.0"},
			"adguard":  {Name: "adguard", Source: "shuangji66", DisplayName: "AdGuard", Version: "1.5.0"},
		},
		"EWEDLCM": {
			"picoclaw": {Name: "picoclaw", Source: "EWEDLCM", DisplayName: "picoclaw", Version: "0.3.1"},
		},
		"聚合源": {
			"vm-tools": {Name: "vm-tools", Source: "聚合源", DisplayName: "vm-tools", Version: "1.0.5"},
			"adguard":  {Name: "adguard", Source: "聚合源", DisplayName: "AdGuard", Version: "1.4.0"},
			"picoclaw": {Name: "picoclaw", Source: "聚合源", DisplayName: "picoclaw", Version: "0.2.8"},
			"terminal": {Name: "terminal", Source: "聚合源", DisplayName: "shuangji66-Terminal", Version: "2.0.0"},
		},
		"双重身份源": {
			"vm-tools": {Name: "vm-tools", Source: "双重身份源", DisplayName: "vm-tools", Version: "1.0.5"},
			"adguard":  {Name: "adguard", Source: "双重身份源", DisplayName: "AdGuard", Version: "1.4.0"},
		},
	})
	f := source.NewGHForks("")
	f.SetForTest("bar/fndepot", "RROrg/fn-apps") // 双重身份源 fork 自 RROrg
	f.SetForTest("foo/fndepot", "")              // 聚合源非 fork
	f.SetForTest("rrorg/fn-apps", "")
	f.SetForTest("shuangji66/fndepot", "")
	f.SetForTest("ewedlcm/fndepot", "")
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// mkMultiCard 造多上游测试卡。mu=maintainer_url（作者声明），releaseRepo=包所在仓库
//（聚合/搬运源通常把包放自己仓，releaseRepo==本源仓库，触发「包在本源内」分支）。
func mkMultiCard(key, app, src, disp, ver, mu, releaseRepo, distributor string) AppInfo {
	a := AppInfo{Key: key, AppName: app, DisplayName: disp, Source: src, LatestVersion: ver}
	if mu != "" {
		a.MaintainerURL = "https://github.com/" + mu
	}
	if releaseRepo != "" {
		a.ReleaseURL = "https://github.com/" + releaseRepo + "/releases/download/v" + ver + "/" + app + ".fpk"
	}
	if distributor != "" {
		a.Distributor = distributor
	}
	return a
}

// ① 脚本同步多个不同开发者：聚合源每卡按 maintainer_url 独立溯源到各自打包者。
func TestMultiUpstream_AggregateScriptSync(t *testing.T) {
	s := newMultiUpstreamServer(t)
	out := []AppInfo{
		mkMultiCard("vm-tools@聚合源", "vm-tools", "聚合源", "vm-tools", "1.0.5", "RROrg/fn-apps", "foo/FnDepot", ""),
		mkMultiCard("adguard@聚合源", "adguard", "聚合源", "AdGuard", "1.4.0", "shuangji66/FnDepot", "foo/FnDepot", ""),
		mkMultiCard("picoclaw@聚合源", "picoclaw", "聚合源", "picoclaw", "0.2.8", "EWEDLCM/FnDepot", "foo/FnDepot", ""),
	}
	s.applyUpstreamLineage(out)
	want := map[string][3]string{
		"vm-tools": {"rrorg/fn-apps", "RROrg", "1.1.0"},
		"adguard":  {"shuangji66/fndepot", "shuangji66", "1.5.0"},
		"picoclaw": {"ewedlcm/fndepot", "EWEDLCM", "0.3.1"},
	}
	for _, a := range out {
		w := want[a.AppName]
		if a.UpstreamRepo != w[0] || a.UpstreamSource != w[1] || a.UpstreamVersion != w[2] {
			t.Fatalf("%s 应溯源到 %s/%s@%s，got repo=%q src=%q ver=%q",
				a.Key, w[0], w[1], w[2], a.UpstreamRepo, a.UpstreamSource, a.UpstreamVersion)
		}
	}
}

// ② 脚本同步 + fork 同时存在：双重身份源 fork 自 RROrg，但脚本同步的 adguard 来自
// shuangji66 —— adguard 不得被整仓 fork 覆盖到 RROrg。
func TestMultiUpstream_DualIdentity(t *testing.T) {
	s := newMultiUpstreamServer(t)
	out := []AppInfo{
		mkMultiCard("vm-tools@双重身份源", "vm-tools", "双重身份源", "vm-tools", "1.0.5", "RROrg/fn-apps", "bar/FnDepot", ""),
		mkMultiCard("adguard@双重身份源", "adguard", "双重身份源", "AdGuard", "1.4.0", "shuangji66/FnDepot", "bar/FnDepot", ""),
	}
	s.applyUpstreamLineage(out)
	// fork 继承的 vm-tools → RROrg
	if out[0].UpstreamRepo != "rrorg/fn-apps" || out[0].UpstreamSource != "RROrg" {
		t.Fatalf("vm-tools 应溯源到 RROrg，got %#v", out[0])
	}
	// 脚本同步的 adguard → shuangji66（关键：不覆盖到 RROrg）
	if out[1].UpstreamRepo != "shuangji66/fndepot" || out[1].UpstreamSource != "shuangji66" {
		t.Fatalf("adguard 应溯源到 shuangji66（不覆盖到 fork parent RROrg），got repo=%q src=%q",
			out[1].UpstreamRepo, out[1].UpstreamSource)
	}
}

// ③ 同名撞车：RROrg 与 shuangji66 都有 appname "terminal"（不同应用）。聚合源收录的是
// shuangji66 的 terminal（maintainer_url 指向 shuangji66）→ 溯源到 shuangji66，绝不误判到 RROrg。
func TestMultiUpstream_SameNameCollision(t *testing.T) {
	s := newMultiUpstreamServer(t)
	out := []AppInfo{
		mkMultiCard("terminal@聚合源", "terminal", "聚合源", "shuangji66-Terminal", "2.0.0", "shuangji66/FnDepot", "foo/FnDepot", ""),
	}
	s.applyUpstreamLineage(out)
	a := out[0]
	if a.UpstreamRepo != "shuangji66/fndepot" || a.UpstreamSource != "shuangji66" {
		t.Fatalf("同名撞车应溯源到 maintainer_url 指向的 shuangji66，got repo=%q src=%q（误判到 RROrg？）",
			a.UpstreamRepo, a.UpstreamSource)
	}
	if a.UpstreamVersion != "2.0.0" {
		t.Fatalf("同名卡版本应取 shuangji66 的 2.0.0，got %q", a.UpstreamVersion)
	}
}

// ④ 开源项目未收录：脚本同步的应用 maintainer_url 指向未收录开源项目（AdguardTeam），
// 且 fork parent（RROrg）源里无同名卡 → 整仓 fork 交叉印证失败，不得覆盖到 RROrg。
func TestMultiUpstream_OpenSourceNotCollected(t *testing.T) {
	s := newMultiUpstreamServer(t)
	out := []AppInfo{
		mkMultiCard("adguard-open@双重身份源", "adguard-open", "双重身份源", "AdGuardOpen", "1.0.0", "AdguardTeam/AdGuardHome", "bar/FnDepot", "shuangji66"),
	}
	s.applyUpstreamLineage(out)
	a := out[0]
	// mu 未收录 → 作者声明不命中；整仓 fork 交叉印证：RROrg 源无 adguard-open → 不判搬运。
	if a.UpstreamRepo != "" || a.UpstreamSource != "" {
		t.Fatalf("未收录开源项目 + parent 无同名卡，应无溯源（不覆盖到 RROrg），got %#v", a)
	}
}

// ⑤ releaseOrigin 转口：download_url 指向 RROrg 的 release（releaseOrigin=RROrg），但
// maintainer_url 指向 shuangji66 → 作者声明优先，溯源到 shuangji66（修正转口）。
func TestMultiUpstream_ReleaseOriginTransshipment(t *testing.T) {
	s := newMultiUpstreamServer(t)
	out := []AppInfo{
		mkMultiCard("terminal@聚合源", "terminal", "聚合源", "shuangji66-Terminal", "2.0.0", "shuangji66/FnDepot", "RROrg/fn-apps", ""),
	}
	s.applyUpstreamLineage(out)
	a := out[0]
	if a.UpstreamRepo != "shuangji66/fndepot" || a.UpstreamSource != "shuangji66" {
		t.Fatalf("作者声明应优先于 releaseOrigin 转口，溯源到 shuangji66，got repo=%q src=%q",
			a.UpstreamRepo, a.UpstreamSource)
	}
}
