package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/official"
	"moo/internal/pipeline"
	"moo/internal/secret"
)

// 2026-11-26（加固 B2）：POST /api/official/session-encryption 的准入与副作用。
//   - 管理员 + 密钥可用 → 200，且「运行态 + 配置文件」同时切到加密；
//   - 非管理员 → 403，配置与文件都不动；
//   - 密钥不可用 + enabled=true → 400，拒绝开启（不得写出解不开的密文）；
//   - POST 缺 X-Moo-Admin 头 → 400（requireAdmin 的 CSRF 纵深防御）；
//   - status 的 session_encrypted / encryption_available 对匿名也可见（不是敏感信息）。

func testSealerOf(b byte) *secret.Sealer {
	key := make([]byte, 32)
	for i := range key {
		key[i] = b
	}
	return secret.New(key)
}

// newSessionEncryptServer 造一个最小 Server：官方管理器 + 独立数据目录。
// sessionJSON 非空时先写一个明文会话文件并 LoadSession（模拟「已连接」）。
func newSessionEncryptServer(t *testing.T, withSealer bool, sessionJSON string) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir()) // 配置落盘到临时目录，绝不碰真实数据
	dir := t.TempDir()
	if sessionJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "official_session.json"), []byte(sessionJSON), 0o600); err != nil {
			t.Fatalf("写会话夹具失败: %v", err)
		}
	}
	m := official.NewManagerWithBase(dir, "http://127.0.0.1:1")
	if withSealer {
		m.SetSealer(testSealerOf(7))
	}
	m.LoadSession()
	// Pipe 仅用于 getSettings 读当前下载目录（nil 会 panic），非本批关注点。
	s := &Server{Cfg: &config.Config{}, Official: m, Panel: &Panel{}, Pipe: &pipeline.Pipeline{}}
	mux := http.NewServeMux()
	s.officialRoutes(mux, s.officialStoreV())
	return s, mux
}

