package api

import (
	"testing"
)

// mkDedupCard 构造去重测试卡（key 唯一；ReleaseURL 用归一可识别的仓库形）。
func mkDedupCard(key, appname, display, source, ver, repo, maintainer, distro, arch string, installed bool, dl *int) AppInfo {
	a := AppInfo{
		Key: key, AppName: appname, DisplayName: display, Source: source,
		LatestVersion: ver, Maintainer: maintainer, Distributor: distro,
		Installed: installed, Arch: arch,
	}
	if repo != "" {
		a.ReleaseURL = "https://github.com/" + repo + "/releases/download/x/x.fpk"
	}
	if maintainer == "" && distro == "" && repo == "" {
		// 无同宗证据的卡
	}
	if dl != nil {
		a.DownloadCount = dl
	}
	return a
}

// 阶梯：已装规范卡 > 官方卡 > 本机可装 > 最高版本 > 下载量。
func TestDedupPolicy_RepLadder(t *testing.T) {
	arch := localArch()
	out := []AppInfo{
		mkDedupCard("k1", "app-a", "App A", "src-low", "1.0.0", "org/repoA", "", "", arch, false, intPtr(999)),
		mkDedupCard("k2", "app-a", "App A", OfficialSourceID, "0.9.0", "", "", "", arch, false, intPtr(0)),
		mkDedupCard("k3", "app-a", "App A", "src-inst", "1.2.0", "org/repoB", "", "", arch, true, intPtr(0)),
	}
	applyDedupPolicy(out, "one")
	if out[2].Hidden {
		t.Fatal("已装规范卡（Installed）永不隐藏")
	}
	if !out[1].Hidden || !out[0].Hidden {
		t.Fatal("one 档每组只留一张代表（应留 k3）")
	}
	if out[2].SameNameCount != 3 {
		t.Fatalf("代表卡 SameNameCount 应为 3，实际 %d", out[2].SameNameCount)
	}

	// 无已装卡：官方卡优先
	out2 := []AppInfo{
		mkDedupCard("k1", "app-b", "App B", "src-x", "2.0.0", "org/repoX", "", "", arch, false, intPtr(5)),
		mkDedupCard("k2", "app-b", "App B", OfficialSourceID, "1.0.0", "", "", "", arch, false, intPtr(0)),
	}
	applyDedupPolicy(out2, "one")
	if out2[1].Hidden {
		t.Fatal("官方目录卡优先于社区卡（官方徽章代表）")
	}

	// 无已装/官方：最高版本优先
	out3 := []AppInfo{
		mkDedupCard("k1", "app-c", "App C", "src-a", "1.0.0", "org/r1", "", "", arch, false, intPtr(100)),
		mkDedupCard("k2", "app-c", "App C", "src-b", "1.2.0", "org/r2", "", "", arch, false, intPtr(1)),
	}
	applyDedupPolicy(out3, "one")
	if out3[1].Hidden {
		t.Fatal("同组留最高版本卡为代表")
	}

	// 版本相同：下载量高者
	out4 := []AppInfo{
		mkDedupCard("k1", "app-d", "App D", "src-a", "1.0.0", "org/r1", "", "", arch, false, intPtr(10)),
		mkDedupCard("k2", "app-d", "App D", "src-b", "1.0.0", "org/r2", "", "", arch, false, intPtr(20)),
	}
	applyDedupPolicy(out4, "one")
	if out4[1].Hidden {
		t.Fatal("同版本留下载量高者为代表")
	}
}

