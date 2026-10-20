package api

// github_token_334_test.go —— 0.6.334：GitHub token 设置（PUT/GET 契约）。
//
// 钉住四件事：
//  ① PUT 合法 token → 落 Cfg、立即对 Forks 生效（无需重启）；
//  ② GET 只下发 github_token_set=true，**绝不回传 token 本身**；
//  ③ 未提交 github_token 时不动现有值（局部保存不能把 token 抹掉）；
//  ④ 非 ghp_/github_pat_ 前缀的脏值整单拒绝；空串 = 显式清除回匿名。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/source"
)

func TestPutSettingsGitHubToken(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	f := source.NewGHForks("")
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Forks: f, Pipe: &pipeline.Pipeline{}}

	putSettingsBody(t, s, `{"github_token":"ghp_testtoken123"}`)
	if cfg.GithubToken != "ghp_testtoken123" {
		t.Fatalf("token 应落 Cfg, got %q", cfg.GithubToken)
	}
	if !f.HasToken() {
		t.Fatal("token 应即时对 Forks 生效（无需重启）")
	}

	// GET 只透出「是否已设置」，绝不回传值
	rec := httptest.NewRecorder()
	s.getSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	body := rec.Body.String()
	if strings.Contains(body, "ghp_testtoken123") {
		t.Fatal("GET /api/settings 不得回传 token 明文")
	}
	if !strings.Contains(body, `"github_token_set":true`) {
		t.Fatalf("GET 应透出 github_token_set=true，body=%s", body[:min(200, len(body))])
	}
	// 未提交该字段：不改动（局部保存不能抹掉 token）
	putSettingsBody(t, s, `{"auto_update":true}`)
	if cfg.GithubToken != "ghp_testtoken123" || !f.HasToken() {
		t.Fatal("未提交 github_token 时不应改动")
	}

	// 脏值整单拒绝，且现有值保持不变
	putSettingsBodyExpect400(t, s, `{"github_token":"Bearer abc"}`)
	if cfg.GithubToken != "ghp_testtoken123" {
		t.Fatalf("脏值被拒后 token 不应变化, got %q", cfg.GithubToken)
	}

	// 空串 = 显式清除（回匿名 60/h）
	putSettingsBody(t, s, `{"github_token":""}`)
	if cfg.GithubToken != "" || f.HasToken() {
		t.Fatal("空串应清除 token 并回到匿名")
	}
}
