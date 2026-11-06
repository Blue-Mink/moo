package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAssetBaseNormalization(t *testing.T) {
	cases := map[string]string{
		"fnmusic-ext-2.12.0-8.fpk":                "fnmusic-ext",
		"fnmusic-ext-2.11.0-4.fpk":                "fnmusic-ext",
		"moo_0.6.506_x86.fpk":                     "moo",
		"moo-mate_0.2.55_x86.fpk":                 "moo-mate",
		"1panel_1.10.34-lts-r8_x86.fpk":           "1panel",
		"1panel_1.10.34-lts-r8_arm.fpk":           "1panel",
		"EasyTier-EUI-fnos-x86_64-0.9.020604.fpk": "easytier-eui-fnos",
		"Nvidia-Driver-580_1.20.0-r1_x86.fpk":     "nvidia-driver-580",
		"php-8.3_8.3.10_x86.fpk":                  "php-8-3",
		"nodejs_v22_22.18.0-1_x86.fpk":            "nodejs-v22",
		"app-2.0.0_x86.fpk":                       "app",
		"app-1.0.0_arm.fpk":                       "app",
	}
	for in, want := range cases {
		if got := assetBase(in); got != want {
			t.Errorf("assetBase(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestLookupAppTotalUsesCurrentAssetBase(t *testing.T) {
	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.stats["o/r"] = &ghRepoStats{
		Assets: map[string]int{
			"moo_0.6.506_x86.fpk":      601,
			"moo_0.6.325_x86.fpk":      402609,
			"moo-mate_0.2.55_x86.fpk":  171,
			"fnmusic-ext-2.12.0-8.fpk": 308,
			"fnmusic-ext-2.11.0-4.fpk": 3668,
		},
		AppTotals: map[string]int{
			"moo":         403210,
			"moo-mate":    171,
			"fnmusic-ext": 3976,
		},
	}
	cases := []struct {
		asset      string
		total, cur int
		ok         bool
	}{
		{"moo_0.6.506_x86.fpk", 403210, 601, true}, // moo 不会吃掉 moo-mate
		{"moo-mate_0.2.55_x86.fpk", 171, 171, true},
		{"fnmusic-ext-2.12.0-8.fpk", 3976, 308, true},
	}
	for _, c := range cases {
		total, cur, ok := g.LookupAppTotal("o", "r", c.asset)
		if total != c.total || cur != c.cur || ok != c.ok {
			t.Errorf("LookupAppTotal(%q)=(%d,%d,%v), want (%d,%d,%v)",
				c.asset, total, cur, ok, c.total, c.cur, c.ok)
		}
	}
	if total, cur, ok := g.LookupAppTotal("o", "r", "nope.fpk"); total != 0 || cur != 0 || ok {
		t.Errorf("unknown asset = (%d,%d,%v), want (0,0,false)", total, cur, ok)
	}
}

func TestLookupAppTotalFallsBackToCurrent(t *testing.T) {
	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.stats["o/r"] = &ghRepoStats{Assets: map[string]int{"app-1.0.fpk": 42}}
	total, cur, ok := g.LookupAppTotal("o", "r", "app-1.0.fpk")
	if total != 0 || cur != 42 || ok {
		t.Fatalf("无 AppTotals 时应返回 (0,42,false)，得 (%d,%d,%v)", total, cur, ok)
	}
}

func TestFetchRepoBuildsAppTotals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "50")
		w.Header().Set("ETag", `"e1"`)
		_, _ = w.Write([]byte(`[
		  {"tag_name":"v2","assets":[
		    {"id":1,"name":"app-2.0.0_x86.fpk","download_count":100},
		    {"id":2,"name":"app-2.0.0_arm.fpk","download_count":50},
		    {"id":3,"name":"app-2.0.0_x86.fpk.sha256","download_count":999},
		    {"id":4,"name":"app-2.0.0_x86.fpk.minisig","download_count":999}
		  ]},
		  {"tag_name":"v1","assets":[
		    {"id":5,"name":"app-1.0.0_x86.fpk","download_count":300},
		    {"id":6,"name":"app-1.0.0_arm.fpk","download_count":200}
		  ]},
		  {"tag_name":"v3-pre","prerelease":true,"assets":[
		    {"id":7,"name":"app-3.0.0_x86.fpk","download_count":9999}
		  ]},
		  {"tag_name":"draft","draft":true,"assets":[
		    {"id":8,"name":"app-4.0.0_x86.fpk","download_count":8888}
		  ]}
		]`))
	}))
	defer srv.Close()

	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.apiBase = srv.URL
	if err := g.fetchRepo(context.Background(), "o", "r"); err != nil {
		t.Fatalf("fetchRepo: %v", err)
	}
	total, cur, ok := g.LookupAppTotal("o", "r", "app-2.0.0_x86.fpk")
	if total != 650 || cur != 100 || !ok {
		t.Fatalf("累计应排除 sidecar/预发布/draft：got (%d,%d,%v), want (650,100,true)", total, cur, ok)
	}
	g.mu.Lock()
	built := g.stats["o/r"].TotalsBuilt
	g.mu.Unlock()
	if !built {
		t.Fatal("fetchRepo 后 TotalsBuilt 应为 true")
	}
}
