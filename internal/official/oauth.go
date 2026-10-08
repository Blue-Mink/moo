package official

// 飞牛官方应用中心 OAuth 2.0（Authorization Code + PKCE S256）客户端。
//
// 契约来源（测试机实锤，tcpdump 本机面板端口明文抓包）：
//   - 授权页:   GET /signin?client_id=...&scope=...&code_challenge=...&
//               code_challenge_method=S256&device_id=...&device_name=...&device_model=CLI
//               （无 redirect_uri；用户浏览器登录并同意后页面显示约 10 位一次性 code）
//   - 换 token: POST /oauthapi/third-part/token
//               body: {"client_id":..., "code":..., "code_verifier":...}
//               → {"code":0,"data":{"access_token","refresh_token","expires_in":3600,
//                  "expires_at"(ms),"server_time","scopes":[...]}}
//   - 刷新:     POST /oauthapi/refresh
//   - 业务代理:  GET/POST /ogh/ac/h<业务路径>?query + Authorization: Bearer <access_token>
//
// code 有效期 5 分钟、一次性；access token 1 小时，临期（<60s）自动刷新。

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ClientID       = "YJNMPJUGA9"
	DefaultScope   = "trim.user.all trim.file.read trim.file.write trim.download.all trim.appcenter.all trim.system.all trim.log.all trim.storage.all trim.docker.all"
	DeviceName     = "Moo"
	DeviceModel    = "CLI"
	TokenLifetime  = time.Hour
	RefreshWindow  = 60 * time.Second // 临期刷新阈值
	sessionFilePerm = 0o600
)

// Session 是持久化的 OAuth 会话。
type Session struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	ExpiresAt    int64    `json:"expires_at"` // ms
	Scopes       []string `json:"scopes,omitempty"`
	UpdatedAt    int64    `json:"updated_at"` // ms
}

func (s *Session) Valid(now time.Time) bool {
	return s != nil && s.AccessToken != "" && now.UnixMilli() < s.ExpiresAt
}

// NeedsRefresh 临期判断。
func (s *Session) NeedsRefresh(now time.Time) bool {
	return s != nil && s.AccessToken != "" &&
		s.ExpiresAt-now.UnixMilli() < int64(RefreshWindow.Milliseconds())
}

// PendingAuthorization 是等待用户粘贴 code 的授权请求（verifier 必须与
// /signin 的 challenge 配对，code 交换前不能丢）。
type PendingAuthorization struct {
	Verifier  string `json:"verifier"`
	Challenge string `json:"challenge"`
	DeviceID  string `json:"device_id"`
	Scope     string `json:"scope"`
	CreatedAt int64  `json:"created_at"` // ms
}

// Manager 管理授权流程 + 会话持久化 + 自动刷新。
type Manager struct {
	mu      sync.Mutex
	baseURL string
	client  *http.Client
	dataDir string // session.json 所在目录（@appdata/moo）

	session  *Session
	pending  *PendingAuthorization
	lastErr  string
	refreshMu sync.Mutex

	// 0.6.254：面板前端授权 UI 支持探测缓存（仅 fnOS 8.0.0+ 前端支持）。
	uiSupportMu      sync.Mutex
	uiSupportKnown   bool
	uiSupport        bool
	uiSupportProbing bool
}

func NewManager(dataDir string) *Manager {
	return NewManagerWithBase(dataDir, "http://127.0.0.1:5666")
}

// NewManagerWithBase 指定面板基址（测试/多实例用；生产默认 127.0.0.1:5666）。
func NewManagerWithBase(dataDir, baseURL string) *Manager {
	return &Manager{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 30 * time.Second},
		dataDir: dataDir,
	}
}

// SessionFile 路径。
func (m *Manager) SessionFile() string {
	return filepath.Join(m.dataDir, "official_session.json")
}

// LoadSession 启动时加载持久化会话。
func (m *Manager) LoadSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		return
	}
	var s Session
	if json.Unmarshal(b, &s) == nil && s.AccessToken != "" {
		m.session = &s
	}
}

// Session 当前会话（副本）。
func (m *Manager) Session() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return nil
	}
	c := *m.session
	return &c
}

