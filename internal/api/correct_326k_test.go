package api

// correct_326k_test.go —— 0.6.326k「自动纠正应用更新（auto_correct）」盲点审计单测。
//
// 审计对象：lineage.go:331-405 applyUpstreamCorrection（调用位置 store_catalog.go:405，
// 夹在 applyLineageUpdates 与 applyPlatformUpgrades 之间）。
//
// 既有覆盖（先读完才敢下结论，勿重复）：
//   - lineage_matrix_test.go：M1 策略三档 ×{纠正 off/on} 6 组、M1b 不降级、
//     M1c 无上游源不纠正（H9）、M1d 平台权威不纠正、M2 fork 12 组、M3 负例；
//   - lineage_test.go:TestApplyUpstreamCorrection：on/off/上游更低/平台权威；
//   - lineage_test.go:TestAutoCorrectSettingsRoundTrip：GET 透出 + PUT 迁移语义
//     （0.6.326x 起 auto_correct=true ⇒ update_policy="upstream"）。
// 本文件只补上述未覆盖的维度：上游版本形态（更旧/相等/更高/缺失/非法/v 前缀/
// 预发布）、未安装与已装版本未知、已装版本带 v 前缀、与忽略更新/平台升级/禁用应用
// 的叠加、上游源被删或被禁用、包字段一致性与架构不筛选、策略目标低于上游时纠正
// 是否接管的覆盖、与 markInstalledCanonical/dedup 的交互、开关的缓存失效副作用。
//
// **0.6.326k 修复后**：原先钉住「现状缺陷」的 4 个断言已按新语义改写 ——
// TestCorrect326K_UpstreamVersionShapes（K1 非法版本 / K2 预发布）、
// TestCorrect326K_InstalledVersionWithVPrefixFixed（K3，原 …Blocks）、
// TestCorrect326K_DisplayNameVetoInCorrectionLayer（K6，原 …OnlyInStrategy）。
// 对应生产改动：lineage.go 的 comparableVersion / isPrereleaseVersion /
// displayNameCompatible / upstreamVersionOf(displayName 参数) /
// applyUpstreamCorrection，以及 store_catalog.go 的 splitVer 剥前导 v。

import (
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/platform"
	"moo/internal/source"
)

// c326kInstRelease 搬运卡自己的包地址（发布在 alice 仓 → 跨 owner → 判搬运）。
const c326kInstRelease = "https://github.com/alice/awesome-apps/releases/download/v1.9.0/app-a.fpk"

// c326kDL 上游卡的下载量（AppInfo.DownloadCount 是 *int，直接拷指针）。
var c326kDL = 7

// c326kServer 环境：上游源 alice/awesome-apps（版本 upVer，空=该源下无此应用）
// + 搬运源 yuexps/FnDepot（app-a 1.9.0，包指向 alice 仓 → 溯源判定走跨 owner 分支）。
func c326kServer(t *testing.T, upVer string) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}}
	m := source.NewManager(cfg)
	up := map[string]*source.App{}
	if upVer != "" {
		up["app-a"] = &source.App{Name: "app-a", Source: "上游源", Version: upVer}
	}
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"上游源": up,
		"搬运源": {"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"}},
	})
	return &Server{Cfg: cfg, Src: m, Forks: source.NewGHForks("")}
}

// c326kCards 已装搬运卡（1.9.0）+ 上游兄弟卡（upVer；空=版本未知）。
func c326kCards(upVer string) []AppInfo {
	inst := AppInfo{
		Key: "app-a@搬运源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		Installed: true, InstalledVersion: "1.9.0", LatestVersion: "1.9.0",
		ReleaseURL: c326kInstRelease,
	}
	sib := AppInfo{
		Key: "app-a@上游源", AppName: "app-a", DisplayName: "app-a", Source: "上游源",
		LatestVersion: upVer,
		Sha256:        "cafebabe", SizeBytes: 4242, ReleaseNotes: "上游说明",
		DownloadCount: &c326kDL, UpdatedAt: "2026-10-16T00:00:00+08:00",
	}
	sib.ReleaseURL = "https://github.com/alice/awesome-apps/releases/download/v" + upVer + "/app-a.fpk"
	return []AppInfo{inst, sib}
}

