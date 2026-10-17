package api

// lineage_326k_test.go —— 0.6.326k 溯源「盲点审计」配套单测（只新增，不改既有断言）。
//
// 既有 5 个文件已覆盖：owner 口径 / 搬运命中 / 同 owner 自建仓 / 整仓 fork 12 格 /
// 三档策略×纠正 6 格 / 添加为源 5 落点 / SourceIsFork 派生 / P2+P3。
// 本文件补的是**没被断言过的边角**：
//   ① releaseOrigin 形态矩阵（raw 直链 / github 五段 / gh-proxy 前缀 / jsDelivr @ref / 非 GitHub）
//   ② 源地址形态（大小写、raw 直链本源、非 GitHub 主机的 host 前缀 key）
//   ③ distributor 空→回落 maintainer / 两者皆空 / 大小写相等 / distributor 优先
//   ④ fork parent 指向自身、多层 fork 链（只取一层）
//   ⑤ 缓存文件损坏、ghforks 404 负缓存、addprobe 缓存损坏（端到端保守行为）
//   ⑥ 跨源同名不同 app 的 upstream_version / 纠正包字段误配
//   ⑦ 溯源字段与 Key 无关 + 删源后遗留卡的溯源丢失
//   ⑧ 已装卡 / 已 Hidden 卡 / 已 UserHidden 卡的派生；禁用源卡的列表可见性
//   ⑨ 与 dedup(merge) 的交互：代表卡可能不带溯源
//   ⑩ addprobe owner 索引仓大小写；本源 key 为空（官方源）时整卡跳过
//
// 命名约定：函数名以 TestLineage326K_ 开头。名字带 Pinned 的用例原为**钉住修复前
// 缺陷现状**的哨兵；0.6.326k 修复批次落地后已按「修复后正确行为」改写（用例名保留，
// 以免打断 FIX_326K_WP1_REPORT.md / LINEAGE_BLINDSPOT_326K.md 的引用），现为回归
// 守卫：行为一旦回退到缺陷态，这些用例即失败。被改写的 4 个用例＝
// JsDelivrRefOriginPinned(F1) / CrossHostSameNameRepoCollisionPinned(F2) /
// SelfReferentialForkParentPinned(F3) / UpstreamVersionNameCollisionRisk(F4)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/platform"
	"moo/internal/source"
)

