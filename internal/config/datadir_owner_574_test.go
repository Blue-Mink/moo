package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-11-26（0.6.574 批次①）：归属标记四条 P1 的行为用例。
// 反向验证：逐条摘除实现里的守卫后，下列用例必须 FAIL。

// 1) P1-a：旁路（显式 MOO_DATA）借用别人已有目录时**不得**盖章。
func TestEnsureDoesNotClaimBorrowedDir(t *testing.T) {
	dir := t.TempDir() // 已存在 = 借用语义
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo")

	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatalf("借用模式应当放行，实际 %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DataDirOwnerFile)); !os.IsNotExist(err) {
		t.Fatalf("借用别人的目录不应盖章（stat err=%v）", err)
	}
}

// 2) 本次新建的目录可以盖章（否则守卫在独立实例上永远起不来）。
func TestEnsureClaimsNewDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo")

	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, DataDirOwnerFile))
	if err != nil {
		t.Fatalf("新建目录应当盖章: %v", err)
	}
	if strings.TrimSpace(string(b)) != "moo" {
		t.Fatalf("标记内容 %q，期望 moo", string(b))
	}
}

// 3) P1-b：标记值随 appname 变化 —— moo 与 moo-dev 不再互相认成自己人。
func TestOwnerValueDistinguishesVariants(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo-dev")
	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, DataDirOwnerFile))
	if strings.TrimSpace(string(b)) != "moo-dev" {
		t.Fatalf("标记内容 %q，期望 moo-dev", string(b))
	}
	t.Setenv("MOO_APPNAME", "moo")
	if err := CheckDataDir(dir); err == nil {
		t.Fatal("moo 读 moo-dev 的标记应当拒绝")
	}
	t.Setenv("MOO_APPNAME", "moo-dev")
	if err := CheckDataDir(dir); err != nil {
		t.Fatalf("moo-dev 读自己的标记应当放行，实际 %v", err)
	}
}

// 4) 标记被换成符号链接/目录 → 拒绝（旧版 ReadFile 跟链接，等于可绕过）。
func TestOwnerMarkerNonRegularRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_APPNAME", "moo")
	t.Setenv("MOO_DATA", dir)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("moo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, DataDirOwnerFile)); err != nil {
		t.Fatal(err)
	}
	if err := CheckDataDirOwner(dir); err == nil {
		t.Fatal("符号链接标记应当拒绝")
	}
}

// 5) 空标记：平台分配的目录（TRIM_PKGVAR == dir、目录名 == appname）里应当被补写。
func TestOwnerEmptyMarkerRepairedOnPlatformHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "moo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, DataDirOwnerFile)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOO_APPNAME", "moo")
	t.Setenv("MOO_DATA", "")
	t.Setenv("TRIM_PKGVAR", dir)

	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != "moo" {
		t.Fatalf("空标记应当补写为 moo，实际 %q", string(b))
	}
}

//  6. 旧版遗留值 "moo" 在「目录名 == 本应用名」时按自己人处理（迁移通道），
//     其它目录仍视为别人占用。
func TestLegacyMooMarkerMigration(t *testing.T) {
	own := filepath.Join(t.TempDir(), "moo-dev")
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, DataDirOwnerFile), []byte("moo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOO_APPNAME", "moo-dev")
	if err := CheckDataDirOwner(own); err != nil {
		t.Fatalf("同名目录里的旧值应当放行，实际 %v", err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, DataDirOwnerFile), []byte("moo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckDataDirOwner(other); err == nil {
		t.Fatal("非同名目录里的旧值仍应视为冲突")
	}
}

// 7) P1-c：Save 失败要计数（守卫拒绝与写盘失败分开）。
func TestSaveStatsCounters(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "some-other-app")
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", foreign)
	t.Setenv("MOO_APPNAME", "moo")

	beforeRej, _ := SaveStats()
	cfg := &Config{WebPort: "38100"}
	if err := cfg.Save(foreign); err == nil {
		t.Fatal("守卫应当拒绝")
	}
	afterRej, _ := SaveStats()
	if afterRej != beforeRej+1 {
		t.Fatalf("守卫拒绝计数 %d → %d，期望 +1", beforeRej, afterRej)
	}
}

// 8) 名字启发式跟随 appname：TRIM_PKGVAR 指向 moo-dev 目录时，moo-dev 可启动。
func TestCheckDataDirNameFollowsAppName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "moo-dev")
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", dir)
	t.Setenv("MOO_APPNAME", "moo-dev")
	if err := CheckDataDir(dir); err != nil {
		t.Fatalf("moo-dev 使用自己的目录应当放行，实际 %v", err)
	}
	t.Setenv("MOO_APPNAME", "moo")
	if err := CheckDataDir(dir); err == nil {
		t.Fatal("moo 使用 moo-dev 的目录应当拒绝")
	}
}

// 10) 写盘失败也要计数（F6）：把 config.json 位置占成目录 → WriteFile 失败。
// 反向验证：删掉 WriteFile 分支的 saveFailed.Add(1) → 本用例 FAIL。
func TestSaveFailureCounter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo")
	if err := os.MkdirAll(Path(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	_, before := SaveStats()
	cfg := &Config{WebPort: "38100"}
	if err := cfg.Save(dir); err == nil {
		t.Fatal("config.json 被目录占用，写盘应当失败")
	}
	_, after := SaveStats()
	if after != before+1 {
		t.Fatalf("写盘失败计数 %d → %d，期望 +1", before, after)
	}
}

// 9) 旧值 "moo" 在「平台分配的同名目录」里迁移为本应用 appname ——
// 否则 moo 会一直把 moo-dev 的目录认成自己家（互认问题只修一半）。
func TestLegacyMooMarkerUpgradedOnPlatformHome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "moo-dev")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, DataDirOwnerFile)
	if err := os.WriteFile(p, []byte("moo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOO_APPNAME", "moo-dev")
	t.Setenv("MOO_DATA", "")
	t.Setenv("TRIM_PKGVAR", dir)

	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.TrimSpace(string(b)) != "moo-dev" {
		t.Fatalf("旧标记应迁移为 moo-dev，实际 %q", string(b))
	}
	t.Setenv("MOO_APPNAME", "moo")
	if err := CheckDataDirOwner(dir); err == nil {
		t.Fatal("迁移后 moo 读该目录应当拒绝")
	}
}