// c326kRun 溯源 → 纠正（buildCatalog 中与本函数相关的实际调用序）。
func c326kRun(t *testing.T, upVer string, correct bool) []AppInfo {
	t.Helper()
	s := c326kServer(t, upVer)
	out := c326kCards(upVer)
	s.applyUpstreamLineage(out)
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, correct)
	return out
}

// c326kTail 复刻 buildCatalog 从策略到忽略更新的完整尾段（389-445 的次序契约）。
func c326kTail(s *Server, out []AppInfo, byName map[string]platform.InstalledApp,
	policy string, correct bool, ignored []string) {
	applyLineageUpdates(out, policy, byName)
	applyUpstreamCorrection(out, byName, correct)
	applyPlatformUpgrades(out, byName)
	applyPlatformUpdateAuthority(out, byName)
	applyIgnoredUpdates(out, ignored)
}

// ── 1) 上游版本形态：更旧 / 相等 / 更高 / 缺失 / 非法 / v 前缀 / 预发布 ──────
//
// **0.6.326k 修复后语义**（原断言的是修复前的缺陷，见 CORRECT_BLINDSPOT_326K.md
// 盲点 K1/K2/K3）：判定链现在是
//   ①comparableVersion(上游) && comparableVersion(已装)（F5=K1，不可解析 = 未知）
//   ②!isPrereleaseVersion(上游)（F6=K2，预发布不主动推）
//   ③compareVersions(上游, 已装) > 0（F7=K3 后 v 前缀两侧同值）
//   ④已有目标不低于上游时跳过（不降级）
func TestCorrect326K_UpstreamVersionShapes(t *testing.T) {
	cells := []struct {
		name    string
		upVer   string
		want    bool
		wantVer string
	}{
		{"上游更旧", "1.8.0", false, ""},
		{"上游相等", "1.9.0", false, ""},
		{"上游更高", "2.5.0", true, "2.5.0"},
		{"上游版本缺失", "", false, ""},
		{"单段版本 3", "3", true, "3"},
		{"v 前缀 v2.5.0", "v2.5.0", true, "v2.5.0"},
		// 0.6.326k（F5=K1 修复后）：不可解析 = 版本未知 → 不点亮、不作目标。
		// 修复前 compareVersions 对非数字段按字符串比较："abc" > "1" → 假更新。
		{"非法版本 abc", "abc", false, ""},
		// 修复前 false 的原因是 splitVer 把 -rc 剥掉后与 1.9.0 判等（凑巧）；
		// 0.6.326k（F6=K2）改为显式「预发布一律不自动纠正」——语义更明确也更严
		// （上游 2.0.0-rc1 同样不推）。
		{"上游预发布 1.9.0-rc1", "1.9.0-rc1", false, ""},
		{"上游预发布 2.0.0-rc1（比已装新）", "2.0.0-rc1", false, ""},
	}
	for _, c := range cells {
		// 关：任何形态都不得改动
		off := c326kRun(t, c.upVer, false)
		if off[0].HasUpdate || off[0].AvailableVersion != "" || off[0].UpdateFromSource != "" {
			t.Fatalf("%s：纠正关时应零改动，实际 %#v", c.name, off[0])
		}
		// 开
		out := c326kRun(t, c.upVer, true)
		c0 := out[0]
		if c0.HasUpdate != c.want {
			t.Fatalf("%s：HasUpdate = %v, want %v", c.name, c0.HasUpdate, c.want)
		}
		if c0.AvailableVersion != c.wantVer {
			t.Fatalf("%s：AvailableVersion = %q, want %q", c.name, c0.AvailableVersion, c.wantVer)
		}
		if c.want {
			if c0.UpdateFromSource != "上游源" {
				t.Fatalf("%s：UpdateFromSource = %q, want 上游源", c.name, c0.UpdateFromSource)
			}
			// 点亮时必须整包跟随上游卡（否则更新会下回搬运源旧包）
			if c0.ReleaseURL != out[1].ReleaseURL || c0.Sha256 != "cafebabe" || c0.SizeBytes != 4242 {
				t.Fatalf("%s：包字段未跟随上游卡：%#v", c.name, c0)
			}
		} else if c0.ReleaseURL != c326kInstRelease {
			t.Fatalf("%s：未点亮时 ReleaseURL 不应被改写：%q", c.name, c0.ReleaseURL)
		}
	}
}