// lkServer 组装溯源环境：refs=源列表；cache=源目录缓存（源名→appname→App）；
// forks=预报的 fork 关系（owner/repo → parent，parent 为空 = 非 fork）。
func lkServer(t *testing.T, refs []config.SourceRef, cache map[string]map[string]*source.App, forks map[string]string) *Server {
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

// mkURL 造卡：直接指定 download_url（"" = 无包地址）。
func mkURL(key, appname, src, ver, url string) AppInfo {
	a := AppInfo{Key: key, AppName: appname, DisplayName: appname, Source: src, LatestVersion: ver}
	if url != "" {
		a.ReleaseURL = url
	}
	return a
}

// srcCache 便捷造源缓存。
func srcCache(entries map[string]string) map[string]*source.App {
	out := map[string]*source.App{}
	for name, ver := range entries {
		out[name] = &source.App{Name: name, Version: ver}
	}
	return out
}

// ── ① releaseOrigin 形态矩阵 ───────────────────────────────────────────────
func TestLineage326K_ReleaseOriginForms(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"github releases", "https://github.com/Alice/Awesome/releases/download/v1/a.fpk", "alice/awesome"},
		{"github 五段 blob", "https://github.com/alice/awesome/blob/main/pkg/a.fpk", "alice/awesome"},
		{"github tree", "https://github.com/alice/awesome/tree/main/dist", "alice/awesome"},
		{"raw 直链", "https://raw.githubusercontent.com/Alice/Awesome/main/a.fpk", "alice/awesome"},
		{"gh-proxy 包裹 raw", "https://gh-proxy.com/https://raw.githubusercontent.com/alice/awesome/main/a.fpk", "alice/awesome"},
		{"gh-proxy 包裹 github", "https://ghfast.top/https://github.com/alice/awesome/releases/download/v1/a.fpk", "alice/awesome"},
		{"jsdelivr 无 ref", "https://cdn.jsdelivr.net/gh/alice/awesome/fnpack.json", "alice/awesome"},
		{"gitlab 非 GitHub", "https://gitlab.com/alice/awesome/raw/main/a.fpk", ""},
		{"自有域名", "https://odn.cc/pkg/a.fpk", ""},
		{"裸 owner/repo", "alice/awesome", ""},
		{"空串", "", ""},
	} {
		if got := releaseOrigin(tc.in); got != tc.want {
			t.Fatalf("%s：releaseOrigin(%q) = %q，期望 %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// 【0.6.326k 修复后改写（原为钉住修复前缺陷 F1=B1），现为回归守卫】
// jsDelivr 的 `gh/owner/repo@ref/...` 形态里 `@ref` 必须被剥掉（repo 段按 "@" 切分），
// releaseOrigin 归一到裸 owner/repo——旧实现会得到 "alice/awesome@main"：UpstreamRepo
// 带上 ref、UpstreamURL 是 404 链接，且该 origin 永远匹配不上任何源 key（详情页
// 「上游」链与自动纠正同时失效，同仓的 GitHub 形态卡与 jsDelivr 形态卡被判不同宗）。
func TestLineage326K_JsDelivrRefOriginPinned(t *testing.T) {
	url := "https://cdn.jsdelivr.net/gh/alice/awesome@main/pkg/a.fpk"
	if got := releaseOrigin(url); got != "alice/awesome" {
		t.Fatalf("jsDelivr @ref 形态应剥掉 ref：releaseOrigin(%q) = %q，期望 alice/awesome", url, got)
	}
	s := newLineageServer(t)
	out := []AppInfo{mkURL("app-a@搬运源", "app-a", "搬运源", "1.9.0", url)}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "alice/awesome" || out[0].UpstreamURL != "https://github.com/alice/awesome" {
		t.Fatalf("上游应为剥离 @ref 后的 alice/awesome：%#v", out[0])
	}
	// 源列表里只有 alice/awesome-apps，没有 alice/awesome ⇒ 不得接上任何源
	// （走的是「添加为源」预判分支 applyUpstreamAddHint）。
	if out[0].UpstreamSource != "" {
		t.Fatalf("源列表无 alice/awesome，UpstreamSource 应为空：%#v", out[0])
	}
}

// ── ② 源地址形态：大小写 / raw 直链本源 / 非 GitHub 主机 key ────────────────
func TestLineage326K_OwnerCaseAndRawSourceURL(t *testing.T) {
	// 源 URL 是 raw 直链 + 大写 owner；包地址是小写仓库根 releases —— 归一后同 owner，
	// 且本源不是 fork ⇒ 必须不判搬运（作者自建仓的英文大小写变体）。
	refs := []config.SourceRef{{Name: "本源", URL: "https://raw.githubusercontent.com/YUEXPS/FnDepot/main/fnpack.json"}}
	if k := source.SourceURLKey(refs[0].URL); k != "github.com/yuexps/fndepot" {
		t.Fatalf("SourceURLKey = %q，期望 github.com/yuexps/fndepot", k)
	}
	s := lkServer(t, refs, map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
		map[string]string{"yuexps/fndepot": ""})
	out := []AppInfo{mkURL("app-a@本源", "app-a", "本源", "1.0.0",
		"https://github.com/yuexps/fndepot/releases/download/v1.0.0/app-a.fpk")}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamURL != "" || out[0].UpstreamSource != "" {
		t.Fatalf("同 owner 大小写变体不应判搬运：%#v", out[0])
	}
}

// 【0.6.326k 修复后改写（原为钉住修复前缺陷 F2=B2），现为回归守卫】
// 本源是**非 GitHub 主机**（Gitea/内网源）时，归一 key 是 "host:port/owner/repo"，
// 旧实现 trimHost 之后变成裸 "owner/repo" 并拿去查 **GitHub** 的 fork 缓存 —— 与
// GitHub 上同名仓库串味（错的「上游」链接、错的「禁用fork源」按钮，点下去会去禁用
// 内网源）。修复后：非 GitHub 主机源一律不查 fork 缓存、不置位、不入队。
func TestLineage326K_CrossHostSameNameRepoCollisionPinned(t *testing.T) {
	refs := []config.SourceRef{
		{Name: "内网源", URL: "http://192.0.2.15:3033/bluemink/moo"},
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
	}
	cache := map[string]map[string]*source.App{
		"内网源": srcCache(map[string]string{"app-a": "1.0.0"}),
		"上游源": srcCache(map[string]string{"app-a": "2.5.0"}),
	}
	// GitHub 上的 bluemink/moo 恰好是 alice/awesome-apps 的 fork（与本内网源无关）。
	s := lkServer(t, refs, cache, map[string]string{"bluemink/moo": "alice/awesome-apps"})
	card := mkURL("app-a@内网源", "app-a", "内网源", "1.0.0", "") // 无包地址 → 走 fork 兜底
	card.Distributor = "第三方打包者"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" || out[0].UpstreamURL != "" {
		t.Fatalf("非 GitHub 主机源不得拿 GitHub 同名仓的 fork 关系：%#v", out[0])
	}
	// 对照：同一份 fork 缓存下，GitHub 源照常判定（防「一律不判」的过度拦截）。
	ghRefs := []config.SourceRef{
		{Name: "bluemink源", URL: "https://github.com/bluemink/moo"},
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
	}
	ghCache := map[string]map[string]*source.App{
		"bluemink源": srcCache(map[string]string{"app-a": "1.0.0"}),
		"上游源":       srcCache(map[string]string{"app-a": "2.5.0"}),
	}
	gh := lkServer(t, ghRefs, ghCache, map[string]string{"bluemink/moo": "alice/awesome-apps"})
	ghCard := mkURL("app-a@bluemink源", "app-a", "bluemink源", "1.0.0", "") // 无包地址 → 走 fork 兜底
	ghCard.Distributor = "第三方打包者"
	ghOut := []AppInfo{ghCard}
	gh.applyUpstreamLineage(ghOut)
	if ghOut[0].UpstreamRepo != "alice/awesome-apps" || ghOut[0].UpstreamSource != "上游源" {
		t.Fatalf("GitHub 源应照常判定 fork 搬运：%#v", ghOut[0])
	}
}

// 【0.6.326k 修复后改写（原为钉住修复前缺陷 F3=B3），现为回归守卫】
// fork 缓存里 parent 指向自身（缓存脏/上游异常）时，旧实现会把卡片自己所在的仓库
// 显示成「上游」（自环）。修复后：三条路径（applyUpstreamLineage / applyForkParentFallback
// / applySourceLineage）都按 sameRepo 跳过，上游三字段全空且不算「本源是 fork」。
func TestLineage326K_SelfReferentialForkParentPinned(t *testing.T) {
	refs := []config.SourceRef{{Name: "本源", URL: "https://github.com/carol/fndepot"}}
	s := lkServer(t, refs, map[string]map[string]*source.App{"本源": srcCache(map[string]string{"app-a": "1.0.0"})},
		map[string]string{"carol/fndepot": "carol/fndepot"})
	card := mkURL("app-a@本源", "app-a", "本源", "1.0.0", "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk")
	card.Distributor = "其他人"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" || out[0].UpstreamVersion != "" {
		t.Fatalf("自环 parent 不得写成上游：%#v", out[0])
	}
	if out[0].UpstreamURL != "" {
		t.Fatalf("自环 parent 不得给出上游链接：%#v", out[0])
	}
	// 「本源是 fork」路径（applySourceLineage）同样要过自环守卫。
	s.applySourceLineage(out)
	if out[0].SourceIsFork {
		t.Fatalf("自环 parent 不算「本源是 fork」：%#v", out[0])
	}
}

// ── ④ 多层 fork 链：只取「直接 parent」一层 ────────────────────────────────
func TestLineage326K_ForkChainUsesImmediateParentOnly(t *testing.T) {
	refs := []config.SourceRef{
		{Name: "本源", URL: "https://github.com/carol/fndepot"},
		{Name: "中游", URL: "https://github.com/bob/fndepot"},
		{Name: "顶层", URL: "https://github.com/alice/fndepot"},
	}
	cache := map[string]map[string]*source.App{
		"本源": srcCache(map[string]string{"app-a": "1.0.0"}),
		"中游": srcCache(map[string]string{"app-a": "2.0.0"}),
		"顶层": srcCache(map[string]string{"app-a": "3.0.0"}),
	}
	s := lkServer(t, refs, cache, map[string]string{
		"carol/fndepot": "bob/fndepot",
		"bob/fndepot":   "alice/fndepot", // 二层：必须不被采用
	})
	card := mkURL("app-a@本源", "app-a", "本源", "1.0.0", "https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk")
	card.Distributor = "第三方打包者"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "bob/fndepot" || out[0].UpstreamSource != "中游" || out[0].UpstreamVersion != "2.0.0" {
		t.Fatalf("多层 fork 链应只取直接 parent：%#v", out[0])
	}
}

// ── ③ 发布者信号：distributor 空→回落 maintainer / 皆空 / 大小写 / 优先级 ────
func TestLineage326K_DistributorMaintainerSignals(t *testing.T) {
	mk := func(dist, maint string) AppInfo {
		card := mkURL("app-a@本源", "app-a", "本源", "1.0.0",
			"https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk")
		card.Distributor, card.Maintainer = dist, maint
		return card
	}
	for _, tc := range []struct {
		name       string
		dist, main string
		hit        bool
	}{
		{"distributor 空 → 回落 maintainer（第三方）", "", "其他人", true},
		{"两者皆空 → 无「他人打包」证据", "", "", false},
		{"distributor 与 owner 大小写相等", "CAROL", "其他人", false},
		{"maintainer 与 owner 相等但 distributor 第三方 → distributor 优先", "其他人", "carol", true},
		{"distributor 等于 owner、maintainer 第三方 → 仍放过（现口径）", "carol", "其他人", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := []config.SourceRef{
				{Name: "本源", URL: "https://github.com/carol/fndepot"},
				{Name: "父源", URL: "https://github.com/alice/awesome-apps"},
			}
			cache := map[string]map[string]*source.App{
				"本源": srcCache(map[string]string{"app-a": "1.0.0"}),
				"父源": srcCache(map[string]string{"app-a": "2.5.0"}),
			}
			s := lkServer(t, refs, cache, map[string]string{"carol/fndepot": "alice/awesome-apps"})
			out := []AppInfo{mk(tc.dist, tc.main)}
			s.applyUpstreamLineage(out)
			got := out[0].UpstreamRepo == "alice/awesome-apps"
			if got != tc.hit {
				t.Fatalf("判搬运 = %v，期望 %v（%#v）", got, tc.hit, out[0])
			}
		})
	}
}

