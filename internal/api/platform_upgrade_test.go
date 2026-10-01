package api

import (
	"testing"

	"moo/internal/platform"
)

// 回归（0.6.72）：面板目录 app/list 的 version 可能滞后于平台实际可升级
// 版本（实测 trim.preview：目录 0.1.16 vs 升级目标 0.2.4）——仅凭目录
// 版本比较会把「应用中心有更新」的应用漏出「有更新」列表。
// applyPlatformUpgrades 用 daemon upgradeInfo（与应用中心 UI 同源）补齐。
func TestApplyPlatformUpgrades_StaleCatalogRecovered(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {
			AppName: "trim.preview", Version: "0.1.16",
			UpgradeInfo: &platform.UpgradeInfo{Version: "0.2.4", ChangeLog: "新增19种格式：<br/>1. 压缩包；"},
		},
	}
	out := []AppInfo{
		{
			AppName: "trim.preview", Installed: true,
			InstalledVersion: "0.1.16", LatestVersion: "0.1.16", // 目录滞后：与已装相同
			Source: OfficialSourceID,
		},
	}
	applyPlatformUpgrades(out, byName)

	a := out[0]
	if !a.HasUpdate {
		t.Fatal("平台报更新时 HasUpdate 必须为 true（应用中心同源）")
	}
	if a.AvailableVersion != "0.2.4" {
		t.Errorf("AvailableVersion = %q, want 0.2.4", a.AvailableVersion)
	}
	if a.LatestVersion != "0.2.4" {
		t.Errorf("LatestVersion = %q, want 0.2.4", a.LatestVersion)
	}
	if a.Changelog != "新增19种格式：\n1. 压缩包；" {
		t.Errorf("ChangeLog 未归一化换行: %q", a.Changelog)
	}
}

func TestApplyPlatformUpgrades_NoSignalKeepsExisting(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"Gitea": {AppName: "Gitea", Version: "1.27.3"}, // 无 upgradeInfo
	}
	out := []AppInfo{
		{
			AppName: "Gitea", Installed: true,
			InstalledVersion: "1.27.3", LatestVersion: "1.27.3",
			Source: OfficialSourceID,
		},
	}
	applyPlatformUpgrades(out, byName)
	if out[0].HasUpdate {
		t.Error("平台未报更新时不得制造更新（0.6.59 规则保持）")
	}
}

func TestApplyPlatformUpgrades_KeepsHigherAvailable(t *testing.T) {
	// 社区源已有更高版本（如 1.11.6），平台 upgradeInfo 更低（1.11.2）：
	// 取高者，更新目标不被平台信号拉低。
	byName := map[string]platform.InstalledApp{
		"wb2api": {
			AppName: "wb2api", Version: "1.11.1",
			UpgradeInfo: &platform.UpgradeInfo{Version: "1.11.2"},
		},
	}
	out := []AppInfo{
		{
			AppName: "wb2api", Installed: true,
			InstalledVersion: "1.11.1", LatestVersion: "1.11.6",
			HasUpdate: true, AvailableVersion: "1.11.6",
		},
	}
	applyPlatformUpgrades(out, byName)
	if out[0].AvailableVersion != "1.11.6" {
		t.Errorf("AvailableVersion 被平台信号拉低: %q", out[0].AvailableVersion)
	}
	if out[0].LatestVersion != "1.11.6" {
		t.Errorf("LatestVersion 被平台信号拉低: %q", out[0].LatestVersion)
	}
}

func TestApplyPlatformUpgrades_SkipsUninstalled(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {
			AppName: "trim.preview", Version: "0.1.16",
			UpgradeInfo: &platform.UpgradeInfo{Version: "0.2.4"},
		},
	}
	// 目录条目（未装形态）不应被平台信号标记为更新。
	out := []AppInfo{
		{AppName: "trim.preview", Installed: false, LatestVersion: "0.1.16"},
	}
	applyPlatformUpgrades(out, byName)
	if out[0].HasUpdate {
		t.Error("未装条目不得被标记 has_update")
	}
}
