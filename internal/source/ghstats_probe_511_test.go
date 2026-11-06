package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 0.6.511：本机测速探针资产的 download_count 不计入应用级累计，只记进
// AppProbeTotals（供详情页透明展示「已排除测速 N 次」）。
func TestFetchRepoExcludesSelfProbeAsset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("ETag", `"e1"`)
		_, _ = w.Write([]byte(`[
		  {"tag_name":"v0.6.506","assets":[
		    {"id":1,"name":"moo_0.6.506_x86.fpk","download_count":601},
		    {"id":2,"name":"moo-server-linux-amd64","download_count":90000}
		  ]},
		  {"tag_name":"v0.6.325","assets":[
		    {"id":3,"name":"moo_0.6.325_x86.fpk","download_count":410035},
		    {"id":4,"name":"moo-server-linux-amd64","download_count":4}
		  ]},
		  {"tag_name":"v0.6.300","assets":[
		    {"id":5,"name":"moo_0.6.300_x86.fpk","download_count":100}
		  ]}
		]`))
	}))
	defer srv.Close()

	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.apiBase = srv.URL
	if err := g.fetchRepo(context.Background(), "Blue-Mink", "moo"); err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	// 701 = 601（0.6.506）+ 100（0.6.300）；410035 的探针资产被剔除
	total, cur, ok := g.LookupAppTotal("Blue-Mink", "moo", "moo_0.6.506_x86.fpk")
	if !ok || total != 701 || cur != 601 {
		t.Fatalf("累计应剔除探针：got total=%d cur=%d ok=%v, want 701/601/true", total, cur, ok)
	}
	if ex := g.LookupAppProbeExcluded("Blue-Mink", "moo", "moo_0.6.506_x86.fpk"); ex != 410035 {
		t.Fatalf("排除量应为 410035，得 %d", ex)
	}
	// 非 .fpk 资产（CI 二进制）本来就不计，不应影响上面两项
	if ex := g.LookupAppProbeExcluded("Blue-Mink", "moo", "moo-server-linux-amd64"); ex != 0 {
		t.Fatalf("非 FPK 资产不应有排除量，得 %d", ex)
	}
}

// 排除规则按「仓库 + tag + 资产名」精确匹配：别的仓库同名资产不受影响，
// 且大小写不敏感（GitHub owner/repo 大小写不固定）。
func TestSelfProbeAssetScopedToRepoAndCase(t *testing.T) {
	if isSelfProbeAsset("other", "moo", "v0.6.325", "moo_0.6.325_x86.fpk") {
		t.Fatal("别的仓库的同名资产不应被当成探针")
	}
	if isSelfProbeAsset("Blue-Mink", "moo", "v0.6.506", "moo_0.6.325_x86.fpk") {
		t.Fatal("tag 不同不应匹配（防止误杀正常版本）")
	}
	if !isSelfProbeAsset("blue-mink", "MOO", "v0.6.325", "moo_0.6.325_x86.fpk") {
		t.Fatal("仓库名大小写不敏感匹配应命中")
	}
}

// 旧缓存（有 AppTotals 但没有 ProbeExcludedBuilt）必须忽略 ETag 重拉一次，
// 否则升级后仍显示灌水后的 41 万。
func TestProbeExcludedMigrationIgnoresETag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghstats.json")
	old := `{"stats":{"Blue-Mink/moo":{"etag":"\"old\"","fetched_at":"2026-10-01T00:00:00+08:00",` +
		`"assets":{"moo_0.6.325_x86.fpk":410035},"sizes":{"moo_0.6.325_x86.fpk":4978821},` +
		`"app_totals":{"moo":410035},"totals_built":true}}}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	sawIfNoneMatch := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("ETag", `"new"`)
		_, _ = w.Write([]byte(`[{"tag_name":"v0.6.325","assets":[{"id":3,"name":"moo_0.6.325_x86.fpk","download_count":410035,"size":4978821}]}]`))
	}))
	defer srv.Close()

	g := NewGHStats(path)
	g.apiBase = srv.URL
	g.Enqueue([]string{"https://github.com/Blue-Mink/moo/releases/download/v0.6.325/moo_0.6.325_x86.fpk"})
	if _, err := g.RefreshRound(context.Background(), 5); err != nil {
		t.Fatalf("RefreshRound: %v", err)
	}
	if sawIfNoneMatch != "" {
		t.Fatalf("缺 ProbeExcludedBuilt 的缓存必须忽略 ETag 重拉，实际发送 If-None-Match=%q", sawIfNoneMatch)
	}
	if total, _, ok := g.LookupAppTotal("Blue-Mink", "moo", "moo_0.6.325_x86.fpk"); ok || total != 0 {
		t.Fatalf("重拉后累计应剔除探针（0/false），得 (%d,%v)", total, ok)
	}
	if ex := g.LookupAppProbeExcluded("Blue-Mink", "moo", "moo_0.6.325_x86.fpk"); ex != 410035 {
		t.Fatalf("排除量应为 410035，得 %d", ex)
	}
}
