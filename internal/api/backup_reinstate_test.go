package api

import (
	"testing"

	"moo/internal/config"
)

// 0.6.215 回归：建备份（maskedConfigForBackup）不得污染 live 内存中的渠道凭据。
// 修复前 snap := *s.Cfg 的 NotifyChannels 与 live 共享底层数组，把掩码值写进
// snap 切片会就地改掉 live 渠道（0.6.144 潜伏 bug，0.6.215 测试机实锤：
// 建备份后任何一次 config 落盘都会把 **** 掩码写进磁盘，推送渠道静默失效）。
func TestMaskedConfigForBackupDoesNotAliasLive(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		NotifyChannels: []config.NotifyChannel{
			{ID: "ch_live", Type: "wecom",
				Params: map[string]string{"webhook_url": "https://qyapi.weixin.qq.com/REALKEY"}},
		},
	}}
	snap := s.maskedConfigForBackup()
	// 备份内应是掩码
	if got := snap.NotifyChannels[0].Params["webhook_url"]; got == "https://qyapi.weixin.qq.com/REALKEY" {
		t.Fatalf("备份未掩码: %q", got)
	}
	// live 必须保持真值
	if got := s.Cfg.NotifyChannels[0].Params["webhook_url"]; got != "https://qyapi.weixin.qq.com/REALKEY" {
		t.Fatalf("live 渠道被建备份污染（别名 bug）: %q", got)
	}
}

// 0.6.215 折中 P0：恢复前回填 live 凭据
func TestReinstateSecrets(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		PanelPassword: "REDACTED-OLD-PANEL-PW",
		NotifyChannels: []config.NotifyChannel{
			{ID: "ch_live", Type: "wecom", Name: "企微",
				Params: map[string]string{"webhook_url": "https://qyapi.weixin.qq.com/REAL"}},
		},
	}}
	restored := &config.Config{
		PanelPassword: "", // 备份恒置空（omitempty → 缺省 ""）
		NotifyChannels: []config.NotifyChannel{
			{ID: "ch_live", Type: "wecom", Name: "企微",
				Params: map[string]string{"webhook_url": "****d557"}},
			{ID: "ch_deleted_live", Type: "wecom",
				Params: map[string]string{"webhook_url": "****gone"}},
		},
	}
	s.reinstateSecrets(restored)

	if restored.PanelPassword != "REDACTED-OLD-PANEL-PW" {
		t.Fatalf("口令未回填: %q", restored.PanelPassword)
	}
	if got := restored.NotifyChannels[0].Params["webhook_url"]; got != "https://qyapi.weixin.qq.com/REAL" {
		t.Fatalf("webhook 未回填: %q", got)
	}
	// live 已删的渠道：无从回填，保持掩码（该渠道本就不该再发）
	if got := restored.NotifyChannels[1].Params["webhook_url"]; got != "****gone" {
		t.Fatalf("非 live 渠道不应回填: %q", got)
	}
}
