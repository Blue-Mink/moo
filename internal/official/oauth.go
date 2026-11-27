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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"moo/internal/panel"
	"moo/internal/secret"
)

const (
	ClientID = "YJNMPJUGA9"
	// DefaultScope = 完整权限（既有行为，9 项能力）：用户/文件读写/下载/应用中心/
	// 系统设置/日志/存储/Docker 全套。**保留原名**：历史调用方与测试都引用它。
	DefaultScope = "trim.user.all trim.file.read trim.file.write trim.download.all trim.appcenter.all trim.system.all trim.log.all trim.storage.all trim.docker.all"
	// ScopeFull 是 DefaultScope 的语义别名（2026-11-26 加固 A：范围可由用户选）。
	ScopeFull = DefaultScope
	// ScopeMinimal 只请求应用中心能力：读官方目录与版本信息，**不能**读写文件、
	// 不能改系统设置。面板若不支持该范围，会在换 token 时拒绝（invalid_scope）
	// → 返回 ErrScopeRejected，前端据此自动落回 full 重试。
	ScopeMinimal = "trim.appcenter.all"
	// ScopeModeFull / ScopeModeMinimal 是配置项与 API 里表达「范围模式」的字面值。
	ScopeModeFull    = "full"
	ScopeModeMinimal = "minimal"

	DeviceName      = "Moo"
	DeviceModel     = "CLI"
	TokenLifetime   = time.Hour
	RefreshWindow   = 60 * time.Second // 临期刷新阈值
	sessionFilePerm = 0o600
	// sessionFileName / sessionTmpName：会话文件与其同目录临时文件。
	// 临时文件用固定名 + O_EXCL，写完 rename 覆盖正式文件——中途失败
	// （磁盘满、进程被杀）时正式文件仍是完整的旧内容，不会被截断成半个 JSON。
	sessionFileName = "official_session.json"
	sessionTmpName  = "official_session.json.tmp"
)

// ErrScopeRejected 面板拒绝了本次授权的 scope（典型：老面板只认完整权限，
// 换 token 时回 invalid_scope）。可通过 errors.Is 判定，供上层把失败翻译成
// 「面板不支持最小权限，改回完整权限重试」而不是笼统的 502。
var ErrScopeRejected = errors.New("面板不支持所选授权范围（scope 被拒绝）")

// NormalizeScope 把外部传入的 scope 白名单化为实际 scope 串。
//
// 接受："" / "full"（大小写忽略）→ 完整权限；"minimal"（大小写忽略）→ 仅
// 应用中心；以及**这两个常量本身的精确字符串**（幂等：内部的 Begin* 包装
// 函数会把 ScopeFull/ScopeMinimal 再喂回来）。
// **其它任何串一律报错**——不做「原样透传」，否则等于把 scope 注入面交给
// 调用方（面板侧 scope 一旦被放大就是提权）。
func NormalizeScope(v string) (string, error) {
	s := strings.TrimSpace(v)
	switch {
	case s == "", strings.EqualFold(s, ScopeModeFull), s == ScopeFull:
		return ScopeFull, nil
	case strings.EqualFold(s, ScopeModeMinimal), s == ScopeMinimal:
		return ScopeMinimal, nil
	}
	return "", fmt.Errorf("未知授权范围 %q：只接受 full 或 minimal", v)
}

