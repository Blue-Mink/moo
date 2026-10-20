package api

// lineage_350_test.go —— 0.6.350 溯源（lineage）全场景 + 盲点测试（WS3）。
//
// 与既有 lineage_*_test.go 的关系：那些文件按「批次」钉住单个修复的缺陷态，
// 本文件按「判定矩阵」把 fork 判定 / 作者声明上游 / releaseOrigin / 显示名否决
// / 版本过滤 / 添加上游提示六条路径的**全场景**收敛到表驱动用例，作为 0.6.350
// 的回归面。命名统一 TestWS3_ 前缀，便于 `-run 'WS3'` 单跑。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/source"
)

// ── A. fork 判定矩阵 ───────────────────────────────────────────────────────
//
// 覆盖：整仓 fork 式搬运（parent 在/不在源列表）、distributor==owner 不误判、
// fork=false 同 owner 不误判、自环、非 GitHub 主机不查 fork 缓存。
func TestWS3_ForkMatrix(t *testing.T) {
	type tc struct {
		name       string
		refs       []config.SourceRef
		cache      map[string]map[string]*source.App
		forks      map[string]string
		dist       string
		url        string // "" = 无包地址（走 fork 兜底）
		wantRepo   string
		wantSrc    string
		wantVer    string
		wantURL    string
	}
	cases := []tc{
		{
			name:  "整仓fork搬运·parent在源列表",
			refs:  []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}, {Name: "父源", URL: "https://github.com/alice/awesome-apps"}},
			cache: map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"}), "父源": srcCache(map[string]string{"app-a": "2.5.0"})},
			forks: map[string]string{"carol/fndepot": "alice/awesome-apps"},
			dist:  "第三方打包者",
			url:   "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk",
			wantRepo: "alice/awesome-apps", wantSrc: "父源", wantVer: "2.5.0",
			wantURL: "https://github.com/alice/awesome-apps",
		},
		{
			name:  "整仓fork搬运·parent不在源列表（只填repo/url）",
			refs:  []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}},
			cache: map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
			forks: map[string]string{"carol/fndepot": "alice/awesome-apps"},
			dist:  "第三方打包者",
			url:   "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk",
			wantRepo: "alice/awesome-apps", wantSrc: "", wantVer: "",
			wantURL: "https://github.com/alice/awesome-apps",
		},
		{
			name:  "fork=true 但 distributor==owner → 不误判",
			refs:  []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}},
			cache: map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
			forks: map[string]string{"carol/fndepot": "alice/awesome-apps"},
			dist:  "carol", // == owner
			url:   "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk",
		},
		{
			name:  "fork=false 同 owner → 不误判（作者自建仓）",
			refs:  []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}},
			cache: map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
			forks: map[string]string{"carol/fndepot": ""},
			dist:  "第三方打包者",
			url:   "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk",
		},
		{
			name:  "自环 parent==self → 不 panic 不误判",
			refs:  []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}},
			cache: map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
			forks: map[string]string{"carol/fndepot": "carol/fndepot"},
			dist:  "第三方打包者",
			url:   "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk",
		},
		{
			name:  "非 GitHub 主机源 → 不查 fork 缓存",
			refs:  []config.SourceRef{{Name: "内网源", URL: "http://192.0.2.15:3033/bluemink/moo"}},
			cache: map[string]map[string]*source.App{"内网源": srcCache(map[string]string{"app-a": "1.0.0"})},
			forks: map[string]string{"bluemink/moo": "alice/awesome-apps"},
			dist:  "第三方打包者",
			url:   "", // 无包地址 → 走 fork 兜底，必须不查 GitHub 同名仓
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := lkServer(t, c.refs, c.cache, c.forks)
			card := mkURL("app-a@本源", "app-a", c.refs[0].Name, "1.0.0", c.url)
			card.Distributor = c.dist
			out := []AppInfo{card}
			s.applyUpstreamLineage(out)
			got := out[0]
			if got.UpstreamRepo != c.wantRepo || got.UpstreamSource != c.wantSrc ||
				got.UpstreamVersion != c.wantVer || got.UpstreamURL != c.wantURL {
				t.Fatalf("fork 判定不符：repo=%q(src=%q ver=%q url=%q)，期望 repo=%q src=%q ver=%q url=%q",
					got.UpstreamRepo, got.UpstreamSource, got.UpstreamVersion, got.UpstreamURL,
					c.wantRepo, c.wantSrc, c.wantVer, c.wantURL)
			}
		})
	}
}

