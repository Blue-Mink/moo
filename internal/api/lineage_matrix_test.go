package api

// lineage_matrix_test.go —— 0.6.326d 溯源「全场景 + 盲点」矩阵单测。
//
// 背景（LIN_GAPS_326D.md）：既有覆盖 = update_policy_test.go 的策略单点 +
// lineage_test.go 的 5 个用例，**更新策略三档 × 自动纠正开关的 6 组矩阵覆盖为 0**
// —— 即「策略给出什么目标 + 纠正又覆盖成什么」这两步的交互从未被联合断言过。
// 本文件补齐三张矩阵：
//   M1 {strict, origin, lineage} × {纠正 off, on} = 6 组
//   M2 {fork true/false} × {parent 在/不在源列表} × {distributor 空/=owner/≠owner} = 12 组
//   M3 负例回归：twin 双胞胎根仓（boater-man ≡ whoisaisiohw 等）与 shuangji66
//      自建仓（fork=false）不得被误判为搬运
// 另含两条派生盲点断言：纠正不降级（策略目标更高时不被上游旧版覆盖）、
// 纠正依赖 UpstreamSource（=H9 的缺口机制，前端「添加为源」修的正是它）。

import (
	"fmt"
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/source"
)

// newMatrixServer M1 环境：上游源 alice/awesome-apps（2.5.0）+ 搬运源
// yuexps/FnDepot（1.9.0）。yuexps 桩为 fork=false → 走「非 fork、跨 owner」分支
// （即 H3 那 32 张卡的判定路径）。
func newMatrixServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"上游源": {"app-a": {Name: "app-a", Source: "上游源", Version: "2.5.0"}},
		"搬运源": {"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"}},
	})
	f := source.NewGHForks("")
	f.SetForTest("yuexps/fndepot", "") // 非 fork：作者自建仓形态，靠跨 owner 判搬运
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// matrixCards 每次返回全新副本（矩阵逐格独立跑，互不污染）。
// 已装卡 = 搬运源 1.9.0，包发布在 alice/awesome-apps（→ 判搬运、上游=上游源）；
// 兄弟卡 = 上游源 2.5.0（同 appname、同 releaseOrigin、同 maintainer）。
func matrixCards() []AppInfo {
	inst := AppInfo{
		Key: "app-a@搬运源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		Installed: true, InstalledVersion: "1.9.0", LatestVersion: "1.9.0",
		Maintainer: "一零一二", Distributor: "原作者甲",
		ReleaseURL: "https://github.com/alice/awesome-apps/releases/download/v1.9.0/app-a.fpk",
	}
	sib := AppInfo{
		Key: "app-a@上游源", AppName: "app-a", DisplayName: "app-a", Source: "上游源",
		LatestVersion: "2.5.0", Maintainer: "一零一二", Distributor: "原作者甲",
		Sha256: "beef", SizeBytes: 2048, ReleaseNotes: "上游说明",
		ReleaseURL: "https://github.com/alice/awesome-apps/releases/download/v2.5.0/app-a.fpk",
	}
	return []AppInfo{inst, sib}
}

// runMatrixPipeline 复刻 buildCatalog 的相关调用序（store_catalog.go:389-401）：
// 溯源派生 → 策略给目标 → 自动纠正覆盖。三段顺序本身就是被测契约。
func runMatrixPipeline(s *Server, policy string, correct bool, byName map[string]platform.InstalledApp) []AppInfo {
	out := matrixCards()
	s.applyUpstreamLineage(out)
	applyLineageUpdates(out, policy, byName)
	applyUpstreamCorrection(out, byName, correct)
	return out
}

