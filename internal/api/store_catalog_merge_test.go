package api

import (
	"testing"

	"moo/internal/platform"
)

// 0.6.336（C 方案）：官方应用中心目录**独立成卡**，不再吞并同名社区卡。
// 旧契约（≤0.6.335）把第一个同名卡的 Source 改写成 fnos-official —— 社区源
// 那张卡就此消失（fnos-store 175 条里 22 条不可见，全目录 80 条被吞），
// 本文件锁死新契约，防回退。

func c336Card(key, app, display, src, ver string) AppInfo {
	return AppInfo{Key: key, AppName: app, DisplayName: display, Source: src, LatestVersion: ver}
}

// ① 同名：社区卡原样保留 + 官方卡追加（顺序 = 社区在前，官方在后）
func TestMergeOfficialCards336_KeepsCommunityCard(t *testing.T) {
	out := []AppInfo{
		c336Card("gridmerge@dysfwhq", "gridmerge", "gridmerge", "dysfwhq", "1.0.0"),
		c336Card("other@dysfwhq", "other", "other", "dysfwhq", "2.0.0"),
	}
	official := []AppInfo{
		c336Card("gridmerge@fnos-official", "gridmerge", "gridmerge", OfficialSourceID, "3.0.0"),
	}
	got := mergeOfficialCards(out, official, map[string]platform.InstalledApp{})
	if len(got) != 3 {
		t.Fatalf("应保留 2 张社区卡 + 追加 1 张官方卡，got %d：%+v", len(got), got)
	}
	if got[0].Source != "dysfwhq" || got[0].LatestVersion != "1.0.0" || got[0].Key != "gridmerge@dysfwhq" {
		t.Fatalf("社区卡必须原样保留（源/版本/key 都不许改写）：%+v", got[0])
	}
	last := got[len(got)-1]
	if last.Source != OfficialSourceID || last.Key != "gridmerge@fnos-official" || last.LatestVersion != "3.0.0" {
		t.Fatalf("官方卡应独立追加在尾部：%+v", last)
	}
}

// ② 官方卡复用 daemon 安装态（否则新造的官方卡永远显示未装）
func TestMergeOfficialCards336_InstalledState(t *testing.T) {
	out := []AppInfo{c336Card("syncthing@fnos-store", "syncthing", "Syncthing", "fnos-store", "1.27.0")}
	official := []AppInfo{c336Card("syncthing@fnos-official", "syncthing", "Syncthing", OfficialSourceID, "1.27.0")}
	byName := map[string]platform.InstalledApp{
		"syncthing": {AppName: "syncthing", Version: "1.27.0"},
	}
	got := mergeOfficialCards(out, official, byName)
	if len(got) != 2 {
		t.Fatalf("应两张卡，got %d", len(got))
	}
	if !got[1].Installed || got[1].InstalledVersion != "1.27.0" {
		t.Fatalf("官方卡应复用 daemon 安装态：%+v", got[1])
	}
	if got[0].Installed {
		t.Fatalf("社区卡不在 merge 阶段置已装（交给 markInstalledCanonical 裁决）：%+v", got[0])
	}
}

// ③ 官方卡缺简介/开发者/发布者时从同名社区卡回填；官方非空值不被覆盖
func TestMergeOfficialCards336_BackfillFromCommunity(t *testing.T) {
	comm := c336Card("foo@fnos-store", "foo", "Foo", "fnos-store", "1.0.0")
	comm.Description = "community desc"
	comm.Maintainer = "comm-maint"
	comm.MaintainerURL = "https://github.com/comm"
	comm.Distributor = "comm-dist"
	out := []AppInfo{comm}
	official := []AppInfo{c336Card("foo@fnos-official", "foo", "Foo", OfficialSourceID, "1.0.0")}
	got := mergeOfficialCards(out, official, map[string]platform.InstalledApp{})
	off := got[len(got)-1]
	if off.Description != "community desc" || off.Maintainer != "comm-maint" ||
		off.MaintainerURL != "https://github.com/comm" || off.Distributor != "comm-dist" {
		t.Fatalf("官方卡空字段应从社区卡回填：%+v", off)
	}
	// 官方值优先：非空不被社区覆盖
	official2 := []AppInfo{{
		Key: "foo@fnos-official", AppName: "foo", DisplayName: "Foo", Source: OfficialSourceID,
		LatestVersion: "1.0.0", Description: "official desc", Maintainer: "off-maint", Distributor: "off-dist",
	}}
	got2 := mergeOfficialCards([]AppInfo{comm}, official2, map[string]platform.InstalledApp{})
	off2 := got2[len(got2)-1]
	if off2.Description != "official desc" || off2.Maintainer != "off-maint" || off2.Distributor != "off-dist" {
		t.Fatalf("官方非空值不得被社区值覆盖：%+v", off2)
	}
}