// merge 档：同仓库并簇留一；不同作者不并；不同版本同 sha 并簇。
func TestDedupPolicy_MergeClusters(t *testing.T) {
	arch := localArch()
	out := []AppInfo{
		// 簇 1：同仓库两卡
		mkDedupCard("k1", "m-app", "M", "src-1", "1.0.0", "org/same", "", "", arch, false, nil),
		mkDedupCard("k2", "m-app", "M", "src-2", "1.1.0", "org/same", "", "", arch, false, nil),
		// 独立：同 appname 不同仓库不同作者（不并）
		mkDedupCard("k3", "m-app", "M", "src-3", "1.0.0", "org/other", "alice", "", arch, false, nil),
		// 独立：显示名不同（同名不同应用，否决）
		mkDedupCard("k4", "m-app", "M-Pro", "src-4", "1.0.0", "org/same", "", "", arch, false, nil),
	}
	merged, one := applyDedupPolicy(out, "merge")
	if one != 1 {
		t.Fatalf("oneVisible 应为 1（单一 appname），实际 %d", one)
	}
	if merged != 3 {
		t.Fatalf("mergedVisible 应为 3（同仓簇 + 异作者 + 异显示名），实际 %d", merged)
	}
	if !out[0].Hidden || out[1].Hidden {
		t.Fatal("同仓库簇应只留一张（k1/k2 同宗，k2 更高版本留 k2、k1 隐藏）")
	}
	if out[2].Hidden {
		t.Fatal("不同作者不同仓库的卡不得并入同仓簇")
	}
	if out[3].Hidden {
		t.Fatal("显示名不同（同名不同应用）不得并入")
	}

	// 同版本同 sha 并簇（无仓库信息时的同构建证据）
	out2 := []AppInfo{
		{Key: "a", AppName: "s-app", DisplayName: "S", Source: "s1", LatestVersion: "1.0.0", Arch: arch, Sha256: "abc"},
		{Key: "b", AppName: "s-app", DisplayName: "S", Source: "s2", LatestVersion: "1.0.0", Arch: arch, Sha256: "abc"},
	}
	m2, _ := applyDedupPolicy(out2, "merge")
	if m2 != 1 {
		t.Fatalf("同版本同 sha 应并为一簇（mergedVisible=1），实际 %d", m2)
	}
	if !out2[0].Hidden && !out2[1].Hidden {
		t.Fatal("同构建簇应隐藏一张")
	}
}

// all 档：零隐藏，但同名组信息仍填充（候选表宿主 = 阶梯代表）。
func TestDedupPolicy_AllNoHide(t *testing.T) {
	arch := localArch()
	out := []AppInfo{
		mkDedupCard("k1", "z-app", "Z", "src-1", "1.0.0", "org/r", "", "", arch, false, nil),
		mkDedupCard("k2", "z-app", "Z", "src-2", "1.1.0", "org/r", "", "", arch, false, nil),
	}
	merged, one := applyDedupPolicy(out, "all")
	for i, a := range out {
		if a.Hidden {
			t.Fatalf("all 档不得隐藏任何卡（idx %d）", i)
		}
	}
	if merged != 1 || one != 1 {
		t.Fatalf("计数应不受策略影响（merged=1 one=1），实际 %d/%d", merged, one)
	}
	if out[1].SameNameCount != 2 {
		t.Fatalf("阶梯代表（k2 更高版本）应承载 SameNameCount=2，实际 %d", out[1].SameNameCount)
	}
	if len(out[1].SameName) != 2 || !out[1].SameName[1].IsRep {
		t.Fatal("SameName 应含全组且代表卡 IsRep=true")
	}
}

