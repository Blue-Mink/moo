package api

// 0.6.207-panel：启动时清理数据目录中的历史凭据残留。
//
// 旧版本（≤0.6.205）曾把面板口令/OAuth 会话明文落盘（config.json.bak-* 备份
// 快照、dc_session.json、official_session.json、含明文口令的 backup 快照）；
// 0.6.220 起口令/渠道密钥改 AES-256-GCM 密文存储（enc:v1:…，见 internal/secret），
// 但旧版本已落盘的残留文件不会自动消失。本模块在启动时扫描数据目录，
// 删除顶层模式残留 + 含明文口令的备份快照，并逐条留痕（logx 脱敏层之外的第二道卫生）。
// （0.6.312：official_session.json 移出清理名单——它是现役 OAuth 会话文件，
//  历史「三类模式」之一的定位已过时，详见 CleanResidualCredentials 注释。）
//
// 注：2026-10-01 查证发现 0.6.207–0.6.219 期间该注释所述的"密文存储"实际
// 并未实现（全仓无加解密代码），一直是明文；0.6.220 才真正落地，故本模块
// 的"非 enc:v1: 前缀即明文"判据现在才名副其实。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// CleanResidualCredentials 扫描数据目录并删除历史凭据残留：
//   - config.json.bak-*   旧版明文口令备份快照
//   - *.bak-removed       旧版会话文件删除失败的占位残留
//   - backups/*.json      仅当内含明文 panel_password（非 enc:v1: 密文）时删除
//
// 0.6.312 修复：official_session.json 从清理名单移除。0.6.207 起它被当作
// 「旧版明文 OAuth 会话残留」每次启动删除，但 0.6.255 起它就是现役 OAuth
// 会话文件（official.Manager.SessionFile，LoadSession 启动即读）——每次
// 重启清空官方源授权，用户被迫反复点 🔑 重新授权（2026-10-07 实锤：
// 主测试机重装后官方目录 -306 应用，日志三处「已清理历史凭据残留」）。
// 旧版残留与现役文件同名不可区分，且现役文件恒为当前二进制读写的那份，
// 删除必然误伤。后续若会话改 enc:v1: 密文存储，可再加「明文旧会话迁移」
// 专段（读旧文件→加密→写回），而不是启动即删。
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

	// 顶层模式（不递归，避免误伤 downloads/staging 子目录里的用户文件）。
	// 注意：official_session.json 是现役 OAuth 会话文件，不得列入（见函数注释
	// 0.6.312 修复说明）。
	patterns := []string{
		"config.json.bak-*",
		"*.bak-removed",
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
