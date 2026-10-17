package api

// lineage_addhint_test.go —— 0.6.326e「添加为源」可加性预判（G3）单测。
//
// 覆盖五种落点：本仓有索引（按钮指上游仓）／本仓无索引但 owner 有索引仓且
// 未收录（按钮改指索引仓）／同前但索引仓已收录（不出按钮、给一行说明）／
// 两处都无索引（不出按钮、给原因）／尚未探测（维持现状，什么都不设）。

import (
	"strings"
	"testing"

	"moo/internal/source"
)

func TestApplyUpstreamAddHint(t *testing.T) {
	s := newLineageServer(t)
	s.AddProbes = source.NewAddProbe("")

	cases := []struct {
		name       string
		upstream   string   // 卡片的 upstream_repo
		probeSelf  string   // 探测器：本仓命中的索引名
		probeOwner string   // 探测器：owner 索引仓
		known      bool     // 是否注入探测结果
		wantURL    string   // 期望 UpstreamAddURL
		wantNoteIn []string // 期望 UpstreamAddNote 含这些子串
		// 0.6.326i（P2）：owner 索引仓已收录时，除说明文字外还要设 UpstreamSource。
		wantSrc string // 期望 UpstreamSource（空=必须保持空）
	}{
		{name: "本仓有索引", upstream: "zed/tool", probeSelf: "fnpack.json", known: true,
			wantURL: "https://github.com/zed/tool"},
		{name: "owner 索引仓未收录", upstream: "dave/pack-only", probeOwner: "dave/FnDepot", known: true,
			wantURL: "https://github.com/dave/FnDepot"},
		// yuexps/FnDepot 在本测试环境里已是配置源（名「搬运源」）→ 不出按钮、给说明
		{name: "owner 索引仓已收录", upstream: "yuexps/pack-only", probeOwner: "yuexps/FnDepot", known: true,
			wantNoteIn: []string{"已由已添加的源", "搬运源"}, wantSrc: "搬运源"},
		{name: "两处都无索引", upstream: "dave/plain", known: true,
			wantNoteIn: []string{"未发布应用源索引"}},
		{name: "尚未探测", upstream: "erin/unknown", known: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.known {
				s.AddProbes.SetForTest(c.upstream, c.probeSelf, c.probeOwner)
			}
			out := []AppInfo{mkLineageCard("x@"+c.upstream, "x", "上游源", "1.0.0", c.upstream, false)}
			s.applyUpstreamLineage(out)
			got := out[0]
			if got.UpstreamRepo != c.upstream {
				t.Fatalf("前置条件不成立：UpstreamRepo = %q", got.UpstreamRepo)
			}
			if got.UpstreamSource != c.wantSrc {
				t.Fatalf("UpstreamSource = %q，期望 %q", got.UpstreamSource, c.wantSrc)
			}
			if got.UpstreamAddURL != c.wantURL {
				t.Fatalf("UpstreamAddURL = %q，期望 %q", got.UpstreamAddURL, c.wantURL)
			}
			for _, sub := range c.wantNoteIn {
				if !strings.Contains(got.UpstreamAddNote, sub) {
					t.Fatalf("UpstreamAddNote = %q，应含 %q", got.UpstreamAddNote, sub)
				}
			}
			if len(c.wantNoteIn) == 0 && got.UpstreamAddNote != "" {
				t.Fatalf("不应有说明文字，实际 %q", got.UpstreamAddNote)
			}
			// 未知时两个字段都必须为空（前端据此维持现状）
			if !c.known && (got.UpstreamAddURL != "" || got.UpstreamAddNote != "") {
				t.Fatalf("未探测时不应设置任何字段：%+v", got)
			}
		})
	}
}

// 上游源已收录的走原有分支（UpstreamSource 非空），不得再出「添加为源」提示。
func TestApplyUpstreamAddHint_SkippedWhenUpstreamKnown(t *testing.T) {
	s := newLineageServer(t)
	s.AddProbes = source.NewAddProbe("")
	out := []AppInfo{
		mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false),
		mkLineageCard("app-a@上游源", "app-a", "上游源", "2.5.0", "alice/awesome-apps", false),
	}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamSource != "上游源" {
		t.Fatalf("前置条件不成立：%q", out[0].UpstreamSource)
	}
	if out[0].UpstreamAddURL != "" || out[0].UpstreamAddNote != "" {
		t.Fatalf("上游源已收录时不应出添加提示：%+v", out[0])
	}
}