// ── 4) 与「忽略更新」叠加：忽略必须压住纠正的信号 ────────────────────────────
func TestCorrect326K_IgnoredSuppressesCorrection(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	s.applyUpstreamLineage(out)
	c326kTail(s, out, map[string]platform.InstalledApp{}, "strict", true, []string{"app-a"})
	c := out[0]
	if c.HasUpdate {
		t.Fatalf("被忽略的应用不得保留更新信号：%#v", c)
	}
	if !c.UpdateIgnored || !c.UpdateIgnoredPending {
		t.Fatalf("应标记 UpdateIgnored/Pending：%#v", c)
	}
	// AvailableVersion 保留（发现页「忽略更新」列表要展示 旧→新）
	if c.AvailableVersion != "2.5.0" || c.UpdateFromSource != "上游源" {
		t.Fatalf("忽略应只压 HasUpdate，保留目标供展示：%#v", c)
	}
}

// ── 5) 与平台升级信号叠加（applyPlatformUpgrades 在纠正之后） ───────────────
func TestCorrect326K_PlatformUpgradeAfterCorrection(t *testing.T) {
	// ① 平台目标更高 → 胜出
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	s.applyUpstreamLineage(out)
	byName := map[string]platform.InstalledApp{"app-a": {
		AppName: "app-a", Source: "thirdparty", // 手动 FPK：非平台跟踪 → 纠正在此应用上生效
		UpgradeInfo: &platform.UpgradeInfo{Version: "3.1.0"},
	}}
	c326kTail(s, out, byName, "strict", true, nil)
	if out[0].AvailableVersion != "3.1.0" || !out[0].HasUpdate {
		t.Fatalf("平台更高目标应胜出：%#v", out[0])
	}

	// ② 平台目标低于纠正目标 → 不得把目标改小；但 LatestVersion 会被平台抬高
	//（现状：latest_version < available_version，见报告盲点 K4）
	out = c326kCards("2.5.0")
	s.applyUpstreamLineage(out)
	byName = map[string]platform.InstalledApp{"app-a": {
		AppName: "app-a", Source: "thirdparty",
		UpgradeInfo: &platform.UpgradeInfo{Version: "2.0.0"},
	}}
	c326kTail(s, out, byName, "strict", true, nil)
	if out[0].AvailableVersion != "2.5.0" {
		t.Fatalf("平台更低目标不得降级纠正目标：%#v", out[0])
	}
	if out[0].LatestVersion != "2.0.0" {
		t.Fatalf("现状：LatestVersion 被平台抬高为 2.0.0（< available 2.5.0）：%q", out[0].LatestVersion)
	}
}

// ── 6) 与「禁用应用」（HiddenApps，applyHiddenApps 最末执行）叠加 ───────────
//
// 禁用只置 UserHidden（卡仍下发，前端过滤），更新信号本身保留 —— 前端
// update_available 计数按 `has_update && !user_hidden` 过滤（App.tsx:1478），
// 故不会出现「角标数得到、列表找不到」。
func TestCorrect326K_HiddenAppKeepsUpdateSignal(t *testing.T) {
	out := c326kRun(t, "2.5.0", true)
	if !out[0].HasUpdate {
		t.Fatalf("前置：纠正应已点亮")
	}
	applyHiddenApps(out, []string{"app-a@搬运源"})
	c := out[0]
	if !c.UserHidden {
		t.Fatalf("命中禁用集合应置 UserHidden：%#v", c)
	}
	if c.Hidden {
		t.Fatalf("禁用展示层不得置 Hidden（卡必须留在响应里）：%#v", c)
	}
	if !c.HasUpdate || c.AvailableVersion != "2.5.0" {
		t.Fatalf("禁用不改更新判定（由前端过滤展示）：%#v", c)
	}
}

