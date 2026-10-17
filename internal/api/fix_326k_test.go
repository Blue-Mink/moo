package api

// fix_326k_test.go —— 0.6.326k WP1（internal/api 溯源/自动纠正缺陷）修复验证单测。
//
// 覆盖 F1–F7 七条修复，每条都带「修复前必然失败」的断言（证据见
// FIX_326K_WP1_REPORT.md「修复前失败证据」节：把 7 条生产改动逐条回退后重跑本
// 文件，逐用例复现失败）。
//
// 本文件自带最小环境（不复用 lineage_326k_test.go 的 helpers，避免与审计产物
// 产生耦合）。

import (
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/source"
)

// f326kEnv 组装溯源环境：refs=源列表；cache=源目录缓存（源名→appname→App）；
// forks=预报的 fork 关系（owner/repo → parent，parent 为空 = 非 fork）。
func f326kEnv(t *testing.T, refs []config.SourceRef, cache map[string]map[string]*source.App, forks map[string]string) *Server {
	t.Helper()
	cfg := &config.Config{Sources: refs}
	m := source.NewManager(cfg)
	if cache != nil {
		m.SeedCacheForTest(cache)
	}
	f := source.NewGHForks("")
	for k, v := range forks {
		f.SetForTest(k, v)
	}
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// f326kCard 造卡（DisplayName = appname）。
func f326kCard(key, appname, src, ver, url string) AppInfo {
	a := AppInfo{Key: key, AppName: appname, DisplayName: appname, Source: src, LatestVersion: ver}
	a.ReleaseURL = url
	return a
}

// f326kSrcCache 源缓存（appname → 版本，不带 display_name）。
func f326kSrcCache(entries map[string]string) map[string]*source.App {
	out := map[string]*source.App{}
	for name, ver := range entries {
		out[name] = &source.App{Name: name, Version: ver}
	}
	return out
}

// f326kStd 标准环境：上游源 alice/awesome-apps（app-a=upVer）+ 搬运源
// yuexps/FnDepot（app-a=1.9.0，包发在 alice 仓 → 跨 owner = 判搬运）。
// 返回已装搬运卡 + 上游兄弟卡的目录切片（顺序：0=搬运卡，1=上游卡）。
func f326kStd(t *testing.T, upVer string) (*Server, []AppInfo) {
	t.Helper()
	s := f326kEnv(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
			{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"app-a": {Name: "app-a", DisplayName: "app-a", Source: "上游源", Version: upVer}},
			"搬运源": f326kSrcCache(map[string]string{"app-a": "1.9.0"}),
		},
		map[string]string{"yuexps/fndepot": ""}) // 搬运源本身不是 fork
	inst := f326kCard("app-a@搬运源", "app-a", "搬运源", "1.9.0",
		"https://github.com/alice/awesome-apps/releases/download/v1.9.0/app-a.fpk")
	inst.Installed, inst.InstalledVersion = true, "1.9.0"
	sib := f326kCard("app-a@上游源", "app-a", "上游源", upVer,
		"https://github.com/alice/awesome-apps/releases/download/v"+upVer+"/app-a.fpk")
	return s, []AppInfo{inst, sib}
}

