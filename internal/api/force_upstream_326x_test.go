package api

// force_upstream_326x_test.go —— 0.6.326x「更新策略单轴化」单测。
//
// 背景：原「三档策略 + 独立 auto_correct 开关」被并入单一轴
// update_policy ∈ {strict, origin, lineage, upstream}，其中 upstream（强制上游）
// = 严格 base + 上游纠正。本文件钉死：
//   T1 四档各自的更新目标（strict 不跨源 / origin 同发布仓 / lineage 同作者 /
//      upstream 只跟上游原作者，即使同作者另有更高版也不取）
//   T2 兼容迁移：旧 auto_correct=true ⇒ 等价 upstream（策略值优先）

import (
	"testing"

	"moo/internal/platform"
)

// upstreamTierCards 构造三卡：已装搬运卡（恒 1.9.0）+ 上游源卡 2.5.0（同发布仓）
// + 可选他源卡 3.0.0（同作者但不同发布仓）。用于区分四档。
func upstreamTierCards(withOtherAuthor bool) []AppInfo {
	inst := AppInfo{
		Key: "app-a@搬运源", AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		Installed: true, InstalledVersion: "1.9.0", LatestVersion: "1.9.0",
		Maintainer: "一零一二", Distributor: "原作者甲",
		ReleaseURL: "https://github.com/alice/awesome-apps/releases/download/v1.9.0/app-a.fpk",
	}
	up := AppInfo{
		Key: "app-a@上游源", AppName: "app-a", DisplayName: "app-a", Source: "上游源",
		LatestVersion: "2.5.0", Maintainer: "一零一二", Distributor: "原作者甲",
		Sha256: "beef", SizeBytes: 2048,
		ReleaseURL: "https://github.com/alice/awesome-apps/releases/download/v2.5.0/app-a.fpk",
	}
	cards := []AppInfo{inst, up}
	if withOtherAuthor {
		cards = append(cards, AppInfo{
			Key: "app-a@他源", AppName: "app-a", DisplayName: "app-a", Source: "他源",
			LatestVersion: "3.0.0", Maintainer: "一零一二", Distributor: "原作者甲",
			Sha256: "cafe", SizeBytes: 4096,
			ReleaseURL: "https://github.com/bob/other/releases/download/v3.0.0/app-a.fpk",
		})
	}
	return cards
}

// runResolve 复刻 buildCatalog 的更新目标段：溯源派生 → resolveUpdateTargetSelection。
func runResolve(s *Server, cards []AppInfo, policy string, correct bool) []AppInfo {
	out := cards
	s.applyUpstreamLineage(out)
	resolveUpdateTargetSelection(out, policy, correct, map[string]platform.InstalledApp{})
	return out
}

// ── T1：四档各自的更新目标 ────────────────────────────────────────────────
func TestResolveUpdateTargetSelection_FourTiers(t *testing.T) {
	s := newMatrixServer(t)

	// strict：不跨源 → 无目标
	o := runResolve(s, upstreamTierCards(true), "strict", false)
	if o[0].HasUpdate || o[0].AvailableVersion != "" || o[0].UpdateFromSource != "" {
		t.Fatalf("strict 应无跨源目标，got has=%v avail=%q from=%q",
			o[0].HasUpdate, o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// origin：只认同发布仓库 → 2.5.0（不是同作者他源的 3.0.0）
	o = runResolve(s, upstreamTierCards(true), "origin", false)
	if o[0].AvailableVersion != "2.5.0" || o[0].UpdateFromSource != "上游源" {
		t.Fatalf("origin 应取同发布仓 2.5.0/上游源，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// lineage：同作者即可 → 取更高的 3.0.0（他源）
	o = runResolve(s, upstreamTierCards(true), "lineage", false)
	if o[0].AvailableVersion != "3.0.0" || o[0].UpdateFromSource != "他源" {
		t.Fatalf("lineage 应取同作者最高 3.0.0/他源，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// upstream（强制上游）：只跟上游原作者版本 → 2.5.0，**不**取同作者的 3.0.0
	o = runResolve(s, upstreamTierCards(true), "upstream", false)
	if o[0].AvailableVersion != "2.5.0" || o[0].UpdateFromSource != "上游源" {
		t.Fatalf("upstream 应只跟上游源 2.5.0，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}
	if o[0].UpstreamSource != "上游源" {
		t.Fatalf("溯源字段应仍派生为 上游源，got %q", o[0].UpstreamSource)
	}
}

// ── T2：兼容迁移 —— 旧 auto_correct=true ⇒ 等价 upstream ───────────────────
func TestResolveUpdateTargetSelection_LegacyAutoCorrectEquivalentUpstream(t *testing.T) {
	s := newMatrixServer(t)

	// 旧配置：strict + auto_correct=true → 行为等于 upstream（2.5.0，而非无目标）
	o := runResolve(s, upstreamTierCards(true), "strict", true)
	if o[0].AvailableVersion != "2.5.0" || o[0].UpdateFromSource != "上游源" {
		t.Fatalf("auto_correct=true 应等价 upstream，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// 旧配置：lineage + auto_correct=true → 纠正优先（2.5.0，而非 lineage 的 3.0.0）
	o = runResolve(s, upstreamTierCards(true), "lineage", true)
	if o[0].AvailableVersion != "2.5.0" {
		t.Fatalf("auto_correct=true 应压过策略取上游 2.5.0，got %q", o[0].AvailableVersion)
	}

	// 策略已是 upstream 时，auto_correct 不改变结果
	o = runResolve(s, upstreamTierCards(true), "upstream", true)
	if o[0].AvailableVersion != "2.5.0" {
		t.Fatalf("upstream + auto_correct=true 应仍为 2.5.0，got %q", o[0].AvailableVersion)
	}
}