// ── 7) 上游源被删 / 被禁用 / 上游已下架该应用 ⇒ 纠正静默失效 ───────────────
//
//	(a) 源被删：bareToSource 无该 origin → UpstreamSource 留空（仅 UpstreamRepo）
//	(b) 源在配置里但目录里没有该应用的兄弟卡（源被禁用后 Apps() 只聚合启用源、
//	    ToggleSource 会清缓存、上游下架）→ UpstreamSource 已设但找不到兄弟卡
func TestCorrect326K_UpstreamSourceUnusable(t *testing.T) {
	// (a) 源被删
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"搬运源": {"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"}},
	})
	s := &Server{Cfg: cfg, Src: m, Forks: source.NewGHForks("")}
	out := []AppInfo{
		{Key: "app-a@搬运源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
			Installed: true, InstalledVersion: "1.9.0", LatestVersion: "1.9.0",
			ReleaseURL: c326kInstRelease},
	}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "alice/awesome-apps" || out[0].UpstreamSource != "" {
		t.Fatalf("上游源被删时应只剩 UpstreamRepo：%#v", out[0])
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("上游源被删时不得纠正：%#v", out[0])
	}

	// (b) 源在配置里，但目录里没有兄弟卡
	s2 := c326kServer(t, "2.5.0")
	only := []AppInfo{{
		Key: "app-a@搬运源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		Installed: true, InstalledVersion: "1.9.0", LatestVersion: "1.9.0",
		ReleaseURL: c326kInstRelease,
	}}
	s2.applyUpstreamLineage(only)
	if only[0].UpstreamSource != "上游源" {
		t.Fatalf("前置：源仍在配置里 → UpstreamSource 应有值：%#v", only[0])
	}
	applyUpstreamCorrection(only, map[string]platform.InstalledApp{}, true)
	if only[0].HasUpdate || only[0].AvailableVersion != "" {
		t.Fatalf("兄弟卡缺席（源禁用/下架）时纠正必须静默跳过：%#v", only[0])
	}
}
// ── 2) 未安装 / 已装版本未知 ⇒ 必须不纠正 ────────────────────────────────────
func TestCorrect326K_SkipUninstalledOrUnknownVersion(t *testing.T) {
	// ① 卡未安装（但有完整溯源信息）
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	out[0].Installed = false
	out[0].InstalledVersion = ""
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource == "" {
		t.Fatalf("前置：未装卡也应被溯源（本用例要验的是纠正侧的门）")
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("未安装卡不得被纠正：%#v", out[0])
	}

	// ② 已装但已装版本未知（daemon 未回报版本）
	s = c326kServer(t, "2.5.0")
	out = c326kCards("2.5.0")
	out[0].InstalledVersion = ""
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource == "" {
		t.Fatalf("前置：溯源应已设值，否则本用例是「为错误的原因通过」")
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("已装版本未知时不得纠偏：%#v", out[0])
	}
}

