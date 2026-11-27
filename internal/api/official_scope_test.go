package api

// 2026-11-26（加固 A）API 侧：授权范围（scope）白名单与失败语义。
//
// 锁住四条：
//   1. GET /api/official/authorize?scope=… 缺省/非法/minimal 三态
//      （缺省 = full，旧客户端行为不变；非法 400；minimal URL 只含 appcenter）；
//   2. POST /api/official/authorize-headless 的 scope 同样白名单，且**在面板
//      登录之前**校验（不为一个非法参数白烧一次面板登录/限流额度）；
//   3. POST /api/official/callback：面板拒绝 minimal → **400 + error_code**
//      （前端据此自动落回 full），其它失败仍 502 且无 error_code；
//   4. GET /api/official/status 的 scope_mode（非法配置按 full）+ 占位布尔。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/official"
	"moo/internal/pipeline"
)

// newScopeServer 造最小 Server（只注册 /api/official/*）。面板基址不可达：
// 发起授权只构造 URL，不真的连面板；换 token 失败正是回调用例要的路径。
func newScopeServer(t *testing.T, cfgMode string) (*Server, http.Handler) {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	m := official.NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1")
	s := &Server{Cfg: &config.Config{OfficialScopeMode: cfgMode}, Official: m, Panel: &Panel{}}
	mux := http.NewServeMux()
	s.officialRoutes(mux, s.officialStoreV())
	return s, mux
}

// authorizeAsAdmin 发一次 GET /api/official/authorize（GET 免 X-Moo-Admin 头）。
func authorizeAsAdmin(t *testing.T, h http.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/official/authorize"+query, nil).
		WithContext(withUserCtx(t.Context(), *adminUser))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// urlScope 从 {url} 响应体里取出授权 URL 的 scope 参数。
func urlScope(t *testing.T, body []byte) string {
	t.Helper()
	var got struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, body)
	}
	u, err := url.Parse(got.URL)
	if err != nil {
		t.Fatalf("授权 URL 解析失败: %v（%s）", err, got.URL)
	}
	return u.Query().Get("scope")
}

// ① 缺省 = 完整权限（旧客户端零变化）；minimal 只含 appcenter；非法 400。
func TestOfficialAuthorize_ScopeParam(t *testing.T) {
	_, h := newScopeServer(t, "")

	w := authorizeAsAdmin(t, h, "?base=http://192.0.2.22:5666")
	if w.Code != http.StatusOK {
		t.Fatalf("缺省 scope 应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	if s := urlScope(t, w.Body.Bytes()); s != official.DefaultScope {
		t.Errorf("缺省应为完整权限 %q，实际 %q", official.DefaultScope, s)
	}

	w = authorizeAsAdmin(t, h, "?scope=minimal")
	if w.Code != http.StatusOK {
		t.Fatalf("minimal 应 200，实际 %d body=%s", w.Code, w.Body.String())
	}
	if s := urlScope(t, w.Body.Bytes()); s != official.ScopeMinimal {
		t.Errorf("minimal 的 scope 应为 %q，实际 %q", official.ScopeMinimal, s)
	}

	// 非法串：整单 400（绝不透传），文案说明白名单。
	for _, bad := range []string{"admin", "full+minimal", "trim.file.write"} {
		w = authorizeAsAdmin(t, h, "?scope="+url.QueryEscape(bad))
		if w.Code != http.StatusBadRequest {
			t.Errorf("scope=%q 应 400，实际 %d body=%s", bad, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "full 或 minimal") {
			t.Errorf("400 文案应说明只接受 full/minimal，实际 %s", w.Body.String())
		}
	}
}

// ② 状态里的范围偏好：非法/空按 full，minimal 原样；占位布尔恒 true。
func TestOfficialStatus_ScopeMode(t *testing.T) {
	cases := []struct{ cfg, want string }{
		{"", official.ScopeModeFull},
		{"full", official.ScopeModeFull},
		{"minimal", official.ScopeModeMinimal},
		{"MINIMAL", official.ScopeModeMinimal},
		{"bogus", official.ScopeModeFull},
	}
	for _, c := range cases {
		_, h := newScopeServer(t, c.cfg)
		req := httptest.NewRequest(http.MethodGet, "/api/official/status", nil).
			WithContext(withUserCtx(t.Context(), *adminUser))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("cfg=%q：status 必须 200，实际 %d", c.cfg, w.Code)
		}
		var st map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("status 解析失败: %v", err)
		}
		if st["scope_mode"] != c.want {
			t.Errorf("cfg=%q：scope_mode 期望 %q，实际 %v", c.cfg, c.want, st["scope_mode"])
		}
		if st["scope_minimal_supported"] != true {
			t.Errorf("cfg=%q：scope_minimal_supported 应恒 true，实际 %v", c.cfg, st["scope_minimal_supported"])
		}
	}
}

