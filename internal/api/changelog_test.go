package api

import "testing"

// 0.6.311r：内嵌发布说明快照必须可解析且自洽（版本与文本非空）；
// 版本不匹配时返回空串（卡片回落源条目，不能拿错版本的日志顶替）。
func TestChangelogCurrentSnapshot(t *testing.T) {
	if changelogCurrentRaw == nil {
		t.Fatal("changelog_current.json 未嵌入")
	}
	if v := changelogCurrentFor("9.9.9"); v != "" {
		t.Fatalf("版本不匹配应返回空串，实际 %q", v)
	}
	changelogOnce.Do(func() {}) // 确保已解析
	if changelogParsed.Version == "" {
		t.Fatal("内嵌快照版本号为空")
	}
	if changelogParsed.Changelog == "" {
		t.Fatal("内嵌快照文本为空（build.sh 未同步 moo.json 或版本不匹配）")
	}
	if got := changelogCurrentFor(changelogParsed.Version); got != changelogParsed.Changelog {
		t.Fatalf("匹配版本应返回内嵌文本，实际 %q", got)
	}
}