// ── M1：更新策略三档 × 自动纠正开关（6 组，既有覆盖 = 0）──────────────────
//
// 逐格断言：①溯源四字段与策略/纠正无关（派生先于两者，任何一格都不该变）；
// ②最终更新目标（HasUpdate / AvailableVersion / UpdateFromSource）与包字段。
// strict 是唯一「策略什么都不给」的档 —— 纠正正是靠它证明自己「无视策略」。
func TestMatrix_PolicyXAutoCorrect(t *testing.T) {
	cells := []struct {
		policy  string
		correct bool
		// 期望最终状态
		upd    bool
		ver    string
		from   string
		reason string
	}{
		{"strict", false, false, "", "", "严格档=策略空操作，纠正关 → 无任何更新目标"},
		{"strict", true, true, "2.5.0", "上游源", "严格档下纠正是唯一目标来源（跨源仍能更新）"},
		{"origin", false, true, "2.5.0", "上游源", "同源档=同 releaseOrigin 兄弟卡点亮"},
		{"origin", true, true, "2.5.0", "上游源", "纠正与策略目标同值 → 不重复覆盖、不降级"},
		{"lineage", false, true, "2.5.0", "上游源", "同宗档=同 maintainer 点亮"},
		{"lineage", true, true, "2.5.0", "上游源", "纠正与策略目标同值"},
	}
	s := newMatrixServer(t)
	for _, c := range cells {
		name := fmt.Sprintf("policy=%s/correct=%v", c.policy, c.correct)
		out := runMatrixPipeline(s, c.policy, c.correct, map[string]platform.InstalledApp{})
		inst, sib := &out[0], &out[1]

		// ① 溯源字段：策略与纠正都不该改变它（派生在两者之前）
		if inst.UpstreamRepo != "alice/awesome-apps" || inst.UpstreamSource != "上游源" ||
			inst.UpstreamVersion != "2.5.0" ||
			inst.UpstreamURL != "https://github.com/alice/awesome-apps" {
			t.Fatalf("%s：溯源字段被策略/纠正污染：%#v", name, inst)
		}
		if sib.UpstreamRepo != "" || sib.UpstreamSource != "" {
			t.Fatalf("%s：上游源自己的卡不应判搬运：%#v", name, sib)
		}

		// ② 最终更新目标
		if inst.HasUpdate != c.upd {
			t.Fatalf("%s：HasUpdate = %v, want %v（%s）", name, inst.HasUpdate, c.upd, c.reason)
		}
		if inst.AvailableVersion != c.ver {
			t.Fatalf("%s：AvailableVersion = %q, want %q（%s）", name, inst.AvailableVersion, c.ver, c.reason)
		}
		if inst.UpdateFromSource != c.from {
			t.Fatalf("%s：UpdateFromSource = %q, want %q（%s）", name, inst.UpdateFromSource, c.from, c.reason)
		}
		// 点亮时必须跟随上游卡（下的是上游包，不是搬运源旧包）
		if c.upd {
			if inst.ReleaseURL != sib.ReleaseURL || inst.Sha256 != "beef" || inst.SizeBytes != 2048 {
				t.Fatalf("%s：包字段未跟随上游卡：%#v", name, inst)
			}
		} else if inst.ReleaseURL != matrixCards()[0].ReleaseURL {
			t.Fatalf("%s：未点亮时 ReleaseURL 不应被改写：%q", name, inst.ReleaseURL)
		}
	}
}

// M1b 盲点：纠正**不降级** —— 策略已给出更高目标（兄弟源 3.0.0）时，纠正不得
// 把目标改回上游源的 2.5.0（否则用户会「越更越旧」）。
func TestMatrix_CorrectionNeverDowngrades(t *testing.T) {
	s := newMatrixServer(t)
	out := matrixCards()
	// 加一张同 releaseOrigin 的兄弟卡（3.0.0），让策略目标高于上游源版本。
	third := AppInfo{
		Key: "app-a@兄弟源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		LatestVersion: "3.0.0", Maintainer: "一零一二", Distributor: "原作者甲",
		ReleaseURL: "https://github.com/alice/awesome-apps/releases/download/v3.0.0/app-a.fpk",
	}
	out = append(out, third)
	s.applyUpstreamLineage(out)
	applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
	if !out[0].HasUpdate || out[0].AvailableVersion != "3.0.0" {
		t.Fatalf("前置：同源档应点亮 3.0.0，实际 %v/%q", out[0].HasUpdate, out[0].AvailableVersion)
	}
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].AvailableVersion != "3.0.0" || out[0].UpdateFromSource != "搬运源" {
		t.Fatalf("已有更高目标时纠正不得降级：%#v", out[0])
	}
}

// M1c 盲点（=H9 的缺口机制，前端「添加为源」按钮修的正是它）：
// 上游源未加入 Moo 时，卡片有 UpstreamRepo 但 UpstreamSource 恒空 →
// applyUpstreamCorrection 首行即跳过 → 三档策略下「自动纠正」全部无效。
// 这不是 bug 而是设计（没有上游源就没有上游版本可比），但必须被钉住：
// 一旦有人「顺手」把首行判据放宽，本用例会立刻变红。
func TestMatrix_CorrectionRequiresUpstreamSource(t *testing.T) {
	// 上游源**不在**源列表：只有搬运源。
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"搬运源": {"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"}},
	})
	f := source.NewGHForks("")
	f.SetForTest("yuexps/fndepot", "")
	s := &Server{Cfg: cfg, Src: m, Forks: f}

	out := matrixCards()
	s.applyUpstreamLineage(out)
	// 溯源仍有 repo（用户看得见「上游 alice/awesome-apps」=H3 现象），但无 source
	if out[0].UpstreamRepo != "alice/awesome-apps" || out[0].UpstreamSource != "" {
		t.Fatalf("上游源未收录时应只有 UpstreamRepo：%#v", out[0])
	}
	for _, policy := range []string{"strict", "origin", "lineage"} {
		applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
		if out[0].HasUpdate {
			t.Fatalf("policy=%s：无上游源时纠正必须跳过（H9 缺口），实际点亮 %q",
				policy, out[0].AvailableVersion)
		}
	}
}