// ── F1 = B1：releaseOrigin 的 jsDelivr `gh/owner/repo@ref/…` 形态 ─────────────
//
// 修复前：`@ref` 被并进 repo 段 → "alice/awesome@main"：详情页「上游」是 404 链接、
// origin 永远匹配不上源列表（upstream_source 恒空 → 自动纠正 /「添加上游源」失效），
// 且同一仓库的 GitHub 形态卡与 jsDelivr 形态卡被判成不同宗（跨源更新与去重同时失效）。
func TestFix326K_F1_ReleaseOriginJsDelivrRef(t *testing.T) {
	// ① 直接断言（验收要求）
	if got := releaseOrigin("https://cdn.jsdelivr.net/gh/alice/awesome@main/x.fpk"); got != "alice/awesome" {
		t.Fatalf("jsDelivr @ref 形态应剥掉 ref：got %q want alice/awesome", got)
	}
	// ② 形态矩阵：修复目标 + 不得误伤（其余形态行为不变）
	for _, tc := range []struct{ name, in, want string }{
		{"jsdelivr @main", "https://cdn.jsdelivr.net/gh/alice/awesome@main/x.fpk", "alice/awesome"},
		{"jsdelivr @v1.2.3", "https://cdn.jsdelivr.net/gh/Alice/Awesome@v1.2.3/fnpack.json", "alice/awesome"},
		{"jsdelivr @main 深层路径", "https://cdn.jsdelivr.net/gh/alice/awesome@main/releases/download/v1.9.0/app-a.fpk", "alice/awesome"},
		{"jsdelivr 无 ref", "https://cdn.jsdelivr.net/gh/alice/awesome/fnpack.json", "alice/awesome"},
		{"github releases", "https://github.com/alice/awesome/releases/download/v1/a.fpk", "alice/awesome"},
		{"github blob 五段", "https://github.com/alice/awesome/blob/main/pkg/a.fpk", "alice/awesome"},
		{"raw 直链", "https://raw.githubusercontent.com/Alice/Awesome/main/a.fpk", "alice/awesome"},
		{"镜像前缀包裹 raw", "https://ghfast.top/https://raw.githubusercontent.com/alice/awesome/main/a.fpk", "alice/awesome"},
		{"gitlab 非 GitHub", "https://gitlab.com/alice/awesome/raw/main/a.fpk", ""},
		{"自有域名", "https://odn.cc/pkg/a.fpk", ""},
	} {
		if got := releaseOrigin(tc.in); got != tc.want {
			t.Fatalf("%s：releaseOrigin(%q) = %q，期望 %q", tc.name, tc.in, got, tc.want)
		}
	}
	// ③ 端到端：同一仓库的 jsDelivr 形态卡必须能接上已收录的同仓源
	s := f326kEnv(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/alice/awesome"},
			{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": f326kSrcCache(map[string]string{"app-a": "2.5.0"}),
			"搬运源": f326kSrcCache(map[string]string{"app-a": "1.9.0"}),
		},
		map[string]string{"yuexps/fndepot": ""})
	out := []AppInfo{f326kCard("app-a@搬运源", "app-a", "搬运源", "1.9.0",
		"https://cdn.jsdelivr.net/gh/alice/awesome@main/releases/download/v1.9.0/app-a.fpk")}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "alice/awesome" || out[0].UpstreamURL != "https://github.com/alice/awesome" {
		t.Fatalf("修复前：UpstreamRepo 会带 @main（404 链接）；got %+v", out[0])
	}
	if out[0].UpstreamSource != "上游源" || out[0].UpstreamVersion != "2.5.0" {
		t.Fatalf("修复前：带 @ref 的 origin 匹配不上源列表 → source/version 恒空；got %+v", out[0])
	}
}

