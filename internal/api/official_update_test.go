package api

import "testing"

// 回归：社区源同名条目版本更高时，已装官方应用不得出现「更新」。
// 实测 nodejs_v22 已装 22.18.0-1（=官方最新），社区 shuangji66 源有
// 22.23.2，旧逻辑跨源取最高 → 假更新，且更新目标是社区包。
func TestApplyOfficialUpdateForInstalled_CommunityHigherNoUpdate(t *testing.T) {
	e := &AppInfo{
		Key:              "nodejs_v22@shuangji66的应用源",
		AppName:          "nodejs_v22",
		Installed:        true,
		InstalledVersion: "22.18.0-1",
		LatestVersion:    "22.23.2",
		HasUpdate:        true,
		AvailableVersion: "22.23.2",
	}
	oa := AppInfo{
		Key:           "nodejs_v22@" + OfficialSourceID,
		AppName:       "nodejs_v22",
		LatestVersion: "22.18.0-1",
		Source:        OfficialSourceID,
	}
	applyOfficialUpdateForInstalled(e, oa)
	if e.HasUpdate {
		t.Error("社区同名版本更高不构成更新，HasUpdate 应为 false")
	}
	if e.AvailableVersion != "" {
		t.Errorf("AvailableVersion 应为空，实际 %q", e.AvailableVersion)
	}
	if e.LatestVersion != "22.18.0-1" {
		t.Errorf("LatestVersion 应为官方版本 22.18.0-1，实际 %q", e.LatestVersion)
	}
	if e.Key != "nodejs_v22@"+OfficialSourceID {
		t.Errorf("Key 应校正为官方 key，实际 %q", e.Key)
	}
}

// 官方版本确实更高 → 正常更新，目标是官方版本、key 为官方 key。
func TestApplyOfficialUpdateForInstalled_OfficialHigherIsUpdate(t *testing.T) {
	e := &AppInfo{
		Key:              "1Panel",
		AppName:          "1Panel",
		Installed:        true,
		InstalledVersion: "1.0.10",
		LatestVersion:    "1.0.10",
	}
	oa := AppInfo{
		Key:           "1Panel@" + OfficialSourceID,
		AppName:       "1Panel",
		LatestVersion: "2.0.14",
		Source:        OfficialSourceID,
	}
	applyOfficialUpdateForInstalled(e, oa)
	if !e.HasUpdate {
		t.Fatal("官方版本更高应 HasUpdate=true")
	}
	if e.AvailableVersion != "2.0.14" {
		t.Errorf("AvailableVersion 应为 2.0.14，实际 %q", e.AvailableVersion)
	}
	if e.Key != "1Panel@"+OfficialSourceID {
		t.Errorf("Key 应校正为官方 key，实际 %q", e.Key)
	}
}

// 无已装版本信息 → 不判更新（防误报）。
func TestApplyOfficialUpdateForInstalled_NoInstalledVersion(t *testing.T) {
	e := &AppInfo{Key: "x@src", AppName: "x", Installed: true, LatestVersion: "9.9.9"}
	oa := AppInfo{Key: "x@" + OfficialSourceID, AppName: "x", LatestVersion: "1.0.0"}
	applyOfficialUpdateForInstalled(e, oa)
	if e.HasUpdate || e.AvailableVersion != "" {
		t.Errorf("无已装版本不应判更新: %+v", e)
	}
}
