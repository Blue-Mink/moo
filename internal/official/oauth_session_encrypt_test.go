package official

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"moo/internal/secret"
)

// 2026-11-26（加固 B2）：OAuth 会话落盘加密 = opt-in（默认关）。
//
// 本文件锁住四条约束：
//  1. 加密外壳仍是**合法 JSON 且 access_token 为空串** —— 旧版本 Unmarshal
//     成 Session 后得到「未连接」，既不报解析错误也不会崩；
//  2. 同密钥的新管理器能从外壳恢复会话（access_token/refresh_token 一致）；
//  3. 换错密钥 / 无密钥 → 不 panic、不删文件、lastErr 有值（密钥换回可复用）；
//  4. sealer 不可用时拒绝开启加密（无会话也一样），且落盘形态不变；
//     关闭加密立即写回明文，旧版本路径（无 sealer）也能加载。

func testSealer(b byte) *secret.Sealer {
	key := make([]byte, 32)
	for i := range key {
		key[i] = b
	}
	return secret.New(key)
}

func readSessionFile(t *testing.T, m *Manager) []byte {
	t.Helper()
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("读取会话文件失败: %v", err)
	}
	return b
}

// putSession 直接落盘并挂上内存会话（绕过网络换 token 的部分）。
func putSession(t *testing.T, m *Manager, s *Session) {
	t.Helper()
	if err := m.saveSessionLocked(s); err != nil {
		t.Fatalf("saveSessionLocked: %v", err)
	}
	m.mu.Lock()
	m.session = s
	m.mu.Unlock()
}

// seedEncryptedSession 造一个「已开启加密 + 已落盘」的管理器，返回数据目录。
func seedEncryptedSession(t *testing.T, at string) (string, *Manager) {
	t.Helper()
	dir := t.TempDir()
	m := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m.SetSealer(testSealer(7))
	putSession(t, m, &Session{
		AccessToken:  at,
		RefreshToken: "rt-" + at,
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
	})
	if err := m.SetEncryption(true); err != nil {
		t.Fatalf("开启会话加密失败: %v", err)
	}
	return dir, m
}

// (a) 开启加密后保存 → 文件合法 JSON、v=2/enc、access_token 为空；旧版本视角干净。
func TestSessionEncrypt_EnvelopeReadableByOldVersion(t *testing.T) {
	_, m := seedEncryptedSession(t, "at-a")
	if !m.EncryptionEnabled() || !m.SessionEncrypted() {
		t.Fatalf("状态错：enabled=%v encrypted=%v", m.EncryptionEnabled(), m.SessionEncrypted())
	}
	b := readSessionFile(t, m)
	if strings.Contains(string(b), "at-a") {
		t.Fatal("加密落盘后文件里不得出现明文 access_token")
	}
	var env map[string]any
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("外壳必须是合法 JSON（旧版本要能解析）: %v body=%s", err, b)
	}
	if v, _ := env["v"].(float64); int(v) != sessionEnvelopeVersion {
		t.Errorf("外壳 v 应为 %d，实际 %v", sessionEnvelopeVersion, env["v"])
	}
	enc, _ := env["enc"].(string)
	if !strings.HasPrefix(enc, secret.Prefix) {
		t.Errorf("enc 应以 %q 开头，实际 %.14q", secret.Prefix, enc)
	}
	if at, ok := env["access_token"]; !ok || at != "" {
		t.Errorf("外壳必须带空串 access_token（旧版本据此显示「未连接」），实际 %v", at)
	}
	// 旧版本视角：直接 Unmarshal 成 Session，不得报错。
	var old Session
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatalf("旧版本 Unmarshal 外壳不得报错: %v", err)
	}
	if old.AccessToken != "" {
		t.Errorf("旧版本应读到空 token，实际 %q", old.AccessToken)
	}
	if old.Valid(time.Now()) {
		t.Error("旧版本视角应为未授权（authorized=false）")
	}
	// 权限仍是 0600（复用既有原子写的 chmod）。
	fi, err := os.Stat(m.SessionFile())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("会话文件权限应为 0600，实际 %v", fi.Mode().Perm())
	}
}

