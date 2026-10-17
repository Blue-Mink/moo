package api

// lineage_fix_326y_test.go —— 0.6.326y 溯源疏漏修复（G1/G3a/G3b/G4）验证单测。
//
// 覆盖四条修复，每条都带「修复前必然失败」的断言：
//   G1  = 官方合并改写 Source 后，溯源仍按 OriginSource 反查本源仓库
//   G3a = 显示名否决放宽为「归一化相等或词前缀包含」
//   G3b = appname 大小写漂移不再导致上游匹配失败
//   G4  = 版本比较器认 `_` 段（1.8.0_492）
//
// 自带最小环境（不复用 fix_326k_test.go / lineage_326k_test.go 的 helpers）。

import (
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/source"
)

// y326Server 组装溯源环境：refs=源列表；cache=源目录缓存（源名→appname→App）；
// forks=预报的 fork 关系（owner/repo → parent，parent 为空 = 非 fork）。
func y326Server(t *testing.T, refs []config.SourceRef, cache map[string]map[string]*source.App, forks map[string]string) *Server {
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

// y326App 源缓存条目。
func y326App(name, display, ver string) *source.App {
	return &source.App{Name: name, DisplayName: display, Version: ver}
}

// y326Card 造卡。
func y326Card(key, appname, display, src, ver, url string) AppInfo {
	a := AppInfo{Key: key, AppName: appname, DisplayName: display, Source: src, LatestVersion: ver}
	a.ReleaseURL = url
	return a
}

// ── G4：splitVer / comparableVersion / compareVersions 认 `_` 段 ──────────────
//
// 修复前：splitVer 只按 "." 切分 → `1.8.0_492` 的第 3 段是 "0_492"（含非数字）
// → comparableVersion=false → 上游版本不显示、自动纠正静默跳过；且
// compareVersions("1.8.0_99","1.8.0_100") 落入字符串分支 → "0_99" > "0_100" → 判反。
func TestLineageFix326Y_G4_UnderscoreVersion(t *testing.T) {
	// ① 验收断言
	if !comparableVersion("1.8.0_492") {
		t.Fatalf("修复前：1.8.0_492 被判「版本不可解析」")
	}
	if got := compareVersions("1.8.0_99", "1.8.0_100"); got != -1 {
		t.Fatalf("修复前：1.8.0_99 vs 1.8.0_100 = %d（期望 -1，旧实现字符串比较得 1）", got)
	}
	if got := compareVersions("1.8.0", "1.8.0_492"); got != -1 {
		t.Fatalf("1.8.0 vs 1.8.0_492 = %d，期望 -1", got)
	}
	if got := compareVersions("1.8.0_492", "1.8.0_500"); got != -1 {
		t.Fatalf("1.8.0_492 vs 1.8.0_500 = %d，期望 -1", got)
	}
	// ② `_rc` 预发布剥离行为不变（仍归一到 1.8.0）
	if got := strings.Join(splitVer("2.0.0_rc3"), "."); got != "2.0.0" {
		t.Fatalf("_rc 预发布剥离行为变了：splitVer(2.0.0_rc3) = %q，期望 2.0.0", got)
	}
	if comparableVersion("1.8.0_rc") != true {
		t.Fatalf("1.8.0_rc 剥 _rc 后可解析，应为 true")
	}
	// ③ 端到端：java-8 卡（上游 1.8.0_492）应拿到 upstream_version
	s := y326Server(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/shuangji66/fndepot"},
			{Name: "搬运源", URL: "https://github.com/LI1223081906/fndepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"java-8-openj9": y326App("java-8-openj9", "java-8-openj9", "1.8.0_492")},
			"搬运源": {"java-8-openj9": y326App("java-8-openj9", "java-8-openj9", "1.8.0_462")},
		},
		map[string]string{"li1223081906/fndepot": "shuangji66/fndepot"})
	out := []AppInfo{y326Card("java-8-openj9@搬运源", "java-8-openj9", "java-8-openj9", "搬运源", "1.8.0_462",
		"https://github.com/shuangji66/fndepot/releases/download/v1.8.0_462/java.fpk")}
	out[0].Distributor = "LI1223081906"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamVersion != "1.8.0_492" {
		t.Fatalf("修复前：java 系 `_` 版本不落 upstream_version：%+v", out[0])
	}
	if out[0].UpstreamSource != "上游源" || out[0].UpstreamRepo != "shuangji66/fndepot" {
		t.Fatalf("上游源/仓应正常派生：%+v", out[0])
	}
}

// ── G3a：显示名否决放宽（归一化相等 或 短者≥3 为长者前缀）────────────────────
func TestLineageFix326Y_G3a_DisplayNamePrefixTolerant(t *testing.T) {
	// ① 正例（修复前必失败）：改名/加后缀的同一应用
	for _, tc := range []struct{ a, b string }{
		{"Hermes", "Hermes Studio"},
		{"Teamspeaker", "Teamspeaker3"},
		{"APP-A", " app-a "},
		{"Teamspeaker3", "Teamspeaker"},
	} {
		if !displayNameCompatible(tc.a, tc.b) {
			t.Fatalf("修复前：%q vs %q 被误否决", tc.a, tc.b)
		}
	}
	// ② 必须保留的负例（硬否决）
	for _, tc := range []struct{ a, b string }{
		{"Fluxor", "三体甜甜圈"},
		{"app-a", "完全无关的应用"},
		{"ab", "abc"}, // 短者 <3 → 不按前缀放过
	} {
		if displayNameCompatible(tc.a, tc.b) {
			t.Fatalf("%q vs %q 必须否决（同名不同应用）", tc.a, tc.b)
		}
	}
	// ③ 空侧放过
	if !displayNameCompatible("", "任意") || !displayNameCompatible("任意", "") || !displayNameCompatible("", "") {
		t.Fatalf("任一侧为空应放过")
	}
	// ④ 端到端：Hermes 卡应拿到上游版本（修复前为 空）
	s := y326Server(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
			{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"Hermes": y326App("Hermes", "Hermes Studio", "2.4.0")},
			"搬运源": {"Hermes": y326App("Hermes", "Hermes", "2.0.0")},
		},
		map[string]string{"yuexps/fndepot": ""})
	out := []AppInfo{y326Card("Hermes@搬运源", "Hermes", "Hermes", "搬运源", "2.0.0",
		"https://github.com/alice/awesome-apps/releases/download/v2.0.0/hermes.fpk")}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamVersion != "2.4.0" {
		t.Fatalf("修复前：显示名「Hermes Studio」被误否决 → 无上游版本：%+v", out[0])
	}
	// ⑤ 负例端到端：Fluxor 型仍不得拿到版本
	s2 := y326Server(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
			{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"Fluxor": y326App("Fluxor", "三体甜甜圈", "9.9.9")},
			"搬运源": {"Fluxor": y326App("Fluxor", "Fluxor", "2.0.0")},
		},
		map[string]string{"yuexps/fndepot": ""})
	o2 := []AppInfo{y326Card("Fluxor@搬运源", "Fluxor", "Fluxor", "搬运源", "2.0.0",
		"https://github.com/alice/awesome-apps/releases/download/v2.0.0/f.fpk")}
	s2.applyUpstreamLineage(o2)
	if o2[0].UpstreamVersion != "" {
		t.Fatalf("Fluxor 型同名不同应用必须仍被否决：%+v", o2[0])
	}
	if o2[0].UpstreamSource != "上游源" {
		t.Fatalf("上游源名应保留（源确实收录该 appname）：%+v", o2[0])
	}
}

