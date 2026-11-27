package official

// oauth_session_migrate_4a_test.go —— 盲点补测 批次 4a · T3（FIXPLAN 盲点矩阵 #7）
//
// 场景：用户升级到「会话加密」版本 → 回滚到旧版本 → 再升级回来。
// 期望（设计文档 oauth.go 注释 + 外壳常量 sessionEnvelopeNote 的承诺）：
//   ① 加密外壳是**合法 JSON 且 access_token 为空串** → 旧版本解析成功、
//      显示「未连接」，既不 panic 也不把密文当 token；
//   ② 回滚期间文件不被旧版改写/删除，token 仍可解；
//   ③ 再升级回来（同密钥）→ access_token / refresh_token 逐字一致。
//
// 与既有 oauth_session_encrypt_test.go 的分工：那边锁单步形态（a/b/c/d/e/f/g），
// 这里只锁「三阶段迁移闭环」——尤其是"旧版读取"两种模型（文件层明文解析 +
// 无 sealer 的 LoadSession）都不得泄露密文。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"moo/internal/secret"
)

// TestSessionMigrate_EncryptThenOldVersionThenBack 三阶段：加密 → 旧版读 → 回来。
// 反向验证：把 LoadSession 的「无 sealer」分支改成 m.session = &Session{AccessToken: env.Enc}
// （= 把密文当 token 用）→ 阶段 2 的"未连接"断言 FAIL。
func TestSessionMigrate_EncryptThenOldVersionThenBack(t *testing.T) {
	dir := t.TempDir()

	// ── 阶段 0：旧版本（未开加密）写入明文会话 ──
	base := NewManagerWithBase(dir, "http://127.0.0.1:1")
	seed := &Session{
		AccessToken:  "at-migrate",
		RefreshToken: "rt-migrate",
		ExpiresAt:    time.Now().Add(2 * time.Hour).UnixMilli(),
		Scopes:       []string{"trim.app.center"},
		UpdatedAt:    time.Now().UnixMilli(),
	}
	putSession(t, base, seed)
	plain, err := os.ReadFile(base.SessionFile())
	if err != nil {
		t.Fatalf("阶段 0 读文件失败: %v", err)
	}
	if strings.Contains(string(plain), secret.Prefix) {
		t.Fatalf("阶段 0 未开加密，文件不应含密文前缀: %.60q", plain)
	}

	// ── 阶段 1：升级到新版（有 sealer）+ 开启加密 ──
	up := NewManagerWithBase(dir, "http://127.0.0.1:1")
	up.SetSealer(testSealer(11))
	up.LoadSession()
	if s := up.Session(); s == nil || s.AccessToken != seed.AccessToken {
		t.Fatalf("升级后必须仍能读到明文会话（兼容旧版写入），实际 %+v", s)
	}
	if err := up.SetEncryption(true); err != nil {
		t.Fatalf("开启会话加密失败: %v", err)
	}
	enc, err := os.ReadFile(up.SessionFile())
	if err != nil {
		t.Fatalf("阶段 1 读文件失败: %v", err)
	}
	if !strings.Contains(string(enc), secret.Prefix) {
		t.Fatalf("开启加密后文件必须含 %q 前缀: %.80q", secret.Prefix, enc)
	}
	if strings.Contains(string(enc), seed.AccessToken) {
		t.Fatal("加密落盘后文件里不得出现明文 access_token")
	}

	// ── 阶段 2a：模拟旧版本「文件层明文解析」──
	// 旧二进制只会 json.Unmarshal 成 Session；外壳是合法 JSON，得到空 token。
	var legacy Session
	if err := json.Unmarshal(enc, &legacy); err != nil {
		t.Fatalf("旧版本 Unmarshal 外壳不得报错（否则会弹解析错误而不是未连接）: %v", err)
	}
	if legacy.AccessToken != "" {
		t.Fatalf("旧版本必须读到空 token（视为未连接），实际 %q（= 把密文当 token 泄漏）", legacy.AccessToken)
	}
	if legacy.Valid(time.Now()) {
		t.Fatal("旧版本视角必须是未授权状态")
	}
	var raw map[string]any
	if err := json.Unmarshal(enc, &raw); err != nil {
		t.Fatalf("外壳必须是合法 JSON 对象: %v", err)
	}
	if v, _ := raw["v"].(float64); int(v) != sessionEnvelopeVersion {
		t.Fatalf("外壳 v 应为 %d，实际 %v", sessionEnvelopeVersion, raw["v"])
	}
	if s, _ := raw["enc"].(string); !strings.HasPrefix(s, secret.Prefix) {
		t.Fatalf("外壳 enc 应以 %q 开头，实际 %.20q", secret.Prefix, s)
	}

	// ── 阶段 2b：模拟旧版本「无 sealer 的 LoadSession」──
	legacyM := NewManagerWithBase(dir, "http://127.0.0.1:1")
	legacyM.LoadSession() // 不得 panic
	if got := legacyM.Session(); got != nil {
		t.Fatalf("无密钥的旧版实例必须视为未连接，实际拿到会话 %+v", got)
	}
	if st := legacyM.Status(time.Now()); st["authorized"] != false {
		t.Fatalf("旧版/无密钥实例 authorized 必须为 false，实际 %v", st["authorized"])
	}
	if after, err := os.ReadFile(legacyM.SessionFile()); err != nil || string(after) != string(enc) {
		t.Fatalf("旧版读取不得改动会话文件（err=%v）", err)
	}

	// ── 阶段 3：再升级回来（同密钥）→ 内容逐字一致 ──
	back := NewManagerWithBase(dir, "http://127.0.0.1:1")
	back.SetSealer(testSealer(11))
	back.LoadSession()
	s := back.Session()
	if s == nil {
		t.Fatal("升级回来必须能解出会话")
	}
	if s.AccessToken != seed.AccessToken || s.RefreshToken != seed.RefreshToken {
		t.Fatalf("会话内容不得变化: at=%q rt=%q", s.AccessToken, s.RefreshToken)
	}
	if len(s.Scopes) != 1 || s.Scopes[0] != "trim.app.center" {
		t.Fatalf("scopes 必须保真，实际 %v", s.Scopes)
	}
	if !back.SessionEncrypted() {
		t.Fatal("加载加密外壳后 SessionEncrypted() 应为 true")
	}
	if msg, ok := back.Status(time.Now())["last_error"]; ok {
		t.Fatalf("正常解密不应有 last_error，实际 %v", msg)
	}
}
