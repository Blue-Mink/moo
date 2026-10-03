package api

import (
	"testing"

	"moo/internal/platform"
)

// 0.6.257 回归：官方/平台应用（appcenter 跟踪：sourceID 非空 或 source=official）
// 在官方源（OAuth）断连时，不得因社区同名条目的更高版本产生假「有更新」——
// 更新判定完全交给 appcenter daemon 权威。手动 FPK（无 sourceID）保留社区源更新。

// ① 平台应用（daemon source=thirdparty 但 sourceID 非空，即从 appcenter 登记源
//    安装的官方应用，如 nodejs_v22/1Panel/python312/git）断连 + 无 daemon 升级：
//    社区同名更高版本 → 假更新被清除。这是用户 2026-10-03 报的 6 条假更新场景。
func TestApplyPlatformUpdateAuthority_TrackedThirdpartyClearsCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"nodejs_v22": {AppName: "nodejs_v22", Version: "22.18.0-1", Source: "thirdparty", SourceID: "98"},
	}
	out := []AppInfo{
		{
			Key:              "nodejs_v22@shuangji66",
			AppName:          "nodejs_v22",
			Source:           "shuangji66",
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.23.2",
			HasUpdate:        true,
			AvailableVersion: "22.23.2",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate {
		t.Error("appcenter 跟踪的官方应用（sourceID 非空）无 daemon 升级时不应 HasUpdate")
	}
	if a.AvailableVersion != "" {
		t.Errorf("AvailableVersion 应清空，实际 %q", a.AvailableVersion)
	}
	if a.LatestVersion != "22.18.0-1" {
		t.Errorf("bogus 高 LatestVersion 应回落已装版本 22.18.0-1，实际 %q", a.LatestVersion)
	}
}

// ② 官方应用（source=official）断连 + 无 daemon 升级：社区同名更高版本 → 清除。
func TestApplyPlatformUpdateAuthority_OfficialClearsCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {AppName: "trim.preview", Version: "0.2.4", Source: "official", SourceID: "388"},
	}
	out := []AppInfo{
		{
			Key:              "trim.preview",
			AppName:          "trim.preview",
			Source:           "someCommunity",
			Installed:        true,
			InstalledVersion: "0.2.4",
			LatestVersion:    "0.9.9",
			HasUpdate:        true,
			AvailableVersion: "0.9.9",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate || a.AvailableVersion != "" || a.LatestVersion != "0.2.4" {
		t.Errorf("官方应用无 daemon 升级应清除假更新: HasUpdate=%v avail=%q latest=%q",
			a.HasUpdate, a.AvailableVersion, a.LatestVersion)
	}
}

// ③ 平台应用断连 + 有 daemon 升级目标：真平台更新保留，目标=daemon 版本
//    （即使社区同名版本更高也不顶替）。
func TestApplyPlatformUpdateAuthority_DaemonUpgradeForcedTarget(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {
			AppName:     "trim.preview",
			Version:     "0.1.16",
			Source:      "official",
			SourceID:    "388",
			Upgrade:     true,
			UpgradeInfo: &platform.UpgradeInfo{Version: "0.2.4"},
		},
	}
	out := []AppInfo{
		{
			Key:              "trim.preview",
			AppName:          "trim.preview",
			Source:           "someCommunity",
			Installed:        true,
			InstalledVersion: "0.1.16",
			LatestVersion:    "0.3.0",
			HasUpdate:        true,
			AvailableVersion: "0.3.0",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if !a.HasUpdate {
		t.Fatal("有 daemon 升级目标时应保留 HasUpdate")
	}
	if a.AvailableVersion != "0.2.4" {
		t.Errorf("目标应为 daemon 权威版本 0.2.4（非社区 0.3.0），实际 %q", a.AvailableVersion)
	}
}

// ④ 手动 FPK 应用（sourceID 空、source=thirdparty）：appcenter 不跟踪，
//    社区同名更高版本是合法更新，保留（零回归）。
func TestApplyPlatformUpdateAuthority_ManualFpkPreservesCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"wb2api": {AppName: "wb2api", Version: "1.11.1", Source: "thirdparty"}, // 无 sourceID
	}
	out := []AppInfo{
		{
			Key:              "wb2api@wabisabi926",
			AppName:          "wb2api",
			Source:           "wabisabi926",
			Installed:        true,
			InstalledVersion: "1.11.1",
			LatestVersion:    "1.11.6",
			HasUpdate:        true,
			AvailableVersion: "1.11.6",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if !a.HasUpdate || a.AvailableVersion != "1.11.6" {
		t.Errorf("手动 FPK 应用的社区同名更高版本应保留为更新，实际 HasUpdate=%v avail=%q",
			a.HasUpdate, a.AvailableVersion)
	}
}

// ⑤ 官方卡（Source==OfficialSourceID）：官方目录已收敛，本函数跳过不碰。
func TestApplyPlatformUpdateAuthority_OfficialCardSkipped(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"nodejs_v22": {AppName: "nodejs_v22", Version: "22.18.0-1", Source: "official", SourceID: "98"},
	}
	out := []AppInfo{
		{
			Key:              "nodejs_v22@fnos-official",
			AppName:          "nodejs_v22",
			Source:           OfficialSourceID,
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.18.0-1",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate || a.AvailableVersion != "" || a.LatestVersion != "22.18.0-1" {
		t.Errorf("官方卡不应被本函数改动: HasUpdate=%v avail=%q latest=%q",
			a.HasUpdate, a.AvailableVersion, a.LatestVersion)
	}
}