// ── ⑥ 跨源同名不同 app：显示名校验后的保守行为（0.6.326k 修复后改写 F4=B4） ──
// upstreamVersionOf 与 applyUpstreamCorrection 现在都带显示名否决：上游源里同名卡
// 若其实是**另一个应用**（同 appname 不同来源、显示名不同），搬运卡既拿不到它的
// 版本号（UpstreamVersion 空），自动纠正也不会把它的包地址/大小/sha 抄过来。
func TestLineage326K_UpstreamVersionNameCollisionRisk(t *testing.T) {
	refs := []config.SourceRef{
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
	}
	cache := map[string]map[string]*source.App{
		"搬运源": srcCache(map[string]string{"clone-a": "1.9.0"}),
		// 同名 "clone-a"，但发布仓库/显示名都是另一回事（另一个应用）：
		"上游源": {"clone-a": {Name: "clone-a", DisplayName: "完全无关的应用", Source: "上游源", Version: "9.9.9",
			DownloadURL: "https://github.com/zed/unrelated/releases/download/v9.9.9/other.fpk", Sha256: "deadbeef"}},
	}
	s := lkServer(t, refs, cache, map[string]string{"yuexps/fndepot": ""})
	card := mkURL("clone-a@搬运源", "clone-a", "搬运源", "1.9.0",
		"https://github.com/alice/awesome-apps/releases/download/v1.9.0/clone-a.fpk")
	card.Installed, card.InstalledVersion = true, "1.9.0"
	// 目录里那张同名但他源的卡（详情/纠正都是按 AppName+Source 找它）：
	unrelated := mkURL("clone-a@上游源", "clone-a", "上游源", "9.9.9",
		"https://github.com/zed/unrelated/releases/download/v9.9.9/other.fpk")
	unrelated.DisplayName = "完全无关的应用"
	unrelated.Sha256 = "deadbeef"
	out := []AppInfo{card, unrelated}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource != "上游源" {
		t.Fatalf("上游源仍应被认出（血缘判定不受显示名影响）：%#v", out[0])
	}
	if out[0].UpstreamVersion != "" {
		t.Fatalf("上游源里同名卡显示名不兼容 → 不得取其版本：%#v", out[0])
	}
	// 自动纠正必须被显示名否决（否则会把「另一个应用」的包抄成更新目标 = 下错包）：
	ownURL := out[0].ReleaseURL
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("同名但显示名不兼容的上游卡不得点亮更新：%#v", out[0])
	}
	if out[0].ReleaseURL != ownURL || out[0].Sha256 != "" {
		t.Fatalf("纠正被否决时不得改写包字段：%#v", out[0])
	}
}

