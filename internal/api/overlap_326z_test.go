package api

import "testing"

// 0.6.326z 源重合率单测（口径冻结见 OVERLAP_326Z_PLAN.md §6）。
// 反向验证：把分母 max 改成 min、去掉官方源排除、去掉最小证据门槛
// ⇒ 用例 3 / 4 / 8 必须 FAIL（证据链脚本 revcheck_326z2.py）。

// ovCard 造一张参与重合率计算的最小卡（Source/AppName/LatestVersion 是唯三输入）。
func ovCard(sname, name, ver, src string) AppInfo {
	return AppInfo{Key: name + "@" + src, AppName: name, LatestVersion: ver, Source: src}
}

func ovCards(src string, pairs [][2]string) []AppInfo {
	out := make([]AppInfo, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, ovCard(src, p[0], p[1], src))
	}
	return out
}

// 用例 1：两源完全相同 → name=exact=1.0、risk=high、unique_names=0。
func TestOverlapIdenticalSources(t *testing.T) {
	cards := append(
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}, {"c", "1.0"}}),
		ovCards("srcB", [][2]string{{"a", "1.0"}, {"b", "1.0"}, {"c", "1.0"}})...,
	)
	ov := computeSourceOverlap(cards)
	a, ok := ov["srcA"]
	if !ok {
		t.Fatal("srcA 应有信号")
	}
	if a.With != "srcB" || a.NameCount != 3 || a.ExactCount != 3 {
		t.Fatalf("with/counts 错：%+v", a)
	}
	if a.NameRatio != 1.0 || a.ExactRatio != 1.0 || a.Risk != "high" || a.UniqueNames != 0 || a.SelfCount != 3 {
		t.Fatalf("比值/档位/独有量错：%+v", a)
	}
}

// 用例 2：完全不相干 → risk=none、overlap_with 空。
func TestOverlapUnrelatedSources(t *testing.T) {
	cards := append(
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}}),
		ovCards("srcC", [][2]string{{"x", "2.0"}, {"y", "2.0"}})...,
	)
	ov := computeSourceOverlap(cards)
	a := ov["srcA"]
	if a.Risk != "none" || a.With != "" || a.NameRatio != 0 || a.ExactRatio != 0 {
		t.Fatalf("不相干源不应有重合：%+v", a)
	}
	if a.SelfCount != 2 {
		t.Fatalf("self_count 应恒有：%+v", a)
	}
}

// 用例 3（反向验证点 A）：分母取 max —— |A|=2、|B|=4、交集 1 ⇒ 1/4=0.25。
// 若误用 min 口径 ⇒ 1/2=0.5，本断言必须挂。
// 注意：0.6.326z 门槛修正后交集=1 不足以给档位 ⇒ risk 必须为 none（见用例 8），
// 故此处用 NameRatio 而不是 Risk 作为 max/min 口径的判据。
func TestOverlapDenominatorIsMax(t *testing.T) {
	cards := append(
		ovCards("srcSmall", [][2]string{{"a", "1.0"}, {"b", "1.0"}}),
		ovCards("srcBig", [][2]string{{"a", "1.0"}, {"c", "1.0"}, {"d", "1.0"}, {"e", "1.0"}})...,
	)
	ov := computeSourceOverlap(cards)
	s := ov["srcSmall"]
	if s.NameCount != 1 || s.SelfCount != 2 {
		t.Fatalf("计数错：%+v", s)
	}
	if s.NameRatio != 0.25 {
		t.Fatalf("分母必须取 max(2,4)=4 ⇒ 0.25，实得 %v（min 口径会得 0.5）", s.NameRatio)
	}
	if s.Risk != "none" {
		t.Fatalf("交集 1 < 最小证据量 ⇒ 档位必须回落 none，实得 %q", s.Risk)
	}
	// 对方视角：|Big|=4、交集 1 ⇒ 同为 0.25
	if b := ov["srcBig"]; b.NameRatio != 0.25 || b.SelfCount != 4 {
		t.Fatalf("大源视角错：%+v", b)
	}
}

// 用例 4（反向验证点 B）：官方源不参与 —— 不作为被评估源，也不计入对方分母。
// 若官方源参与：srcA 的 partner 会变成 fnos-official（2/max(2,5)=0.4=low），
// 本断言（risk=none / with 空）必须挂。
func TestOverlapExcludesOfficialSource(t *testing.T) {
	cards := append(
		ovCards(OfficialSourceID, [][2]string{{"a", "1.0"}, {"b", "1.0"}, {"c", "1.0"}, {"d", "1.0"}, {"e", "1.0"}}),
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}})...,
	)
	cards = append(cards, ovCards("srcC", [][2]string{{"x", "2.0"}, {"y", "2.0"}})...)
	ov := computeSourceOverlap(cards)
	if _, ok := ov[OfficialSourceID]; ok {
		t.Fatalf("官方源不应出现在信号表里：%+v", ov[OfficialSourceID])
	}
	a := ov["srcA"]
	if a.With != "" || a.Risk != "none" {
		t.Fatalf("官方源不得作为 partner：%+v", a)
	}
	for name, s := range ov {
		if s.With == OfficialSourceID {
			t.Fatalf("源 %s 的 overlap_with 指向了官方源", name)
		}
	}
}