// M1d 平台权威 × 纠正：三档策略下，daemon 跟踪应用（sourceID 非空）与官方源
// 应用都不得被上游纠正（0.6.257 平台权威收敛）。
func TestMatrix_CorrectionRespectsPlatformAuthority(t *testing.T) {
	for _, policy := range []string{"strict", "origin", "lineage"} {
		for _, tc := range []struct {
			name string
			ia   platform.InstalledApp
		}{
			{"sourceID 非空", platform.InstalledApp{AppName: "app-a", Source: "thirdparty", SourceID: "49"}},
			{"source=official", platform.InstalledApp{AppName: "app-a", Source: "official"}},
		} {
			s := newMatrixServer(t)
			out := runMatrixPipeline(s, policy, true, map[string]platform.InstalledApp{"app-a": tc.ia})
			if out[0].HasUpdate {
				t.Fatalf("policy=%s/%s：平台权威应用不得被上游纠正：%#v", policy, tc.name, out[0])
			}
		}
	}
}

// ── M2：整仓 fork 判定 12 组 = {fork true/false} × {parent 在/不在源列表} ×
//
//	{distributor 空 / == owner / ≠ owner} ──────────────────────────────
//
// 判定链（lineage.go:131-155）：同 owner 且本源仓是 fork → 再看
// republishedByOther（distributor 空则回落 maintainer，仍空 = 无「他人打包」
// 证据 → 放过）→ 命中才取 fork parent 作上游。三档乘积里只有一档该判搬运。
func TestMatrix_ForkParentDistributor(t *testing.T) {
	cases := []struct {
		fork        bool
		parentIn    bool
		distributor string
	}{
		{true, true, ""}, {true, true, "Chordi0124"}, {true, true, "EWEDL"},
		{true, false, ""}, {true, false, "Chordi0124"}, {true, false, "EWEDL"},
		{false, true, ""}, {false, true, "Chordi0124"}, {false, true, "EWEDL"},
		{false, false, ""}, {false, false, "Chordi0124"}, {false, false, "EWEDL"},
	}
	for _, c := range cases {
		name := fmt.Sprintf("fork=%v/parentInList=%v/distributor=%q", c.fork, c.parentIn, c.distributor)
		s := newForkMatrixServer(t, c.fork, c.parentIn)
		card := mkLineageCard("picoclaw@本源", "picoclaw", "本源", "0.2.8", "Chordi0124/FnDepot", false)
		card.Distributor = c.distributor
		out := []AppInfo{card}
		s.applyUpstreamLineage(out)

		// 命中条件：本源仓是 fork 且 distributor 非空且 ≠ 本源 owner
		hit := c.fork && c.distributor != "" && c.distributor != "Chordi0124"
		if !hit {
			if out[0].UpstreamRepo != "" || out[0].UpstreamURL != "" || out[0].UpstreamSource != "" {
				t.Fatalf("%s：不应判搬运，实际 %#v", name, out[0])
			}
			continue
		}
		if out[0].UpstreamRepo != "ewedlcm/fndepot" ||
			out[0].UpstreamURL != "https://github.com/ewedlcm/fndepot" {
			t.Fatalf("%s：应溯源到 fork parent，实际 %#v", name, out[0])
		}
		if c.parentIn {
			if out[0].UpstreamSource != "上游源" || out[0].UpstreamVersion != "0.3.1" {
				t.Fatalf("%s：parent 是已配源时应填 Source/Version：%#v", name, out[0])
			}
		} else if out[0].UpstreamSource != "" || out[0].UpstreamVersion != "" {
			// = H3 形态：有 repo 无 source（前端「添加为源」按钮的服务对象）
			t.Fatalf("%s：parent 未收录时应留空 Source/Version：%#v", name, out[0])
		}
	}
}