// ── ⑦ 溯源字段与 Key 无关 + 删源后遗留卡溯源丢失（钉住） ───────────────────
func TestLineage326K_KeyRevertAndSourceRemoval(t *testing.T) {
	s := newLineageServer(t)
	bare := mkLineageCard("app-a", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false)
	scoped := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false)
	out := []AppInfo{bare, scoped}
	s.applyUpstreamLineage(out)
	// 裸键（同名唯一/删源后回落）与 appname@源名 派生结果一致 —— 溯源字段不依赖 Key。
	if out[0].UpstreamRepo != out[1].UpstreamRepo || out[0].UpstreamSource != out[1].UpstreamSource ||
		out[0].UpstreamVersion != out[1].UpstreamVersion || out[0].UpstreamURL != out[1].UpstreamURL {
		t.Fatalf("溯源字段不应依赖 Key：%#v vs %#v", out[0], out[1])
	}
	// 钉住现状：源被删除后，已装遗留卡（Source==""）既无本源也无包地址 ⇒ 溯源行消失，
	// 自动纠正随之失效（源删除 = 溯源信息丢失，upstream_* 不落盘）。
	leftover := AppInfo{Key: "app-a", AppName: "app-a", DisplayName: "app-a",
		Installed: true, InstalledVersion: "1.9.0"}
	out2 := []AppInfo{leftover}
	s.applyUpstreamLineage(out2)
	if out2[0].UpstreamRepo != "" || out2[0].UpstreamSource != "" || out2[0].UpstreamURL != "" {
		t.Fatalf("无源遗留卡不应有溯源：%#v", out2[0])
	}
}

