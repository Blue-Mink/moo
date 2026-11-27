package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-11-26（P1-3）：归属标记 .moo-owner 的校验与创建。

// setOwnerApp 固定「本二进制」的 appname（0.6.574 起身份来自 MOO_APPNAME /
// 构建注入，不取 TRIM_APPNAME —— 后者会被别的应用 shell 继承）。
func setOwnerApp(t *testing.T, name string) {
	t.Helper()
	t.Setenv("MOO_APPNAME", name)
	t.Setenv("TRIM_APPNAME", "") // 证明身份不依赖平台变量
}

func writeOwner(t *testing.T, dir, value string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DataDirOwnerFile), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDataDirOwnerConflict(t *testing.T) {
	setOwnerApp(t, "moo")
	t.Run("MOO_DATA 显式给了也要看标记", func(t *testing.T) {
		dir := t.TempDir()
		writeOwner(t, dir, "moo-dev\n")
		t.Setenv("MOO_DATA", dir)
		t.Setenv("TRIM_PKGVAR", "")
		t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
		if err := CheckDataDir(dir); err == nil {
			t.Fatal("目录已被 moo-dev 认领，应当拒绝")
		}
	})
	t.Run("目录名恰好叫 moo 但标记是别人的", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "moo")
		writeOwner(t, dir, "moo-dev\n")
		t.Setenv("MOO_DATA", "")
		t.Setenv("TRIM_PKGVAR", dir)
		if err := CheckDataDir(dir); err == nil {
			t.Fatal("名字像 moo 但标记冲突，仍应拒绝")
		}
	})
}

func TestCheckDataDirOwnerCompatible(t *testing.T) {
	setOwnerApp(t, "moo")
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"无标记", ""},
		{"标记等于 moo", "moo\n"},
		{"标记只有空白", "  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.value != "" {
				writeOwner(t, dir, tc.value)
			}
			t.Setenv("MOO_DATA", dir)
			if err := CheckDataDir(dir); err != nil {
				t.Fatalf("应当放行，实际 %v", err)
			}
		})
	}
}

func TestSaveRefusesForeignOwnerMarker(t *testing.T) {
	setOwnerApp(t, "moo")
	dir := filepath.Join(t.TempDir(), "shared")
	writeOwner(t, dir, "moo-dev\n")
	t.Setenv("MOO_DATA", dir)

	cfg := &Config{WebPort: "38100"}
	if err := cfg.Save(dir); err == nil {
		t.Fatal("挂了别人的归属标记，Save 应当拒绝")
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("被拒绝后不应落盘 config.json（err=%v）", err)
	}
}

func TestEnsureDataDirOwner(t *testing.T) {
	t.Run("首次创建并幂等", func(t *testing.T) {
		// 0.6.574：只有「本次新建」或「平台分配的目录」才盖章，所以这里用
		// 尚不存在的子目录（t.TempDir() 本身已存在，属借用语义，不盖章）。
		dir := filepath.Join(t.TempDir(), "data")
		t.Setenv("MOO_DATA", dir)
		if err := EnsureDataDirOwner(dir); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, DataDirOwnerFile)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(b)) != DataDirOwnerValue() {
			t.Fatalf("标记内容 %q，期望 %q", string(b), DataDirOwnerValue())
		}
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("标记权限 %o，期望 600", fi.Mode().Perm())
		}
		if err := EnsureDataDirOwner(dir); err != nil {
			t.Fatalf("第二次调用应当幂等: %v", err)
		}
	})
	t.Run("冲突时不覆盖别人的标记", func(t *testing.T) {
		setOwnerApp(t, "moo")
		dir := t.TempDir()
		writeOwner(t, dir, "moo-dev\n")
		t.Setenv("MOO_DATA", dir)
		if err := EnsureDataDirOwner(dir); err == nil {
			t.Fatal("冲突时应当返回错误")
		}
		b, _ := os.ReadFile(filepath.Join(dir, DataDirOwnerFile))
		if strings.TrimSpace(string(b)) != "moo-dev" {
			t.Fatalf("别人的标记被改写了: %q", string(b))
		}
	})
}