// ── 3) 已装版本带 v 前缀（**0.6.326k 修复后语义**，原 K3 缺陷已修）────────────
//
// 修复前：compareVersions("2.5.0","v1.9.0") 第 0 段 "2" vs "v1" 一个数字一个不是 →
// 字符串分支 → '2'(0x32) < 'v'(0x76) → 判「2.5.0 更低」→ 上游永远不比已装新，
// 纠正被系统性压制。0.6.326k（F7=K3）splitVer 剥前导 v → 两侧同值，纠正恢复正常。
func TestCorrect326K_InstalledVersionWithVPrefixFixed(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	out[0].InstalledVersion = "v1.9.0"
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource == "" {
		t.Fatalf("前置：溯源应已设值，否则本用例是「为错误的原因通过」")
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if !out[0].HasUpdate || out[0].AvailableVersion != "2.5.0" || out[0].UpdateFromSource != "上游源" {
		t.Fatalf("0.6.326k 修复后：已装版本带 v 前缀也应按 1.9.0 比较并纠正：%#v", out[0])
	}
	if out[0].ReleaseURL != c326kCards("2.5.0")[1].ReleaseURL {
		t.Fatalf("点亮后包字段应跟随上游卡：%q", out[0].ReleaseURL)
	}
	// 对照：裸号已装版本同样输入 → 同样纠正（两口径必须同值）
	out2 := c326kCards("2.5.0")
	s.applyUpstreamLineage(out2)
	applyUpstreamCorrection(out2, map[string]platform.InstalledApp{}, true)
	if !out2[0].HasUpdate || out2[0].AvailableVersion != "2.5.0" {
		t.Fatalf("对照：裸号已装版本应纠正成功：%#v", out2[0])
	}
	// 反向对照（F7 验收）：已装带 v 前缀且**不低于**上游 → 仍不纠正
	out3 := c326kCards("2.5.0")
	out3[0].InstalledVersion = "v2.5.0"
	s.applyUpstreamLineage(out3)
	applyUpstreamCorrection(out3, map[string]platform.InstalledApp{}, true)
	if out3[0].HasUpdate {
		t.Fatalf("已装 v2.5.0 == 上游 2.5.0，不得点亮：%#v", out3[0])
	}
}

// ── 8) 纠正后的字段一致性：包字段（含架构）与上游卡同源 ─────────────────────
//
// 纠正尾段逐字段拷上游卡的包信息（ReleaseURL/Sha256/SizeBytes/ReleaseNotes/
// UpdatedAt/DownloadCount + 0.6.326 起的 Arch/Archs）。
// 0.6.326 审计 L-5 之前不拷 Arch/Archs：arm 上线后会出现「更新目标是 arm 包、
// 卡片架构仍标 x86」的错标。本用例下半段现在的断言＝回归守卫（旧实现必 FAIL）。
func TestCorrect326K_PackageFieldsFollowUpstream(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	out[1].Arch = "arm"
	out[1].Archs = []string{"arm"}
	out[1].SourceIsFork = true // 本源卡属性，不属于「包字段」
	s.applyUpstreamLineage(out)
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)

	c, sib := out[0], out[1]
	if c.ReleaseURL != sib.ReleaseURL || c.Sha256 != sib.Sha256 || c.SizeBytes != sib.SizeBytes {
		t.Fatalf("ReleaseURL/Sha256/Size 必须与上游卡同源：%#v vs %#v", c, sib)
	}
	if c.ReleaseNotes != sib.ReleaseNotes || c.UpdatedAt != sib.UpdatedAt || c.DownloadCount != sib.DownloadCount {
		t.Fatalf("ReleaseNotes/UpdatedAt/DownloadCount 应跟随上游卡：%#v", c)
	}
	// 0.6.326 L-5：架构与包同源（上游卡 arm → 纠正后也是 arm）
	if c.Arch != "arm" || len(c.Archs) != 1 || c.Archs[0] != "arm" {
		t.Fatalf("纠正后架构必须跟随上游卡（L-5 回归守卫）：%#v", c)
	}
	if c.SourceIsFork {
		t.Fatalf("SourceIsFork 是本源仓库属性，纠正不应改写：%#v", c)
	}
}

// ── 9) 策略已给目标但低于上游 ⇒ 纠正接管（M1 只覆盖了策略目标≥上游的情形）──
func TestCorrect326K_CorrectionTakesOverLowerStrategyTarget(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	// 上游卡的包地址换到另一个仓（origin 策略因此不收它——策略只收同 releaseOrigin
	// 的兄弟卡；这也是「策略目标低于上游」的现实成因：搬运源自己发了一版）。
	out[1].ReleaseURL = "https://github.com/alice/upstream-releases/releases/download/v2.5.0/app-a.fpk"
	// 同 releaseOrigin 的兄弟卡 2.0.0：origin 策略会把目标定在 2.0.0
	mid := AppInfo{
		Key: "app-a@搬运源2", AppName: "app-a", DisplayName: "app-a", Source: "搬运源2",
		LatestVersion: "2.0.0",
		ReleaseURL:    "https://github.com/alice/awesome-apps/releases/download/v2.0.0/app-a.fpk",
	}
	out = append(out, mid)
	s.applyUpstreamLineage(out)
	applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
	if out[0].AvailableVersion != "2.0.0" || out[0].UpdateFromSource != "搬运源2" {
		t.Fatalf("前置：origin 策略应给出 2.0.0：%#v", out[0])
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].AvailableVersion != "2.5.0" || out[0].UpdateFromSource != "上游源" {
		t.Fatalf("上游更高时纠正应接管策略目标：%#v", out[0])
	}
	if out[0].ReleaseURL != out[1].ReleaseURL {
		t.Fatalf("接管后包地址应跟随上游卡：%q", out[0].ReleaseURL)
	}
}

