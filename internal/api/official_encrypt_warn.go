package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// 2026-11-26（批次 C）：会话加密「已开启但文件仍是明文」的一次性提醒。
//
// 为什么需要：开关显示「开」、磁盘却是明文时，界面提示容易被忽略；这类
// 「以为安全了其实没有」必须主动推一条。节流状态落 cache/official_encrypt_warn.json，
// 同一台机器 24 小时内只推一次（重启不重置，避免崩溃循环刷屏）。

const officialEncryptWarnGap = 24 * time.Hour
const officialEncryptWarnKey = "official_session_encrypt_mismatch"

type officialEncryptWarnState struct {
	LastAt map[string]int64 `json:"last_at"`
}

func (s *Server) officialEncryptWarnPath() string {
	return filepath.Join(dataDirOf(s), "cache", "official_encrypt_warn.json")
}

// warmedRecently 读节流状态；失败按「未推送过」处理（宁推一次不漏）。
func (s *Server) warnedRecently(key string, gap time.Duration) bool {
	b, err := os.ReadFile(s.officialEncryptWarnPath())
	if err != nil {
		return false
	}
	var st officialEncryptWarnState
	if json.Unmarshal(b, &st) != nil || st.LastAt == nil {
		return false
	}
	at := st.LastAt[key]
	return at > 0 && time.Since(time.Unix(at, 0)) < gap
}

func (s *Server) markWarned(key string) {
	p := s.officialEncryptWarnPath()
	var st officialEncryptWarnState
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.LastAt == nil {
		st.LastAt = map[string]int64{}
	}
	st.LastAt[key] = time.Now().Unix()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if b, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

// MaybeWarnOfficialEncrypt 启动后检查一次；满足「配置要求加密、文件仍是明文」时推一条。
// ctx 仅用于与启动流程一致（立即返回，不常驻）。
func (s *Server) MaybeWarnOfficialEncrypt(ctx context.Context) {
	_ = ctx
	if s.Official == nil || !s.Official.EncryptionMismatch() {
		return
	}
	if s.warnedRecently(officialEncryptWarnKey, officialEncryptWarnGap) {
		return
	}
	s.notifyEvent(officialEncryptWarnKey, "会话加密未生效",
		"已开启「加密保存会话凭据」，但当前会话文件仍是明文（多为旧版本重新授权覆盖）。重新授权一次即会转为密文；在设置里关闭再开启也可立即重写。",
		false)
	s.markWarned(officialEncryptWarnKey)
}
