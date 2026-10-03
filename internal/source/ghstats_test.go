package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseGhReleaseURL(t *testing.T) {
	cases := []struct {
		url    string
		owner  string
		repo   string
		asset  string
		ok     bool
	}{
		{
			"https://github.com/RROrg/fn-apps/releases/download/2026.09.21-1639/fn-VirtualHereServer_all_v4.8.8-1.fpk",
			"RROrg", "fn-apps", "fn-VirtualHereServer_all_v4.8.8-1.fpk", true,
		},
		{
			"https://github.com/o/r/releases/download/v1.0/a%20b.fpk",
			"o", "r", "a b.fpk", true, // URL 转义文件名应解码
		},
		{"https://raw.githubusercontent.com/o/r/main/x.fpk", "", "", "", false},
		{"https://cdn.jsdelivr.net/gh/o/r/x.fpk", "", "", "", false},
		{"", "", "", "", false},
	}
	for _, c := range cases {
		o, r, a, ok := ParseGhReleaseURL(c.url)
		if ok != c.ok || (ok && (o != c.owner || r != c.repo || a != c.asset)) {
			t.Errorf("ParseGhReleaseURL(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.url, o, r, a, ok, c.owner, c.repo, c.asset, c.ok)
		}
	}
}

func TestGHStatsRefreshRoundAndLookup(t *testing.T) {
	// 模拟 GitHub API：/repos/O/R/releases 返回带 download_count 的资产
	var lastETag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == lastETag && lastETag != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		lastETag = `"etag-1"`
		w.Header().Set("ETag", lastETag)
		w.Header().Set("X-RateLimit-Remaining", "50")
		_, _ = w.Write([]byte(`[{"assets":[
			{"name":"app_a_v1.0.fpk","download_count":1234},
			{"name":"app_b_v2.0.fpk","download_count":56}
		]}]`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	g := NewGHStats(filepath.Join(dir, "ghstats.json"))
	g.apiBase = srv.URL

	g.Enqueue([]string{
		"https://github.com/O/R/releases/download/v1/app_a_v1.0.fpk",
		"https://github.com/O/R/releases/download/v2/app_b_v2.0.fpk",
		"https://raw.githubusercontent.com/x/y/main/skip.fpk", // 非 Release 链接应忽略
	})
	n, err := g.RefreshRound(context.Background(), 10)
	if err != nil || n != 1 {
		t.Fatalf("RefreshRound = (%d, %v), want (1, nil)", n, err)
	}
	if got, ok := g.Lookup("O", "R", "app_a_v1.0.fpk"); !ok || got != 1234 {
		t.Errorf("Lookup app_a = (%d,%v), want (1234,true)", got, ok)
	}
	if got, ok := g.Lookup("O", "R", "app_b_v2.0.fpk"); !ok || got != 56 {
		t.Errorf("Lookup app_b = (%d,%v), want (56,true)", got, ok)
	}
	if got, ok := g.Lookup("O", "R", "nope.fpk"); ok || got != 0 {
		t.Errorf("Lookup 未命中的资产 = (%d,%v), want (0,false)", got, ok)
	}

	// 304 路径：队列为空时再拉同一仓库应命中 ETag 不消耗请求
	// （直接调 fetchRepo 验证 304 不报错）
	if err := g.fetchRepo(context.Background(), "O", "R"); err != nil {
		t.Errorf("304 路径 fetchRepo: %v", err)
	}
}

func TestGHStatsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.apiBase = srv.URL
	g.Enqueue([]string{"https://github.com/O/R/releases/download/v1/a.fpk"})
	n, err := g.RefreshRound(context.Background(), 10)
	if err != ErrRateLimited {
		t.Fatalf("限流时应返回 ErrRateLimited, got %v", err)
	}
	if n != 0 {
		t.Errorf("限流轮不应有成功仓库, got %d", n)
	}
	// 限流失败的仓库应回队，下轮可重试
	g.mu.Lock()
	queued := len(g.queue)
	g.mu.Unlock()
	if queued != 1 {
		t.Errorf("限流后队列应有 1 个仓库待重试, got %d", queued)
	}
}

func TestGHStatsPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghstats.json")
	g := NewGHStats(path)
	g.mu.Lock()
	g.stats["O/R"] = &ghRepoStats{ETag: "e1", FetchedAt: "2026-09-23T00:00:00Z", Assets: map[string]int{"a.fpk": 42}}
	g.mu.Unlock()
	g.save()

	g2 := NewGHStats(path)
	if got, ok := g2.Lookup("O", "R", "a.fpk"); !ok || got != 42 {
		t.Errorf("重启后 Lookup = (%d,%v), want (42,true)", got, ok)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
