package api

// lineage_mirror_353_test.go —— 0.6.353 溯源误判修复（F1 守卫 / F2 自持优先 /
// F3 假地址兜底）单测。
//
// 场景来源：主 NAS 现网 `Plex@shuangji66` 的「上游」被显示成 moo-mate，
// upstream_url = https://github.com/moo/mate.json（不存在的地址）。

import (
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// F1：本地聚合源（moo-mate，http 源）不参与指纹配对 ——
// 真打包者的卡绝不能被反向标成「搬运上游」。
func TestMirror353_AggregatorLocalSourceExcluded(t *testing.T) {
	s := lkServer(t,
		[]config.SourceRef{
			{Name: "shuangji66", URL: "https://github.com/shuangji66/FnDepot"},
			{Name: "moo-mate", URL: "http://127.0.0.1:38108/moo/mate.json"},
		},
		map[string]map[string]*source.App{
			"shuangji66": srcCache(map[string]string{"Plex": "1.43.4-1"}),
			"moo-mate":   srcCache(map[string]string{"Plex": "1.43.4-1"}),
		}, nil)
	pkg := "https://github.com/shuangji66/FnDepot/releases/download/2026.10.10/Plex-1.43.4-1-x86.fpk"
	out := []AppInfo{
		mkFp("Plex@shuangji66", "Plex", "shuangji66", "1.43.4-1", pkg, "deadbeef", "shuangji66"),
		mkFp("Plex@moo-mate", "Plex", "moo-mate", "1.43.4-1", pkg, "deadbeef", ""),
	}
	out[1].Maintainer = "Plex" // 聚合源卡片：打包者字段缺省，maintainer 是原厂名
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" || out[0].UpstreamSource != "" {
		t.Fatalf("真打包者的卡不得被标搬运：repo=%q src=%q", out[0].UpstreamRepo, out[0].UpstreamSource)
	}
	if out[1].UpstreamRepo != "" {
		t.Fatalf("聚合源不参与指纹配对，不得有上游：%q", out[1].UpstreamRepo)
	}
}

// F2：两个都是 GitHub 源时，包地址自持者才是根 —— 不再靠仓库名字典序赌方向。
// 构造「字典序在前的聚合源」正是旧实现翻车的形状。
func TestMirror353_SelfHostedWinsOverLexical(t *testing.T) {
	s := lkServer(t,
		[]config.SourceRef{
			{Name: "agg", URL: "https://github.com/bluemink/aggregator"},
			{Name: "origin", URL: "https://github.com/shuangji66/FnDepot"},
		},
		map[string]map[string]*source.App{
			"agg":    srcCache(map[string]string{"Plex": "1.43.4-1"}),
			"origin": srcCache(map[string]string{"Plex": "1.43.4-1"}),
		}, nil)
	own := "https://github.com/shuangji66/FnDepot/releases/download/t/Plex.fpk"
	out := []AppInfo{
		mkFp("Plex@agg", "Plex", "agg", "1.43.4-1", own, "deadbeef", ""),
		mkFp("Plex@origin", "Plex", "origin", "1.43.4-1", own, "deadbeef", "shuangji66"),
	}
	out[0].Maintainer = "Plex"
	s.applyMirrorFingerprint(out)
	if out[1].UpstreamRepo != "" {
		t.Fatalf("包地址自持的本源不得被标搬运：%q", out[1].UpstreamRepo)
	}
	if out[0].UpstreamRepo != "shuangji66/fndepot" || out[0].UpstreamSource != "origin" {
		t.Fatalf("聚合源应溯源到真源：repo=%q src=%q", out[0].UpstreamRepo, out[0].UpstreamSource)
	}
	if out[0].UpstreamURL != "https://github.com/shuangji66/fndepot" {
		t.Fatalf("上游地址拼错：%q", out[0].UpstreamURL)
	}
}

// F3：根 key 是索引文件名形态（moo/mate.json）时不得拼 GitHub 地址下发。
func TestMirror353_IndexFileKeyRejected(t *testing.T) {
	s := lkServer(t,
		[]config.SourceRef{
			{Name: "idx", URL: "https://github.com/moo/mate.json"},
			{Name: "other", URL: "https://github.com/carol/apps"},
		},
		map[string]map[string]*source.App{
			"idx":   srcCache(map[string]string{"Plex": "1.43.4-1"}),
			"other": srcCache(map[string]string{"Plex": "1.43.4-1"}),
		}, nil)
	own := "https://github.com/moo/mate.json/releases/download/t/Plex.fpk"
	out := []AppInfo{
		mkFp("Plex@idx", "Plex", "idx", "1.43.4-1", own, "deadbeef", ""),
		mkFp("Plex@other", "Plex", "other", "1.43.4-1", "", "deadbeef", ""),
	}
	s.applyMirrorFingerprint(out)
	if out[1].UpstreamRepo != "" || out[1].UpstreamURL != "" {
		t.Fatalf("伪 owner/repo 不得下发给前端：repo=%q url=%q", out[1].UpstreamRepo, out[1].UpstreamURL)
	}
}

// 回归：全组都拿不到自持证据时仍按确定性排序兜底（0.6.351 能力不回归）。
func TestMirror353_NoSelfHostedFallsBack(t *testing.T) {
	s := lkServer(t,
		[]config.SourceRef{
			{Name: "A", URL: "https://github.com/alice/awesome-apps"},
			{Name: "B", URL: "https://github.com/bob/mirror"},
		},
		map[string]map[string]*source.App{
			"A": srcCache(map[string]string{"app-a": "2.0.0"}),
			"B": srcCache(map[string]string{"app-a": "2.0.0"}),
		}, nil)
	out := []AppInfo{
		mkFp("app-a@A", "app-a", "A", "2.0.0", "", "cafebabe", ""),
		mkFp("app-a@B", "app-a", "B", "2.0.0", "", "cafebabe", ""),
	}
	s.applyMirrorFingerprint(out)
	if out[0].UpstreamRepo != "" {
		t.Fatalf("字典序最小的成员应保持为根，不该被溯源：%q", out[0].UpstreamRepo)
	}
	if out[1].UpstreamRepo != "alice/awesome-apps" {
		t.Fatalf("兜底溯源失效：%q", out[1].UpstreamRepo)
	}
}

func TestValidOwnerRepo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"shuangji66/fndepot", true},
		{"moo/mate.json", false},
		{"mate.json", false},
		{"a/b/c", false},
		{"", false},
		{"/x", false},
		{"x/", false},
		{"foo/bar.yaml", false},
		{"foo/bar.txt", false},
	}
	for _, c := range cases {
		if got := validOwnerRepo(c.in); got != c.want {
			t.Fatalf("validOwnerRepo(%q)=%v want %v", c.in, got, c.want)
		}
	}
}