// ── B. 作者声明上游 maintainer_url 矩阵 ─────────────────────────────────────
func TestWS3_AuthorDeclaredMatrix(t *testing.T) {
	// B1 已收录 + owner≠本源 → 优先于 fork 转口
	s := lkServer(t,
		[]config.SourceRef{{Name: "RROrg", URL: "https://github.com/RROrg/fn-apps"}, {Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}},
		map[string]map[string]*source.App{"RROrg": srcCache(map[string]string{"fn-x": "1.1.2"}), "搬运源": srcCache(map[string]string{"fn-x": "1.0.0"})},
		map[string]string{"yuexps/fndepot": "alice/xxx"}) // fork 转口应被覆盖
	card := mkURL("fn-x@搬运源", "fn-x", "搬运源", "1.0.0", "https://github.com/yuexps/FnDepot/releases/download/v1.0.0/fn-x.fpk")
	card.Maintainer = "RROrg"
	card.MaintainerURL = "https://github.com/RROrg/fn-apps"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "rrorg/fn-apps" || out[0].UpstreamSource != "RROrg" || out[0].UpstreamVersion != "1.1.2" {
		t.Fatalf("B1 作者声明应优先于 fork 转口：%#v", out[0])
	}

	// B2 未收录开源项目 → 忽略（走 fork parent）
	s2 := lkServer(t,
		[]config.SourceRef{{Name: "打包者", URL: "https://github.com/shuangji66/FnDepot"}, {Name: "搬运源", URL: "https://github.com/wabisabi926/FnDepot"}},
		map[string]map[string]*source.App{"打包者": srcCache(map[string]string{"AdGuard": "1.0.0"}), "搬运源": srcCache(map[string]string{"AdGuard": "1.0.0"})},
		map[string]string{"wabisabi926/fndepot": "shuangji66/FnDepot"})
	c2 := mkURL("AdGuard@搬运源", "AdGuard", "搬运源", "1.0.0", "https://github.com/wabisabi926/FnDepot/releases/download/v1.0.0/AdGuard.fpk")
	c2.Maintainer = "AdguardTeam"
	c2.MaintainerURL = "https://github.com/AdguardTeam/AdGuardHome"
	c2.Distributor = "shuangji66"
	out2 := []AppInfo{c2}
	s2.applyUpstreamLineage(out2)
	if out2[0].UpstreamRepo != "shuangji66/fndepot" {
		t.Fatalf("B2 未收录开源项目应走 fork parent：%#v", out2[0])
	}

	// B3 已收录但无同名卡 → 只填 repo/url/source，不填 version
	s3 := lkServer(t,
		[]config.SourceRef{{Name: "RROrg", URL: "https://github.com/RROrg/fn-apps"}, {Name: "搬运源", URL: "https://github.com/mah1618/FnDepot"}},
		map[string]map[string]*source.App{"RROrg": srcCache(map[string]string{}), "搬运源": srcCache(map[string]string{"fn-msedge": "1.0.1"})},
		map[string]string{"mah1618/fndepot": ""})
	c3 := mkURL("fn-msedge@搬运源", "fn-msedge", "搬运源", "1.0.1", "https://github.com/mah1618/FnDepot/releases/download/v1.0.1/fn-msedge.fpk")
	c3.Maintainer = "RROrg"
	c3.MaintainerURL = "https://github.com/RROrg/fn-apps"
	out3 := []AppInfo{c3}
	s3.applyUpstreamLineage(out3)
	if out3[0].UpstreamRepo != "rrorg/fn-apps" || out3[0].UpstreamSource != "RROrg" || out3[0].UpstreamVersion != "" {
		t.Fatalf("B3 无同名卡应只填 repo/source 不填 version：%#v", out3[0])
	}

	// B4 同 owner → 忽略
	s4 := lkServer(t,
		[]config.SourceRef{{Name: "RROrg", URL: "https://github.com/RROrg/fn-apps"}},
		map[string]map[string]*source.App{"RROrg": srcCache(map[string]string{"fn-x": "1.1.2"})},
		map[string]string{"rrorg/fn-apps": ""})
	c4 := mkURL("fn-x@RROrg", "fn-x", "RROrg", "1.1.2", "https://github.com/RROrg/fn-apps/releases/download/v1.1.2/fn-x.fpk")
	c4.Maintainer = "RROrg"
	c4.MaintainerURL = "https://github.com/RROrg/fn-apps"
	out4 := []AppInfo{c4}
	s4.applyUpstreamLineage(out4)
	if out4[0].UpstreamRepo != "" || out4[0].UpstreamSource != "" {
		t.Fatalf("B4 同 owner 自建仓不应判上游：%#v", out4[0])
	}
}

