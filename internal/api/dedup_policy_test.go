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

// 溯源级（0.6.575）：搬运卡即便版本更高，代表卡也必须是组内「非搬运」的真实
// 项目源；官方卡 / 已装卡 / 可装性仍排在溯源级之前。
// 反向验证：摘掉 dedupRepClass 里的 UpstreamRepo 加分 → 本用例 FAIL。
func TestDedupPolicy_LineageBeatsHigherVersion(t *testing.T) {
	arch := localArch()
	mk := func(key, src, ver, repo string) AppInfo {
		return mkDedupCard(key, "l-app", "L", src, ver, repo, "", "", arch, false, nil)
	}
	mover := mk("k-move", "src-move", "9.9.9", "carol/fndepot")
	mover.UpstreamRepo = "alice/awesome-apps"
	real := mk("k-real", "src-real", "1.0.0", "alice/awesome-apps")
	out := []AppInfo{mover, real}
	if got := applyDedupPolicy(out, "one"); got != 1 {
		t.Fatalf("oneVisible 应为 1，实际 %d", got)
	}
	if !out[0].Hidden || out[1].Hidden {
		t.Fatalf("去重档代表卡应为非搬运的真实项目源（版本更高也不行）：%+v", out)
	}

	// 官方目录卡优先于溯源级（阶梯 1 < 2）
	mv2 := mk("k-move2", "src-move", "9.9.9", "carol/fndepot")
	mv2.UpstreamRepo = "alice/awesome-apps"
	rl2 := mk("k-real2", "src-real", "1.0.0", "alice/awesome-apps")
	off := mkDedupCard("k-off", "l-app", "L", OfficialSourceID, "0.1.0", "", "", "", arch, false, nil)
	out2 := []AppInfo{mv2, rl2, off}
	applyDedupPolicy(out2, "one")
	if out2[2].Hidden {
		t.Fatalf("官方目录卡优先于溯源级：%+v", out2)
	}

	// 已装卡优先（已装规范卡永不隐藏，即便它是搬运卡）
	mv3 := mk("k-move3", "src-move", "9.9.9", "carol/fndepot")
	mv3.UpstreamRepo = "alice/awesome-apps"
	mv3.Installed = true
	rl3 := mk("k-real3", "src-real", "1.0.0", "alice/awesome-apps")
	out3 := []AppInfo{mv3, rl3}
	applyDedupPolicy(out3, "one")
	if out3[0].Hidden || !out3[1].Hidden {
		t.Fatalf("已装卡代表其安装来源、永不隐藏：%+v", out3)
	}

	// 可装性优先于溯源级：本机装不上的真实卡不得顶掉能装的搬运卡
	mv4 := mk("k-move4", "src-move", "1.0.0", "carol/fndepot")
	mv4.UpstreamRepo = "alice/awesome-apps"
	rl4 := mk("k-real4", "src-real", "9.9.9", "alice/awesome-apps")
	rl4.Archs = []string{"arm"}
	out4 := []AppInfo{mv4, rl4}
	applyDedupPolicy(out4, "one")
	if out4[0].Hidden || !out4[1].Hidden {
		t.Fatalf("可装性优先于溯源级：%+v", out4)
	}
}

// SourceIsFork 不得作为降级信号（整仓 fork 源里的作者二创会被误伤）。
func TestDedupPolicy_SourceIsForkNotDemoted(t *testing.T) {
	arch := localArch()
	forkCard := mkDedupCard("k-fork", "f-app", "F", "src-fork", "2.0.0", "carol/fndepot", "", "", arch, false, nil)
	forkCard.SourceIsFork = true
	plain := mkDedupCard("k-plain", "f-app", "F", "src-plain", "1.0.0", "carol/fndepot", "", "", arch, false, nil)
	out := []AppInfo{forkCard, plain}
	applyDedupPolicy(out, "one")
	if out[0].Hidden || !out[1].Hidden {
		t.Fatalf("SourceIsFork 不参与降级：版本更高的 fork 源卡仍应作代表：%+v", out)
	}
}

// 同名组条目带溯源标注：is_upstream / upstream_repo / source_is_fork。
func TestDedupPolicy_SameNameLineageFields(t *testing.T) {
	arch := localArch()
	mover := mkDedupCard("k-move", "s-app", "S", "src-move", "2.0.0", "carol/fndepot", "", "", arch, false, nil)
	mover.UpstreamRepo = "alice/awesome-apps"
	mover.SourceIsFork = true
	real := mkDedupCard("k-real", "s-app", "S", "src-real", "1.0.0", "alice/awesome-apps", "", "", arch, false, nil)
	out := []AppInfo{mover, real}
	applyDedupPolicy(out, "one")
	if len(out[1].SameName) != 2 {
		t.Fatalf("代表卡应承载两条同名条目：%+v", out[1].SameName)
	}
	var m, r SameNameEntry
	for _, e := range out[1].SameName {
		if e.Key == "k-move" {
			m = e
		}
		if e.Key == "k-real" {
			r = e
		}
	}
	if r.Origin != "alice/awesome-apps" || !r.IsUpstream {
		t.Fatalf("真实项目源应标 is_upstream：%+v", r)
	}
	if !m.SourceIsFork || m.UpstreamRepo != "alice/awesome-apps" || m.IsUpstream {
		t.Fatalf("搬运卡应带 fork/上游标注且不标 is_upstream：%+v", m)
	}
	if !r.IsRep || m.IsRep {
		t.Fatalf("IsRep 只应落在代表卡：%+v / %+v", r, m)
	}
}