// ── ⑧ 已装卡 / 已 Hidden 卡 / 已 UserHidden 卡 / 禁用源卡 ──────────────────
func TestLineage326K_InstalledHiddenCardsStillDerive(t *testing.T) {
	s := newLineageServer(t)
	inst := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", true)
	hid := mkLineageCard("app-b@搬运源", "app-b", "搬运源", "1.0.0", "alice/awesome-apps", false)
	hid.Hidden = true // 模拟已被 applyDedupPolicy 标为隐藏
	uh := mkLineageCard("app-c@搬运源", "app-c", "搬运源", "1.0.0", "alice/awesome-apps", false)
	uh.UserHidden = true
	out := []AppInfo{inst, hid, uh}
	s.applyUpstreamLineage(out)
	for i := range out {
		if out[i].UpstreamRepo != "alice/awesome-apps" || out[i].UpstreamSource != "上游源" {
			t.Fatalf("第 %d 张卡（已装/Hidden/UserHidden）应照样派生溯源：%#v", i, out[i])
		}
	}
	if out[0].UpstreamVersion != "2.5.0" { // 上游源里有同名 app-a
		t.Fatalf("已装卡应取上游源同名卡版本，实际 %q", out[0].UpstreamVersion)
	}
	for _, i := range []int{1, 2} {
		if out[i].UpstreamVersion != "" {
			t.Fatalf("上游源无同名卡时 version 应留空（前端回落显示源名）：%#v", out[i])
		}
	}
}