// ── F2 = B2：非 GitHub 主机源不得查 GitHub fork 缓存 ──────────────────────────
//
// 修复前：本源 key "192.0.2.15:3033/bluemink/moo" 经 trimHost 变成裸
// "bluemink/moo" 就送进 Forks.Lookup —— GitHub 上同名仓库的 fork 关系会串到内网源
// 的卡上（错误的「上游」链接 + 错误的「禁用fork源」按钮，点下去会去禁用内网源）。
func TestFix326K_F2_NonGitHubSourceSkipsForkCache(t *testing.T) {
	refs := []config.SourceRef{
		{Name: "内网源", URL: "http://192.0.2.15:3033/bluemink/moo"},
		{Name: "GitHub源", URL: "https://github.com/bluemink/moo"},
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
	}
	cache := map[string]map[string]*source.App{
		"内网源":    f326kSrcCache(map[string]string{"app-a": "1.0.0"}),
		"GitHub源": f326kSrcCache(map[string]string{"app-a": "1.0.0"}),
		"上游源":    f326kSrcCache(map[string]string{"app-a": "2.5.0"}),
	}
	// GitHub 上的 bluemink/moo 恰好是 alice/awesome-apps 的 fork（与内网源无关）。
	s := f326kEnv(t, refs, cache, map[string]string{"bluemink/moo": "alice/awesome-apps"})

	// ① 同 owner 分支（包就发在本源仓库里）② 包地址拿不到 → 兜底分支
	// （applyForkParentFallback）：两条路径对非 GitHub 源都必须跳过 fork 判定。
	c1 := f326kCard("app-a@内网源", "app-a", "内网源", "1.0.0",
		"https://github.com/bluemink/moo/releases/download/v1.0.0/app-a.fpk")
	c2 := f326kCard("app-b@内网源", "app-b", "内网源", "1.0.0", "")
	c1.Distributor, c2.Distributor = "第三方打包者", "第三方打包者"
	out := []AppInfo{c1, c2}
	s.applyUpstreamLineage(out)
	for i, c := range out {
		if c.UpstreamRepo != "" || c.UpstreamSource != "" || c.UpstreamVersion != "" {
			t.Fatalf("内网源卡[%d] 不得拿 GitHub 同名仓的 fork 关系：%+v", i, c)
		}
	}
	// ③ source_is_fork 同样不得跨主机串味（否则详情页会出「禁用fork源」按钮）
	s.applySourceLineage(out)
	for i, c := range out {
		if c.SourceIsFork {
			t.Fatalf("内网源卡[%d] 不得置 SourceIsFork：%+v", i, c)
		}
	}
	// ④ 对照（防过度拦截）：同一个 fork 缓存，GitHub 源照常判定
	gh := f326kCard("app-a@GitHub源", "app-a", "GitHub源", "1.0.0",
		"https://github.com/bluemink/moo/releases/download/v1.0.0/app-a.fpk")
	gh.Distributor = "第三方打包者"
	out2 := []AppInfo{gh}
	s.applyUpstreamLineage(out2)
	if out2[0].UpstreamRepo != "alice/awesome-apps" || out2[0].UpstreamSource != "上游源" || out2[0].UpstreamVersion != "2.5.0" {
		t.Fatalf("对照：GitHub 源的整仓 fork 搬运仍应生效：%+v", out2[0])
	}
	s.applySourceLineage(out2)
	if !out2[0].SourceIsFork {
		t.Fatalf("对照：GitHub 源是 fork 时应置 SourceIsFork：%+v", out2[0])
	}
}

// ── F3 = B3：fork parent 指向自身（自环）时必须跳过 ───────────────────────────
//
// 修复前：缓存脏数据给出 parent == 本仓时，卡片把**自己**写成上游
// （详情页出现「上游 <自己的仓库>」自环），SourceIsFork 也会被误置（多出
// 「禁用fork源」按钮）。
func TestFix326K_F3_SelfForkParentGuard(t *testing.T) {
	refs := []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}}
	cache := map[string]map[string]*source.App{
		"本源": f326kSrcCache(map[string]string{"app-a": "1.0.0", "app-b": "1.0.0"}),
	}
	s := f326kEnv(t, refs, cache, map[string]string{"carol/fndepot": "carol/fndepot"}) // 自环脏数据

	c1 := f326kCard("app-a@本源", "app-a", "本源", "1.0.0",
		"https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk")
	c2 := f326kCard("app-b@本源", "app-b", "本源", "1.0.0", "") // 走 fork 兜底分支
	c1.Distributor, c2.Distributor = "其他人", "其他人"
	out := []AppInfo{c1, c2}
	s.applyUpstreamLineage(out)
	for i, c := range out {
		if c.UpstreamRepo != "" || c.UpstreamSource != "" || c.UpstreamVersion != "" {
			t.Fatalf("自环 parent 不得写成上游（卡[%d]）：%+v", i, c)
		}
	}
	s.applySourceLineage(out)
	for i, c := range out {
		if c.SourceIsFork {
			t.Fatalf("自环 parent 不算「本源是 fork」（卡[%d]）：%+v", i, c)
		}
	}
	// 对照：非自环的真 fork 关系照常生效（防过度拦截）
	s2 := f326kEnv(t, refs, cache, map[string]string{"carol/fndepot": "upstream/root"})
	c3 := f326kCard("app-a@本源", "app-a", "本源", "1.0.0",
		"https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk")
	c3.Distributor = "其他人"
	out2 := []AppInfo{c3}
	s2.applyUpstreamLineage(out2)
	if out2[0].UpstreamRepo != "upstream/root" {
		t.Fatalf("对照：真 fork 应给出 parent：%+v", out2[0])
	}
	s2.applySourceLineage(out2)
	if !out2[0].SourceIsFork {
		t.Fatalf("对照：真 fork 应置 SourceIsFork：%+v", out2[0])
	}
}

