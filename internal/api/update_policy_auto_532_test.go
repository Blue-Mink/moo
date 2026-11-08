package api

// update_policy_auto_532_test.go —— 0.6.532「自动（推荐）」策略单测。
//
// auto = lineage 同宗目标 + 上游纠正（有上游血缘的搬运应用取更高者）。
// 目的：把设置页四档策略收敛成一个用户无需理解的推荐档，同时钉死行为：
//   A1 同作者其它仓库更高 → 取该版本（lineage 生效）
//   A2 上游原作者版本更高 → 取上游版本（upstream 纠正生效）
//   A3 strict 对照无跨源目标
//   A4 设置保存白名单接受 auto（GET 回读一致）

import (
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/source"
)

// autoTierCards 已装搬运卡 1.9.0（上游=alice/awesome-apps 2.5.0）+ 同作者他源卡。
func autoTierCards(otherVer string) []AppInfo {
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
	if otherVer != "" {
		cards = append(cards, AppInfo{
			Key: "app-a@他源", AppName: "app-a", DisplayName: "app-a", Source: "他源",
			LatestVersion: otherVer, Maintainer: "一零一二", Distributor: "原作者甲",
			Sha256: "cafe", SizeBytes: 4096,
			ReleaseURL: "https://github.com/bob/other/releases/download/v" + otherVer + "/app-a.fpk",
		})
	}
	return cards
}

func TestResolveUpdateTargetSelection_Auto(t *testing.T) {
	s := newMatrixServer(t)

	// A1：同作者他源 3.0.0 更高 → auto 取 3.0.0（lineage 生效）
	o := runResolve(s, autoTierCards("3.0.0"), "auto", false)
	if o[0].AvailableVersion != "3.0.0" || o[0].UpdateFromSource != "他源" {
		t.Fatalf("auto 应取同作者最高 3.0.0/他源，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// A2：他源仅 2.0.0，上游 2.5.0 更高 → auto 纠正到上游 2.5.0
	o = runResolve(s, autoTierCards("2.0.0"), "auto", false)
	if o[0].AvailableVersion != "2.5.0" || o[0].UpdateFromSource != "上游源" {
		t.Fatalf("auto 应纠正到上游 2.5.0，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}

	// A3：strict 对照——不跨源，无更新目标
	o = runResolve(s, autoTierCards("3.0.0"), "strict", false)
	if o[0].AvailableVersion != "" || o[0].UpdateFromSource != "" {
		t.Fatalf("strict 不应有跨源目标，got %q/%q", o[0].AvailableVersion, o[0].UpdateFromSource)
	}
}

func TestSettingsUpdatePolicyAutoWhitelisted(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}
	putSettingsBody(t, s, `{"update_policy": "auto"}`)
	if cfg.UpdatePolicy != "auto" {
		t.Fatalf("update_policy 应保存为 auto，got %q", cfg.UpdatePolicy)
	}
	if raw := doSettingsGet(t, s).Body.String(); !strings.Contains(raw, `"update_policy":"auto"`) {
		t.Fatalf("GET 应透出 update_policy=auto：%s", raw)
	}
	// 白名单外值整单拒绝
	putSettingsBodyExpect400(t, s, `{"update_policy": "bogus"}`)
	if cfg.UpdatePolicy != "auto" {
		t.Fatalf("非法值不应改动配置，got %q", cfg.UpdatePolicy)
	}
}