// 用例 5：同名不同版 —— appname 全同、版本全不同 ⇒ 名称重合率高 / 精确重合率为 0。
func TestOverlapSameNamesDifferentVersions(t *testing.T) {
	cards := append(
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}}),
		ovCards("srcB", [][2]string{{"a", "2.0"}, {"b", "2.0"}})...,
	)
	ov := computeSourceOverlap(cards)
	a := ov["srcA"]
	if a.NameCount != 2 || a.NameRatio != 1.0 {
		t.Fatalf("名称重合应为 2/1.0：%+v", a)
	}
	if a.ExactCount != 0 || a.ExactRatio != 0 {
		t.Fatalf("版本全不同 ⇒ 精确重合必须为 0：%+v", a)
	}
	if a.Risk != "high" {
		t.Fatalf("档位取 max(name,exact) ⇒ 名称档 high，实得 %q", a.Risk)
	}
}

// 用例 6：平票确定性 —— 两方比值相同 ⇒ overlap_with 取源名字典序小者，
// 且与 map 迭代顺序无关（严格全序：分数降序 → 源名升序）。
func TestOverlapTieBreakLexicographic(t *testing.T) {
	cards := append(
		ovCards("srcA", [][2]string{{"p", "1.0"}, {"q", "1.0"}}),
		ovCards("srcBBB", [][2]string{{"p", "1.0"}, {"q", "1.0"}})...,
	)
	cards = append(cards, ovCards("srcAAA", [][2]string{{"p", "1.0"}, {"q", "1.0"}})...)
	ov := computeSourceOverlap(cards)
	if got := ov["srcA"].With; got != "srcAAA" {
		t.Fatalf("平票应取字典序小者 srcAAA，实得 %q", got)
	}
	// 反向：字典序互换后结论必须跟着换（证明比较的是名字而不是迭代序）
	if got := ov["srcAAA"].With; got != "srcA" {
		t.Fatalf("srcAAA 的 partner 应为 srcA（另一端 srcBBB 更大），实得 %q", got)
	}
}

// 用例 7：独有量 —— |A|=5、|B|=3、交集 2 ⇒ unique_names=3（「停掉 A 会丢多少」）。
func TestOverlapUniqueNames(t *testing.T) {
	cards := append(
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}, {"c", "1.0"}, {"d", "1.0"}, {"e", "1.0"}}),
		ovCards("srcB", [][2]string{{"a", "1.0"}, {"b", "1.0"}, {"z", "1.0"}})...,
	)
	ov := computeSourceOverlap(cards)
	a := ov["srcA"]
	if a.NameCount != 2 || a.SelfCount != 5 || a.UniqueNames != 3 {
		t.Fatalf("独有量应为 5-2=3：%+v", a)
	}
	if a.NameRatio != 0.4 || a.Risk != "low" {
		t.Fatalf("2/max(5,3)=0.4=low：%+v", a)
	}
	if b := ov["srcB"]; b.UniqueNames != 1 || b.SelfCount != 3 || b.NameRatio != 0.4 {
		t.Fatalf("小源视角：3-2=1：%+v", b)
	}
}

// 用例 8（反向验证点 C，0.6.326z 门槛修正）：只共享 1 个 appname 不构成重合信号。
// 两源各只有 1 个应用且同名（比值 100%）⇒ 档位必须回落 none，但 with/ratio/count
// 仍照常填（证据不足只压档位、不丢数据）；交集达到门槛（2）⇒ 档位必须给回。
// 去掉门槛（恢复无条件给档位）本用例必然失败。
func TestOverlapMinEvidenceGate(t *testing.T) {
	tiny := computeSourceOverlap(append(
		ovCards("srcA", [][2]string{{"a", "1.0"}}),
		ovCards("srcB", [][2]string{{"a", "1.0"}})...,
	))
	a := tiny["srcA"]
	if a.NameCount != 1 || a.NameRatio != 1.0 || a.With != "srcB" {
		t.Fatalf("原始数值必须保留（不丢数据）：%+v", a)
	}
	if a.Risk != "none" {
		t.Fatalf("交集 1 < %d ⇒ 档位必须 none，实得 %q", overlapMinEvidence, a.Risk)
	}
	if b := tiny["srcB"]; b.Risk != "none" || b.NameRatio != 1.0 {
		t.Fatalf("对方视角同样回落 none：%+v", b)
	}

	// 同名同版本各 1 个（name=exact=1）同样吃门槛，但精确计数照常保留
	exactTiny := computeSourceOverlap(append(
		ovCards("srcC", [][2]string{{"z", "9.9"}}),
		ovCards("srcD", [][2]string{{"z", "9.9"}})...,
	))
	if c := exactTiny["srcC"]; c.ExactCount != 1 || c.ExactRatio != 1.0 || c.Risk != "none" {
		t.Fatalf("精确交集 1 同样不足以给档位：%+v", c)
	}

	// 交集恰好 = 门槛 ⇒ 档位给回（2/2=1.0=high）
	pair := computeSourceOverlap(append(
		ovCards("srcA", [][2]string{{"a", "1.0"}, {"b", "1.0"}}),
		ovCards("srcB", [][2]string{{"a", "1.0"}, {"b", "1.0"}})...,
	))
	if p := pair["srcA"]; p.NameCount != 2 || p.Risk != "high" {
		t.Fatalf("交集 2 = 门槛 ⇒ 应给 high：%+v", p)
	}

	// 名称交集达标但精确交集不足 ⇒ 仍给档位（档位取 max(name,exact)）
	mixed := computeSourceOverlap(append(
		ovCards("srcE", [][2]string{{"p", "1.0"}, {"q", "1.0"}, {"r", "1.0"}}),
		ovCards("srcF", [][2]string{{"p", "1.0"}, {"q", "2.0"}, {"s", "1.0"}})...,
	))
	if e := mixed["srcE"]; e.NameCount != 2 || e.ExactCount != 1 || e.Risk != "medium" {
		t.Fatalf("2/max(3,3)=0.667=medium：%+v", e)
	}
}