func TestLineage326K_DisabledSourceCardsAbsentFromCatalog(t *testing.T) {
	off := false
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "禁用源", URL: "https://github.com/alice/fndepot", Enabled: &off},
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"禁用源": srcCache(map[string]string{"only-in-disabled": "1.0.0"}),
		"上游源": srcCache(map[string]string{"app-a": "2.5.0"}),
	})
	if got := m.Apps("", ""); len(got) != 1 || got[0].Name != "app-a" {
		t.Fatalf("聚合目录应跳过禁用源，实际 %d 条", len(got))
	}
	if got := m.Apps("禁用源", ""); len(got) != 1 || got[0].Name != "only-in-disabled" {
		t.Fatalf("按源名显式查询仍应可达（详情逃生门），实际 %d 条", len(got))
	}
	// 派生层：禁用 fork 源同时置 SourceIsFork + SourceDisabled；但列表层拿不到该卡，
	// 故「禁用fork源」在卡片上不可撤销（恢复只能走源管理页）——钉住现状。
	s := &Server{Cfg: cfg, Src: m, Forks: source.NewGHForks("")}
	s.Forks.SetForTest("alice/fndepot", "alice/awesome-apps")
	out := []AppInfo{mkLineageCard("app-a@禁用源", "app-a", "禁用源", "1.0.0", "", false)}
	s.applySourceLineage(out)
	if !out[0].SourceDisabled || !out[0].SourceIsFork {
		t.Fatalf("禁用 fork 源应同时置位：%#v", out[0])
	}
}

// ── ⑨ 与 dedup 的交互：merge 档代表卡可能不带溯源（钉住） ─────────────────
func TestLineage326K_DedupMergeHidesUpstreamCardPinned(t *testing.T) {
	s := newLineageServer(t)
	build := func() (AppInfo, AppInfo) {
		mover := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false)
		mover.Maintainer = "一零一二" // 同宗（merge 簇）证据
		up := mkLineageCard("app-a@上游源", "app-a", "上游源", "2.5.0", "alice/awesome-apps", false)
		up.Maintainer = "一零一二"
		return mover, up
	}
	mover, up := build()
	out := []AppInfo{mover, up}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo == "" {
		t.Fatalf("前置：搬运卡应有溯源")
	}
	applyDedupPolicy(out, "merge")
	if out[1].Hidden {
		t.Fatalf("代表卡不应被 Hidden")
	}
	if !out[0].Hidden {
		t.Fatalf("钉住行为已变（可能已让代表卡优先带溯源）：%#v", out[0])
	}
	if out[1].UpstreamRepo != "" {
		t.Fatalf("代表卡（版本更高的上游源卡）本无溯源 → 列表里看不到溯源：%#v", out[1])
	}
	// 去重不得改写溯源字段（详情端点仍按 key 可达 = 逃生门）
	if out[0].UpstreamSource != "上游源" || out[0].UpstreamVersion != "2.5.0" {
		t.Fatalf("去重不得改写溯源字段：%#v", out[0])
	}
	// all 档：两张都保留，不置 Hidden
	m2, u2 := build()
	out2 := []AppInfo{m2, u2}
	applyDedupPolicy(out2, "all")
	if out2[0].Hidden || out2[1].Hidden {
		t.Fatalf("all 档不应置 Hidden：%#v / %#v", out2[0], out2[1])
	}
}