// ── 10) 与 markInstalledCanonical / dedup 的交互 ────────────────────────────
//
// 两张同名卡都标已装时，规范卡（=搬运卡）留 Installed，上游卡被降级为未装 ——
// 降级卡仍是合法的「上游版本事实来源」，纠正照样能取它当目标；随后 dedup 隐藏
// 展示不影响更新判定（已装规范卡永不隐藏）。
func TestCorrect326K_CanonicalAndDedupInteraction(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	out[1].Installed = true
	out[1].InstalledVersion = "2.5.0"
	markInstalledCanonical(out, nil)
	if !out[0].Installed || out[1].Installed {
		t.Fatalf("前置：规范卡应为搬运卡（已装版本一致的自身来源）：%#v", out)
	}
	s.applyUpstreamLineage(out)
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if !out[0].HasUpdate || out[0].AvailableVersion != "2.5.0" || out[0].UpdateFromSource != "上游源" {
		t.Fatalf("被降级的上游卡仍应提供纠正目标：%#v", out[0])
	}
	if out[1].HasUpdate || out[1].AvailableVersion != "" {
		t.Fatalf("降级卡自身不得带回更新标记：%#v", out[1])
	}
	applyDedupPolicy(out, "merge")
	if out[0].Hidden {
		t.Fatalf("已装规范卡永不隐藏（dedup 不得吞掉纠正目标）：%#v", out[0])
	}
	if out[0].AvailableVersion != "2.5.0" || out[0].UpdateFromSource != "上游源" || !out[0].HasUpdate {
		t.Fatalf("dedup 只改展示身份，不得改更新判定：%#v", out[0])
	}
}

// ── 12) 显示名否决现在两层都有（**0.6.326k 修复后语义**，原 K6 口径不一致已修）──
//
// 修复前：applyLineageUpdates 有「显示名不同 ⇒ 不同应用」的否决，而
// applyUpstreamCorrection 只认 appname + UpstreamSource —— 同一张兄弟卡可能
// 「策略不收、纠正收」。0.6.326k（F4=B4）给纠正层补了同口径否决：上游源里
// 同 appname 但不同显示名 = 另一个应用，不得当更新目标、更不得抄它的包字段。
func TestCorrect326K_DisplayNameVetoInCorrectionLayer(t *testing.T) {
	s := c326kServer(t, "2.5.0")
	out := c326kCards("2.5.0")
	// 0.6.326y（G3a）**已改**：此处原用 "app-a（上游）"，归一化后是
	// "appa上游"，其前缀 "appa" == 规范卡的显示名 → 新口径下判「同一应用」是
	// 正确行为（正是 G3a 要放过的「加后缀改名」）。本用例验的是「同名不同应用
	// 必须否决」，故改用真正无关的显示名（与 ② 的源侧负例同款）。
	out[1].DisplayName = "完全无关的应用" // 同 appname、不同展示名
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource == "" {
		t.Fatalf("前置：溯源字段仍应设（源确实收录了同名卡），本用例验的是纠正层的门")
	}
	applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
	if out[0].HasUpdate {
		t.Fatalf("前置：策略层应因显示名不同而否决这张兄弟卡：%#v", out[0])
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" || out[0].UpdateFromSource != "" {
		t.Fatalf("0.6.326k 修复后：纠正层也必须否决同名不同应用：%#v", out[0])
	}
	if out[0].ReleaseURL != c326kInstRelease {
		t.Fatalf("被否决时不得改写 ReleaseURL（否则会下错包）：%q", out[0].ReleaseURL)
	}

	// ② 源侧显示名不兼容（upstreamVersionOf 读的是源缓存）→ upstream_version 留空
	s2 := c326kServer(t, "2.5.0")
	s2.Src.SeedCacheForTest(map[string]map[string]*source.App{
		"上游源": {"app-a": {Name: "app-a", DisplayName: "完全无关的应用", Source: "上游源", Version: "2.5.0"}},
		"搬运源": {"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"}},
	})
	out2 := c326kCards("2.5.0")
	s2.applyUpstreamLineage(out2)
	if out2[0].UpstreamSource != "上游源" {
		t.Fatalf("上游源名应保留（源确实收录了该 appname），只是版本留空：%#v", out2[0])
	}
	if out2[0].UpstreamVersion != "" {
		t.Fatalf("0.6.326k 修复后：源缓存里同名卡显示名不兼容时 upstream_version 应为空：%#v", out2[0])
	}

	// ③ 对照：显示名仅大小写/空白差异 → 视为同一应用，纠正照常生效
	out3 := c326kCards("2.5.0")
	out3[1].DisplayName = " APP-A "
	s.applyUpstreamLineage(out3)
	applyUpstreamCorrection(out3, map[string]platform.InstalledApp{}, true)
	if !out3[0].HasUpdate || out3[0].AvailableVersion != "2.5.0" {
		t.Fatalf("对照：显示名仅大小写/空白差异应视为同一应用并照常纠正：%#v", out3[0])
	}
}

