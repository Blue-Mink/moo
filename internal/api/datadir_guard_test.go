package api

import (
	"os"
	"path/filepath"
	"testing"

	"moo/internal/operation"
)

// 2026-11-26（批次 B）：写点守卫。继承的外来 TRIM_PKGVAR 决定落点时必须
// 静默拒绝（只打日志，不建目录、不写文件）——这两处写点的调用方不看返回值，
// 所以守卫只能在函数内部兜住。
func TestOpsPersistRefusesForeignDataDir(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "some-other-app")
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", foreign)

	p := NewOpsPersister(filepath.Join(foreign, "operations.json"))
	p.Save([]operation.View{{ID: "x"}})

	if _, err := os.Stat(p.path); !os.IsNotExist(err) {
		t.Fatalf("不应落盘 operations.json（stat err=%v）", err)
	}
	if _, err := os.Stat(foreign); !os.IsNotExist(err) {
		t.Fatalf("不应创建目标目录（stat err=%v）", err)
	}
}

func TestBlacklistLKGRefusesForeignDataDir(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "some-other-app")
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", foreign)

	path := filepath.Join(foreign, "blacklist_last.json")
	s := &Server{BlacklistLKGPath: path}
	s.saveBlacklistLKG([]blacklistEntry{})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("不应落盘 blacklist_last.json（stat err=%v）", err)
	}
	if _, err := os.Stat(foreign); !os.IsNotExist(err) {
		t.Fatalf("不应创建目标目录（stat err=%v）", err)
	}
}

// TestWritePointsAllowOwnDataDir：没有外来 TRIM_PKGVAR 时两处写点照旧工作，
// 防止守卫误伤正常运行。
func TestWritePointsAllowOwnDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", "")

	p := NewOpsPersister(filepath.Join(dir, "operations.json"))
	p.Save(nil)
	if _, err := os.Stat(p.path); err != nil {
		t.Fatalf("operations.json 未落盘: %v", err)
	}

	path := filepath.Join(dir, "blacklist_last.json")
	s := &Server{BlacklistLKGPath: path}
	s.saveBlacklistLKG(nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("blacklist_last.json 未落盘: %v", err)
	}
}
