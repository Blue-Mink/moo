package main

// token_0228_test.go —— 0.2.28：Mate 复用 Moo 的 GitHub token。
//
// 钉住四件事：
//  ① 请求打到 /api/mate/github-token 且带 X-Moo-Mate 头；
//  ② Moo 返回 set=true → 用 Moo 的 token，来源 "moo"；
//  ③ 2 分钟缓存内不再打 Moo（改服务端值不立即生效）；
//  ④ Moo 未配置 / 拒绝（403）→ 回落启动参数的 token，来源 "flag"。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func newTokenTestServer(t *testing.T, token string, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/mate/github-token" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Moo-Mate") == "" {
			http.Error(w, "missing mate header", http.StatusForbidden)
			return
		}
		atomic.AddInt32(calls, 1)
		if token == "" {
			_, _ = w.Write([]byte(`{"github_token_set":false,"github_token":""}`))
			return
		}
		_, _ = w.Write([]byte(`{"github_token_set":true,"github_token":"` + token + `"}`))
	}))
}

func TestGithubTokenReusesMoo(t *testing.T) {
	var calls int32
	srv := newTokenTestServer(t, "ghp_from_moo", &calls)
	defer srv.Close()

	c := &companion{moo: newMooClient(srv.URL), token: "flag_tok"}
	tok, src, err := c.githubTokenWithSource()
	if err != nil || tok != "ghp_from_moo" || src != "moo" {
		t.Fatalf("应复用 Moo token: tok=%q src=%q err=%v", tok, src, err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("应请求 Moo 一次，实际 %d", got)
	}

	// 缓存期内不再请求（改服务端 token 也不立即变）
	tok2, _, _ := c.githubTokenWithSource()
	if tok2 != "ghp_from_moo" {
		t.Fatalf("2 分钟缓存内应保持旧值，got %q", tok2)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("缓存期内不应再请求 Moo，实际 %d 次", got)
	}
}

func TestGithubTokenFallsBackToFlag(t *testing.T) {
	var calls int32
	srv := newTokenTestServer(t, "", &calls)
	defer srv.Close()

	c := &companion{moo: newMooClient(srv.URL), token: "flag_tok"}
	tok, src, err := c.githubTokenWithSource()
	if err != nil || tok != "flag_tok" || src != "flag" {
		t.Fatalf("Moo 未配置时应回落 flag: tok=%q src=%q err=%v", tok, src, err)
	}
}

func TestGithubTokenFallsBackOnForbidden(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "forbidden", http.StatusForbidden) // 缺头/不可信通道
	}))
	defer srv.Close()

	c := &companion{moo: newMooClient(srv.URL), token: "flag_tok"}
	tok, src, _ := c.githubTokenWithSource()
	if tok != "flag_tok" || src != "flag" {
		t.Fatalf("Moo 403 时应回落 flag: tok=%q src=%q", tok, src)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("应实际请求过一次 Moo")
	}
}