// ── F4 = B4/K6：纠正层补显示名否决（与 applyLineageUpdates 同口径）──────────
//
// 修复前：applyUpstreamCorrection 只按 AppName + UpstreamSource 选上游卡 —— 上游源
// 里同 appname 但实为另一个应用时，会把对方版本当更新目标，并把它的
// ReleaseURL/Sha256 抄进「更新」（更新会下错包）；upstreamVersionOf 同样无校验。
func TestFix326K_F4_DisplayNameVetoOnCorrection(t *testing.T) {
	// ① 兄弟卡显示名不同 → 不纠正、不改包字段
	s, out := f326kStd(t, "9.9.9")
	out[1].DisplayName = "完全无关的应用"
	out[1].ReleaseURL = "https://github.com/zed/unrelated/releases/download/v9.9.9/other.fpk"
	out[1].Sha256, out[1].SizeBytes = "deadbeef", 111
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource != "上游源" {
		t.Fatalf("前置：源确实收录了同名 appname，UpstreamSource 应有值：%+v", out[0])
	}
	before := out[0].ReleaseURL
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" || out[0].UpdateFromSource != "" {
		t.Fatalf("修复前：纠正层无显示名校验，会把「另一个应用」当上游：%+v", out[0])
	}
	if out[0].ReleaseURL != before || out[0].Sha256 == "deadbeef" {
		t.Fatalf("被否决时不得改包字段（否则更新下错包）：%+v", out[0])
	}

	// ② 源缓存侧显示名不同 → upstreamVersionOf 返回 ""（不给出错应用的版本）
	s2 := f326kEnv(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
			{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"app-a": {Name: "app-a", DisplayName: "完全无关的应用", Source: "上游源", Version: "9.9.9"}},
			"搬运源": f326kSrcCache(map[string]string{"app-a": "1.9.0"}),
		},
		map[string]string{"yuexps/fndepot": ""})
	o2 := []AppInfo{f326kCard("app-a@搬运源", "app-a", "搬运源", "1.9.0",
		"https://github.com/alice/awesome-apps/releases/download/v1.9.0/app-a.fpk")}
	s2.applyUpstreamLineage(o2)
	if o2[0].UpstreamVersion != "" {
		t.Fatalf("源缓存里同名卡显示名不兼容时 upstream_version 应为空：%+v", o2[0])
	}
	if o2[0].UpstreamSource != "上游源" {
		t.Fatalf("上游源名应保留（源确实收录了该 appname），只是版本留空：%+v", o2[0])
	}

	// ③ 对照：显示名一致（忽略大小写/空白）→ 纠正照常生效
	s3, o3 := f326kStd(t, "2.5.0")
	o3[1].DisplayName = " APP-A "
	s3.applyUpstreamLineage(o3)
	applyUpstreamCorrection(o3, map[string]platform.InstalledApp{}, true)
	if !o3[0].HasUpdate || o3[0].AvailableVersion != "2.5.0" {
		t.Fatalf("对照：显示名仅大小写差异应视为同一应用：%+v", o3[0])
	}
}

