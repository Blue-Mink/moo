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
		url   string
		owner string
		repo  string
		asset string
		ok    bool
	}{
		{
			"https://github.com/RROrg/fn-apps/releases/download/2026.09.21-1639/fn-VirtualHereServer_all_v4.8.8-1.fpk",
			"RROrg", "fn-apps", "fn-VirtualHereServer_all_v4.8.8-1.fpk", true,
		},
		{
			"https://github.com/o/r/releases/download/v1.0/a%20b.fpk",
			"o", "r", "a b.fpk", true, // URL 转义文件名应解码
		},
		{
			// 0.6.336：tag 含 `/`（conversun/fnos-apps 的 `1panel/v1.10.34-lts-r8`）。
			// 旧正则把 tag 当单段 → asset 变成 `v1.10.../1panel_..._x86.fpk`，
			// GitHub 资产元数据永远查不到（下载量/大小都拿不到）。
			"https://github.com/conversun/fnos-apps/releases/download/1panel/v1.10.34-lts-r8/1panel_1.10.34-lts-r8_x86.fpk",
			"conversun", "fnos-apps", "1panel_1.10.34-lts-r8_x86.fpk", true,
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

// 0.6.336：Release 资产 size 采集（分页 / LookupSize / 落盘 / ETag 只取首页）
func TestGHStatsSizesPagingAndPersist(t *testing.T) {
	var page2Hit bool
	var etags []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etags = append(etags, r.Header.Get("If-None-Match"))
		w.Header().Set("X-RateLimit-Remaining", "50")
		if r.URL.Query().Get("page") == "2" {
			page2Hit = true
			w.Header().Set("ETag", `"etag-page2"`)
			_, _ = w.Write([]byte(`[{"assets":[{"name":"last.fpk","download_count":7,"size":9999}]}]`))
			return
		}
		w.Header().Set("ETag", `"etag-page1"`)
		var b []byte
		b = append(b, '[')
		for i := 0; i < 100; i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, []byte(`{"assets":[{"name":"a`+itoa(i)+`.fpk","download_count":`+itoa(i)+`,"size":`+itoa(1000+i)+`}]}`)...)
		}
		b = append(b, ']')
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "ghstats.json")
	g := NewGHStats(path)
	g.apiBase = srv.URL
	g.Enqueue([]string{"https://github.com/o/r/releases/download/v1/a0.fpk"})
	n, err := g.RefreshRound(context.Background(), 5)
	if err != nil || n != 1 {
		t.Fatalf("RefreshRound = (%d,%v)，want (1,nil)", n, err)
	}
	if !page2Hit {
		t.Fatalf("首页满 100 条时必须继续拉第 2 页")
	}
	if sz, ok := g.LookupSize("o", "r", "a0.fpk"); !ok || sz != 1000 {
		t.Fatalf("LookupSize(a0) = (%d,%v)，want (1000,true)", sz, ok)
	}
	if sz, ok := g.LookupSize("o", "r", "last.fpk"); !ok || sz != 9999 {
		t.Fatalf("第二页资产 size 未合并：(%d,%v)", sz, ok)
	}
	g.mu.Lock()
	gotETag := g.stats["o/r"].ETag
	g.mu.Unlock()
	if gotETag != `"etag-page1"` {
		t.Fatalf("ETag 必须只取首页（分页 ETag 会让首页请求永不 304）：%q", gotETag)
	}
	// 落盘 → 重新加载后 size 仍在
	g2 := NewGHStats(path)
	if sz, ok := g2.LookupSize("o", "r", "a0.fpk"); !ok || sz != 1000 {
		t.Fatalf("sizes 未落盘/未加载：(%d,%v)", sz, ok)
	}
}

// 0.6.336 迁移：旧缓存只有 assets、没有 sizes → 忽略 ETag 强制重拉一次
func TestGHStatsSizesMigrationIgnoresETag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghstats.json")
	old := `{"stats":{"o/r":{"etag":"\"old-etag\"","fetched_at":"2026-10-01T00:00:00+08:00","assets":{"a0.fpk":5}}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	var sawIfNoneMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("ETag", `"new-etag"`)
		_, _ = w.Write([]byte(`[{"assets":[{"name":"a0.fpk","download_count":5,"size":4242}]}]`))
	}))
	defer srv.Close()

	g := NewGHStats(path)
	g.apiBase = srv.URL
	g.Enqueue([]string{"https://github.com/o/r/releases/download/v1/a0.fpk"})
	if _, err := g.RefreshRound(context.Background(), 5); err != nil {
		t.Fatalf("RefreshRound err: %v", err)
	}
	if sawIfNoneMatch != "" {
		t.Fatalf("缺 sizes 的历史缓存必须忽略 ETag 重拉，实际发送 If-None-Match=%q", sawIfNoneMatch)
	}
	if sz, ok := g.LookupSize("o", "r", "a0.fpk"); !ok || sz != 4242 {
		t.Fatalf("迁移后应补到 size：(%d,%v)", sz, ok)
	}
}

// itoa 避免为测试引入 strconv（保持文件依赖最小）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