// Status 供 API 返回。
func (m *Manager) Status(now time.Time) map[string]any {
	m.mu.Lock()
	s, p := m.session, m.pending
	err := m.lastErr
	m.mu.Unlock()
	st := map[string]any{"authorized": false}
	if s != nil {
		// 0.6.312：过期但可续期（refresh_token 在）仍算已连接——与
		// hasOAuthSession 同口径，Do 内联自动刷新；无 refresh_token 才
		// 需要重新授权。
		st["authorized"] = s.Valid(now) || s.RefreshToken != ""
		st["expired"] = s.ExpiresAt <= now.UnixMilli()
		st["expires_at"] = s.ExpiresAt
		st["scopes"] = s.Scopes
	}
	if p != nil {
		st["pending"] = true
	}
	if err != "" {
		st["last_error"] = err
	}
	return st
}

func newVerifier() (string, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:64], nil
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func newDeviceID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "moo-device-00000001"
	}
	return fmt.Sprintf("moo-%x%04x", time.Now().UnixMilli()&0xffffffff, b)
}

// BeginAuthorization 生成 PKCE 对并构造 /signin 授权 URL（本机基址）。
func (m *Manager) BeginAuthorization() (string, error) {
	return m.beginAuthorizationAt(m.baseURL)
}

// BeginAuthorizationAt 生成 PKCE 对并构造用户浏览器可达的授权 URL。
// browserBase 例：http://192.0.2.22:5666 —— 面板 API 调用仍走 m.baseURL
// （本机回环），仅授权页 URL 换成本机不可达时的对外地址。
func (m *Manager) BeginAuthorizationAt(browserBase string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(browserBase), "/")
	if base == "" {
		base = m.baseURL
	}
	return m.beginAuthorizationAt(base)
}

func (m *Manager) beginAuthorizationAt(base string) (string, error) {
	verifier, err := newVerifier()
	if err != nil {
		return "", err
	}
	p := &PendingAuthorization{
		Verifier:  verifier,
		Challenge: challengeOf(verifier),
		DeviceID:  newDeviceID(),
		Scope:     DefaultScope,
		CreatedAt: time.Now().UnixMilli(),
	}
	q := url.Values{}
	q.Set("client_id", ClientID)
	q.Set("scope", p.Scope)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("device_id", p.DeviceID)
	q.Set("device_name", DeviceName)
	q.Set("device_model", DeviceModel)
	m.mu.Lock()
	m.pending = p
	m.lastErr = ""
	m.mu.Unlock()
	return base + "/signin?" + q.Encode(), nil
}

// BeginHeadlessAuthorization 生成 PKCE 对并返回原始参数（无头授权用：
// 调用方拿 challenge/deviceID 直接调 /oauthapi/authorize 取码，再走
// CompleteAuthorization 换 token）。与 BeginAuthorization 同语义，
// 只返回参数不构造 URL。
func (m *Manager) BeginHeadlessAuthorization() (challenge, deviceID, scope string, err error) {
	verifier, err := newVerifier()
	if err != nil {
		return "", "", "", err
	}
	p := &PendingAuthorization{
		Verifier:  verifier,
		Challenge: challengeOf(verifier),
		DeviceID:  newDeviceID(),
		Scope:     DefaultScope,
		CreatedAt: time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.pending = p
	m.lastErr = ""
	m.mu.Unlock()
	return p.Challenge, p.DeviceID, p.Scope, nil
}

// Cancel 丢弃待完成的授权（用户放弃本次登录）。
func (m *Manager) Cancel() {
	m.mu.Lock()
	m.pending = nil
	m.lastErr = ""
	m.mu.Unlock()
}

func (m *Manager) postJSON(ctx context.Context, path string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("响应解析失败(HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("HTTP %d: %v", resp.StatusCode, out)
	}
	if c, _ := out["code"].(float64); c != 0 {
		return out, fmt.Errorf("code=%v msg=%v", out["code"], out["msg"])
	}
	return out, nil
}

// CompleteAuthorization 用用户粘贴的 code 换 token 并持久化。
func (m *Manager) CompleteAuthorization(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("code 为空")
	}
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("没有待完成的授权，请先生成授权链接")
	}
	out, err := m.postJSON(ctx, "/oauthapi/third-part/token", map[string]any{
		"client_id":     ClientID,
		"code":          code,
		"code_verifier": p.Verifier,
	})
	if err != nil {
		m.mu.Lock()
		m.lastErr = err.Error()
		m.mu.Unlock()
		return err
	}
	data, _ := out["data"].(map[string]any)
	if data == nil {
		return fmt.Errorf("token 响应缺少 data")
	}
	at, _ := data["access_token"].(string)
	if at == "" {
		return fmt.Errorf("token 响应缺少 access_token")
	}
	var rt string
	if v, ok := data["refresh_token"].(string); ok {
		rt = v
	}
	var exp int64
	if v, ok := data["expires_at"].(float64); ok {
		exp = int64(v)
	}
	if exp == 0 {
		if v, ok := data["expires_in"].(float64); ok {
			exp = time.Now().UnixMilli() + int64(v)*1000
		}
	}
	var scopes []string
	if v, ok := data["scopes"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				scopes = append(scopes, s)
			}
		}
	}
	s := &Session{
		AccessToken:  at,
		RefreshToken: rt,
		ExpiresAt:    exp,
		Scopes:       scopes,
		UpdatedAt:    time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.session = s
	m.pending = nil
	m.lastErr = ""
	m.mu.Unlock()
	return m.saveSessionLocked(s)
}