// ── ⑩ 缓存文件损坏 / ghforks 404 负缓存 / addprobe 损坏 / owner 大小写 ──────
func TestLineage326K_CacheCorruptAndNegativeFork(t *testing.T) {
	dir := t.TempDir()
	// ① fork 缓存文件损坏：读不了当空 → 未知 → 保守不置位（不得 panic）
	bad := filepath.Join(dir, "ghforks-bad.json")
	if err := os.WriteFile(bad, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, known := source.NewGHForks(bad).Lookup("carol/fndepot"); known {
		t.Fatalf("损坏缓存应视为未知")
	}
	// ② ghforks 404/私有负缓存：known=true 但 fork=false → 不判搬运（自愈前保守）
	neg := filepath.Join(dir, "ghforks-neg.json")
	raw := `{"repos":{"carol/fndepot":{"not_found":true,"fetched_at":"2026-10-01T00:00:00Z"}}}`
	if err := os.WriteFile(neg, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	g := source.NewGHForks(neg)
	if _, fork, known := g.Lookup("carol/fndepot"); !known || fork {
		t.Fatalf("负缓存应 known=true / fork=false")
	}
	refs := []config.SourceRef{
		{Name: "本源", URL: "https://github.com/carol/fndepot"},
		{Name: "父源", URL: "https://github.com/alice/awesome-apps"},
	}
	cfg := &config.Config{Sources: refs}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"本源": srcCache(map[string]string{"app-a": "1.0.0"}),
		"父源": srcCache(map[string]string{"app-a": "2.5.0"}),
	})
	s := &Server{Cfg: cfg, Src: m, Forks: g}
	card := mkLineageCard("app-a@本源", "app-a", "本源", "1.0.0",
		"https://github.com/carol/fndepot/releases/download/v1.0.0/app-a.fpk", false)
	card.Distributor = "其他人"
	out := []AppInfo{card}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" {
		t.Fatalf("负缓存下不应判搬运：%#v", out[0])
	}
	// ③ addprobe 缓存损坏 → 未知 → 两个 add 字段都不设（前端维持现状，不出死按钮）
	badp := filepath.Join(dir, "addprobe-bad.json")
	if err := os.WriteFile(badp, []byte("xx"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AddProbes = source.NewAddProbe(badp)
	c2 := mkLineageCard("x@本源", "x", "本源", "1.0.0", "zed/pack-only", false)
	out2 := []AppInfo{c2}
	s.applyUpstreamLineage(out2)
	if out2[0].UpstreamRepo != "zed/pack-only" || out2[0].UpstreamAddURL != "" || out2[0].UpstreamAddNote != "" {
		t.Fatalf("探测未知时应维持现状（两字段皆空）：%#v", out2[0])
	}
}

func TestLineage326K_AddProbeOwnerIndexCaseInsensitive(t *testing.T) {
	refs := []config.SourceRef{
		{Name: "dave源", URL: "https://github.com/Dave/FnDepot"},
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}
	cache := map[string]map[string]*source.App{
		"dave源": srcCache(map[string]string{"app-a": "1.0.0"}),
		"搬运源":   srcCache(map[string]string{"app-a": "0.9.0"}),
	}
	s := lkServer(t, refs, cache, map[string]string{"yuexps/fndepot": ""})
	s.AddProbes = source.NewAddProbe("")
	// 探测结果里的 owner 索引仓按 GitHub 目录名带大写；源列表 key 全小写 → 须等价匹配。
	s.AddProbes.SetForTest("dave/pack-only", "", "Dave/FnDepot")
	out := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "0.9.0", "dave/pack-only", false)}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "dave/pack-only" {
		t.Fatalf("前置不成立：%#v", out[0])
	}
	if out[0].UpstreamSource != "dave源" || out[0].UpstreamVersion != "1.0.0" {
		t.Fatalf("owner 索引仓大小写应等价匹配：%#v", out[0])
	}
	if !strings.Contains(out[0].UpstreamAddNote, "已由已添加的源") {
		t.Fatalf("应同时给「已收录」说明：%#v", out[0])
	}
	if out[0].UpstreamAddURL != "" {
		t.Fatalf("已收录时不应给 add_url：%#v", out[0])
	}
}

