package api

// D3 大小写同名变体（0.6.579「只提示不合并」）守卫用例。
//
// 设计边界（改本文件前先读 _audit_0578/BATCH3_D3_REPORT 口径）：
//   - 分组键 = strings.EqualFold(appname)，但**只产出展示信息**；
//   - Hidden 判定 / 去重档计数（/api/dedup 的 one）/ 列表卡数量一律不变；
//   - same_name 的语义完全不动（仍为字面完全相同组）；
//   - same_name_ci 与 same_name **互补**（前者只装字面不同的卡）⇒ 前端合并
//     渲染不会出现重复行。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sameNameCIFixture D3 真实形态：1Panel/1panel（双字面）、openlist 三字面、
// 单卡无变体（solo）。每张卡各自是独立字面组（真实 52 组绝大多数如此）。
func sameNameCIFixture() []AppInfo {
	arch := localArch()
	mover := mkDedupCard("1panel@ZJB518", "1panel", "1Panel", "ZJB518", "2.0.14", "hxido-rxm/fndepot", "", "", arch, false, nil)
	mover.UpstreamRepo = "org/1panel-upstream" // 搬运卡：上游 = 真实项目仓
	mover.SourceIsFork = true
	return []AppInfo{
		mkDedupCard("1Panel@fnos-official", "1Panel", "1Panel", OfficialSourceID, "1.0.12", "", "", "", arch, false, nil),
		mover,
		mkDedupCard("OpenList@fnos-official", "OpenList", "OpenList", OfficialSourceID, "4.2.9", "", "", "", arch, false, nil),
		mkDedupCard("Openlist@shuangji66", "Openlist", "OpenList", "shuangji66", "4.2.10", "x/y", "", "", arch, false, nil),
		mkDedupCard("openlist@fnos-store", "openlist", "OpenList", "fnos-store", "4.2.10", "", "", "", arch, false, nil),
		mkDedupCard("solo@src", "solo", "Solo", "src", "1.0.0", "", "", "", arch, false, nil),
	}
}

// ① 分组键 = EqualFold：1Panel/1panel 互认，openlist 三字面互认。
func TestSameNameCI_EqualFoldGrouping(t *testing.T) {
	out := sameNameCIFixture()
	applyDedupPolicy(out, "one")
	want := map[string]int{
		"1Panel@fnos-official":   1, // 另有 1panel@ZJB518
		"1panel@ZJB518":          1,
		"OpenList@fnos-official": 2, // 另有 Openlist / openlist 两张
		"Openlist@shuangji66":    2,
		"openlist@fnos-store":    2,
		"solo@src":               0, // 无变体 ⇒ 不下发
	}
	for _, a := range out {
		if got := a.SameNameCICount; got != want[a.Key] {
			t.Fatalf("%s SameNameCICount = %d, want %d", a.Key, got, want[a.Key])
		}
	}
}

// ② 无变体不下发：omitempty 生效（旧前端/旧后端双向静默）。
func TestSameNameCI_OmittedWhenAbsent(t *testing.T) {
	out := sameNameCIFixture()
	applyDedupPolicy(out, "one")
	raw, err := json.Marshal(out[5]) // solo@src
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, "same_name_ci") || strings.Contains(s, "case_variant") {
		t.Fatalf("无变体卡不得下发 new 字段：%s", s)
	}
	// 有变体卡必须下发计数
	raw2, _ := json.Marshal(out[0])
	if !strings.Contains(string(raw2), `"same_name_ci_count":1`) {
		t.Fatalf("有变体卡应下发 same_name_ci_count：%s", raw2)
	}
}

// ③ 去重计数守卫：变体卡既不计入可见数，也不会被隐藏。
// 口径 = 「可见卡数 = 字面组数」（现状：每个字面各出一张），与变体是否存在无关。
func TestSameNameCI_DedupCountsUnchanged(t *testing.T) {
	out := sameNameCIFixture()
	one := applyDedupPolicy(out, "one")
	if one != 6 {
		t.Fatalf("可见计数应为 6（6 个字面组），实际 %d —— 变体不得合并", one)
	}
	for _, a := range out {
		if a.Hidden {
			t.Fatalf("单卡字面组不得隐藏：%s", a.Key)
		}
		if a.SameNameCount != 0 || len(a.SameName) != 0 {
			t.Fatalf("相同字面组只有 1 张卡时不得填 same_name：%s", a.Key)
		}
	}
	// 去掉变体（1panel@ZJB518）后可见数只减 1，剩余卡 Hidden 与 SameNameCount 一字不变。
	trimmed := sameNameCIFixture()[:1]
	trimmed = append(trimmed, sameNameCIFixture()[2:]...)
	if got := applyDedupPolicy(trimmed, "one"); got != 5 {
		t.Fatalf("去掉变体后可见计数应为 5，实际 %d", got)
	}
	for _, a := range trimmed {
		if a.Hidden {
			t.Fatalf("去掉变体后不得出现隐藏卡：%s", a.Key)
		}
		// 1Panel 的唯一变体已被移除 ⇒ 计数归零（openlist 三字面仍在，不受影响）
		if a.Key == "1Panel@fnos-official" && a.SameNameCICount != 0 {
			t.Fatalf("移除变体后 1Panel 的计数应归零，实际 %d", a.SameNameCICount)
		}
	}
}