// ScopeModeOf 把配置值归一为 "full" / "minimal"。读侧宽容：非法值按 full
// 返回（旧配置、手改坏值都不会让设置页或授权流程卡死）；写侧（PUT settings）
// 仍整单拒绝非法值。
func ScopeModeOf(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), ScopeModeMinimal) {
		return ScopeModeMinimal
	}
	return ScopeModeFull
}

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
	baseAt  time.Time
	dynamic bool // true = 未显式指定基址，按 panel.ResolveBaseCached 自适应
	client  *http.Client
	dataDir string // session.json 所在目录（@appdata/moo）

	session   *Session
	pending   *PendingAuthorization
	lastErr   string
	refreshMu sync.Mutex

	// 0.6.254：面板前端授权 UI 支持探测缓存（仅 fnOS 8.0.0+ 前端支持）。
	uiSupportMu      sync.Mutex
	uiSupportKnown   bool
	uiSupport        bool
	uiSupportProbing bool

	// 2026-11-26（加固 B2）：会话文件可选加密（opt-in，默认关）。
	// sealer == nil = 本机密钥不可用（.moo-secret.key 缺失/损坏）→ 拒绝开启
	// 加密（否则会写出自己都解不开的密文），也解不开已加密的存量会话。
	sealer  *secret.Sealer
	encrypt bool // 配置意图：落盘是否加密
	// sessionEncrypted = 当前磁盘上的会话文件是加密外壳（状态展示用）。
	sessionEncrypted bool
	// encMismatch = 配置要求加密，但读到的会话文件是明文——只可能是旧版本
	// 重授权把它覆盖回明文。仅日志 + 该标志，**不自动再加密**（尊重旧版本动作）。
	encMismatch bool
}

// NewManager 生产构造：基址自适应（0.6.349，issue moo#7）。
//
// 历史实现写死 http://127.0.0.1:5666 → 用户自定义面板端口后，授权页能
// 打开、code 也能拿到，但换 token / 刷新 / 业务代理全部打空。现在基址由
// panel.ResolveBaseCached 解析（env → 网关配置端口 → 探测 → 5666），并带
// 5 分钟缓存；连接失败时 InvalidateBaseCache + 重解析一次。
func NewManager(dataDir string) *Manager {
	info := panel.ResolveBaseCached("")
	log.Printf("[panel] 官方源通道面板基址: %s（来源 %s）", info.URL, info.Source)
	return &Manager{
		baseURL: info.URL,
		baseAt:  time.Now(),
		dynamic: true,
		client:  panel.HTTPClientForBase(info.URL, 30*time.Second),
		dataDir: dataDir,
	}
}

// NewManagerWithBase 指定面板基址（测试/多实例用，固定不再重解析）。
func NewManagerWithBase(dataDir, baseURL string) *Manager {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return &Manager{
		baseURL: baseURL,
		client:  panel.HTTPClientForBase(baseURL, 30*time.Second),
		dataDir: dataDir,
	}
}

// ── 会话落盘加密（2026-11-26 加固 B2，opt-in）────────────────────────────
//
// 设计取舍：session 里的 access_token / refresh_token 是「能直连面板业务代理」
// 的凭据，明文落盘只靠 0600 + root 保护。真正剩下的风险是整目录 tarball 备份
// 与误拷贝单文件。加密外壳（{"v":2,"enc":"enc:v1:…"}）能挡住后者；前者因
// 密钥与密文同目录而无效（主修复是备份排除，见部署脚本）。所以：
//   - 默认关（零体验变化），用户显式开启；
//   - 旧版本读到外壳时 Unmarshal 成功、access_token 为空 → 干净地显示
//     「未连接」，**不报解析错误、不反复重试**；
//   - 解不开（密钥变更/丢失）时不删文件、只记 lastErr，恢复密钥后仍可复用。

// SetSealer 注入本机加解密器。**必须在 LoadSession 之前调用**，否则已加密的
// 会话文件在启动时解不开（会落到「会话已加密但密钥不可用（需重新授权）」）。
func (m *Manager) SetSealer(s *secret.Sealer) {
	m.mu.Lock()
	m.sealer = s
	m.mu.Unlock()
}

// Sealer 当前注入的加解密器（诊断/测试用；nil = 本机密钥不可用）。
func (m *Manager) Sealer() *secret.Sealer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sealer
}

// EncryptionEnabled 落盘加密开关（配置意图；真值来源见 SessionEncrypted）。
func (m *Manager) EncryptionEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.encrypt
}

// EncryptionAvailable 本机密钥是否可用。false = 不允许开启加密。
func (m *Manager) EncryptionAvailable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sealer != nil
}

// SessionEncrypted 磁盘上的会话文件当前是否为加密形态。
func (m *Manager) SessionEncrypted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionEncrypted
}

// EncryptionMismatch 配置要求加密但文件是明文（旧版本重授权覆盖过）。
func (m *Manager) EncryptionMismatch() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.encMismatch
}