func sessionFixture() string {
	return `{"access_token":"api-at","refresh_token":"api-rt","expires_at":` +
		itoa(time.Now().Add(time.Hour).UnixMilli()) + `}`
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func postSessionEncryption(t *testing.T, h http.Handler, u *User, body string, adminHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/official/session-encryption", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if u != nil {
		req = req.WithContext(withUserCtx(req.Context(), *u))
	}
	if adminHeader {
		req.Header.Set("X-Moo-Admin", "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var adminUser = &User{Username: "admin", UserID: "1", Trusted: true, IsAdmin: true}
var guestUser = &User{Username: "guest", UserID: "2", Trusted: true, IsAdmin: false}

// ① 管理员开启（有会话）→ 200 / encrypted=true / 文件变密文 / 配置标志为真。
func TestSessionEncryptionEndpoint_AdminEnable(t *testing.T) {
	s, h := newSessionEncryptServer(t, true, sessionFixture())
	w := postSessionEncryption(t, h, adminUser, `{"enabled":true}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员开启应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got["ok"] != true || got["encrypted"] != true {
		t.Errorf("期望 {ok:true,encrypted:true}，实际 %v", got)
	}
	if !s.Official.EncryptionEnabled() {
		t.Error("运行态 encrypted 标志应为真")
	}
	if !s.Cfg.OfficialSessionEncrypt {
		t.Error("配置文件标志 official_session_encrypt 应为真")
	}
	b, err := os.ReadFile(s.Official.SessionFile())
	if err != nil {
		t.Fatalf("读会话文件失败: %v", err)
	}
	if !strings.Contains(string(b), secret.Prefix) {
		t.Errorf("开启后文件应为密文外壳，实际 %s", b)
	}

	// status 对管理员暴露两个公开布尔。
	req := httptest.NewRequest(http.MethodGet, "/api/official/status", nil).
		WithContext(withUserCtx(t.Context(), *adminUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status 解析失败: %v", err)
	}
	if st["session_encrypted"] != true || st["encryption_available"] != true {
		t.Errorf("status 应下发 session_encrypted/encryption_available 均为 true，实际 %v", st)
	}
}

// ② 非管理员 → 403，且配置/文件都不动（requireAdmin 先于任何副作用）。
func TestSessionEncryptionEndpoint_NonAdminForbidden(t *testing.T) {
	s, h := newSessionEncryptServer(t, true, sessionFixture())
	before, err := os.ReadFile(s.Official.SessionFile())
	if err != nil {
		t.Fatalf("读会话文件失败: %v", err)
	}
	w := postSessionEncryption(t, h, guestUser, `{"enabled":true}`, true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("非管理员应 403，实际 %d body=%s", w.Code, w.Body.String())
	}
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Error("被拒绝的请求不得改变任何状态")
	}
	after, err := os.ReadFile(s.Official.SessionFile())
	if err != nil || string(after) != string(before) {
		t.Error("被拒绝的请求不得改动会话文件")
	}
}

// ③ 密钥不可用 + enabled=true → 400（拒绝写出解不开的密文），文件保持明文。
func TestSessionEncryptionEndpoint_NoSealerRejected(t *testing.T) {
	s, h := newSessionEncryptServer(t, false, sessionFixture())
	before, err := os.ReadFile(s.Official.SessionFile())
	if err != nil {
		t.Fatalf("读会话文件失败: %v", err)
	}
	w := postSessionEncryption(t, h, adminUser, `{"enabled":true}`, true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("密钥不可用应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "密钥不可用") {
		t.Errorf("错误信息应说明密钥不可用，实际 %s", w.Body.String())
	}
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Error("拒绝后不得写配置标志或切换运行态")
	}
	after, _ := os.ReadFile(s.Official.SessionFile())
	if string(after) != string(before) {
		t.Error("拒绝后文件形态不得变化")
	}
}

// ④ 关闭加密 → 200 / encrypted=false / 文件回到明文；缺 enabled → 400。
func TestSessionEncryptionEndpoint_DisableAndValidation(t *testing.T) {
	s, h := newSessionEncryptServer(t, true, sessionFixture())
	if w := postSessionEncryption(t, h, adminUser, `{"enabled":true}`, true); w.Code != http.StatusOK {
		t.Fatalf("前置开启失败: %d %s", w.Code, w.Body.String())
	}
	w := postSessionEncryption(t, h, adminUser, `{"enabled":false}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("关闭应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if got["encrypted"] != false {
		t.Errorf("关闭后 encrypted 应为 false，实际 %v", got)
	}
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Error("关闭后配置与运行态都应为 false")
	}
	b, _ := os.ReadFile(s.Official.SessionFile())
	if strings.Contains(string(b), secret.Prefix) {
		t.Error("关闭后文件应回到明文")
	}
	var sess official.Session
	if err := json.Unmarshal(b, &sess); err != nil || sess.AccessToken != "api-at" {
		t.Errorf("关闭后应保留原会话: err=%v at=%q", err, sess.AccessToken)
	}

	// body 缺 enabled（nil 指针）→ 400，不做任何切换。
	if w := postSessionEncryption(t, h, adminUser, `{}`, true); w.Code != http.StatusBadRequest {
		t.Errorf("缺 enabled 应 400，实际 %d", w.Code)
	}
}

// ⑤ POST 缺 X-Moo-Admin 头 → 400（CSRF 纵深防御），状态不变。
func TestSessionEncryptionEndpoint_RequiresAdminHeader(t *testing.T) {
	s, h := newSessionEncryptServer(t, true, sessionFixture())
	w := postSessionEncryption(t, h, adminUser, `{"enabled":true}`, false)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 X-Moo-Admin 应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Error("缺校验头的请求不得改变状态")
	}
}

// ⑥ status 的两个新布尔是公开字段：匿名也拿得到（只表达形态，不含内容）。
func TestSessionEncryptionEndpoint_StatusPublicFields(t *testing.T) {
	_, h := newSessionEncryptServer(t, true, "")
	req := httptest.NewRequest(http.MethodGet, "/api/official/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status 必须恒 200，实际 %d", rec.Code)
	}
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status 解析失败: %v", err)
	}
	if _, ok := st["session_encrypted"]; !ok {
		t.Error("匿名也应拿到 session_encrypted")
	}
	if st["encryption_available"] != true {
		t.Errorf("密钥可用时 encryption_available 应为 true，实际 %v", st["encryption_available"])
	}
	if st["session_encrypted"] != false {
		t.Errorf("没有会话文件时应为 false，实际 %v", st["session_encrypted"])
	}
	// 2026-11-26（盲点修复）：意图与不一致标记同样是公开字段。
	if _, ok := st["session_encryption_on"]; !ok {
		t.Error("匿名也应拿到 session_encryption_on")
	}
	if _, ok := st["encryption_mismatch"]; !ok {
		t.Error("匿名也应拿到 encryption_mismatch")
	}
}

