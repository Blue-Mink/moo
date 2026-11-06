package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGHStatsTotalsMigrationIgnoresETag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghstats.json")
	old := `{"stats":{"o/r":{"etag":"\"old-etag\"","fetched_at":"2026-10-01T00:00:00+08:00","assets":{"a.fpk":5},"sizes":{"a.fpk":100}}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	sawIfNoneMatch := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("ETag", `"new-etag"`)
		_, _ = w.Write([]byte(`[{"tag_name":"v1","assets":[{"id":1,"name":"a-1.0.0.fpk","download_count":5,"size":100}]}]`))
	}))
	defer srv.Close()

	g := NewGHStats(path)
	g.apiBase = srv.URL
	g.Enqueue([]string{"https://github.com/o/r/releases/download/v1/a-1.0.0.fpk"})
	if _, err := g.RefreshRound(context.Background(), 5); err != nil {
		t.Fatalf("RefreshRound: %v", err)
	}
	if sawIfNoneMatch != "" {
		t.Fatalf("缺 AppTotals 的历史缓存必须忽略 ETag 重拉，实际发送 If-None-Match=%q", sawIfNoneMatch)
	}
	total, cur, ok := g.LookupAppTotal("o", "r", "a-1.0.0.fpk")
	if total != 5 || cur != 5 || !ok {
		t.Fatalf("迁移后累计 = (%d,%d,%v), want (5,5,true)", total, cur, ok)
	}
}