// newForkMatrixServer M2 环境：本源 = Chordi0124/FnDepot（桩：fork / 非 fork），
// 可选把 fork parent（EWEDLCM/FnDepot）也配成源。
func newForkMatrixServer(t *testing.T, fork, parentInList bool) *Server {
	t.Helper()
	refs := []config.SourceRef{{Name: "本源", URL: "https://github.com/Chordi0124/FnDepot"}}
	cache := map[string]map[string]*source.App{
		"本源": {"picoclaw": {Name: "picoclaw", Source: "本源", Version: "0.2.8"}},
	}
	if parentInList {
		refs = append(refs, config.SourceRef{Name: "上游源", URL: "https://github.com/EWEDLCM/FnDepot"})
		cache["上游源"] = map[string]*source.App{
			"picoclaw": {Name: "picoclaw", Source: "上游源", Version: "0.3.1"},
		}
	}
	cfg := &config.Config{Sources: refs}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(cache)
	f := source.NewGHForks("")
	if fork {
		f.SetForTest("chordi0124/fndepot", "EWEDLCM/FnDepot")
	} else {
		f.SetForTest("chordi0124/fndepot", "")
	}
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// ── M3：负例回归 —— twin 双胞胎根仓 + shuangji66 自建仓 ────────────────────
//
// H1 实锤（LIN_GAPS_326D.md §2）：GitHub 上 6 组「非 fork 的同内容根仓」
// （boater-man/fndepot ≡ whoisaisiohw/fndepot 等）互为副本，两者 fork=false。
// 判定的第一道门必须挡住它们：包放在自己仓里 ⇒ 不判搬运。
// 第二组是 shuangji66/FnDepot（fork=false，打包者就是 owner）——H1 点名的
// 假设「它是 EWEDLCM 的复制」已被 5 条实锤否掉，它必须继续被放过。
func TestMatrix_TwinReposAndSelfBuildNotMisjudged(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "双胞胎A", URL: "https://github.com/boater-man/fndepot"},
		{Name: "双胞胎B", URL: "https://github.com/whoisaisiohw/fndepot"},
		{Name: "自打打包源", URL: "https://github.com/shuangji66/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"双胞胎A":  {"bitmagnet": {Name: "bitmagnet", Source: "双胞胎A", Version: "1.0.0"}},
		"双胞胎B":  {"bitmagnet": {Name: "bitmagnet", Source: "双胞胎B", Version: "1.0.0"}},
		"自打打包源": {"app-x": {Name: "app-x", Source: "自打打包源", Version: "1.0.0"}},
	})
	f := source.NewGHForks("")
	f.SetForTest("boater-man/fndepot", "") // 两个 twin 都不是 fork
	f.SetForTest("whoisaisiohw/fndepot", "")
	f.SetForTest("shuangji66/fndepot", "")
	s := &Server{Cfg: cfg, Src: m, Forks: f}

	// ① twin A 的包在自己仓 → 不判（distributor 空，也无「他人打包」证据）
	a := mkLineageCard("bitmagnet@双胞胎A", "bitmagnet", "双胞胎A", "1.0.0", "boater-man/fndepot", false)
	// ② twin B 的包在自己仓 → 不判
	b := mkLineageCard("bitmagnet@双胞胎B", "bitmagnet", "双胞胎B", "1.0.0", "whoisaisiohw/fndepot", false)
	// ③ shuangji66 自打包（distributor = owner 自己）→ 不判
	c := mkLineageCard("app-x@自打打包源", "app-x", "自打打包源", "1.0.0", "shuangji66/FnDepot", false)
	c.Distributor = "shuangji66"
	// ④ shuangji66 场景（distributor 空 + fork=false）→ 仍不判（保守放过）
	d := mkLineageCard("app-y@自打打包源", "app-y", "自打打包源", "1.0.0", "shuangji66/FnDepot", false)
	out := []AppInfo{a, b, c, d}
	s.applyUpstreamLineage(out)
	for i, card := range out {
		if card.UpstreamRepo != "" || card.UpstreamSource != "" || card.UpstreamURL != "" {
			t.Fatalf("负例 %d（%s）不应判搬运：%#v", i, card.Key, card)
		}
	}

	// ⑤ 残余（钉住现状，非新回归）：twin A 的索引里若把包指向 twin B 的仓
	//（H1 实测双胞胎同内容，指向对方与指向自己等价），跨 owner 判定成立
	// → 判搬运、上游 = twin B。语义上「包确实挂在别人名下」站得住，但 twin B
	// 并非真上游（真上游不在两者中）——记为已知残余，未改判定（H1 结论：收益≈0）。
	cross := mkLineageCard("bitmagnet@双胞胎A", "bitmagnet", "双胞胎A", "1.0.0", "whoisaisiohw/fndepot", false)
	crossOut := []AppInfo{cross} // 注意：applyUpstreamLineage 改写切片元素，必须回读切片
	s.applyUpstreamLineage(crossOut)
	if crossOut[0].UpstreamRepo != "whoisaisiohw/fndepot" || crossOut[0].UpstreamSource != "双胞胎B" {
		t.Fatalf("twin 交叉引用的现状（残余）应与实现一致：%#v", crossOut[0])
	}
}
