package api

import (
	"os"
	"path/filepath"
	"testing"

	"moo/internal/config"
	"moo/internal/operation"
)

// 2026-11-26（P1-3）：归属标记冲突时，两处写点同样必须拒绝——即使 MOO_DATA
// 显式指向该目录（环境变量放行不等于目录归属放行）。
func markForeign(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.DataDirOwnerFile), []byte("moo-dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpsPersistRefusesForeignOwnerMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	markForeign(t, dir)
	t.Setenv("MOO_DATA", dir)

	p := NewOpsPersister(filepath.Join(dir, "operations.json"))
	p.Save([]operation.View{{ID: "x"}})

	if _, err := os.Stat(p.path); !os.IsNotExist(err) {
		t.Fatalf("挂了别人的标记，不应落盘 operations.json（err=%v）", err)
	}
}

func TestBlacklistLKGRefusesForeignOwnerMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	markForeign(t, dir)
	t.Setenv("MOO_DATA", dir)

	path := filepath.Join(dir, "blacklist_last.json")
	s := &Server{BlacklistLKGPath: path}
	s.saveBlacklistLKG(nil)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("挂了别人的标记，不应落盘 blacklist_last.json（err=%v）", err)
	}
}