// all 档：零隐藏，但同名组信息仍填充（候选表宿主 = 阶梯代表）。
func TestDedupPolicy_AllNoHide(t *testing.T) {
	arch := localArch()
	out := []AppInfo{
		mkDedupCard("k1", "z-app", "Z", "src-1", "1.0.0", "org/r", "", "", arch, false, nil),
		mkDedupCard("k2", "z-app", "Z", "src-2", "1.1.0", "org/r", "", "", arch, false, nil),
	}
	one := applyDedupPolicy(out, "all")
	for i, a := range out {
		if a.Hidden {
			t.Fatalf("all 档不得隐藏任何卡（idx %d）", i)
		}
	}
	if one != 1 {
		t.Fatalf("all 档计数 = 组数（1），实际 %d", one)
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

// 0.6.575：详情候选表（buildSameNameGroup）也必须带溯源标注——它与列表路径
// 是两套组装代码，漏一处就会出现「列表有标注、详情全 None」。
func TestBuildSameNameGroup_LineageFields(t *testing.T) {
	arch := localArch()
	mover := mkDedupCard("k-move", "d-app", "D", "src-move", "2.0.0", "carol/fndepot", "", "", arch, false, nil)
	mover.UpstreamRepo = "alice/awesome-apps"
	mover.SourceIsFork = true
	real := mkDedupCard("k-real", "d-app", "D", "src-real", "1.0.0", "alice/awesome-apps", "", "", arch, false, nil)
	catalog := []AppInfo{mover, real}
	applyDedupPolicy(catalog, "one")
	entries := buildSameNameGroup(catalog, "k-real")
	if len(entries) != 2 {
		t.Fatalf("组应含 2 卡，实际 %d", len(entries))
	}
	seen := map[string]SameNameEntry{}
	for _, e := range entries {
		seen[e.Key] = e
	}
	r := seen["k-real"]
	if !r.IsUpstream || !r.IsRep || r.SourceIsFork || r.UpstreamRepo != "" {
		t.Fatalf("真实项目源条目标注不对：%+v", r)
	}
	m := seen["k-move"]
	if !m.SourceIsFork || m.UpstreamRepo != "alice/awesome-apps" || m.IsUpstream || m.IsRep {
		t.Fatalf("搬运卡条目标注不对：%+v", m)
	}
}

// 溯源指向：搬运卡（display 不同）与真源卡必须并成一张（同应用证据 = 上游 = 对方仓库）。
func TestDedupPolicy_UpstreamLinkCollapses(t *testing.T) {
	arch := localArch()
	mover := mkDedupCard("k-move", "n-app", "N音乐播放器", "src-move", "0.64.2", "carol/fndepot", "", "", arch, false, nil)
	mover.UpstreamRepo = "alice/awesome-apps"
	real := mkDedupCard("k-real", "n-app", "N", "src-real", "0.60.0", "alice/awesome-apps", "", "", arch, false, nil)
	out := []AppInfo{mover, real}
	vis := applyDedupPolicy(out, "one")
	if vis != 1 || out[1].Hidden || !out[0].Hidden {
		t.Fatalf("上游指向的真源应留下、搬运卡隐藏：vis=%d %+v", vis, out)
	}
}

// 同上游的多个搬运（display 不同）互相并簇 → 只留一张。
func TestDedupPolicy_SharedUpstreamCollapses(t *testing.T) {
	arch := localArch()
	a := mkDedupCard("k-a", "p-app", "P 播放器", "src-a", "0.61.0", "carol/fndepot", "", "", arch, false, nil)
	a.UpstreamRepo = "alice/awesome-apps"
	b := mkDedupCard("k-b", "p-app", "P播放器", "src-b", "0.63.0", "dave/fndepot", "", "", arch, false, nil)
	b.UpstreamRepo = "alice/awesome-apps"
	out := []AppInfo{a, b}
	vis := applyDedupPolicy(out, "one")
	if vis != 1 || !out[0].Hidden || out[1].Hidden {
		t.Fatalf("同上游的多个搬运应并簇留高版本：vis=%d %+v", vis, out)
	}
}
