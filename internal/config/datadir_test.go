package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-11-26（批次 B）：数据目录来源校验。语义细化后只有「dir 与继承的
// TRIM_PKGVAR 是同一路径」时才判目录名，其它路径一律放行（单测临时目录
// 不该被误杀）。
func TestCheckDataDir(t *testing.T) {
	setOwnerApp(t, "moo") // 0.6.574：身份改为 MOO_APPNAME/构建注入，用例必须固定
	foreign := "/appdata/other-app"
	// 0.6.574：归属标记改为 fail-closed（读不到就拒绝），原来这条用例拿真实
	// 生产数据目录当「放行」样本 —— 非 root 跑测试必然读不到标记（0600 root）。
	// 改用临时目录，语义不变、不碰生产。
	mooDir := filepath.Join(t.TempDir(), "moo")
	if err := os.MkdirAll(mooDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		dir     string
		data    string
		allow   string
		pkgvar  string
		wantErr bool
	}{
		{"外来 env 且 dir==env 拒绝", foreign, "", "", foreign, true},
		{"外来 env 尾斜杠归一后仍拒绝", foreign + "/", "", "", foreign, true},
		{"dir 是别的临时目录则放行", "/tmp/moo-unit-cfg", "", "", foreign, false},
		{"目录名为 moo 放行", mooDir, "", "", mooDir, false},
		{"显式 MOO_DATA 信任", foreign, "/tmp/x", "", foreign, false},
		{"显式放行开关", foreign, "", "1", foreign, false},
		{"没有任何环境变量", "/tmp/anywhere", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("MOO_DATA", c.data)
			t.Setenv("MOO_ALLOW_FOREIGN_DATA", c.allow)
			t.Setenv("TRIM_PKGVAR", c.pkgvar)
			err := CheckDataDir(c.dir)
			if c.wantErr && err == nil {
				t.Fatal("应当拒绝，实际通过")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("应当通过，实际 %v", err)
			}
		})
	}
}

// TestSaveRefusesForeignDataDir：继承的外来 TRIM_PKGVAR 直接决定落点时必须
// 拒绝写入 —— 返回错误，且不建目录、不产生 config.json。
// 反向验证：删掉 Save 里的 CheckDataDir 调用后本用例必须 FAIL。
func TestSaveRefusesForeignDataDir(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "some-other-app")
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", foreign)

	cfg := &Config{WebPort: "38100"}
	if err := cfg.Save(foreign); err == nil {
		t.Fatal("外来数据目录应当拒绝写入，实际 Save 返回 nil")
	}
	if _, err := os.Stat(Path(foreign)); !os.IsNotExist(err) {
		t.Fatalf("被拒绝后不应落盘 config.json（stat err=%v）", err)
	}
	if _, err := os.Stat(foreign); !os.IsNotExist(err) {
		t.Fatalf("被拒绝后不应创建目录（stat err=%v）", err)
	}
}

// TestSaveAllowsOwnDataDir：正常情况（没有外来 TRIM_PKGVAR）照旧能写。
func TestSaveAllowsOwnDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", "")
	t.Setenv("MOO_ALLOW_FOREIGN_DATA", "")
	t.Setenv("TRIM_PKGVAR", "")

	cfg := &Config{WebPort: "38100"}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("正常目录应当能写: %v", err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config.json 权限 %o，期望 600", fi.Mode().Perm())
	}
}