// ── D. 展示名否决 / 版本过滤 / v 前缀 ───────────────────────────────────────
func TestWS3_DisplayNameAndVersionGuard(t *testing.T) {
	// D1 displayName 不兼容 → 不纠正（同 appname 不同应用）
	refs := []config.SourceRef{{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}, {Name: "上游源", URL: "https://github.com/alice/awesome-apps"}}
	s := lkServer(t, refs, map[string]map[string]*source.App{
		"搬运源": srcCache(map[string]string{"clone-a": "1.9.0"}),
		"上游源": {"clone-a": {Name: "clone-a", DisplayName: "完全无关的应用", Version: "9.9.9"}},
	}, map[string]string{"yuexps/fndepot": ""})
	card := mkURL("clone-a@搬运源", "clone-a", "搬运源", "1.9.0", "https://github.com/alice/awesome-apps/releases/download/v1.9.0/clone-a.fpk")
	card.Installed, card.InstalledVersion = true, "1.9.0"
	unrel := mkURL("clone-a@上游源", "clone-a", "上游源", "9.9.9", "https://github.com/zed/unrelated/releases/download/v9.9.9/o.fpk")
	unrel.DisplayName = "完全无关的应用"
	out := []AppInfo{card, unrel}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamVersion != "" {
		t.Fatalf("D1 显示名不兼容不得取上游版本：%#v", out[0])
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("D1 显示名不兼容不得点亮更新：%#v", out[0])
	}

	// D2 不可解析版本 → comparableVersion=false；D3 预发布 → isPrereleaseVersion=true；D4 前导 v
	for _, tc := range []struct{ in string; comparable, prerelease bool }{
		{"1.2.3", true, false},
		{"v1.2.3", true, false},
		{"abc", false, false},
		{"1.2.x", false, false},
		{"", false, false},
		{"1.0.0-rc1", true, true}, // splitVer 剥 -rc 后剩 1.0.0 → 可解析；但预发布=true
		{"2.0.0-beta2", true, true},
	} {
		if got := comparableVersion(tc.in); got != tc.comparable {
			t.Fatalf("D2 comparableVersion(%q)=%v 期望 %v", tc.in, got, tc.comparable)
		}
		if got := isPrereleaseVersion(tc.in); got != tc.prerelease {
			t.Fatalf("D3 isPrereleaseVersion(%q)=%v 期望 %v", tc.in, got, tc.prerelease)
		}
	}
	// D4 splitVer 前导 v 剥离
	if got := splitVer("v2.5.0"); len(got) == 0 || got[0] != "2" {
		t.Fatalf("D4 splitVer 应剥前导 v：%v", got)
	}
}