// SetEncryption 切换落盘加密形态：切换时把**当前内存会话立即按新形态重写一次**
// （加密开 → 外壳密文；关 → 明文），无会话则只改标志（下次保存按新形态写）。
// 写盘失败时完全回滚（标志与形态都不动），调用方据此回滚配置。
func (m *Manager) SetEncryption(on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on && m.sealer == nil {
		return fmt.Errorf("本机密钥不可用（%s 缺失或损坏），无法开启会话加密", secret.KeyFile)
	}
	if m.encrypt == on {
		return nil
	}
	prevEnc, prevFlag, prevMismatch := m.sessionEncrypted, m.encrypt, m.encMismatch
	m.encrypt = on
	m.encMismatch = false
	if m.session != nil {
		if err := m.saveSessionLocked(m.session); err != nil {
			m.encrypt, m.sessionEncrypted, m.encMismatch = prevFlag, prevEnc, prevMismatch
			return err
		}
		m.sessionEncrypted = on
	}
	return nil
}

// currentBase 返回当前生效的面板基址。dynamic 时每次问 panel 缓存
// （缓存自身带 5 分钟 TTL + env 变化检测，成本极低）。
func (m *Manager) currentBase() string {
	if !m.dynamic {
		return m.baseURL
	}
	info := panel.ResolveBaseCached("")
	if info.URL == "" {
		return m.baseURL
	}
	m.mu.Lock()
	m.baseURL, m.baseAt = info.URL, time.Now()
	m.mu.Unlock()
	return info.URL
}

// rebase 连接失败后强制重解析；返回 false 表示固定基址（测试）无需重试。
func (m *Manager) rebase() bool {
	if !m.dynamic {
		return false
	}
	panel.InvalidateBaseCache()
	info := panel.ResolveBaseCached("")
	m.mu.Lock()
	old := m.baseURL
	m.baseURL, m.baseAt = info.URL, time.Now()
	m.mu.Unlock()
	if old != info.URL {
		log.Printf("[panel] 面板基址重解析: %s → %s（来源 %s）", old, info.URL, info.Source)
	}
	return true
}

// httpClientFor 按基址取 http.Client（https 回环放宽证书校验）。
func (m *Manager) httpClientFor(base string) *http.Client {
	if !m.dynamic && m.client != nil {
		return m.client
	}
	return panel.HTTPClientForBase(base, 30*time.Second)
}

// SessionFile 路径。
func (m *Manager) SessionFile() string {
	return filepath.Join(m.dataDir, sessionFileName)
}

// sessionTmpFile 旧固定名临时文件路径（保留：诊断/清理口径引用）。
// 原子写已改用 os.CreateTemp 的唯一名（批次 1 F1），同目录约束不变：
// 跨目录 rename 会退化成 copy+unlink，失去原子性。
func (m *Manager) sessionTmpFile() string {
	return filepath.Join(m.dataDir, sessionTmpName)
}

// cleanupSessionTmp 清理同目录残留的会话临时文件（只认 official_session.json.tmp
// 前缀，绝不碰 official_session.json 本体与 *.bak 等其它文件）。
//
// 为什么需要：saveSessionLocked 用临时文件 + rename 原子替换，进程在写完
// rename 之前被杀会留下残骸（唯一名，本身不会再毒化后续保存），启动时清一次
// 免得残留堆积。
func (m *Manager) cleanupSessionTmp() {
	dir := m.dataDir
	if dir == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, sessionTmpName) {
			continue
		}
		full := filepath.Join(dir, name)
		if rmErr := os.RemoveAll(full); rmErr != nil {
			log.Printf("[official] 清理会话临时文件失败 %s: %v", name, rmErr)
			continue
		}
		log.Printf("[official] 已清理残留会话临时文件 %s", name)
	}
}

