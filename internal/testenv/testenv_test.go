package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuardIsolatesForeignTrimPkgVar：继承来的 TRIM_PKGVAR 指向别的应用目录时，
// Guard 必须把 MOO_DATA 指到一个真实存在的临时目录（并且不是原目录）。
func TestGuardIsolatesForeignTrimPkgVar(t *testing.T) {
	t.Setenv("MOO_DATA", "")
	t.Setenv("TRIM_PKGVAR", filepath.Join(t.TempDir(), "some-other-app"))

	cleanup := Guard()
	defer cleanup()

	got := os.Getenv("MOO_DATA")
	if got == "" {
		t.Fatal("MOO_DATA 仍为空，测试没有被隔离")
	}
	if !strings.Contains(filepath.Base(got), "moo-test-data-") {
		t.Fatalf("MOO_DATA=%q 不像临时数据目录", got)
	}
	fi, err := os.Stat(got)
	if err != nil || !fi.IsDir() {
		t.Fatalf("临时数据目录不存在: %v", err)
	}
}

// TestGuardKeepsMooDataDir：继承的就是 Moo 自己的目录（basename=moo）时不动环境。
func TestGuardKeepsMooDataDir(t *testing.T) {
	t.Setenv("MOO_DATA", "")
	t.Setenv("TRIM_PKGVAR", filepath.Join(t.TempDir(), "moo"))

	cleanup := Guard()
	defer cleanup()

	if got := os.Getenv("MOO_DATA"); got != "" {
		t.Fatalf("Moo 自己的目录不该被改写，实际 MOO_DATA=%q", got)
	}
}

// TestGuardRespectsPresetMooData：已显式设置 MOO_DATA 时，Guard 不得覆盖。
func TestGuardRespectsPresetMooData(t *testing.T) {
	preset := filepath.Join(t.TempDir(), "preset")
	if err := os.MkdirAll(preset, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOO_DATA", preset)
	t.Setenv("TRIM_PKGVAR", filepath.Join(t.TempDir(), "some-other-app"))

	cleanup := Guard()
	defer cleanup()

	if got := os.Getenv("MOO_DATA"); got != preset {
		t.Fatalf("MOO_DATA 被覆盖为 %q，期望保持 %q", got, preset)
	}
}

// TestGuardCleanupRestores：cleanup 必须复原 MOO_DATA 并删掉临时目录。
func TestGuardCleanupRestores(t *testing.T) {
	t.Setenv("MOO_DATA", "")
	t.Setenv("TRIM_PKGVAR", filepath.Join(t.TempDir(), "some-other-app"))

	cleanup := Guard()
	dir := os.Getenv("MOO_DATA")
	if dir == "" {
		t.Fatal("未进入隔离分支，用例前提不成立")
	}
	cleanup()

	if got := os.Getenv("MOO_DATA"); got != "" {
		t.Fatalf("cleanup 后 MOO_DATA=%q，期望为空", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cleanup 后临时目录仍在（err=%v）", err)
	}
}
