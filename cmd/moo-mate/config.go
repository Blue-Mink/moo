package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// mateConfig 伴侣配置。
// 0.2.28 起 GitHub token 不再在伴侣侧保存：统一读取 Moo 设置里的那一份
// （见 companion.githubToken），避免两处维护、两处泄露面。
type mateConfig struct {
	SoftstoreURL    string `json:"softstore_url,omitempty"`    // 个人源地址（非敏感，明文；前端不暴露）
	SoftstoreSource string `json:"softstore_source,omitempty"` // 软仓源选择："personal"（默认）| "gitee"
	SyncInterval    string `json:"sync_interval,omitempty"`    // 同步间隔：Go duration 或 "manual"/""（不自动）

	// 数据备份目录（空 = 数据目录本身；可指向 /volN 下任意目录，参考 Moo backup_dir）。
	BackupDir string `json:"backup_dir,omitempty"`

	// 自动备份（参考 Moo 备份设置）：开启后每 N 天自动写一份快照，
	// 同目录保留最近 backupRetention 份，更早自动淘汰。
	BackupAuto         bool `json:"backup_auto,omitempty"`
	BackupIntervalDays int  `json:"backup_interval_days,omitempty"`
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

// defaultSoftstoreURL 软仓个人源默认地址（私有，UI 不暴露）。
const defaultSoftstoreURL = "http://nas.hhxs2025.top:5660"

// defaultGiteeSoftstore 软仓 Gitee 源（软仓官方仓库，release 扫描）。
const defaultGiteeSoftstore = "https://gitee.com/hhxs2025/fn-appstores"

// softstoreURL 返回生效的个人源地址：配置优先，回落默认值。
func (c *mateConfig) softstoreURL() string {
	if v := c.SoftstoreURL; v != "" {
		return v
	}
	return defaultSoftstoreURL
}

// softstoreSource 返回生效的软仓源选择："gitee" 或 "personal"（默认）。
func (c *mateConfig) softstoreSource() string {
	if c.SoftstoreSource == "gitee" {
		return "gitee"
	}
	return "personal"
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