// ── E. upstream_add 提示（索引探测）矩阵 ────────────────────────────────────
func TestWS3_UpstreamAddHintMatrix(t *testing.T) {
	// E1 上游仓自己有索引 → UpstreamAddURL 指向上游仓
	s := lkServer(t,
		[]config.SourceRef{{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}},
		map[string]map[string]*source.App{"搬运源": srcCache(map[string]string{"app-a": "1.0.0"})},
		map[string]string{"yuexps/fndepot": ""})
	s.AddProbes = source.NewAddProbe("")
	s.AddProbes.SetForTest("alice/awesome-apps", "moo.json", "")
	out := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.0.0", "alice/awesome-apps", false)}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamAddURL != "https://github.com/alice/awesome-apps" || out[0].UpstreamAddNote != "" {
		t.Fatalf("E1 本仓有索引应给 add_url：%#v", out[0])
	}

	// E2 本仓无索引但 owner 有索引仓且已收录 → 设 source + note，不给 url
	s2 := lkServer(t,
		[]config.SourceRef{{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}, {Name: "dave源", URL: "https://github.com/Dave/FnDepot"}},
		map[string]map[string]*source.App{"搬运源": srcCache(map[string]string{"app-a": "0.9.0"}), "dave源": srcCache(map[string]string{"app-a": "1.0.0"})},
		map[string]string{"yuexps/fndepot": ""})
	s2.AddProbes = source.NewAddProbe("")
	s2.AddProbes.SetForTest("dave/pack-only", "", "Dave/FnDepot")
	out2 := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "0.9.0", "dave/pack-only", false)}
	s2.applyUpstreamLineage(out2)
	if out2[0].UpstreamSource != "dave源" || out2[0].UpstreamVersion != "1.0.0" {
		t.Fatalf("E2 已收录 owner 索引仓应设 source+version：%#v", out2[0])
	}
	if out2[0].UpstreamAddURL != "" || out2[0].UpstreamAddNote == "" {
		t.Fatalf("E2 已收录应给 note 不给 url：%#v", out2[0])
	}

	// E3 两处都无索引 → 不可加，给原因
	s3 := lkServer(t,
		[]config.SourceRef{{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}},
		map[string]map[string]*source.App{"搬运源": srcCache(map[string]string{"app-a": "1.0.0"})},
		map[string]string{"yuexps/fndepot": ""})
	s3.AddProbes = source.NewAddProbe("")
	s3.AddProbes.SetForTest("zed/pack-only", "", "")
	out3 := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.0.0", "zed/pack-only", false)}
	s3.applyUpstreamLineage(out3)
	if out3[0].UpstreamAddNote == "" || out3[0].UpstreamAddURL != "" {
		t.Fatalf("E3 不可加应给原因不给 url：%#v", out3[0])
	}

	// E4 上游源已收录时不上 hint（走正常 UpstreamSource 路径）
	s4 := lkServer(t,
		[]config.SourceRef{{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"}, {Name: "上游源", URL: "https://github.com/alice/awesome-apps"}},
		map[string]map[string]*source.App{"搬运源": srcCache(map[string]string{"app-a": "1.0.0"}), "上游源": srcCache(map[string]string{"app-a": "2.5.0"})},
		map[string]string{"yuexps/fndepot": ""})
	s4.AddProbes = source.NewAddProbe("")
	out4 := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.0.0", "alice/awesome-apps", false)}
	s4.applyUpstreamLineage(out4)
	if out4[0].UpstreamSource != "上游源" || out4[0].UpstreamVersion != "2.5.0" {
		t.Fatalf("E4 已收录应走正常溯源：%#v", out4[0])
	}
	if out4[0].UpstreamAddNote != "" || out4[0].UpstreamAddURL != "" {
		t.Fatalf("E4 已收录不应有 add hint：%#v", out4[0])
	}
}