// ── G3b：appname 大小写漂移不再导致匹配失败 ─────────────────────────────────
func TestLineageFix326Y_G3b_AppNameCaseDrift(t *testing.T) {
	s := y326Server(t,
		[]config.SourceRef{
			{Name: "上游源", URL: "https://github.com/shuangji66/fndepot"},
			{Name: "搬运源", URL: "https://github.com/Geeeeeker/FnDepot"},
		},
		map[string]map[string]*source.App{
			"上游源": {"BitComet": y326App("BitComet", "BitComet", "2.22.0")},
			"搬运源": {"BitComet": y326App("BitComet", "BitComet", "2.10.0")},
		},
		map[string]string{"geeeeeker/fndepot": ""})
	// ① 大小写不同 → 命中
	out := []AppInfo{y326Card("BitComet@Geeeeeker", "Bitcomet", "BitComet", "搬运源", "2.10.0",
		"https://github.com/shuangji66/fndepot/releases/download/v2.10.0/b.fpk")}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamVersion != "2.22.0" {
		t.Fatalf("修复前：appname 大小写漂移 → 拿不到上游版本：%+v", out[0])
	}
	// ② 不同 appname → 不得误配
	out2 := []AppInfo{y326Card("Bitcometz@Geeeeeker", "Bitcometz", "Bitcometz", "搬运源", "2.10.0",
		"https://github.com/shuangji66/fndepot/releases/download/v2.10.0/b.fpk")}
	s.applyUpstreamLineage(out2)
	if out2[0].UpstreamVersion != "" {
		t.Fatalf("不同 appname 不得误配：%+v", out2[0])
	}
	// ③ 纠正层：sibling 的 appname 大小写不同仍应被找到
	inst := y326Card("Bitcomet@Geeeeeker", "Bitcomet", "BitComet", "搬运源", "2.10.0",
		"https://github.com/shuangji66/fndepot/releases/download/v2.10.0/b.fpk")
	inst.Installed, inst.InstalledVersion, inst.UpstreamSource = true, "2.10.0", "上游源"
	sib := y326Card("BitComet@上游源", "BitComet", "BitComet", "上游源", "2.22.0",
		"https://github.com/shuangji66/fndepot/releases/download/v2.22.0/b.fpk")
	o3 := []AppInfo{inst, sib}
	applyUpstreamCorrection(o3, map[string]platform.InstalledApp{}, true)
	if !o3[0].HasUpdate || o3[0].AvailableVersion != "2.22.0" {
		t.Fatalf("修复前：纠正层按精确大小写找 sibling 找不到 → 不纠正：%+v", o3[0])
	}
}

