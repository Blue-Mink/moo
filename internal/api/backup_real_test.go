package api

import (
	"encoding/json"
	"os"
	"testing"

	"moo/internal/config"
)

// 用测试机真实备份文件复刻 restoreBackup 的凭据序列（0.6.215 折中 P0 回归）。
// 备份 = /tmp/real-backup.json（scp 自测试机 backups/moo-backup-20261001-005610.json）。
func TestRestoreRealBackupReinstate(t *testing.T) {
	if os.Getenv("MOO_REAL_BACKUP_TEST") == "" {
		t.Skip("需 MOO_REAL_BACKUP_TEST=1（含真实凭据，默认跳过）")
	}
	data, err := os.ReadFile("/tmp/real-backup.json")
	if err != nil {
		t.Fatalf("读备份: %v", err)
	}
	var snap BackupSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal 备份: %v", err)
	}
	var restored config.Config
	if err := json.Unmarshal(snap.Config, &restored); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	s := &Server{Cfg: &config.Config{
		PanelPassword: "REDACTED-OLD-PANEL-PW",
		NotifyChannels: []config.NotifyChannel{
			{ID: "ch_20260927175858ox8k", Type: "wecom",
				Params: map[string]string{"webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=REALKEY"}},
		},
	}}
	s.reinstateSecrets(&restored)
	out, _ := json.Marshal(restored)
	t.Logf("restore 结果: %s", out[:min(400, len(out))])
	ch := restored.NotifyChannels[0]
	if got := ch.Params["webhook_url"]; got != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=REALKEY" {
		t.Fatalf("webhook 未回填: %q", got)
	}
	if restored.PanelPassword != "REDACTED-OLD-PANEL-PW" {
		t.Fatalf("口令未回填: %q", restored.PanelPassword)
	}
}

func min(a, b int) int { if a < b { return a }; return b }