// ④ 既有 same_name 语义不变：仍是「字面完全相同」组，不把大小写变体并进来。
func TestSameNameCI_ExactGroupUnchanged(t *testing.T) {
	arch := localArch()
	catalog := sameNameCIFixture()
	// 追加一张与 1panel 字面完全相同的卡 → 精确同名组回到 2，变体仍为 1。
	catalog = append(catalog, mkDedupCard("1panel@other", "1panel", "1Panel", "other", "2.1.0", "o/r", "", "", arch, false, nil))
	applyDedupPolicy(catalog, "one")
	var rep *AppInfo
	for i := range catalog {
		if catalog[i].Key == "1panel@other" {
			rep = &catalog[i]
		}
	}
	if rep.SameNameCount != 2 {
		t.Fatalf("1panel 字面组应为 2 张（不含 1Panel 变体），实际 %d", rep.SameNameCount)
	}
	for _, e := range rep.SameName {
		if e.CaseVariant {
			t.Fatalf("same_name 条目不得带 case_variant：%+v", e)
		}
	}
	if rep.SameNameCICount != 1 {
		t.Fatalf("1panel 的变体计数仍应为 1（1Panel 那张），实际 %d", rep.SameNameCICount)
	}
}

// ⑤ 详情候选表：CaseVariant 标记 + 与 same_name 互补（并集 = 全组，无重复）。
func TestSameNameCI_DetailGroupComplement(t *testing.T) {
	catalog := sameNameCIFixture()
	applyDedupPolicy(catalog, "one")
	exact := buildSameNameGroup(catalog, "1Panel@fnos-official")
	ci := buildSameNameCIGroup(catalog, "1Panel@fnos-official", exact)
	if len(ci) != 1 || ci[0].Key != "1panel@ZJB518" {
		t.Fatalf("1Panel 的变体组应只有 1panel@ZJB518：%+v", ci)
	}
	if !ci[0].CaseVariant || !ci[0].IsRep {
		t.Fatalf("变体条目应 case_variant=true 且 IsRep=!Hidden：%+v", ci[0])
	}
	if ci[0].Version != "2.0.14" || ci[0].Source != "ZJB518" || ci[0].DisplayName != "1Panel" {
		t.Fatalf("变体条目基础字段不对：%+v", ci[0])
	}
	// openlist 三字面：任一卡看到的变体 = 另外两个字面
	oex := buildSameNameGroup(catalog, "OpenList@fnos-official")
	oci := buildSameNameCIGroup(catalog, "OpenList@fnos-official", oex)
	if len(oci) != 2 {
		t.Fatalf("OpenList 应有 2 个变体，实际 %d：%+v", len(oci), oci)
	}
	if !oci[0].CaseVariant || !oci[1].CaseVariant {
		t.Fatalf("变体条目必须全部 case_variant=true：%+v", oci)
	}
	// 互补：same_name 里的 key 不得出现在 same_name_ci
	for _, a := range exact {
		for _, b := range ci {
			if a.Key == b.Key {
				t.Fatalf("same_name 与 same_name_ci 出现重复 key %s", a.Key)
			}
		}
	}
	// 无变体卡：nil（前端静默）
	if got := buildSameNameCIGroup(catalog, "solo@src", nil); got != nil {
		t.Fatalf("solo 无变体，应为 nil：%+v", got)
	}
}