// ── 0.6.326z3：飞牛官方源不参与溯源（用户规则）────────────────────────────
//
// 用户规则：飞牛官方源（fnos-official）是权威来源，官方收录的应用不显示
// 「溯源/搬运」标记——即使它是官方目录与社区源同名合并来的（OriginSource
// 有社区源），也一律不溯源。0.6.326y 的「按 OriginSource 反查」据此废止。
func TestLineage326Z3_OfficialSourceNotUpstream(t *testing.T) {
	// ① originSourceOf 口径：官方卡一律返回空（不溯源）
	if got := originSourceOf(&AppInfo{Source: OfficialSourceID, OriginSource: "搬运源"}); got != "" {
		t.Fatalf("官方合并卡（含 OriginSource）应不溯源返回空，got %q", got)
	}
	if got := originSourceOf(&AppInfo{Source: OfficialSourceID}); got != "" {
		t.Fatalf("纯官方卡应返回空，got %q", got)
	}
	if got := originSourceOf(&AppInfo{Source: "搬运源"}); got != "搬运源" {
		t.Fatalf("普通卡应取 Source，got %q", got)
	}
	if got := originSourceOf(&AppInfo{}); got != "" {
		t.Fatalf("无源卡应为空，got %q", got)
	}
	if got := originSourceOf(nil); got != "" {
		t.Fatalf("nil 应为空，got %q", got)
	}

	// ② mergeOfficialCards 记 OriginSource（改写前）
	merged := mergeOfficialCards(
		[]AppInfo{{Key: "gridmerge@dysfwhq", AppName: "gridmerge", DisplayName: "gridmerge", Source: "dysfwhq", LatestVersion: "1.0.0"}},
		[]AppInfo{{AppName: "gridmerge", DisplayName: "gridmerge", LatestVersion: "3.0.0"}},
		map[string]bool{})
	if merged[0].Source != OfficialSourceID {
		t.Fatalf("官方合并应把 Source 改写为 %q：%+v", OfficialSourceID, merged[0])
	}
	if merged[0].OriginSource != "dysfwhq" {
		t.Fatalf("修复前：改写 Source 前没记 OriginSource（真来源丢失）：%+v", merged[0])
	}
	// ③ 纯官方卡（Source 已是官方）不得把官方值记进 OriginSource
	merged2 := mergeOfficialCards(
		[]AppInfo{{Key: "x@fnos-official", AppName: "x", DisplayName: "x", Source: OfficialSourceID}},
		[]AppInfo{{AppName: "x", DisplayName: "x"}},
		map[string]bool{})
	if merged2[0].OriginSource != "" {
		t.Fatalf("纯官方卡不得置 OriginSource：%+v", merged2[0])
	}

	// ④ 端到端：官方合并卡（整仓 fork 式搬运）应拿到上游
	s := y326Server(t,
		[]config.SourceRef{
			{Name: "搬运源", URL: "https://github.com/dysfwhq/fndepot"},
			{Name: "父源", URL: "https://github.com/ctllo-bit/fndepot"},
		},
		map[string]map[string]*source.App{
			"父源":  {"gridmerge": y326App("gridmerge", "gridmerge", "2.0.0")},
			"搬运源": {"gridmerge": y326App("gridmerge", "gridmerge", "1.0.0")},
		},
		map[string]string{"dysfwhq/fndepot": "ctllo-bit/fndepot"})
	card := y326Card("gridmerge@dysfwhq", "gridmerge", "gridmerge", OfficialSourceID, "1.0.0",
		"https://github.com/dysfwhq/fndepot/releases/download/v1.0.0/gridmerge.fpk")
	card.OriginSource = "搬运源" // = mergeOfficialCards 的改写结果
	card.Distributor = "MR_XIAOBO"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" || out[0].UpstreamVersion != "" {
		t.Fatalf("飞牛官方源不应溯源（即使 OriginSource 有社区源）：%+v", out[0])
	}
	// ⑤ applySourceLineage **故意不改**：官方徽章卡不出「禁用fork源」按钮
	s.applySourceLineage(out)
	if out[0].SourceIsFork {
		t.Fatalf("顶着官方徽章的卡不得置 SourceIsFork（源级动作会误导）：%+v", out[0])
	}

	// ⑥ 负例：纯官方卡（无 OriginSource）仍整卡跳过
	pure := y326Card("pure@fnos-official", "pure", "pure", OfficialSourceID, "1.0.0",
		"https://github.com/dysfwhq/fndepot/releases/download/v1.0.0/pure.fpk")
	pure.Distributor = "MR_XIAOBO"
	o2 := []AppInfo{pure}
	s.applyUpstreamLineage(o2)
	if o2[0].UpstreamRepo != "" || o2[0].UpstreamSource != "" || o2[0].UpstreamVersion != "" {
		t.Fatalf("纯官方卡（无 OriginSource）必须仍跳过溯源：%+v", o2[0])
	}

	// ⑦ 纠正层：官方合并卡（Source=fnos-official）不再纠正——它没有 upstream
	// （UpstreamSource 空），纠正层据此跳过，不需要 sibling。
	inst := y326Card("gridmerge@dysfwhq", "gridmerge", "gridmerge", OfficialSourceID, "1.0.0",
		"https://github.com/dysfwhq/fndepot/releases/download/v1.0.0/gridmerge.fpk")
	inst.OriginSource = "搬运源"
	inst.Installed, inst.InstalledVersion, inst.UpstreamSource = true, "1.0.0", ""
	sib := y326Card("gridmerge@父源", "gridmerge", "gridmerge", OfficialSourceID, "2.0.0",
		"https://github.com/ctllo-bit/fndepot/releases/download/v2.0.0/gridmerge.fpk")
	sib.OriginSource = "父源"
	o3 := []AppInfo{inst, sib}
	applyUpstreamCorrection(o3, map[string]platform.InstalledApp{}, true)
	if o3[0].HasUpdate {
		t.Fatalf("官方源卡不应纠正（UpstreamSource 空）：%+v", o3[0])
	}
	// ⑧ 对照：普通（未合并）卡路径不受影响
	s3 := y326Server(t,
		[]config.SourceRef{
			{Name: "搬运源", URL: "https://github.com/dysfwhq/fndepot"},
			{Name: "父源", URL: "https://github.com/ctllo-bit/fndepot"},
		},
		map[string]map[string]*source.App{
			"父源":  {"gridmerge": y326App("gridmerge", "gridmerge", "2.0.0")},
			"搬运源": {"gridmerge": y326App("gridmerge", "gridmerge", "1.0.0")},
		},
		map[string]string{"dysfwhq/fndepot": "ctllo-bit/fndepot"})
	c4 := y326Card("gridmerge@dysfwhq", "gridmerge", "gridmerge", "搬运源", "1.0.0",
		"https://github.com/dysfwhq/fndepot/releases/download/v1.0.0/gridmerge.fpk")
	c4.Distributor = "MR_XIAOBO"
	o4 := []AppInfo{c4}
	s3.applyUpstreamLineage(o4)
	if o4[0].UpstreamRepo != "ctllo-bit/fndepot" || o4[0].UpstreamVersion != "2.0.0" {
		t.Fatalf("对照：未合并的搬运卡溯源应照常：%+v", o4[0])
	}
	s3.applySourceLineage(o4)
	if !o4[0].SourceIsFork {
		t.Fatalf("对照：未合并的 fork 源卡仍应置 SourceIsFork：%+v", o4[0])
	}
}
