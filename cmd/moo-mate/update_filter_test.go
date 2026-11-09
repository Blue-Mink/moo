package main

import "testing"

// 本轮修复：mate 的 release 筛选必须跳过 draft / prerelease / 非版本 tag。
// 背景：Moo 与 Moo Mate 的 release 同放 Blue-Mink/moo 一个仓，mate 探测复用 Moo 的
// token（有写权限），GitHub 的 GET /releases 会把 draft 一并返回。若发布流程改走
// 「先 draft/prerelease 传资产、再转正」，旧逻辑会把没传完资产的半成品推给用户。

func TestLatestMateReleaseSkipsDraftAndPrerelease(t *testing.T) {
	rels := []mateGhRelease{
		{TagName: "v0.6.539"},                       // Moo 主线，必须忽略
		{TagName: "mate-v0.2.55"},                   // 当前正式版
		{TagName: "mate-v0.2.99", Draft: true},      // 草稿，必须忽略
		{TagName: "mate-v0.2.98", Prerelease: true}, // 预发布，必须忽略
		{TagName: "mate-vnext"},                     // 非版本 tag，必须忽略
		{TagName: "mate-v0.2.54"},                   // 旧版
	}
	best, ver := latestMateRelease(rels)
	if best == nil {
		t.Fatal("应选中 mate-v0.2.55，实际返回 nil")
	}
	if ver != "0.2.55" {
		t.Fatalf("应选中 0.2.55，实际 %q（draft/prerelease 未被过滤？）", ver)
	}
}

func TestLatestMateReleaseEmpty(t *testing.T) {
	if best, ver := latestMateRelease(nil); best != nil || ver != "" {
		t.Fatalf("空列表应返回 nil/空，实际 %v/%q", best, ver)
	}
	onlyDraft := []mateGhRelease{{TagName: "mate-v0.2.99", Draft: true}}
	if best, _ := latestMateRelease(onlyDraft); best != nil {
		t.Fatal("只有草稿时应返回 nil")
	}
}

// 去掉过滤后本用例必红（反向验证用）：把 Draft/Prerelease 两行 continue 删掉即可复现。
func TestPickMateReleaseIgnoresUnpublished(t *testing.T) {
	rels := []mateGhRelease{
		{TagName: "mate-v0.2.97", Prerelease: true, Assets: []mateReleaseAsset{{Name: "moo-mate_0.2.97_x86.fpk", URL: "u97"}}},
		{TagName: "mate-v0.2.96", Draft: true, Assets: []mateReleaseAsset{{Name: "moo-mate_0.2.96_x86.fpk", URL: "u96"}}},
		{TagName: "mate-v0.2.95", Body: "note95", HTMLURL: "https://h/95", Assets: []mateReleaseAsset{{Name: "moo-mate_0.2.95_x86.fpk", URL: "u95"}}},
	}
	ver, notes, page, dl, ok := pickMateRelease(rels)
	if !ok {
		t.Fatal("应选中已发布的 0.2.95，实际 ok=false")
	}
	if ver != "0.2.95" || notes != "note95" || page != "https://h/95" || dl != "u95" {
		t.Fatalf("选错条目：ver=%q notes=%q page=%q dl=%q", ver, notes, page, dl)
	}
}