// ⑥ D2 三反例（代表卡 = 已装搬运卡 + 同组真实项目卡）的标注正确。
// 前端「原项目源：<source> v<ver>」提示的判据全在这里：代表卡 installed 且
// upstream_repo!=""，同组存在 upstream_repo=="" 且未装的真实卡。
func TestSameNameCI_D2InstalledMoverGroups(t *testing.T) {
	arch := localArch()
	type tc struct {
		name       string
		repKey     string
		repVer     string
		repUp      string
		repFork    bool
		realKey    string
		realVer    string
		realRepo   string
		wantUpstrm bool // 真实卡的 is_upstream（本卡发布仓库 == 代表卡声明的上游）
	}
	cases := []tc{
		{"1panel", "1panel@ZJB518", "2.0.14", "hxido-rxm/fndepot", true, "1panel@fnos-store", "1.10.34-lts", "1panel-dev/1Panel", false},
		{"fnmedia-ext", "fnmedia-ext@latte03", "1.4.1-beta.1", "yujian0815/fnmedia-ext", true, "fnmedia-ext@yujian0815", "1.4.0", "yujian0815/fnmedia-ext", true},
		{"java-25-openj9", "java-25-openj9@33205q", "25.0.3", "shuangji66/fndepot", false, "java-25-openj9@shuangji66", "25.0.3", "shuangji66/fndepot", true},
	}
	for _, c := range cases {
		rep := mkDedupCard(c.repKey, c.name, c.name, "mover-src", c.repVer, "mover/fndepot", "", "", arch, true, nil)
		rep.UpstreamRepo = c.repUp
		rep.SourceIsFork = c.repFork
		real := mkDedupCard(c.realKey, c.name, c.name, "real-src", c.realVer, c.realRepo, "", "", arch, false, nil)
		out := []AppInfo{rep, real}
		applyDedupPolicy(out, "one")
		// 已装卡 class 0 ⇒ 代表卡恒为已装搬运卡，真实卡被隐藏（现状，本批不改）
		if out[0].Hidden || !out[1].Hidden {
			t.Fatalf("%s: 现状应为「已装搬运卡可见、真实卡隐藏」，实际 %v/%v", c.name, out[0].Hidden, out[1].Hidden)
		}
		entries := buildSameNameGroup(out, c.repKey)
		if len(entries) != 2 {
			t.Fatalf("%s: 候选表应含 2 条，实际 %d", c.name, len(entries))
		}
		var repE, realE SameNameEntry
		for _, e := range entries {
			switch e.Key {
			case c.repKey:
				repE = e
			case c.realKey:
				realE = e
			}
		}
		if !repE.Installed || repE.UpstreamRepo != c.repUp || repE.SourceIsFork != c.repFork || !repE.IsRep {
			t.Fatalf("%s: 代表卡条目标注不对：%+v", c.name, repE)
		}
		if realE.Installed || realE.UpstreamRepo != "" || realE.IsRep {
			t.Fatalf("%s: 真实卡条目应为「未装 / 非搬运 / 不可见」，实际 %+v", c.name, realE)
		}
		if realE.IsUpstream != c.wantUpstrm {
			t.Fatalf("%s: 真实卡 is_upstream = %v, want %v", c.name, realE.IsUpstream, c.wantUpstrm)
		}
		// 前端提示判据（Go 侧等价断言）
		hasHint := repE.Installed && repE.UpstreamRepo != "" && !realE.Installed && realE.UpstreamRepo == ""
		if !hasHint {
			t.Fatalf("%s: 应可出「原项目源」提示", c.name)
		}
	}
}

// ⑦ 生产夹具计数校验（数字来自 _audit_0578/BATCH3_D3_AUDIT.md，快照 2026-11-27 18:23）。
//   - 分组只用 appname（真实实现同样只用 AppName）⇒ 不依赖 archs/upstream 等字段，
//     这正是本批「只读展示」的红利；
//   - 未设 MOO_D3_FIXTURES 时 Skip：CI 不依赖工作区外的审计数据。
//
// 跑法：sh _audit_0578/verify_d3_fixture.sh
func TestSameNameCI_ProdFixtureCounts(t *testing.T) {
	dir := os.Getenv("MOO_D3_FIXTURES")
	if dir == "" {
		t.Skip("MOO_D3_FIXTURES 未设置（指向 _audit_0578/data，含 full_apps.json/visible_apps.json）")
	}
	type card struct {
		Key     string `json:"key"`
		AppName string `json:"appname"`
	}
	readCards := func(name string) []card {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读夹具 %s 失败：%v", name, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("解析夹具 %s 失败：%v", name, err)
		}
		var out []card
		switch x := v.(type) {
		case []any:
			for _, it := range x {
				b, _ := json.Marshal(it)
				var c card
				_ = json.Unmarshal(b, &c)
				out = append(out, c)
			}
		case map[string]any:
			arr, _ := x["apps"].([]any)
			for _, it := range arr {
				b, _ := json.Marshal(it)
				var c card
				_ = json.Unmarshal(b, &c)
				out = append(out, c)
			}
		}
		return out
	}

	full := readCards("full_apps.json")
	visible := readCards("visible_apps.json")
	if len(full) != 2776 || len(visible) != 1334 {
		t.Fatalf("夹具规模变了（full=%d visible=%d，期望 2776/1334）——请重拍快照并更新期望值",
			len(full), len(visible))
	}
	out := make([]AppInfo, len(full))
	for i, c := range full {
		out[i] = AppInfo{Key: c.Key, AppName: c.AppName}
	}
	applySameNameCaseVariants(out)

	ciCards := 0
	ciKeys := map[string]bool{}
	for _, a := range out {
		if a.SameNameCICount > 0 {
			ciCards++
			ciKeys[sameNameCIKey(a.AppName)] = true
		}
	}
	if len(ciKeys) != 52 {
		t.Fatalf("大小写同名组应为 52，实际 %d", len(ciKeys))
	}
	if ciCards != 491 {
		t.Fatalf("组内卡片应为 491，实际 %d", ciCards)
	}
	visHit := 0
	for _, c := range visible {
		if ciKeys[sameNameCIKey(c.AppName)] {
			visHit++
		}
	}
	if visHit != 105 {
		t.Fatalf("当前同时可见的变体卡应为 105，实际 %d", visHit)
	}
}
