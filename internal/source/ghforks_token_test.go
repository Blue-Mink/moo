package source

// ghforks_token_test.go —— 0.6.334：可选 GitHub token 的请求头接线。
//
// 背景：匿名 60 次/小时，159 个源仓冷启动要 4 小时以上才能补满 fork 关系；
// 配 token 后 5000 次/小时。这里钉住两件事：
//  ① SetToken 后请求带 Authorization: Bearer <token>；
//  ② 未设置时**不得**带该头（避免把空值发给 GitHub 被当成非法凭据）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGHForksTokenHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Etag", `W/"v1"`)
		_, _ = w.Write([]byte(`{"fork":true,"parent":{"full_name":"Up/Parent"}}`))
	}))
	defer srv.Close()

	g := NewGHForks("")
	g.apiBase = srv.URL
	g.SetToken("ghp_test_123")
	if !g.HasToken() {
		t.Fatal("SetToken 后 HasToken 应为 true")
	}
	g.Enqueue("a/b")
	if n, err := g.RefreshRound(context.Background(), 5); err != nil || n != 1 {
		t.Fatalf("RefreshRound = %d, %v; want 1, nil", n, err)
	}
	if gotAuth != "Bearer ghp_test_123" {
		t.Fatalf("请求头 Authorization = %q, want Bearer ghp_test_123", gotAuth)
	}
	if p, isFork, known := g.Lookup("a/b"); !known || !isFork || p != "up/parent" {
		t.Fatalf("Lookup = %q,%v,%v", p, isFork, known)
	}

	// 清空 token 后不得再带 Authorization 头
	g.SetToken("")
	if g.HasToken() {
		t.Fatal("清空 token 后 HasToken 应为 false")
	}
	g.Enqueue("c/d")
	if _, err := g.RefreshRound(context.Background(), 5); err != nil {
		t.Fatalf("RefreshRound: %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("未配置 token 时不应带 Authorization, got %q", gotAuth)
	}
}
