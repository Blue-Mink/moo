package api

// 0.6.207-panel：启动时清理数据目录中的历史凭据残留。
//
// 旧版本（≤0.6.205）曾把面板口令/OAuth 会话明文落盘（config.json.bak-* 备份
// 快照、dc_session.json、official_session.json、含明文口令的 backup 快照）；
// 206-panel 起口令改 AES-256-GCM 密文存储（enc:v1:…），但旧版本已落盘的残留
// 文件不会自动消失。本模块在启动时扫描数据目录，删除三类模式残留 +
// 含明文口令的备份快照，并逐条留痕（logx 脱敏层之外的第二道卫生）。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// CleanResidualCredentials 扫描数据目录并删除历史凭据残留：
//   - config.json.bak-*        旧版明文口令备份快照
//   - *.bak-removed            旧版会话文件删除失败的占位残留
//   - official_session.json*   OAuth 会话文件（token 明文）
//   - backups/*.json           仅当内含明文 panel_password（非 enc:v1: 密文）时删除
//
// 返回被删除文件的基名列表（供调用方汇总留痕）。JSON 解析失败的备份一律保留
// （不误删未知文件）。
func CleanResidualCredentials(dataDir string) []string {
	var removed []string
	del := func(p string) {
		if err := os.Remove(p); err == nil {
			removed = append(removed, filepath.Base(p))
			log.Printf("[security] 已清理历史凭据残留: %s", filepath.Base(p))
		}
	}

	// 顶层模式（不递归，避免误伤 downloads/staging 子目录里的用户文件）
	patterns := []string{
		"config.json.bak-*",
		"*.bak-removed",
		"official_session.json",
		"official_session.json.*",
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(filepath.Join(dataDir, pat))
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil || fi.IsDir() {
				continue
			}
			del(m)
		}
	}

	// 备份快照：仅当内含明文口令时删除（密文/无该字段的保留）
	matches, _ := filepath.Glob(filepath.Join(dataDir, "backups", "*.json"))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil || fi.IsDir() {
			continue
		}
		if backupHasPlaintextPassword(m) {
			del(m)
		}
	}
	return removed
}

// backupHasPlaintextPassword 递归查找备份 JSON 中的 panel_password 字段，
// 值为非空字符串且不是 enc:v1: 密文前缀 → 判含明文口令。
func backupHasPlaintextPassword(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	return containsPlaintextPassword(root)
}

func containsPlaintextPassword(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if k == "panel_password" {
				if s, ok := val.(string); ok && s != "" && !strings.HasPrefix(s, "enc:v1:") {
					return true
				}
				continue
			}
			if containsPlaintextPassword(val) {
				return true
			}
		}
	case []any:
		for _, item := range t {
			if containsPlaintextPassword(item) {
				return true
			}
		}
	}
	return false
}
