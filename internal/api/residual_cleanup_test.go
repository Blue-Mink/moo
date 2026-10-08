package api

import (
	"os"
	"path/filepath"
	"testing"
)

// 0.6.312 回归：official_session.json 是现役 OAuth 会话文件，启动清理不得删除；
// 其余历史凭据模式仍须照常清理。
func TestCleanResidualCredentialsKeepsLiveOAuthSession(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("official_session.json", `{"access_token":"live"}`)
	mk("official_session.json.1", `{"access_token":"rotated"}`)
	mk("config.json.bak-20260101", `{"panel_password":"plain"}`)
	mk("foo.bak-removed", "")
	mk("backups/snap.json", `{"panel_password":"plain"}`)
	mk("backups/snap-enc.json", `{"panel_password":"enc:v1:xx"}`)
	mk("config.json", `{}`) // 现役配置不得动

	removed := CleanResidualCredentials(dir)

	for _, keep := range []string{
		"official_session.json",
		"official_session.json.1",
		"config.json",
		"backups/snap-enc.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("现役/密文文件被误删: %s (removed=%v)", keep, removed)
		}
	}
	for _, gone := range []string{
		"config.json.bak-20260101",
		"foo.bak-removed",
		"backups/snap.json",
	} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("历史残留未清理: %s", gone)
		}
	}
}
