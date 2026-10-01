package api

import (
	"strings"
	"testing"

	"moo/internal/config"
)

// 0.6.210：渠道通知排版统一（用户反馈「成功 1 / 失败 5 · - 1Panel · - Gitea …」乱）。
func TestBuildAutoUpdateRoundMsg(t *testing.T) {
	// ① 部分成功（还原用户截图场景：1 成功 5 失败）
	c, tb, rows, v := buildAutoUpdateRoundMsg(
		[]string{"Gitea"},
		[]string{"1Panel", "nodejs_v22", "nodejs_v24", "python312", "java-21-openjdk"},
	)
	want := "成功 1 / 失败 5\n成功：Gitea\n失败：1Panel、nodejs_v22、nodejs_v24、python312、java-21-openjdk"
	if c != want {
		t.Fatalf("content 不符:\n%q\nwant:\n%q", c, want)
	}
	if !strings.Contains(tb, "| Gitea | 成功 |") || !strings.Contains(tb, "| 1Panel | 失败 |") {
		t.Fatalf("table 应含成功/失败行: %q", tb)
	}
	if len(rows) != 6 || rows[0].Key != "Gitea" || rows[0].Value != "成功" || rows[1].Value != "失败" {
		t.Fatalf("rows 应为 成功在前共 6 行: %#v", rows)
	}
	if v.ContentConcise != "成功 1 / 失败 5" {
		t.Fatalf("concise 不符: %q", v.ContentConcise)
	}

	// ② 全部成功
	c2, _, rows2, v2 := buildAutoUpdateRoundMsg([]string{"a", "b"}, nil)
	if c2 != "全部 2 个成功：\na、b" { // 尾部换行被 TrimSpace 吃掉
		t.Fatalf("全成功 content 不符: %q", c2)
	}
	if v2.ContentConcise != "全部 2 个成功" {
		t.Fatalf("全成功 concise 不符: %q", v2.ContentConcise)
	}
	if len(rows2) != 2 || rows2[1].Value != "成功" {
		t.Fatalf("全成功 rows 不符: %#v", rows2)
	}

	// ③ 超长列表截断（成功>5 / 失败>5）
	many := []string{"a", "b", "c", "d", "e", "f", "g"}
	c3, _, rows3, _ := buildAutoUpdateRoundMsg(many, many)
	if !strings.Contains(c3, "成功：a、b、c、d、e 等 7 个") {
		t.Fatalf("成功列表应截断: %q", c3)
	}
	if !strings.Contains(c3, "失败：a、b、c、d、e 等 7 个") {
		t.Fatalf("失败列表应截断: %q", c3)
	}
	if len(rows3) != 8 {
		t.Fatalf("卡片行上限 8: %d", len(rows3))
	}
}

func TestMirrorLabel210(t *testing.T) {
	if got := mirrorLabel("gh", "direct"); got != "直连" {
		t.Fatalf("direct → %q", got)
	}
	if got := mirrorLabel("gh", "custom"); got != "自定义源" {
		t.Fatalf("custom → %q", got)
	}
	// 内置 key 应解析到中文/品牌标签（非 key 本身）
	ghKeys := map[string]bool{}
	for _, o := range config.GitHubMirrorOptions() {
		ghKeys[o.Key] = true
	}
	if k := firstKey(ghKeys, "gh-proxy-hk", "gh-proxy"); k != "" {
		if got := mirrorLabel("gh", k); got == k {
			t.Fatalf("内置 key %s 应解析为标签而非 key", k)
		}
	}
	// 未知 key 回退原样
	if got := mirrorLabel("gh", "unknown-key-123"); got != "unknown-key-123" {
		t.Fatalf("未知 key 应回退原样: %q", got)
	}
}

func firstKey(m map[string]bool, cands ...string) string {
	for _, c := range cands {
		if m[c] {
			return c
		}
	}
	return ""
}
