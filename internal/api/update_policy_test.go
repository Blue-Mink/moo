package api

import (
	"testing"

	"moo/internal/platform"
)

// 0.6.272 跨源更新策略（严格/同源/同宗）单测。
// 案例数据来自主 NAS 2026-10-05 实测（Fluxor/golang/fnclearup/
// deepseek.harness 四应用跨源新版 dry-run）。

func mkCard(key, name, disp, src, inst, latest, url, maint, dist string) AppInfo {
	a := AppInfo{Key: key, AppName: name, DisplayName: disp, Source: src, ReleaseURL: url}
	a.Maintainer = maint
	a.Distributor = dist
	if inst != "" {
		a.Installed = true
		a.InstalledVersion = inst
		a.LatestVersion = latest
	} else {
		a.LatestVersion = latest
	}
	return a
}

func TestApplyLineageUpdates_OriginPolicy_SameRepoDiffCase(t *testing.T) {
	// golang：规范卡 nanhai31（转发 shuangji66/FnDepot 的包，装 1.26.4），
	// 兄弟卡 shuangji66 1.27.1 挂 shuangji66/Fndepot（大小写不同=同仓库）。
	out := []AppInfo{
		mkCard("golang@nanhai31", "golang", "Go", "nanhai31", "1.26.4", "1.26.4",
			"https://github.com/shuangji66/FnDepot/releases/download/2026.6.25/golang-1.26.4-x86.fpk", "", "shuangji66"),
		mkCard("golang@shuangji66", "golang", "Go", "shuangji66", "", "1.27.1",
			"https://github.com/shuangji66/Fndepot/releases/download/2026.9.12/golang-1.27.1-x86.fpk", "golang", "shuangji66"),
	}
	applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
	c := &out[0]
	if !c.HasUpdate || c.AvailableVersion != "1.27.1" {
		t.Fatalf("同源策略应点亮 1.27.1，实际 has=%v ver=%q", c.HasUpdate, c.AvailableVersion)
	}
	if c.UpdateFromSource != "shuangji66" {
		t.Fatalf("UpdateFromSource 应为 shuangji66，实际 %q", c.UpdateFromSource)
	}
	if c.ReleaseURL != out[1].ReleaseURL {
		t.Fatalf("ReleaseURL 应跟随同宗卡，实际 %q", c.ReleaseURL)
	}
}

func TestApplyLineageUpdates_ProxyPrefixSameOrigin(t *testing.T) {
	// Fluxor：规范卡直链 raw，兄弟卡挂 cdn.gh-proxy.org 前缀的同一 raw 地址。
	out := []AppInfo{
		mkCard("Fluxor@A", "Fluxor", "三体甜甜圈", "A", "2.5.1", "2.5.1",
			"https://raw.githubusercontent.com/shuangji66/Fndepot/main/x.fpk", "", ""),
		mkCard("Fluxor@B", "Fluxor", "三体甜甜圈", "B", "", "2.6.0",
			"https://cdn.gh-proxy.org/https://raw.githubusercontent.com/shuangji66/Fndepot/main/y.fpk", "shuangji66", "shuangji66"),
	}
	applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
	if !out[0].HasUpdate || out[0].AvailableVersion != "2.6.0" {
		t.Fatalf("代理前缀应归一同源并点亮 2.6.0，实际 has=%v ver=%q", out[0].HasUpdate, out[0].AvailableVersion)
	}
}

func TestApplyLineageUpdates_LineageAuthorOnly(t *testing.T) {
	// fnclearup：跨仓库但作者同为「一零一二」→ lineage 点亮，origin 不点亮。
	mk := func(pol string) *AppInfo {
		out := []AppInfo{
			mkCard("fnclearup@lab", "fnclearup", "清理精灵", "18926922221-lab", "0.9.8", "0.9.7",
				"https://cdn.gh-proxy.org/https://raw.githubusercontent.com/18926922221-lab/FnDepot/main/fnclearup.fpk", "一零一二", "一零一二"),
			mkCard("fnclearup@wyf", "fnclearup", "清理精灵", "Wyf841015", "", "0.11.2",
				"https://raw.githubusercontent.com/Wyf841015/FnDepot/main/fnclearup/fnclearup.fpk", "一零一二", "一零一二"),
		}
		applyLineageUpdates(out, pol, map[string]platform.InstalledApp{})
		return &out[0]
	}
	if c := mk("origin"); c.HasUpdate {
		t.Fatalf("origin 策略下跨仓库同作者不应点亮")
	}
	if c := mk("lineage"); !c.HasUpdate || c.AvailableVersion != "0.11.2" || c.UpdateFromSource != "Wyf841015" {
		t.Fatalf("lineage 策略应点亮 0.11.2 来自 Wyf841015，实际 has=%v ver=%q src=%q",
			c.HasUpdate, c.AvailableVersion, c.UpdateFromSource)
	}
}