// ④ 官方目录重名条目不得重复造卡
func TestMergeOfficialCards336_NoDuplicateOfficial(t *testing.T) {
	official := []AppInfo{
		c336Card("x@fnos-official", "x", "X", OfficialSourceID, "1.0.0"),
		c336Card("x@fnos-official", "x", "X", OfficialSourceID, "1.0.0"),
	}
	got := mergeOfficialCards(nil, official, map[string]platform.InstalledApp{})
	if len(got) != 1 {
		t.Fatalf("同名官方条目应只造一张卡，got %d：%+v", len(got), got)
	}
}

// ⑤ 已装代表卡：官方优先（社区同名卡降级为未装且不显示更新）
func TestOfficialSplit336_InstalledCanonical(t *testing.T) {
	comm := c336Card("bunjs@wabisabi926b", "bunjs", "bunjs", "wabisabi926b", "1.4.2")
	off := c336Card("bunjs@fnos-official", "bunjs", "bunjs", OfficialSourceID, "1.3.9")
	comm.LatestVersion = "1.4.2"
	byName := map[string]platform.InstalledApp{
		"bunjs": {AppName: "bunjs", Version: "1.3.9", SourceID: "official"},
	}
	// 模拟 buildCatalog：社区卡先按 daemon 安装态填充，官方卡由合并追加时填充
	out := []AppInfo{comm}
	fillInstalled(&out[0], byName["bunjs"])
	out = mergeOfficialCards(out, []AppInfo{off}, byName)
	if len(out) != 2 {
		t.Fatalf("应两张卡，got %d", len(out))
	}
	markInstalledCanonical(out, map[string]string{})
	if !out[1].Installed || out[1].InstalledVersion != "1.3.9" {
		t.Fatalf("官方卡应为已装代表卡：%+v", out[1])
	}
	if out[1].HasUpdate {
		t.Fatalf("已装 1.3.9 = 官方最新 1.3.9，不该有更新：%+v", out[1])
	}
	if out[0].Installed || out[0].HasUpdate || out[0].AvailableVersion != "" {
		t.Fatalf("社区同名卡应降级为未装且不显示更新：%+v", out[0])
	}
}

// ⑥ 去重档位：all 两张都可见；one 只留官方代表卡
func TestOfficialSplit336_DedupPolicies(t *testing.T) {
	mk := func() []AppInfo {
		return []AppInfo{
			c336Card("gridmerge@dysfwhq", "gridmerge", "gridmerge", "dysfwhq", "1.0.0"),
			c336Card("gridmerge@fnos-official", "gridmerge", "gridmerge", OfficialSourceID, "3.0.0"),
		}
	}
	all := mk()
	applyDedupPolicy(all, "all")
	if all[0].Hidden || all[1].Hidden {
		t.Fatalf("all 档不得隐藏任何同名卡：%+v", all)
	}
	one := mk()
	applyDedupPolicy(one, "one")
	vis := 0
	for _, a := range one {
		if !a.Hidden {
			vis++
		}
	}
	if vis != 1 {
		t.Fatalf("one 档应只留一张可见卡，got %d：%+v", vis, one)
	}
	if !one[0].Hidden || one[1].Hidden {
		t.Fatalf("one 档应隐藏社区卡、保留官方卡为唯一可见代表：%+v", one)
	}
}

// ⑦ 无源卡（已装但任何源都查不到）仍被官方条目吸收，不得多出一张空源卡
func TestMergeOfficialCards336_AbsorbsSourceLessCard(t *testing.T) {
	inst := AppInfo{Key: "trim.docs", AppName: "trim.docs", DisplayName: "trim.docs", Installed: true, InstalledVersion: "1.0.0"}
	official := []AppInfo{{
		Key: "trim.docs@fnos-official", AppName: "trim.docs", DisplayName: "文档",
		Source: OfficialSourceID, LatestVersion: "1.0.1", Description: "官方文档应用",
	}}
	got := mergeOfficialCards([]AppInfo{inst}, official, map[string]platform.InstalledApp{})
	if len(got) != 1 {
		t.Fatalf("无源卡应被官方条目吸收（不得新增同名卡），got %d：%+v", len(got), got)
	}
	e := got[0]
	if e.Source != OfficialSourceID || e.Key != "trim.docs@fnos-official" {
		t.Fatalf("吸收后应为官方卡：%+v", e)
	}
	if !e.Installed || e.InstalledVersion != "1.0.0" {
		t.Fatalf("已装状态必须保留：%+v", e)
	}
	if !e.HasUpdate || e.AvailableVersion != "1.0.1" {
		t.Fatalf("已装 1.0.0 vs 官方 1.0.1 应判有更新：%+v", e)
	}
	if e.Description != "官方文档应用" {
		t.Fatalf("官方元数据应补齐：%+v", e)
	}
}
