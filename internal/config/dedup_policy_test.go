package config

import (
	"os"
	"testing"
)

// 0.6.575 两档化：merge（旧标准档）→ one；脏值 → all（安全默认，不隐藏）。
func TestNormalizeDedupPolicy(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "all"},
		{"all", "all"},
		{"one", "one"},
		{"merge", "one"},
		{" merge ", "one"},
		{"standard", "all"},
		{"ONE", "all"}, // 大小写不宽容：未知值走安全默认
	}
	for _, c := range cases {
		if got := NormalizeDedupPolicy(c.in); got != c.want {
			t.Fatalf("NormalizeDedupPolicy(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Load 读到存量 merge 必须归一为 one（不必等用户在设置页点一次）。
func TestLoadNormalizesLegacyMerge(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte(`{"dedup_policy":"merge"}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DedupPolicy != "one" {
		t.Fatalf("存量 merge 应归一为 one，实际 %q", cfg.DedupPolicy)
	}
}
