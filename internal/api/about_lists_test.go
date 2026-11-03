package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
)

// 0.6.384：关于页远程清单（changelog.md / docs.md / devdocs.md）解析与兜底。

func TestParseAboutDocs(t *testing.T) {
	body := "\ufeff# 文档\n\n" +
		"- [README](https://example.com/readme) — 使用说明\n" +
		"- **标题** | https://example.com/a | 描述A\n" +
		"- 裸标题 https://example.com/b\n" +
		"- [坏链接](javascript:alert(1)) — 拒绝\n" +
		"- [重复](https://example.com/a)\n" +
		"```\n- [围栏内](https://example.com/fence)\n```\n"
	items := parseAboutDocs(body)
	if len(items) != 3 {
		t.Fatalf("应解析 3 条（坏链接/重复/围栏内跳过），got %d: %+v", len(items), items)
	}
	if items[0].Title != "README" || items[0].URL != "https://example.com/readme" || items[0].Desc != "使用说明" {
		t.Errorf("第 1 条解析错: %+v", items[0])
	}
	if items[1].Title != "标题" || items[1].URL != "https://example.com/a" || items[1].Desc != "描述A" {
		t.Errorf("pipe 格式解析错: %+v", items[1])
	}
	if items[2].Title != "裸标题" || items[2].URL != "https://example.com/b" {
		t.Errorf("裸 URL 格式解析错: %+v", items[2])
	}
}

func TestParseAboutDocsCapsAndWhitespace(t *testing.T) {
	body := ""
	for i := 0; i < aboutDocsMaxItems+10; i++ {
		body += "- [标题" + itoaTest(i) + "](https://example.com/" + itoaTest(i) + ")\n"
	}
	if got := len(parseAboutDocs(body)); got != aboutDocsMaxItems {
		t.Fatalf("应截断到 %d 条，got %d", aboutDocsMaxItems, got)
	}
	if n := len(parseAboutDocs("- [x](https://example.com/1)\n- [y](https://example.com/2)\n")); n != 2 {
		t.Fatalf("普通两条清单解析错: %d", n)
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestParseAboutChangelogVersion(t *testing.T) {
	if got := parseAboutChangelogVersion("## 0.6.384\n- 修复", "9.9.9"); got != "0.6.384" {
		t.Errorf("应取标题里的版本，got %q", got)
	}
	if got := parseAboutChangelogVersion("### v0.6.385\n- x", "9.9.9"); got != "0.6.385" {
		t.Errorf("应支持 v 前缀，got %q", got)
	}
	if got := parseAboutChangelogVersion("- 无版本标题", "9.9.9"); got != "9.9.9" {
		t.Errorf("无版本文应回落，got %q", got)
	}
}

func aboutTestServer(t *testing.T, handler http.HandlerFunc) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return &Server{Version: "9.9.9", Cfg: &config.Config{
		AboutListsURL:          ts.URL,
		SourceListAllowPrivate: true,
	}}
}

func TestAboutListsRemote(t *testing.T) {
	files := map[string]string{
		"/changelog.md": "## 1.2.3\n- 修复 A\n",
		"/docs.md":      "- [文档](https://example.com/doc) — 说明\n",
		"/devdocs.md":   "- [API](https://example.com/api)\n",
	}
	s := aboutTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	snap, err := s.refreshAboutLists(context.Background())
	if err != nil {
		t.Fatalf("三文件都应成功: %v", err)
	}
	if !snap.Remote || snap.ChangelogVersion != "1.2.3" || snap.Changelog == "" {
		t.Errorf("远程更新日志状态错: %+v", snap)
	}
	if snap.Changelog == "" {
		t.Error("远程更新日志应非空")
	}
	// 0.6.387：药丸弹窗只显示最新版——第一个版本段
	if snap.ChangelogLatest != "## 1.2.3\n- 修复 A" {
		t.Errorf("ChangelogLatest 解析错: %q", snap.ChangelogLatest)
	}
	if len(snap.Docs) != 1 || snap.Docs[0].URL != "https://example.com/doc" {
		t.Errorf("docs 解析错: %+v", snap.Docs)
	}
	if len(snap.DevDocs) != 1 || snap.DevDocs[0].Title != "API" {
		t.Errorf("devdocs 解析错: %+v", snap.DevDocs)
	}
}

func TestAboutListsFallbackWhenDead(t *testing.T) {
	s := &Server{Version: "1.0.0", Cfg: &config.Config{
		AboutListsURL:          "http://127.0.0.1:1",
		SourceListAllowPrivate: true,
	}}
	snap, err := s.refreshAboutLists(context.Background())
	if err == nil {
		t.Fatal("全部失败应返回错误")
	}
	if snap.Remote {
		t.Fatal("远程失败不应标记 Remote")
	}
	if snap.Changelog != changelogCurrentFor("1.0.0") {
		t.Errorf("更新日志应回落内嵌文本")
	}
	if snap.Docs == nil || snap.DevDocs == nil {
		t.Errorf("文档切片应为空数组而非 nil")
	}
}

func TestAboutListsBlockedPrivateWithoutSwitch(t *testing.T) {
	s := &Server{Version: "1.0.0", Cfg: &config.Config{AboutListsURL: "http://127.0.0.1:1"}}
	if _, err := s.fetchAboutFile(context.Background(), "changelog.md"); err == nil {
		t.Fatal("未开 source_list_allow_private 时应拒绝内网清单地址")
	}
}

// 0.6.387（用户令）：药丸弹窗只显示最新版日志——取第一个版本段，
// 下一个版本标题起截断；没有版本标题时整段返回。
func TestLatestChangelogSection(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"两版取第一段", "## 0.6.387\n- 新功能\n- 修复\n\n## 0.6.386\n- 旧改动\n",
			"## 0.6.387\n- 新功能\n- 修复"},
		{"v 前缀与三级标题", "\n### v0.7.0\n- A\n### 0.6.9\n- B\n", "### v0.7.0\n- A"},
		{"单版本整段", "## 1.0.0\n- only\n", "## 1.0.0\n- only"},
		{"无版本标题", "- 纯文本\n- 第二条\n", "- 纯文本\n- 第二条"},
		{"空文本", "   \n", ""},
		{"三版本只取第一段", "## 3.0.0\n- c\n## 2.0.0\n- b\n## 1.0.0\n- a\n", "## 3.0.0\n- c"},
	}
	for _, c := range cases {
		if got := latestChangelogSection(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
