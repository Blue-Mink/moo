package official

// 2026-11-26（加固 A）：授权范围（scope）由用户选。
//
// 三条不可退让的口径：
//   1. **默认零变化**：不带 scope 的旧调用方（BeginAuthorization /
//      BeginAuthorizationAt / BeginHeadlessAuthorization）必须仍然拿到完整权限；
//   2. **白名单**：只接受 ""/full/minimal（大小写、首尾空白忽略），其它任何串
//      报错——绝不原样透传（透传等于把「提权范围」交给调用方）；
//   3. **可判定失败**：minimal 被面板拒绝（invalid_scope）→ ErrScopeRejected，
//      且 pending 保留（用户可改回 full 重试）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// scopeOf 解析授权 URL 里的 scope（url.Values 会把空格还原，不会出现 +）。
func scopeOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("授权 URL 解析失败: %v（%s）", err, raw)
	}
	return u.Query().Get("scope")
}

// ① 白名单：合法写法归一，非法串一律报错。
func TestNormalizeScope(t *testing.T) {
	ok := []struct {
		in   string
		want string
	}{
		{"", ScopeFull},
		{"full", ScopeFull},
		{"FULL", ScopeFull},
		{"  Full  ", ScopeFull},
		{"minimal", ScopeMinimal},
		{"MINIMAL", ScopeMinimal},
		{"\tminimal ", ScopeMinimal},
		// 幂等：内部 Begin* 包装会把常量本身喂回来，必须同样认。
		{ScopeFull, ScopeFull},
		{ScopeMinimal, ScopeMinimal},
	}
	for _, c := range ok {
		got, err := NormalizeScope(c.in)
		if err != nil {
			t.Errorf("NormalizeScope(%q) 不应报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeScope(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}

	// 非法串：含「看起来像 scope 的真实串」——必须拒绝，不能顺手透传。
	// 注意：trim.appcenter.all 是 ScopeMinimal 的精确值，属合法输入（见上表），
	// 这里只列「确实不该放行」的串——尤其 ScopeFull 的任意子集/超集。
	for _, bad := range []string{"admin", "all", "trim.appcenter.all trim.file.read", "full minimal", "minimal,full", "0"} {
		if got, err := NormalizeScope(bad); err == nil {
			t.Errorf("NormalizeScope(%q) 应报错（防止 scope 注入），实际放行为 %q", bad, got)
		}
	}
}

// ② minimal 的授权 URL 只含 trim.appcenter.all；full 含 DefaultScope 的全部项。
func TestBeginAuthorizationScopeURLs(t *testing.T) {
	m := NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1")
	const base = "http://192.0.2.22:5666"

	minURL, err := m.BeginAuthorizationAtScope(base, "minimal")
	if err != nil {
		t.Fatalf("minimal 授权 URL 生成失败: %v", err)
	}
	if got := scopeOf(t, minURL); got != ScopeMinimal {
		t.Errorf("minimal URL 的 scope 应为 %q，实际 %q", ScopeMinimal, got)
	}
	if !strings.HasPrefix(minURL, base+"/signin?") {
		t.Errorf("授权 URL 应挂在面板基址上，实际 %s", minURL)
	}

	fullURL, err := m.BeginAuthorizationAtScope(base, "")
	if err != nil {
		t.Fatalf("full 授权 URL 生成失败: %v", err)
	}
	if got := scopeOf(t, fullURL); got != DefaultScope {
		t.Errorf("full URL 的 scope 应为 DefaultScope，实际 %q", got)
	}
	// 项数用常量自算（设计文档写「11 项」，实际 DefaultScope 是 9 项；
	// 硬编码数字会在文案口径漂移时变成假失败）。
	if n := len(strings.Fields(scopeOf(t, fullURL))); n != len(strings.Fields(DefaultScope)) {
		t.Errorf("full scope 项数应为 %d，实际 %d", len(strings.Fields(DefaultScope)), n)
	}
}

// ③ 默认路径未变：不带 scope 的旧调用方一律完整权限（零行为变化）。
func TestBeginAuthorizationDefaultIsFull(t *testing.T) {
	m := NewManagerWithBase(t.TempDir(), "http://127.0.0.1:5666")

	u1, err := m.BeginAuthorization()
	if err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}
	if got := scopeOf(t, u1); got != ScopeFull {
		t.Errorf("BeginAuthorization 的 scope 必须是完整权限，实际 %q", got)
	}

	u2, err := m.BeginAuthorizationAt("http://192.0.2.22:5666")
	if err != nil {
		t.Fatalf("BeginAuthorizationAt: %v", err)
	}
	if got := scopeOf(t, u2); got != DefaultScope {
		t.Errorf("BeginAuthorizationAt 的 scope 必须是完整权限，实际 %q", got)
	}

	_, _, scope, err := m.BeginHeadlessAuthorization()
	if err != nil {
		t.Fatalf("BeginHeadlessAuthorization: %v", err)
	}
	if scope != DefaultScope {
		t.Errorf("BeginHeadlessAuthorization 的 scope 必须是完整权限，实际 %q", scope)
	}

	// 非法值：报错且**不**留下 pending（避免后续回调拿到错配的 verifier）。
	m.Cancel()
	if _, err := m.BeginAuthorizationAtScope("", "admin"); err == nil {
		t.Fatal("非法 scope 必须报错")
	}
	if st := m.Status(time.Now()); st["pending"] == true {
		t.Error("非法 scope 失败后不应留下 pending")
	}
}

// ④ 无头授权的范围同样可选，且默认不变。
func TestBeginHeadlessAuthorizationScope(t *testing.T) {
	m := NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1")
	challenge, deviceID, scope, err := m.BeginHeadlessAuthorizationScope("minimal")
	if err != nil {
		t.Fatalf("BeginHeadlessAuthorizationScope: %v", err)
	}
	if scope != ScopeMinimal {
		t.Errorf("scope 应为 %q，实际 %q", ScopeMinimal, scope)
	}
	if challenge == "" || deviceID == "" {
		t.Errorf("challenge/deviceID 不能为空: %q / %q", challenge, deviceID)
	}
	if _, _, _, err := m.BeginHeadlessAuthorizationScope("bogus"); err == nil {
		t.Error("非法 scope 必须报错")
	}
}

// ⑤ minimal 被面板拒绝（invalid_scope）→ ErrScopeRejected，pending 保留。
func TestCompleteAuthorizationScopeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":400,"msg":"invalid_scope"}`)
	}))
	defer srv.Close()

	m := NewManagerWithBase(t.TempDir(), srv.URL)
	if _, err := m.BeginAuthorizationAtScope("", "minimal"); err != nil {
		t.Fatalf("BeginAuthorizationAtScope: %v", err)
	}
	err := m.CompleteAuthorization(context.Background(), "CODE123")
	if err == nil {
		t.Fatal("面板 400 时换 token 必须失败")
	}
	if !errors.Is(err, ErrScopeRejected) {
		t.Errorf("minimal 被拒应可判定为 ErrScopeRejected，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "invalid_scope") {
		t.Errorf("包装后仍应带面板原文（排障用），实际 %v", err)
	}
	if st := m.Status(time.Now()); st["pending"] != true {
		t.Error("scope 被拒后 pending 必须保留（允许用户改回完整权限重试）")
	}
	if m.Session() != nil {
		t.Error("失败不得写入会话")
	}

	// 反向：同样的 400 在 full 范围下**不是** ErrScopeRejected（仍是普通失败）。
	m2 := NewManagerWithBase(t.TempDir(), srv.URL)
	if _, err := m2.BeginAuthorizationAtScope("", "full"); err != nil {
		t.Fatalf("BeginAuthorizationAtScope(full): %v", err)
	}
	err2 := m2.CompleteAuthorization(context.Background(), "CODE123")
	if err2 == nil {
		t.Fatal("面板 400 时换 token 必须失败")
	}
	if errors.Is(err2, ErrScopeRejected) {
		t.Error("完整权限下的失败不应被误判为 scope 拒绝")
	}
}

// ⑥ 面板支持 minimal 时正常换到 token：范围只影响请求，不影响落地会话。
func TestCompleteAuthorizationMinimalSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":0,"data":{"access_token":"at-min","refresh_token":"rt-min",`+
			`"expires_in":3600,"scopes":["trim.appcenter.all"]}}`)
	}))
	defer srv.Close()

	m := NewManagerWithBase(t.TempDir(), srv.URL)
	if _, err := m.BeginAuthorizationAtScope("", "minimal"); err != nil {
		t.Fatalf("BeginAuthorizationAtScope: %v", err)
	}
	if err := m.CompleteAuthorization(context.Background(), "CODE123"); err != nil {
		t.Fatalf("换 token 应成功: %v", err)
	}
	s := m.Session()
	if s == nil || s.AccessToken != "at-min" {
		t.Fatalf("会话未写入: %+v", s)
	}
	if st := m.Status(time.Now()); st["pending"] == true {
		t.Error("成功后 pending 必须清掉")
	}
}