// (b) 同 sealer 的新管理器 LoadSession → 会话完整恢复。
func TestSessionEncrypt_LoadRestoresSessionWithSealer(t *testing.T) {
	dir, _ := seedEncryptedSession(t, "at-b")
	m2 := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m2.SetSealer(testSealer(7))
	m2.LoadSession()
	s := m2.Session()
	if s == nil {
		t.Fatal("同密钥应能解出会话，实际 nil")
	}
	if s.AccessToken != "at-b" || s.RefreshToken != "rt-at-b" {
		t.Errorf("会话内容不一致: at=%q rt=%q", s.AccessToken, s.RefreshToken)
	}
	if !m2.SessionEncrypted() {
		t.Error("加载加密文件后 SessionEncrypted() 应为 true")
	}
	if e, ok := m2.Status(time.Now())["last_error"]; ok {
		t.Errorf("正常解密不应有 last_error，实际 %v", e)
	}
}

// (c) 换错密钥 / 无密钥 → 不 panic、不删文件、lastErr 有值。
func TestSessionEncrypt_WrongOrMissingKeyKeepsFile(t *testing.T) {
	dir, _ := seedEncryptedSession(t, "at-c")
	before := readSessionFile(t, &Manager{dataDir: dir})

	// c1：密钥不符（换了 .moo-secret.key）
	m := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m.SetSealer(testSealer(9)) // 与写盘时的 7 不同
	m.LoadSession()            // 不得 panic
	if m.Session() != nil {
		t.Error("密钥不符时不得加载出会话（否则等于绕过加密）")
	}
	if msg, _ := m.Status(time.Now())["last_error"].(string); !strings.Contains(msg, "需重新授权") {
		t.Errorf("应给出「需重新授权」类 lastErr，实际 %q", msg)
	}
	if after := readSessionFile(t, m); string(after) != string(before) {
		t.Error("解密失败不得改动/删除会话文件")
	}

	// c2：完全没注入 sealer（密钥文件损坏 → main 跳过 SetSealer）
	m2 := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m2.LoadSession()
	if m2.Session() != nil {
		t.Error("无密钥时应视为未连接")
	}
	if msg, _ := m2.Status(time.Now())["last_error"].(string); !strings.Contains(msg, "密钥不可用") {
		t.Errorf("应给出「密钥不可用」lastErr，实际 %q", msg)
	}
	if after := readSessionFile(t, m2); string(after) != string(before) {
		t.Error("无密钥时同样不得删除会话文件")
	}
}