// ⑨ 2026-11-26 盲点修复：**无会话**时开启加密 → status 必须能表达「已开启」意图
// （session_encryption_on=true），否则前端只认磁盘形态，开关会弹回「关」（用户实报）。
func TestSessionEncryptionEndpoint_IntentVisibleWithoutSession(t *testing.T) {
	s, h := newSessionEncryptServer(t, true, "")
	if w := postSessionEncryption(t, h, adminUser, `{"enabled":true}`, true); w.Code != http.StatusOK {
		t.Fatalf("开启失败: %d body=%s", w.Code, w.Body.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/official/status", nil))
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("status 解析失败: %v", err)
	}
	if st["session_encryption_on"] != true {
		t.Errorf("意图应为 true，实际 %v", st["session_encryption_on"])
	}
	if st["session_encrypted"] != false {
		t.Errorf("无会话文件时落盘形态仍应为 false，实际 %v", st["session_encrypted"])
	}
	if !s.Official.EncryptionEnabled() {
		t.Error("运行态也应已开启")
	}
}

// ⑦ 设置页那条路：GET 透出开关、PUT 同时切运行态与配置（含按新形态重写）。
func TestSettingsOfficialSessionEncrypt_RoundTrip(t *testing.T) {
	s, _ := newSessionEncryptServer(t, true, sessionFixture())
	var got Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/settings 解析失败: %v", err)
	}
	if got.OfficialSessionEncrypt {
		t.Error("默认应为 false（opt-in）")
	}
	putSettingsBody(t, s, `{"official_session_encrypt":true}`)
	if !s.Cfg.OfficialSessionEncrypt || !s.Official.EncryptionEnabled() {
		t.Error("PUT 后配置与运行态都应切到加密")
	}
	b, err := os.ReadFile(s.Official.SessionFile())
	if err != nil || !strings.Contains(string(b), secret.Prefix) {
		t.Errorf("PUT 后会话文件应已加密: err=%v body=%s", err, b)
	}
	// 再 GET：开关为真（配置文件与运行态一致）。
	var back Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &back); err != nil {
		t.Fatalf("GET 解析失败: %v", err)
	}
	if !back.OfficialSessionEncrypt {
		t.Error("GET 应回读到 true")
	}
	// 关闭：回到明文。
	putSettingsBody(t, s, `{"official_session_encrypt":false}`)
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Error("关闭后两处都应为 false")
	}
	if b, _ := os.ReadFile(s.Official.SessionFile()); strings.Contains(string(b), secret.Prefix) {
		t.Error("关闭后文件应回到明文")
	}
}

// ⑧ PUT 密钥不可用时开启 → 400，配置标志不被写入。
func TestSettingsOfficialSessionEncrypt_NoSealerRejected(t *testing.T) {
	s, _ := newSessionEncryptServer(t, false, sessionFixture())
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(`{"official_session_encrypt":true}`)).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("密钥不可用应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if s.Cfg.OfficialSessionEncrypt {
		t.Error("被拒绝的请求不得写配置标志")
	}
}