// saveSessionLocked 调用方无需持锁（内部自锁）。
func (m *Manager) saveSessionLocked(s *Session) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(m.SessionFile(), b, sessionFilePerm)
}

// Do 带 Bearer token 访问 /ogh/ac/h 代理，临期自动刷新（最多一次）。
func (m *Manager) Do(ctx context.Context, method, bizPath string, query url.Values, body []byte) ([]byte, error) {
	m.mu.Lock()
	s := m.session
	m.mu.Unlock()
	if s == nil || s.AccessToken == "" {
		return nil, fmt.Errorf("未授权：请先连接官方应用中心")
	}
	if !s.Valid(time.Now()) {
		if err := m.Refresh(ctx); err != nil {
			return nil, fmt.Errorf("token 已过期且刷新失败：%w", err)
		}
		m.mu.Lock()
		s = m.session
		m.mu.Unlock()
	}
	path := "/ogh/ac/h" + bizPath
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var respBody []byte
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		var req *http.Request
		var err error
		if body != nil {
			req, err = http.NewRequestWithContext(ctx, method, m.baseURL+path, bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
		} else {
			req, err = http.NewRequestWithContext(ctx, method, m.baseURL+path, nil)
		}
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+s.AccessToken)
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, err
		}
		respBody, err = readAll(resp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			lastErr = fmt.Errorf("401: %s", strings.TrimSpace(string(respBody)))
			if err := m.Refresh(ctx); err != nil {
				return nil, lastErr
			}
			m.mu.Lock()
			s = m.session
			m.mu.Unlock()
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(respBody, 200))
		}
		return respBody, nil
	}
	return nil, lastErr
}

// Refresh 用 refresh_token 换新 token（401/临期时调用）。
func (m *Manager) Refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	m.mu.Lock()
	s := m.session
	m.mu.Unlock()
	if s == nil || s.RefreshToken == "" {
		return fmt.Errorf("无 refresh_token，需重新授权")
	}
	out, err := m.postJSON(ctx, "/oauthapi/refresh", map[string]any{
		"client_id":     ClientID,
		"refresh_token": s.RefreshToken,
	})
	if err != nil {
		m.mu.Lock()
		m.lastErr = "refresh: " + err.Error()
		m.mu.Unlock()
		return err
	}
	data, _ := out["data"].(map[string]any)
	if data == nil {
		return fmt.Errorf("refresh 响应缺少 data")
	}
	ns := *s
	if v, ok := data["access_token"].(string); ok && v != "" {
		ns.AccessToken = v
	}
	if v, ok := data["refresh_token"].(string); ok && v != "" {
		ns.RefreshToken = v
	}
	if v, ok := data["expires_at"].(float64); ok && int64(v) > 0 {
		ns.ExpiresAt = int64(v)
	} else if v, ok := data["expires_in"].(float64); ok {
		ns.ExpiresAt = time.Now().UnixMilli() + int64(v)*1000
	}
	ns.UpdatedAt = time.Now().UnixMilli()
	m.mu.Lock()
	m.session = &ns
	m.lastErr = ""
	m.mu.Unlock()
	return m.saveSessionLocked(&ns)
}

// Logout 清除会话。
func (m *Manager) Logout() {
	m.mu.Lock()
	m.session = nil
	m.pending = nil
	m.mu.Unlock()
	os.Remove(m.SessionFile())
}

func readAll(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
