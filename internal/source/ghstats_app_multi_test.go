package source

import (
	"path/filepath"
	"testing"
)

func TestLookupAppTotalMultiAppRepo(t *testing.T) {
	g := NewGHStats(filepath.Join(t.TempDir(), "ghstats.json"))
	g.stats["conversun/fnos-apps"] = &ghRepoStats{
		Assets: map[string]int{
			"1panel_1.10.34-lts-r8_x86.fpk": 1000,
			"Aellus_1.0.5_x86.fpk":          20,
		},
		AppTotals: map[string]int{"1panel": 1000, "aellus": 20},
	}
	total, cur, ok := g.LookupAppTotal("conversun", "fnos-apps", "1panel_1.10.34-lts-r8_x86.fpk")
	if total != 1000 || cur != 1000 || !ok {
		t.Fatalf("1panel 累计被其它应用污染：got (%d,%d,%v), want (1000,1000,true)", total, cur, ok)
	}
	total, cur, ok = g.LookupAppTotal("conversun", "fnos-apps", "Aellus_1.0.5_x86.fpk")
	if total != 20 || cur != 20 || !ok {
		t.Fatalf("Aellus = (%d,%d,%v), want (20,20,true)", total, cur, ok)
	}
}
