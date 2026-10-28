package api

// mate_354_test.go —— 0.6.354：Moo Mate 复用 GitHub token 端点契约。
//
// 钉住三件事：
//  ① 缺 X-Moo-Mate 头 → 403（不是 200 空体，避免静默失败）；
//  ② 带头 → 返回 set/token，值为当前生效的 Cfg.GithubToken；
//  ③ 未配置 token → set=false 且 token 为空串（Moo Mate 回退匿名）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
)

func mateTokenReq(t *testing.T, s *Server, withMateHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, GatewayPrefix+"/api/mate/github-token", strings.NewReader("")).WithContext(t.Context())
	r.Header.Set("X-Trim-Username", "fnos")
	r.Header.Set("X-Trim-Userid", "1000")
	r.Header.Set("X-Trim-Isadmin", "true")
	if withMateHeader {
		r.Header.Set("X-Moo-Mate", "1")
	}
	rec := httptest.NewRecorder()
	s.Handler(true).ServeHTTP(rec, r)
	return rec
}

func TestMateGithubTokenEndpoint(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{GithubToken: "ghp_mate_test_354"}}

	rec := mateTokenReq(t, s, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 X-Moo-Mate 应 403, got %d %s", rec.Code, rec.Body.String())
	}

	rec = mateTokenReq(t, s, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("带头应 200, got %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"github_token_set":true`) || !strings.Contains(body, `"github_token":"ghp_mate_test_354"`) {
		t.Fatalf("响应字段不符: %s", body)
	}

	s.Cfg.GithubToken = ""
	rec = mateTokenReq(t, s, true)
	body = rec.Body.String()
	if !strings.Contains(body, `"github_token_set":false`) || !strings.Contains(body, `"github_token":""`) {
		t.Fatalf("未配置时应 set=false + 空串: %s", body)
	}
}