// ── F5 = K1：不可解析版本 = 未知（不点亮、不作目标、不给上游版本）─────────────
//
// 修复前：compareVersions 对非数字段按字符串比较（"abc" > "1"）→ 垃圾版本会点亮
// 更新角标并成为安装目标；upstream_version 也会把垃圾串直接展示出去。
func TestFix326K_F5_UnparseableVersionNotCorrected(t *testing.T) {
	// ① upstream_version 不落垃圾串
	s, out := f326kStd(t, "abc")
	s.applyUpstreamLineage(out)
	if out[0].UpstreamVersion != "" {
		t.Fatalf("上游版本不可解析时 upstream_version 应为空：%+v", out[0])
	}
	if out[0].UpstreamSource != "上游源" {
		t.Fatalf("上游源名应保留（版本未知 ≠ 源未知）：%+v", out[0])
	}
	// ② 上游垃圾版本不得点亮更新
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("修复前：\"abc\" > \"1\" → 垃圾版本点亮假更新：%+v", out[0])
	}
	// ③ 已装版本不可解析 → 同样不纠正（"2" > "1abc"，修复前会判「上游更高」）
	s3, o3 := f326kStd(t, "2.5.0")
	o3[0].InstalledVersion = "1abc"
	s3.applyUpstreamLineage(o3)
	applyUpstreamCorrection(o3, map[string]platform.InstalledApp{}, true)
	if o3[0].HasUpdate || o3[0].AvailableVersion != "" {
		t.Fatalf("已装版本不可解析时不得纠正：%+v", o3[0])
	}
	// ④ 判定函数表格（口径单一事实源）
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"1", true}, {"1.9.0", true}, {"v2.5.0", true}, {"1.0.0-1", true}, {"1.9.0-rc1", true},
		{"", false}, {"   ", false}, {"abc", false}, {"1.2.x", false}, {"1..2", false}, {"1.2.", false},
	} {
		if got := comparableVersion(tc.v); got != tc.want {
			t.Fatalf("comparableVersion(%q) = %v，期望 %v", tc.v, got, tc.want)
		}
	}
	// ⑤ 对照：可解析且更高 → 照常纠正
	s5, o5 := f326kStd(t, "2.5.0")
	s5.applyUpstreamLineage(o5)
	applyUpstreamCorrection(o5, map[string]platform.InstalledApp{}, true)
	if !o5[0].HasUpdate || o5[0].AvailableVersion != "2.5.0" {
		t.Fatalf("对照：正常版本应照常纠正：%+v", o5[0])
	}
}

// ── F6 = K2：预发布不自动纠正（不把 rc/beta 推给用户）───────────────────────
//
// 修复前：上游 2.0.0-rc1 经 splitVer 剥 -rc 后是 2.0.0 > 已装 1.9.0 → 纠正生效，
// 把候选版推给用户；另一半情形（上游 rc 与已装同号）又表现为「静默不生效」——
// 两种表现都由「碰巧」决定。修复后统一：上游是预发布一律不自动纠正。
func TestFix326K_F6_PrereleaseNotCorrected(t *testing.T) {
	for _, v := range []string{
		"2.0.0-rc1", "2.0.0-RC2", "2.0.0_rc3", "2.0.0-beta", "2.0.0-BETA1",
		"2.0.0-alpha3", "2.0.0-dev", "2.0.0_dev4", "2.0.0-pre9",
	} {
		s, out := f326kStd(t, v)
		s.applyUpstreamLineage(out)
		applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
		if out[0].HasUpdate || out[0].AvailableVersion != "" {
			t.Fatalf("预发布 %q 不得自动纠正：%+v", v, out[0])
		}
	}
	// 溯源展示不受影响：上游版本仍如实展示（只是不作为纠正目标）
	s, shown := f326kStd(t, "2.0.0-rc1")
	s.applyUpstreamLineage(shown)
	if shown[0].UpstreamVersion != "2.0.0-rc1" {
		t.Fatalf("预发布版本仍应在详情页如实展示：%+v", shown[0])
	}
	// 判定函数表格
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"2.0.0-rc1", true}, {"1.0.0_rc", true}, {"3.1.0-beta2", true}, {"3.1.0-alpha", true},
		{"1.1.3-dev3", true}, {"2.0.0-pre", true}, {"V2.0.0-RC1", true},
		{"2.0.0", false}, {"1.9.0", false}, {"v2.0.0", false}, {"", false},
	} {
		if got := isPrereleaseVersion(tc.v); got != tc.want {
			t.Fatalf("isPrereleaseVersion(%q) = %v，期望 %v", tc.v, got, tc.want)
		}
	}
	// 对照：正式版照常纠正
	s2, o := f326kStd(t, "2.0.0")
	s2.applyUpstreamLineage(o)
	applyUpstreamCorrection(o, map[string]platform.InstalledApp{}, true)
	if !o[0].HasUpdate || o[0].AvailableVersion != "2.0.0" {
		t.Fatalf("对照：正式版应照常纠正：%+v", o[0])
	}
}

