package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/secret"
)

func testSealer(t *testing.T, seed byte) *secret.Sealer {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return secret.New(key)
}

func resetCodec(t *testing.T) {
	t.Helper()
	prevC, prevE := codec, extraCodec
	codec, extraCodec, migrationNeeded = nil, nil, false
	t.Cleanup(func() { codec, extraCodec, migrationNeeded = prevC, prevE, false })
}

// 正常路径：Save 落盘为密文、Load 解回明文、再次 Load 不需要迁移。
func TestSaveSealsLoadOpens(t *testing.T) {
	resetCodec(t)
	dir := t.TempDir()
	SetCodec(testSealer(t, 7))

	cfg := Default()
	cfg.PanelPassword = "REDACTED-NAS-PANEL-PW"
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "enc:v1:") {
		t.Fatalf("落盘应为密文，实际: %s", raw[:200])
	}
	if strings.Contains(string(raw), "REDACTED-NAS-PANEL-PW") {
		t.Fatalf("落盘内容含明文口令")
	}
	// 内存实例不被改写
	if cfg.PanelPassword != "REDACTED-NAS-PANEL-PW" {
		t.Fatalf("Save 不应改写内存明文: %q", cfg.PanelPassword)
	}
	// 密钥文件权限
	st, err := os.Stat(filepath.Join(dir, secret.KeyFile))
	if err == nil && st.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应 0600: %o", st.Mode().Perm())
	}

	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.PanelPassword != "REDACTED-NAS-PANEL-PW" {
		t.Fatalf("Load 应解回明文: %q", reloaded.PanelPassword)
	}
	if MigrationNeeded() {
		t.Fatalf("密文配置不应触发迁移")
	}
	if reloaded.SecretDecryptFailed {
		t.Fatalf("正常解密不应打失败标记")
	}
}

// 存量明文：Load 识别为需迁移 → Save 改写为密文 → 之后不再需要迁移。
func TestLegacyPlaintextMigrates(t *testing.T) {
	resetCodec(t)
	dir := t.TempDir()
	SetCodec(testSealer(t, 7))

	legacy := []byte(`{"panel_username":"fnos","panel_password":"legacy-pass"}`)
	if err := os.WriteFile(Path(dir), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PanelPassword != "legacy-pass" {
		t.Fatalf("旧明文应原样可用: %q", cfg.PanelPassword)
	}
	if !MigrationNeeded() {
		t.Fatalf("明文配置应触发迁移标记")
	}
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("迁移 Save: %v", err)
	}
	saved, _ := os.ReadFile(Path(dir))
	if strings.Contains(string(saved), "legacy-pass") || !strings.Contains(string(saved), "enc:v1:") {
		t.Fatalf("迁移后应为密文: %s", saved)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if MigrationNeeded() {
		t.Fatalf("迁移完成后不应再需要迁移")
	}
}

// 密钥变化（换机器/密钥文件丢失）：Load 不崩，把口令按未设置处理并打标记。
func TestDecryptFailureDegrades(t *testing.T) {
	resetCodec(t)
	dir := t.TempDir()
	SetCodec(testSealer(t, 7))
	cfg := Default()
	cfg.PanelPassword = "secret-pw"
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}

	SetCodec(testSealer(t, 99)) // 换密钥
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("解密失败不应让 Load 报错: %v", err)
	}
	if reloaded.PanelPassword != "" {
		t.Fatalf("解不开时应按未设置处理，实际 %q", reloaded.PanelPassword)
	}
	if !reloaded.SecretDecryptFailed {
		t.Fatalf("应打上解密失败标记")
	}
}

// 未注入加密器：保持旧行为（明文读写）。
func TestNoCodecKeepsPlaintext(t *testing.T) {
	resetCodec(t)
	dir := t.TempDir()
	cfg := Default()
	cfg.PanelPassword = "plain-pw"
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path(dir))
	if !strings.Contains(string(raw), "plain-pw") {
		t.Fatalf("无加密器时应明文落盘")
	}
}

// 扩展钩子（notify 渠道参数）在 Save/Load 两侧都被调用。
func TestExtraCodecHookInvoked(t *testing.T) {
	resetCodec(t)
	dir := t.TempDir()
	SetCodec(testSealer(t, 7))
	var seals, opens int
	SetExtraCodec(func(c *Config, seal bool) (bool, error) {
		if seal {
			seals++
		} else {
			opens++
		}
		return false, nil
	})
	cfg := Default()
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	if seals != 1 || opens != 1 {
		t.Fatalf("钩子调用次数异常: seal=%d open=%d", seals, opens)
	}
}