func TestApplyLineageUpdates_StrictNoop(t *testing.T) {
	out := []AppInfo{
		mkCard("golang@nanhai31", "golang", "Go", "nanhai31", "1.26.4", "1.26.4",
			"https://github.com/shuangji66/FnDepot/releases/golang-1.26.4.fpk", "", "shuangji66"),
		mkCard("golang@shuangji66", "golang", "Go", "shuangji66", "", "1.27.1",
			"https://github.com/shuangji66/Fndepot/releases/golang-1.27.1.fpk", "golang", "shuangji66"),
	}
	applyLineageUpdates(out, "", map[string]platform.InstalledApp{})
	applyLineageUpdates(out, "strict", map[string]platform.InstalledApp{})
	if out[0].HasUpdate || out[0].UpdateFromSource != "" {
		t.Fatalf("strict/空策略必须是空操作")
	}
}

func TestApplyLineageUpdates_VetoDisplayName(t *testing.T) {
	// 显示名不同（三体甜甜圈 vs 赛博甜甜圈）= 不同应用，同作者也不得点亮。
	out := []AppInfo{
		mkCard("Fluxor@A", "Fluxor", "三体甜甜圈", "A", "2.5.1", "2.5.1",
			"https://github.com/a/r/releases/x.fpk", "shuangji66", "shuangji66"),
		mkCard("Fluxor@B", "Fluxor", "赛博甜甜圈", "B", "", "2.9.9",
			"https://github.com/b/r/releases/y.fpk", "shuangji66", "shuangji66"),
	}
	applyLineageUpdates(out, "lineage", map[string]platform.InstalledApp{})
	if out[0].HasUpdate {
		t.Fatalf("显示名不同必须否决")
	}
}

func TestApplyLineageUpdates_VetoDifferentAuthor(t *testing.T) {
	// maintainer 明显不同且 distributor 不救 → 否决（lineage）。
	out := []AppInfo{
		mkCard("app@A", "app", "某应用", "A", "1.0.0", "1.0.0",
			"https://github.com/a/r/releases/x.fpk", "作者甲", "甲发行"),
		mkCard("app@B", "app", "某应用", "B", "", "9.9.9",
			"https://github.com/b/r/releases/y.fpk", "作者乙", "乙发行"),
	}
	applyLineageUpdates(out, "lineage", map[string]platform.InstalledApp{})
	if out[0].HasUpdate {
		t.Fatalf("不同作者必须否决（供应链防冒充）")
	}
	// distributor 相等则救回（同发行方的重打包）
	out[1].Distributor = "甲发行"
	applyLineageUpdates(out, "lineage", map[string]platform.InstalledApp{})
	if !out[0].HasUpdate {
		t.Fatalf("distributor 相同应视为同宗")
	}
}

func TestApplyLineageUpdates_PlatformTrackedSkipped(t *testing.T) {
	// 平台跟踪应用（sourceID 非空）：策略不介入（0.6.257 归 daemon 权威）。
	out := []AppInfo{
		mkCard("git@official", "git", "Git", "fnos-official", "2.43.0", "2.43.0",
			"https://github.com/g/r/a.fpk", "git", ""),
		mkCard("git@sj", "git", "Git", "shuangji66", "", "2.56.0",
			"https://github.com/g/r/b.fpk", "git", ""),
	}
	byName := map[string]platform.InstalledApp{
		"git": {AppName: "git", Source: "thirdparty", SourceID: "49"},
	}
	applyLineageUpdates(out, "origin", byName)
	if out[0].HasUpdate || out[0].UpdateFromSource != "" {
		t.Fatalf("平台跟踪应用必须跳过")
	}
}

func TestApplyLineageUpdates_OwnSourceStaysHigher(t *testing.T) {
	// 规范卡自身源已有更高更新（2.5.5），兄弟卡仅 2.5.4 → 保持自身目标；
	// 兄弟卡 2.6.0 → 目标换给兄弟卡。
	run := func(sibVer string) *AppInfo {
		out := []AppInfo{
			mkCard("app@A", "app", "A", "A", "2.5.1", "2.5.5",
				"https://github.com/x/r/a.fpk", "", "同宗"),
			mkCard("app@B", "app", "A", "B", "", sibVer,
				"https://github.com/x/r/b.fpk", "", "同宗"),
		}
		out[0].HasUpdate = true
		out[0].AvailableVersion = "2.5.5"
		applyLineageUpdates(out, "origin", map[string]platform.InstalledApp{})
		return &out[0]
	}
	if c := run("2.5.4"); c.AvailableVersion != "2.5.5" || c.UpdateFromSource != "" {
		t.Fatalf("自身源更高应保留：ver=%q src=%q", c.AvailableVersion, c.UpdateFromSource)
	}
	if c := run("2.6.0"); c.AvailableVersion != "2.6.0" || c.UpdateFromSource != "B" {
		t.Fatalf("兄弟卡更高应换目标：ver=%q src=%q", c.AvailableVersion, c.UpdateFromSource)
	}
}

func TestApplyLineageUpdates_NotNewerNoFlag(t *testing.T) {
	out := []AppInfo{
		mkCard("app@A", "app", "A", "A", "1.2.0", "1.2.0",
			"https://github.com/x/r/a.fpk", "", "d"),
		mkCard("app@B", "app", "A", "B", "", "1.1.9",
			"https://github.com/x/r/b.fpk", "", "d"),
	}
	applyLineageUpdates(out, "lineage", map[string]platform.InstalledApp{})
	if out[0].HasUpdate {
		t.Fatalf("不比已装高的版本不得点亮")
	}
}
