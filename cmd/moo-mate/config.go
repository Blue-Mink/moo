package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// mateConfig 伴侣配置。
// 0.2.28 起 GitHub token 不再在伴侣侧保存：统一读取 Moo 设置里的那一份
// （见 companion.githubToken），避免两处维护、两处泄露面。
type mateConfig struct {
	SyncInterval string `json:"sync_interval,omitempty"` // 同步间隔：Go duration 或 "manual"/""（不自动）

	// 数据备份目录（空 = 数据目录本身；可指向 /volN 下任意目录，参考 Moo backup_dir）。
	BackupDir string `json:"backup_dir,omitempty"`

	// 自动备份（参考 Moo 备份设置）：开启后每 N 天自动写一份快照，
	// 同目录保留最近 backupRetention 份，更早自动淘汰。
	BackupAuto         bool `json:"backup_auto,omitempty"`
	BackupIntervalDays int  `json:"backup_interval_days,omitempty"`

	// 0.2.38：设置页「日志」卡片——查看行数（50/100/200/500/1000），对齐 Moo 的 log_lines。
	LogLines int `json:"log_lines,omitempty"`

	// 0.2.37：远程清单（空 = 内置默认公网地址；测试期可指向本地 Gitea）。
	// DefaultIndexURL 是「出厂默认应用列表」，仅在首次安装/索引缺失时读取；
	// ChangelogURL 是 mate-changelog.md，版本药丸弹窗读最新一段。
	DefaultIndexURL string `json:"default_index_url,omitempty"`
	ChangelogURL    string `json:"changelog_url,omitempty"`
}

// validLogLines 允许的日志行数档位（与 Moo 的 50/100/200/500/1000 一致）。
func validLogLines(n int) bool {
	switch n {
	case 50, 100, 200, 500, 1000:
		return true
	}
	return false
}

// logLines 生效的日志查看行数（非法/缺省 = 200）。
func (c *mateConfig) logLines() int {
	if validLogLines(c.LogLines) {
		return c.LogLines
	}
	return 200
}

// 0.2.37：远程清单默认地址（公网）。代码里不写内网地址；本地测试用配置覆盖。
const (
	defaultMateChangelogURL = "https://raw.githubusercontent.com/Blue-Mink/moo/main/mate-changelog.md"
	defaultMateIndexURL     = "https://raw.githubusercontent.com/Blue-Mink/moo/main/mate-default-index.json"
)

// changelogURL 生效的更新日志清单地址。
func (c *mateConfig) changelogURL() string {
	if v := strings.TrimSpace(c.ChangelogURL); v != "" {
		return v
	}
	return defaultMateChangelogURL
}

// defaultIndexURL 生效的出厂默认应用列表地址。
func (c *mateConfig) defaultIndexURL() string {
	if v := strings.TrimSpace(c.DefaultIndexURL); v != "" {
		return v
	}
	return defaultMateIndexURL
}

// backupRetention 同目录保留的备份份数（与 Moo 一致）。
const backupRetention = 20

// defaultBackupIntervalDays 备份周期默认值（天）。
const defaultBackupIntervalDays = 7

// backupIntervalDays 返回生效的备份周期天数（clamp 1–30，默认 7）。
func (c *mateConfig) backupIntervalDays() int {
	if c.BackupIntervalDays < 1 || c.BackupIntervalDays > 30 {
		return defaultBackupIntervalDays
	}
	return c.BackupIntervalDays
}

// validSyncInterval 校验间隔值："manual"/空（不自动）或任意合法 Go duration。
func validSyncInterval(v string) bool {
	if v == "" || v == "manual" {
		return true
	}
	_, err := time.ParseDuration(v)
	return err == nil
}

// syncInterval 返回生效的同步间隔：配置合法即用，否则回落默认 1h。
func (c *mateConfig) syncInterval() string {
	if validSyncInterval(c.SyncInterval) && c.SyncInterval != "" {
		return c.SyncInterval
	}
	return "1h"
}

// loadMateConfig 加载配置；文件不存在返回空配置（不报错）。
func loadMateConfig(path string) (*mateConfig, error) {
	cfg := &mateConfig{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// save 落盘（目录自动建，0600）。
func (c *mateConfig) save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