// ── 11) 开关副作用：GET 恒透出键（无 omitempty）+ 变化才失效目录缓存 ────────
//
// 字段漏透出是本项目历史上出过的事故类型（0.6.325 H1），故断言原始 JSON 里
// 键恒在（false 也要有）；开关变化必须让 /api/apps（目录 + body 缓存）失效，
// 否则用户切开关后最长 TTL 内「看起来没反应」。
func TestCorrect326K_SettingsRawKeyAndCacheInvalidation(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}

	raw := doSettingsGet(t, s).Body.String()
	if !strings.Contains(raw, `"auto_correct":false`) {
		t.Fatalf("GET 响应必须无条件带 auto_correct 键（含 false）：%s", raw)
	}

	s.catalogBody = []byte("stale")
	s.catalogBodyETag = "etag-x"
	// 0.6.326x：auto_correct=true 迁移为 update_policy="upstream"（独立开关不再保留）。
	putSettingsBody(t, s, `{"auto_correct": true}`)
	if cfg.AutoCorrect {
		t.Fatal("独立开关不应再保留（恒 false）")
	}
	if cfg.UpdatePolicy != "upstream" {
		t.Fatalf("auto_correct=true 应迁移 update_policy=upstream，got %q", cfg.UpdatePolicy)
	}
	if s.catalogBody != nil || s.catalogBodyETag != "" {
		t.Fatalf("开关变化必须失效 /api/apps body 缓存：%v/%q", s.catalogBody, s.catalogBodyETag)
	}
	if raw2 := doSettingsGet(t, s).Body.String(); !strings.Contains(raw2, `"update_policy":"upstream"`) {
		t.Fatalf("GET 应透出 update_policy=upstream：%s", raw2)
	}

	// 同值重复提交：不触发失效（现状，钉住即可）
	s.catalogBody = []byte("keep")
	putSettingsBody(t, s, `{"auto_correct": true}`)
	if s.catalogBody == nil {
		t.Fatal("现状：值未变化时 putSettings 不应失效缓存")
	}
	// 缺省字段（未提交）也不得改动
	putSettingsBody(t, s, `{}`)
	if cfg.UpdatePolicy != "upstream" {
		t.Fatal("未提交不应改动 update_policy")
	}
	// 显式策略值优先（不再有独立开关）
	putSettingsBody(t, s, `{"update_policy": "lineage"}`)
	if cfg.UpdatePolicy != "lineage" || cfg.AutoCorrect {
		t.Fatalf("显式 update_policy 应生效且归一 auto_correct，got %q/%v", cfg.UpdatePolicy, cfg.AutoCorrect)
	}
}