// ③ 无头授权的 scope 白名单：非法值 400，且**必须先于面板登录**被拒
// （否则白白消耗一次登录、还可能撞上限流窗口）。
func TestOfficialAuthorizeHeadless_ScopeWhitelist(t *testing.T) {
	_, h := newScopeServer(t, "")
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/official/authorize-headless", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Moo-Admin", "1")
		req = req.WithContext(withUserCtx(req.Context(), *adminUser))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	w := post(`{"username":"u","password":"p","scope":"admin"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 scope 应 400，实际 %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "full 或 minimal") {
		t.Errorf("400 文案应说明白名单，实际 %s", w.Body.String())
	}
	// 关键：错误必须是 scope 校验报的，而不是「面板登录失败」——后者意味着
	// 校验顺序错了（先登录后校验）。
	if strings.Contains(w.Body.String(), "面板登录失败") {
		t.Errorf("scope 校验必须在面板登录之前，实际 %s", w.Body.String())
	}

	// 合法 scope 会走到面板登录（本用例面板基址不可达 → 502 登录失败），
	// 说明合法值没有被拦在第一道门。
	w = post(`{"username":"u","password":"p","scope":"minimal"}`)
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "full 或 minimal") {
		t.Errorf("合法 scope=minimal 不应被白名单拦下，实际 %d body=%s", w.Code, w.Body.String())
	}
}

// newScopePanel 造一个「拒绝 scope」的假面板：换 token 回 400 invalid_scope。
func newScopePanel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":400,"msg":"invalid_scope"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func postCallback(t *testing.T, h http.Handler, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/official/callback", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Moo-Admin", "1")
	req = req.WithContext(withUserCtx(req.Context(), *adminUser))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// scopeCallbackServer 造一套「面板拒绝 scope」的服务端，并预先按 scope 生成 pending。
func scopeCallbackServer(t *testing.T, panelBase, scope string) http.Handler {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	m := official.NewManagerWithBase(t.TempDir(), panelBase)
	if _, err := m.BeginAuthorizationAtScope("", scope); err != nil {
		t.Fatalf("BeginAuthorizationAtScope(%q): %v", scope, err)
	}
	s := &Server{Cfg: &config.Config{}, Official: m, Panel: &Panel{}}
	mux := http.NewServeMux()
	s.officialRoutes(mux, s.officialStoreV())
	return mux
}

// ④ callback：minimal 被面板拒绝 → 400 + error_code=scope_rejected + scope=minimal。
func TestOfficialCallback_ScopeRejected(t *testing.T) {
	h := scopeCallbackServer(t, newScopePanel(t).URL, "minimal")
	w := postCallback(t, h, "CODE123")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("scope 被拒应 400（不是 502），实际 %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, w.Body.String())
	}
	if got["error_code"] != "scope_rejected" {
		t.Errorf("error_code 应为 scope_rejected，实际 %v", got["error_code"])
	}
	if got["scope"] != official.ScopeModeMinimal {
		t.Errorf("scope 字段应为 minimal，实际 %v", got["scope"])
	}
	if msg, _ := got["error"].(string); !strings.Contains(msg, "invalid_scope") {
		t.Errorf("error 应带面板原文（排障用），实际 %v", got["error"])
	}

	// 反向：完整权限下同样 400（面板拒绝）→ 仍是 502，且**不带** error_code。
	h2 := scopeCallbackServer(t, newScopePanel(t).URL, "full")
	w2 := postCallback(t, h2, "CODE123")
	if w2.Code != http.StatusBadGateway {
		t.Fatalf("完整权限下的失败应 502，实际 %d body=%s", w2.Code, w2.Body.String())
	}
	if strings.Contains(w2.Body.String(), "scope_rejected") {
		t.Errorf("非 scope 拒绝不得带 error_code，实际 %s", w2.Body.String())
	}
}

// ⑤ 设置页：GET 恒给显式 full/minimal；PUT 白名单 + 归一（full → ""）+ 非法整单拒绝。
func TestSettingsOfficialScopeMode_RoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Pipe: &pipeline.Pipeline{}}

	var got Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/settings 解析失败: %v", err)
	}
	if got.OfficialScopeMode != official.ScopeModeFull {
		t.Errorf("默认 GET 应为 full（显式值，前端不用猜），实际 %q", got.OfficialScopeMode)
	}

	putSettingsBody(t, s, `{"official_scope_mode":"minimal"}`)
	if cfg.OfficialScopeMode != official.ScopeModeMinimal {
		t.Fatalf("PUT minimal 应落盘，实际 %q", cfg.OfficialScopeMode)
	}
	var back Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &back); err != nil {
		t.Fatalf("GET 解析失败: %v", err)
	}
	if back.OfficialScopeMode != official.ScopeModeMinimal {
		t.Errorf("GET 应回读 minimal，实际 %q", back.OfficialScopeMode)
	}

	// 未提交该字段 → 不改动。
	putSettingsBody(t, s, `{"check_interval_hours": 6}`)
	if cfg.OfficialScopeMode != official.ScopeModeMinimal {
		t.Errorf("未提交不应改动，实际 %q", cfg.OfficialScopeMode)
	}

	// 显式 full（含大小写/空白）→ 归一为空串：默认值不写进 config.json，
	// 旧配置文件的形状不变。
	putSettingsBody(t, s, `{"official_scope_mode":"  FULL "}`)
	if cfg.OfficialScopeMode != "" {
		t.Errorf("full 应归一为空串（默认值不落盘），实际 %q", cfg.OfficialScopeMode)
	}

	// 非法值整单拒绝，已存值不变。
	for _, body := range []string{
		`{"official_scope_mode":"admin"}`,
		`{"official_scope_mode":"trim.appcenter.all"}`,
		`{"official_scope_mode":"minimal,full"}`,
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)).WithContext(t.Context())
		rec := httptest.NewRecorder()
		s.putSettings(rec, r)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 期望 400，实际 %d body=%s", body, rec.Code, rec.Body.String())
		}
		if cfg.OfficialScopeMode != "" {
			t.Fatalf("%s 被拒后不得改动，实际 %q", body, cfg.OfficialScopeMode)
		}
	}
}