// 加密形态的会话文件外壳（2026-11-26 加固 B2）。
//
// 关键：外壳本身是**合法 JSON 且 access_token 显式为空串**——旧版本
// Unmarshal 成 Session 时不会报错，只会读到空 token → 干净地显示「未连接」，
// 不会崩、不会重复重试，也不需要用户做任何清理动作。
const (
	sessionEnvelopeVersion = 2
	sessionEnvelopeNote    = "Moo 会话已加密；旧版本需重新授权"
	// sessionKeyUnavailableMsg 解密失败/无密钥时的 lastErr 文案（前端直接展示）。
	sessionKeyUnavailableMsg = "会话已加密但密钥不可用（需重新授权）"
)

type sessionEnvelope struct {
	V           int    `json:"v"`
	Enc         string `json:"enc"`
	Note        string `json:"note,omitempty"`
	AccessToken string `json:"access_token"`
}

// LoadSession 启动时加载持久化会话。
//
// 两种落盘形态都要认：
//   - 加密外壳（v==2 且 enc 非空）→ 用注入的 sealer 解密内层 Session；
//     密钥不可用/解密失败 → 只记 lastErr，**不删文件**（密钥恢复后仍可复用）。
//   - 明文（旧版本写入 / 未开启加密）→ 照旧加载；若配置要求加密但读到明文，
//     只记日志与标志，不自动再加密。
func (m *Manager) LoadSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 2026-11-26（加固 B）：启动先清残留临时文件——上次写入半途退出会留下
	// official_session.json.tmp，不清则后续保存全部 EEXIST 失败。
	m.cleanupSessionTmp()
	// 0.6.329 卫生项：启动加载时修正存量会话文件 mode 为 0600（历史 0700
	// 文件仅靠写入时 chmod 不够——会话无变化则不写入、不触发修正）。
	_ = os.Chmod(m.SessionFile(), 0o600)
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		return
	}
	// 先按加密外壳解析（结构不匹配则 env.V/Env.Enc 为零值，落到明文分支）。
	var env sessionEnvelope
	if json.Unmarshal(b, &env) == nil && env.V == sessionEnvelopeVersion && env.Enc != "" {
		m.sessionEncrypted = true
		m.encMismatch = false
		if m.sealer == nil {
			m.lastErr = sessionKeyUnavailableMsg
			log.Printf("[official] 会话文件已加密但本机密钥不可用（%s 缺失/损坏），需重新授权", secret.KeyFile)
			return
		}
		plain, oerr := m.sealer.Open(env.Enc)
		if oerr != nil {
			m.lastErr = sessionKeyUnavailableMsg
			// 不删文件：换回原密钥文件后仍可解开，删了才是真丢会话。
			log.Printf("[official] 会话文件解密失败（需重新授权）: %v", oerr)
			return
		}
		var s Session
		if json.Unmarshal([]byte(plain), &s) == nil && s.AccessToken != "" {
			m.session = &s
		}
		return
	}
	// 明文形态（旧版本写入 / 未开启加密）：照旧加载。
	m.sessionEncrypted = false
	var s Session
	if json.Unmarshal(b, &s) == nil && s.AccessToken != "" {
		m.session = &s
	}
	if m.encrypt {
		// 配置要求加密但文件是明文：只可能是旧版本重授权把它覆盖回明文。
		// 尊重旧版本动作——只留痕，不自动再加密。
		m.encMismatch = true
		log.Printf("[official] 会话文件为明文但配置要求加密（可能被旧版本重授权覆盖）；保持现状，不自动再加密")
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

// BeginAuthorization 生成 PKCE 对并构造 /signin 授权 URL（本机基址，完整权限）。
func (m *Manager) BeginAuthorization() (string, error) {
	return m.beginAuthorizationAtScope(m.currentBase(), ScopeFull)
}

// BeginAuthorizationAt 生成 PKCE 对并构造用户浏览器可达的授权 URL（完整权限）。
// browserBase 例：http://192.0.2.22:5666 —— 面板 API 调用仍走 m.baseURL
// （本机回环），仅授权页 URL 换成本机不可达时的对外地址。
func (m *Manager) BeginAuthorizationAt(browserBase string) (string, error) {
	return m.BeginAuthorizationAtScope(browserBase, ScopeFull)
}

// BeginAuthorizationAtScope 同 BeginAuthorizationAt，但授权范围由调用方指定：
// ""/"full" = 完整权限（9 项），"minimal" = 仅应用中心，其它值报错。
//
// 默认路径零变化：旧调用方（BeginAuthorization / BeginAuthorizationAt）内部
// 一律转调本函数并传 ScopeFull。
func (m *Manager) BeginAuthorizationAtScope(browserBase, scope string) (string, error) {
	sc, err := NormalizeScope(scope)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(browserBase), "/")
	if base == "" {
		base = m.currentBase()
	}
	return m.beginAuthorizationAtScope(base, sc)
}