// 安装冲突分支（0.6.312 B2 详情页 install_conflict）。
func TestInstallConflict_Branches(t *testing.T) {
	arch := localArch()
	mk := func(key, source, ver, repo, sha string, installed bool) AppInfo {
		a := mkDedupCard(key, "c-app", "C", source, ver, repo, "", "", arch, installed, nil)
		a.Sha256 = sha
		return a
	}
	group := func(cards ...AppInfo) []SameNameEntry {
		out := make([]SameNameEntry, 0, len(cards))
		for i := range cards {
			out = append(out, SameNameEntry{
				Key: cards[i].Key, Source: cards[i].Source, Version: cards[i].LatestVersion,
				Installed: cards[i].Installed, Origin: releaseOrigin(cards[i].ReleaseURL), Sha256: cards[i].Sha256,
			})
		}
		return out
	}
	inst := mk("inst", "src-inst", "1.0.0", "org/repo", "shaA", true)

	// official：已装卡=官方平台应用（组 = 官方已装卡 + 本社区卡）
	gOff := []SameNameEntry{
		{Key: "o", Source: OfficialSourceID, Version: "1.0.0", Installed: true},
		{Key: "x", Source: "src-x", Version: "1.0.0"},
	}
	xOff := mk("x", "src-x", "1.0.0", "org/xx", "", false)
	if got := installConflictOf(&xOff, gOff); got != "official" {
		t.Fatalf("official 分支，实际 %q", got)
	}
	// same：同源同版本
	xSame := mk("x", "src-inst", "1.0.0", "org/repo", "shaB", false)
	if got := installConflictOf(&xSame, group(inst, xSame)); got != "same" {
		t.Fatalf("same 分支，实际 %q", got)
	}
	// lineage：同仓库（版本/sha 不同）
	xLine := mk("x", "src-x", "1.1.0", "org/repo", "shaC", false)
	if got := installConflictOf(&xLine, group(inst, xLine)); got != "lineage" {
		t.Fatalf("lineage（同仓库）分支，实际 %q", got)
	}
	// lineage：同版本同 sha（无仓库信息）
	g2 := []SameNameEntry{
		{Key: "inst", Source: "s1", Version: "1.0.0", Installed: true, Sha256: "same"},
		{Key: "x", Source: "s2", Version: "1.0.0", Sha256: "same"},
	}
	if got := installConflictOf(&AppInfo{Key: "x", AppName: "c-app", Source: "s2", LatestVersion: "1.0.0", Sha256: "same"}, g2); got != "lineage" {
		t.Fatalf("lineage（同版本同 sha）分支，实际 %q", got)
	}
	// different：不同源不同构建
	xDiff := mk("x", "src-x", "2.0.0", "org/other", "shaZ", false)
	if got := installConflictOf(&xDiff, group(inst, xDiff)); got != "different" {
		t.Fatalf("different 分支，实际 %q", got)
	}
	// 无冲突：本卡已装 / 组内无已装卡 / 单卡组
	xInst := mk("x", "src-inst", "1.0.0", "org/repo", "", true)
	if got := installConflictOf(&xInst, group(inst)); got != "" {
		t.Fatalf("本卡已装应无冲突，实际 %q", got)
	}
	xNoInst := mk("x", "src-x", "1.0.0", "org/x", "", false)
	if got := installConflictOf(&xNoInst, []SameNameEntry{{Key: "a", Source: "s1"}, {Key: "x", Source: "s2"}}); got != "" {
		t.Fatalf("组内无已装卡应无冲突，实际 %q", got)
	}
	_ = arch
}

// buildSameNameGroup：含隐藏卡的组也完整收集；IsRep=!Hidden。
func TestBuildSameNameGroup_IncludesHidden(t *testing.T) {
	arch := localArch()
	catalog := []AppInfo{
		mkDedupCard("k1", "g-app", "G", "s1", "1.0.0", "org/r", "", "", arch, false, nil),
		mkDedupCard("k2", "g-app", "G", "s2", "1.1.0", "org/r", "", "", arch, false, nil),
		mkDedupCard("k3", "other", "O", "s3", "1.0.0", "", "", "", arch, false, nil),
	}
	applyDedupPolicy(catalog, "one") // 组内只留一张，其余 Hidden
	entries := buildSameNameGroup(catalog, "k1")
	if len(entries) != 2 {
		t.Fatalf("组应含 2 卡（含隐藏卡），实际 %d", len(entries))
	}
	rep := 0
	for _, e := range entries {
		if e.IsRep {
			rep++
		}
	}
	if rep != 1 {
		t.Fatalf("恰一张 IsRep（当前策略下可见），实际 %d", rep)
	}
}

// 独立卡（组大小 1）：不填同名信息、永不隐藏。
func TestDedupPolicy_SingletonGroup(t *testing.T) {
	arch := localArch()
	out := []AppInfo{mkDedupCard("only", "solo", "Solo", "src", "1.0.0", "org/r", "", "", arch, false, nil)}
	applyDedupPolicy(out, "one")
	if out[0].Hidden || out[0].SameNameCount != 0 || len(out[0].SameName) != 0 {
		t.Fatal("单卡组不得隐藏、不得填同名信息")
	}
}