// 本源仓库归一 key 为空（官方 OAuth 源：URL 是中文描述、无路径）→ 整卡跳过，
// 即便包地址指向 GitHub 他人仓库。现网实例：picoclaw@AstideLabs（残余，未修）。
func TestLineage326K_UnknownSourceRepoSkipsLineage(t *testing.T) {
	const officialURL = "本机官方应用中心（OAuth 免登录连接）"
	if k := source.SourceURLKey(officialURL); k != "" {
		t.Fatalf("SourceURLKey = %q，期望空（本源未知）", k)
	}
	refs := []config.SourceRef{{Name: "fnos-official", URL: officialURL}}
	s := lkServer(t, refs, map[string]map[string]*source.App{
		"fnos-official": srcCache(map[string]string{"picoclaw": "1.0.0"}),
	}, nil)
	out := []AppInfo{mkLineageCard("picoclaw@AstideLabs", "picoclaw", "fnos-official", "1.0.0", "AstideLabs/FnDepot", false)}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamURL != "" || out[0].UpstreamSource != "" {
		t.Fatalf("本源未知（官方源）时不应判上游：%#v", out[0])
	}
}

// ── ⑪ 同名多源（same_name_count>1）与「同仓库配多个源名」 ──────────────────
func TestLineage326K_SameNameMultiSourceAndDupRepo(t *testing.T) {
	// ① 同名多源：两张同名卡各属不同源，各自派生各自的血缘，互不串味。
	s := newLineageServer(t)
	a := mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false)
	b := mkLineageCard("app-a@自建源", "app-a", "自建源", "1.0.0", "bob/mine", false)
	out := []AppInfo{a, b}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "alice/awesome-apps" || out[0].UpstreamSource != "上游源" {
		t.Fatalf("搬运源卡应溯源到上游源：%#v", out[0])
	}
	if out[1].UpstreamRepo != "" || out[1].UpstreamSource != "" {
		t.Fatalf("自建源卡（包在自己仓）不应判搬运：%#v", out[1])
	}
	// ② 同一仓库配了多个源名（同 URL 不同写法/不同名）→ upstream_source 取源列表
	// 首位（确定性：ETag 不漂移）；第二个源名的版本永不参与（钉住现状）。
	refs := []config.SourceRef{
		{Name: "甲", URL: "https://github.com/alice/awesome-apps"},
		{Name: "乙", URL: "https://github.com/Alice/Awesome-Apps/"},
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
	}
	cache := map[string]map[string]*source.App{
		"甲":   srcCache(map[string]string{"app-a": "2.5.0"}),
		"乙":   srcCache(map[string]string{"app-a": "9.9.9"}),
		"搬运源": srcCache(map[string]string{"app-a": "1.9.0"}),
	}
	s2 := lkServer(t, refs, cache, map[string]string{"yuexps/fndepot": ""})
	out2 := []AppInfo{mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false)}
	s2.applyUpstreamLineage(out2)
	if out2[0].UpstreamSource != "甲" || out2[0].UpstreamVersion != "2.5.0" {
		t.Fatalf("同仓库多源名应取源列表首位且版本来自它：%#v", out2[0])
	}
}