func (m *Manager) beginAuthorizationAtScope(base, scope string) (string, error) {
	verifier, err := newVerifier()
	if err != nil {
		return "", err
	}
	p := &PendingAuthorization{
		Verifier:  verifier,
		Challenge: challengeOf(verifier),
		DeviceID:  newDeviceID(),
		Scope:     scope,
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
	return m.BeginHeadlessAuthorizationScope(ScopeFull)
}

// BeginHeadlessAuthorizationScope 同 BeginHeadlessAuthorization，但授权范围
// 由调用方指定（""/full = 完整权限，minimal = 仅应用中心，其它值报错）。
// 默认路径不变：BeginHeadlessAuthorization 内部传 ScopeFull。
func (m *Manager) BeginHeadlessAuthorizationScope(scope string) (challenge, deviceID, scopeOut string, err error) {
	sc, err := NormalizeScope(scope)
	if err != nil {
		return "", "", "", err
	}
	verifier, err := newVerifier()
	if err != nil {
		return "", "", "", err
	}
	p := &PendingAuthorization{
		Verifier:  verifier,
		Challenge: challengeOf(verifier),
		DeviceID:  newDeviceID(),
		Scope:     sc,
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
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		base := m.currentBase()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = m.httpClientFor(base).Do(req)
		if err != nil {
			// 0.6.349：面板端口变了但缓存还没过期 → 强制重解析后重试一次。
			if attempt == 0 && m.rebase() {
				continue
			}
			return nil, err
		}
		break
	}
	if resp == nil {
		return nil, fmt.Errorf("面板请求失败")
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

// looksLikeScopeRejection 判断换 token 失败是否「因为 scope 不被接受」。
//
// 面板的拒绝文案未公开，实测/文档口径是 invalid_scope（也可能是一句
// "invalid scope"）。判定故意宽松（小写文本里出现 scope 字样）：误判的代价
// 只是「改用完整权限重试一次」（与现状失败重试一致）；漏判则会让用户卡在
// minimal 上无从恢复。
func looksLikeScopeRejection(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "scope")
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
		// 2026-11-26（加固 A）：minimal 范围被面板拒绝（老面板只认完整权限）时
		// 包成可判定的 ErrScopeRejected，让上层/前端自动改回 full 重试。
		// pending **不清理**：用户可用完整权限重新发起，也可以直接重试。
		if p.Scope == ScopeMinimal && looksLikeScopeRejection(err) {
			return fmt.Errorf("%w: %v", ErrScopeRejected, err)
		}
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
	return m.saveSessionAndMark(s)
}

// marshalSessionLocked 按当前形态序列化会话（加密开 → 外壳 + 密文）。
//
// 加密失败一律返回 error 而**不**降级写明文：静默退明文会让「已开启加密」
// 变成谎言，用户以为安全了其实没有。
func (m *Manager) marshalSessionLocked(s *Session) ([]byte, error) {
	if !m.encrypt {
		return json.MarshalIndent(s, "", "  ")
	}
	if m.sealer == nil {
		return nil, fmt.Errorf("会话加密已开启但本机密钥不可用（%s 缺失或损坏）", secret.KeyFile)
	}
	inner, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	enc, err := m.sealer.Seal(string(inner))
	if err != nil {
		return nil, fmt.Errorf("加密会话内容失败: %w", err)
	}
	return json.MarshalIndent(sessionEnvelope{
		V:           sessionEnvelopeVersion,
		Enc:         enc,
		Note:        sessionEnvelopeNote,
		AccessToken: "", // 显式空串：旧版本读到「未连接」，不报错
	}, "", "  ")
}

// saveSessionLocked 原子写会话文件（调用方无需持锁）。
//
// 顺序：同目录唯一名 .tmp（CreateTemp：O_EXCL 语义 + 0600）→ Write → Sync → Close
// → Rename 覆盖本体 → chmod 0600。任一步失败都删掉 tmp 并返回 error，
// 正式文件保持写入前的完好内容（绝不用 os.WriteFile 直接截断本体——那正是
// 「写到一半断电/磁盘满 → 会话文件变半截 JSON → 用户被强制重新授权」的根因）。
func (m *Manager) saveSessionLocked(s *Session) error {
	b, err := m.marshalSessionLocked(s)
	if err != nil {
		return err
	}
	// 唯一名临时文件（批次 1 F1 / N1）：旧实现用固定名
	// official_session.json.tmp + O_EXCL，任何残留（上次写到一半进程被杀）
	// 都会让之后每次保存都 EEXIST 失败——而启动清理只覆盖「重启」这一条
	// 路径，进程活着期间无法自愈。CreateTemp 自带 O_EXCL 语义、默认 0600，
	// 名字仍是 official_session.json.tmp<rand> 前缀，cleanupSessionTmp 照旧命中。
	dir := m.dataDir
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, sessionTmpName+"*")
	if err != nil {
		return fmt.Errorf("创建会话临时文件失败: %w", err)
	}
	tmp := f.Name()
	// discard 收尾：删掉临时文件并关掉仍持有的句柄（Close 幂等失败可忽略）。
	discard := func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}
	if _, err := f.Write(b); err != nil {
		discard()
		return fmt.Errorf("写入会话临时文件失败: %w", err)
	}
	// Sync：确保数据先落盘，再做 rename——否则崩溃后可能 rename 成功而内容为空。
	if err := f.Sync(); err != nil {
		discard()
		return fmt.Errorf("同步会话临时文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭会话临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, m.SessionFile()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换会话文件失败: %w", err)
	}
	// 0.6.329 卫生项 + 加固 B：显式收紧到 0600（rename 保留 tmp 的 0600，
	// 这里再设一次是兜底，防止 umask / 存量 0700 文件带来的意外）。
	if err := os.Chmod(m.SessionFile(), sessionFilePerm); err != nil {
		return fmt.Errorf("修正会话文件权限失败: %w", err)
	}
	return nil
}