// ── F7 = K3：splitVer 剥前导 v（消除 v 前缀造成的比较不对称）─────────────────
//
// 修复前：compareVersions("2.5.0","v1.9.0") 第 0 段 "2" vs "v1" 一个数字一个不是 →
// 字符串分支 → '2'(0x32) < 'v'(0x76) → 判「更低」；("v2.0.0","10.0.0") 同样判反。
// v 前缀同时污染更新判定、自动纠正与去重排序，必须两口径同值。
func TestFix326K_F7_SplitVerLeadingVPrefix(t *testing.T) {
	// ① 验收断言
	if got := compareVersions("v1.2.3", "1.2.3"); got != 0 {
		t.Fatalf("compareVersions(\"v1.2.3\",\"1.2.3\") = %d，期望 0", got)
	}
	if got := compareVersions("1.2.2", "v1.2.3"); got != -1 {
		t.Fatalf("compareVersions(\"1.2.2\",\"v1.2.3\") = %d，期望 -1", got)
	}
	if got := compareVersions("v1.2.2", "1.2.3"); got != -1 {
		t.Fatalf("compareVersions(\"v1.2.2\",\"1.2.3\") = %d，期望 -1", got)
	}
	// ② 多位数段不再被 v 前缀带偏（修复前 "v2" vs "10" 走字符串比较 → 判反）
	if got := compareVersions("v2.0.0", "10.0.0"); got != -1 {
		t.Fatalf("compareVersions(\"v2.0.0\",\"10.0.0\") = %d，期望 -1", got)
	}
	if got := compareVersions("v10.0.0", "2.0.0"); got != 1 {
		t.Fatalf("compareVersions(\"v10.0.0\",\"2.0.0\") = %d，期望 1", got)
	}
	// ③ splitVer 本身（大写 V 与首尾空白）
	if got := strings.Join(splitVer("V1.2.3"), "."); got != "1.2.3" {
		t.Fatalf("splitVer(\"V1.2.3\") = %q，期望 1.2.3", got)
	}
	if got := strings.Join(splitVer(" v1.2.3 "), "."); got != "1.2.3" {
		t.Fatalf("splitVer 应容忍首尾空白并剥 v：%q", got)
	}
	// ④ 端到端：已装版本带 v 前缀（FPK manifest 常见）不再压制自动纠正
	s, out := f326kStd(t, "2.5.0")
	out[0].InstalledVersion = "v1.9.0"
	s.applyUpstreamLineage(out)
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if !out[0].HasUpdate || out[0].AvailableVersion != "2.5.0" {
		t.Fatalf("已装 v1.9.0 应能纠正到 2.5.0：%+v", out[0])
	}
	// ⑤ 反向：v 前缀不再制造假更新（已装 v2.5.0 == 上游 2.5.0）
	s2, out2 := f326kStd(t, "2.5.0")
	out2[0].InstalledVersion = "v2.5.0"
	s2.applyUpstreamLineage(out2)
	applyUpstreamCorrection(out2, map[string]platform.InstalledApp{}, true)
	if out2[0].HasUpdate {
		t.Fatalf("已装 v2.5.0 == 上游 2.5.0，不得点亮：%+v", out2[0])
	}
}