// (d) SetEncryption(true) 且 sealer==nil → 返回错误且落盘形态不变。
func TestSessionEncrypt_RefuseEnableWithoutSealer(t *testing.T) {
	t.Run("有会话：拒绝且字节不变", func(t *testing.T) {
		dir := t.TempDir()
		m := NewManagerWithBase(dir, "http://127.0.0.1:1")
		putSession(t, m, &Session{AccessToken: "at-d", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
		before := readSessionFile(t, m)
		if err := m.SetEncryption(true); err == nil {
			t.Fatal("本机密钥不可用时必须拒绝开启会话加密")
		}
		if m.EncryptionEnabled() || m.SessionEncrypted() {
			t.Errorf("拒绝后标志不得变化: enabled=%v encrypted=%v", m.EncryptionEnabled(), m.SessionEncrypted())
		}
		if after := readSessionFile(t, m); string(after) != string(before) {
			t.Error("拒绝后落盘形态不得变化")
		}
	})

	// 无会话也必须拒绝——否则标志被置真，之后任何保存都会失败/写不出去。
	t.Run("无会话：同样拒绝（不得只改标志）", func(t *testing.T) {
		m := NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1")
		if err := m.SetEncryption(true); err == nil {
			t.Fatal("无密钥时即使没有会话也必须拒绝开启")
		}
		if m.EncryptionEnabled() {
			t.Error("拒绝后 encrypt 标志必须仍为 false")
		}
	})
}

// (e) 关闭加密 → 立即写回明文，旧版本路径（无 sealer）也能加载。
func TestSessionEncrypt_DisableWritesBackPlaintext(t *testing.T) {
	dir, m := seedEncryptedSession(t, "at-e")
	if err := m.SetEncryption(false); err != nil {
		t.Fatalf("关闭会话加密失败: %v", err)
	}
	if m.SessionEncrypted() {
		t.Error("关闭后 SessionEncrypted() 应为 false")
	}
	b := readSessionFile(t, m)
	if strings.Contains(string(b), secret.Prefix) {
		t.Error("关闭后应写回明文，文件里不应再有密文前缀")
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("明文文件必须是合法 Session JSON: %v", err)
	}
	if s.AccessToken != "at-e" {
		t.Errorf("关闭后应保留原会话，实际 at=%q", s.AccessToken)
	}
	// 旧版本等价路径：不注入 sealer 也能正常加载。
	mOld := NewManagerWithBase(dir, "http://127.0.0.1:1")
	mOld.LoadSession()
	got := mOld.Session()
	if got == nil || got.AccessToken != "at-e" {
		t.Error("关闭加密后，无密钥的实例必须能正常加载会话")
	}
	if mOld.SessionEncrypted() {
		t.Error("明文文件不得标记为加密形态")
	}
}

// (f) 配置要求加密但文件是明文（旧版本重授权覆盖）→ 照常加载，不自动再加密。
func TestSessionEncrypt_CleartextWhileEnabledNotReEncrypted(t *testing.T) {
	dir := t.TempDir()
	// 先用「未开启加密」的管理器写一个明文会话（等价旧版本写的文件）。
	putSession(t, NewManagerWithBase(dir, "http://127.0.0.1:1"),
		&Session{AccessToken: "at-f", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})

	m := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m.SetSealer(testSealer(7))
	if err := m.SetEncryption(true); err != nil { // 无内存会话 = 只改标志
		t.Fatalf("置位加密标志失败: %v", err)
	}
	m.LoadSession()
	if m.Session() == nil {
		t.Fatal("明文会话文件必须照常加载（兼容旧版本写入）")
	}
	if m.SessionEncrypted() {
		t.Error("明文文件不得被标记成加密形态")
	}
	if !m.EncryptionMismatch() {
		t.Error("应记录「配置要求加密但文件是明文」")
	}
	if strings.Contains(string(readSessionFile(t, m)), secret.Prefix) {
		t.Error("不得自动再加密（尊重旧版本的重授权动作）")
	}
}

// (g) 2026-11-26 用户实报回归：先开「加密保存会话凭据」（当时无会话）→
// 之后再授权保存，磁盘形态标志必须同步为「已加密」，否则状态页/开关一直显示关闭
// （文件其实是加密的，用户以为没生效）。
func TestSessionEncrypt_ToggleBeforeAuthorizeMarksEncrypted(t *testing.T) {
	dir := t.TempDir()
	m := NewManagerWithBase(dir, "http://127.0.0.1:1")
	m.SetSealer(testSealer(9))
	if err := m.SetEncryption(true); err != nil { // 无内存会话 = 只改标志
		t.Fatalf("置位加密标志失败: %v", err)
	}
	if m.SessionEncrypted() {
		t.Fatal("无会话时不应声称文件已加密")
	}
	// 授权成功后的保存路径（不联网）。
	s := &Session{AccessToken: "at-g", ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), UpdatedAt: time.Now().UnixMilli()}
	if err := m.saveSessionAndMark(s); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}
	if !m.SessionEncrypted() {
		t.Error("加密开启后保存的会话必须同步标志为已加密（否则开关显示关闭）")
	}
	if !strings.Contains(string(readSessionFile(t, m)), secret.Prefix) {
		t.Error("文件应为加密外壳")
	}
	// 反向：关掉加密再存 → 文件回明文，标志也必须跟着回 false。
	if err := m.SetEncryption(false); err != nil {
		t.Fatalf("关闭加密失败: %v", err)
	}
	if err := m.saveSessionAndMark(s); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}
	if m.SessionEncrypted() {
		t.Error("关闭加密后保存的会话不得标记为已加密")
	}
}