// saveSessionAndMark 保存会话，并把「磁盘形态」标志同步为当前加密开关。
//
// 2026-11-26（用户实报：先开「加密保存会话凭据」、之后再授权，开关仍显示关闭）：
// 授权 / 刷新走的是本保存路径，旧实现只把文件写成了加密外壳，却没有回写
// sessionEncrypted —— 状态接口读到 false，前端开关就弹回「关」。这里统一收口，
// 保证「文件怎么写的」与「状态怎么报的」永远一致（调用方勿持 m.mu）。
func (m *Manager) saveSessionAndMark(s *Session) error {
	if err := m.saveSessionLocked(s); err != nil {
		return err
	}
	m.mu.Lock()
	m.sessionEncrypted = m.encrypt
	m.encMismatch = false
	m.mu.Unlock()
	return nil
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
	rebased, refreshed := false, false
	for attempt := 0; attempt < 3; attempt++ {
		base := m.currentBase()
		var req *http.Request
		var err error
		if body != nil {
			req, err = http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
		} else {
			req, err = http.NewRequestWithContext(ctx, method, base+path, nil)
		}
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+s.AccessToken)
		resp, err := m.httpClientFor(base).Do(req)
		if err != nil {
			// 0.6.349：端口变更 → 重解析再试一次。
			if !rebased && m.rebase() {
				rebased = true
				continue
			}
			return nil, err
		}
		respBody, err = readAll(resp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && !refreshed {
			refreshed = true
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
	return m.saveSessionAndMark(&ns)
}

// Logout 清除会话。
func (m *Manager) Logout() {
	m.mu.Lock()
	m.session = nil
	m.pending = nil
	// 会话文件即将删除 → 加密形态标志一并归零（避免状态页残留「已加密」）。
	m.sessionEncrypted = false
	m.encMismatch = false
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
