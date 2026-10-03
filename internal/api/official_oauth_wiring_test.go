package api

// 0.6.253：OAuth 免登录通道接线测试（httptest 模拟面板 OAuth + /ogh 代理）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/official"
	"moo/internal/panel"
)

// newOAuthMockPanel 起一个模拟面板：OAuth 换 token + /ogh/ac/h 业务代理。
// listFail 控制业务代理返回 500（验证回退）。
func newOAuthMockPanel(t *testing.T, listFail *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/oauthapi/third-part/token":
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"access_token":"at-test","refresh_token":"rt-test","expires_at":`+
				fmt.Sprint(time.Now().Add(time.Hour).UnixMilli())+
				`,"scopes":["trim.appcenter.all"]}}`)
		case r.URL.Path == "/oauthapi/refresh":
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"access_token":"at-refreshed","expires_at":`+
				fmt.Sprint(time.Now().Add(time.Hour).UnixMilli())+`}}`)
		case r.URL.Path == "/ogh/ac/h/app-center/v1/app/list":
			if listFail != nil && listFail.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"code":500,"msg":"模拟 OAuth 通道故障"}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"page":1,"limit":200,"total":2,"list":[`+
				`{"appName":"trim.media","name":"影视","version":"0.9.8-1","icon":"https://i/x.png","download":100,"source":"official","sourceID":"2","status":"noinstall","tags":["Audio"]},`+
				`{"appName":"oauthflag","name":"OAuth标记应用","version":"1.0.0","source":"official","sourceID":"99","status":"noinstall","tags":["工具"]}`+
				`]}}`)
		case r.URL.Path == "/ogh/ac/h/app-center/v1/app/detail":
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"appName":"trim.media","appDetail":{"desc":"OAuth 通道详情","maintainer":"fnOS","installSize":1024}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"msg":"not found"}`)
		}
	}))
}

// authorizeSession 完成一次完整授权（BeginAuthorizationAt → 换 token）。
func authorizeSession(t *testing.T, m *official.Manager, browserBase string) {
	t.Helper()
	u, err := m.BeginAuthorizationAt(browserBase)
	if err != nil {
		t.Fatalf("BeginAuthorizationAt: %v", err)
	}
	if !strings.HasPrefix(u, strings.TrimRight(browserBase, "/")+"/signin?") {
		t.Fatalf("授权 URL 未使用浏览器基址: %s", u)
	}
	if !strings.Contains(u, "code_challenge=") || !strings.Contains(u, "client_id=YJNMPJUGA9") {
		t.Fatalf("授权 URL 缺少 PKCE 参数: %s", u)
	}
	if err := m.CompleteAuthorization(context.Background(), "MOCKCODE"); err != nil {
		t.Fatalf("CompleteAuthorization: %v", err)
	}
}

func TestWireOAuth_CatalogPreferOAuth(t *testing.T) {
	var fail atomic.Bool
	ts := newOAuthMockPanel(t, &fail)
	defer ts.Close()

	var origCalls int32
	p := &Panel{
		listApps: func(ctx context.Context) ([]panel.PanelApp, error) {
			atomic.AddInt32(&origCalls, 1)
			return []panel.PanelApp{{AppName: "homebox", Name: "面板通道应用", Version: "0.1"}}, nil
		},
		detailFn:    func(ctx context.Context, name string) (*panel.PanelDetail, error) { return nil, errors.New("不应走面板详情") },
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
	m := official.NewManagerWithBase(t.TempDir(), ts.URL)
	srv := &Server{Official: m, Panel: p}
	authorizeSession(t, m, "http://192.0.2.22:5666")
	WireOfficialOAuth(srv)

	apps, errStr := p.Apps(context.Background())
	if errStr != "" {
		t.Fatalf("Apps: %v", errStr)
	}
	if atomic.LoadInt32(&origCalls) != 0 {
		t.Errorf("OAuth 有效时不应回退面板通道（origCalls=%d）", origCalls)
	}
	var found bool
	for _, a := range apps {
		if a.AppName == "oauthflag" {
			found = true
			if a.Source != OfficialSourceID || a.Key != "oauthflag@"+OfficialSourceID {
				t.Errorf("OAuth 条目 Key/Source 异常: %+v", a)
			}
		}
	}
	if !found {
		t.Fatalf("目录里缺少 OAuth 通道条目（apps=%d）", len(apps))
	}
	// 描述回填应来自 OAuth 详情（detailFn 已包 OAuth 通道，直接调用验证）
	d, err := p.detailFn(context.Background(), "trim.media")
	if err != nil || d == nil || d.AppDetail.Desc != "OAuth 通道详情" {
		t.Errorf("detailFn 应走 OAuth 通道: d=%+v err=%v", d, err)
	}
}

// 0.6.255：无会话且无面板账号 → listApps 诚实报「未连接」（不再静默回退面板）。
func TestWireOAuth_NoSessionNotConnected(t *testing.T) {
	ts := newOAuthMockPanel(t, nil)
	defer ts.Close()

	var origCalls int32
	p := &Panel{
		listApps: func(ctx context.Context) ([]panel.PanelApp, error) {
			atomic.AddInt32(&origCalls, 1)
			return []panel.PanelApp{{AppName: "homebox", Name: "面板通道应用", Version: "0.1"}}, nil
		},
		detailFn:    func(ctx context.Context, name string) (*panel.PanelDetail, error) { return nil, nil },
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
	m := official.NewManagerWithBase(t.TempDir(), ts.URL)
	WireOfficialOAuth(&Server{Official: m, Panel: p})

	_, errStr := p.Apps(context.Background())
	if !strings.Contains(errStr, "未连接") {
		t.Fatalf("无会话且无面板账号应报「未连接」: err=%q", errStr)
	}
	if atomic.LoadInt32(&origCalls) != 0 {
		t.Errorf("不应再回退面板通道: %d", origCalls)
	}
}

// 0.6.255：会话有效但 /ogh 拉取失败 → 透传真实错误（不再静默回退面板）。
func TestWireOAuth_OAuthFailSurfacesError(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true) // /ogh 列表恒 500
	ts := newOAuthMockPanel(t, &fail)
	defer ts.Close()

	var origCalls int32
	p := &Panel{
		listApps: func(ctx context.Context) ([]panel.PanelApp, error) {
			atomic.AddInt32(&origCalls, 1)
			return []panel.PanelApp{{AppName: "homebox", Name: "面板通道应用", Version: "0.1"}}, nil
		},
		detailFn:    func(ctx context.Context, name string) (*panel.PanelDetail, error) { return nil, nil },
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
	m := official.NewManagerWithBase(t.TempDir(), ts.URL)
	authorizeSession(t, m, "http://192.0.2.22:5666")
	WireOfficialOAuth(&Server{Official: m, Panel: p})

	_, errStr := p.Apps(context.Background())
	if errStr == "" {
		t.Fatalf("OAuth 故障应返回错误（不再回退面板）")
	}
	if strings.Contains(errStr, "未连接") {
		t.Errorf("会话有效时的错误不应是「未连接」: %q", errStr)
	}
	if atomic.LoadInt32(&origCalls) != 0 {
		t.Errorf("不应回退面板通道: %d", origCalls)
	}
}

func TestOfficialBrowserBaseValidation(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"http://192.0.2.22:5666", "http://192.0.2.22:5666"},
		{"https://nas.example.com:5667/", "https://nas.example.com:5667"},
		{"ftp://1.2.3.4", ""},
		{"http://user:pass@1.2.3.4/", ""},
		{"http://", ""},
	} {
		req := &http.Request{URL: &url.URL{Path: "/api/official/authorize", RawQuery: "base=" + tc.raw}}
		if got := s.officialBrowserBase(req); got != tc.want {
			t.Errorf("base=%q: got %q want %q", tc.raw, got, tc.want)
		}
	}
}

func TestBeginAuthorization_Cancel(t *testing.T) {
	m := official.NewManagerWithBase(t.TempDir(), "http://127.0.0.1:5666")
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}
	if !m.Status(time.Now())["pending"].(bool) {
		t.Fatal("授权开始后 status.pending 应为 true")
	}
	m.Cancel()
	st := m.Status(time.Now())
	if _, ok := st["pending"]; ok {
		t.Errorf("Cancel 后 pending 应消失: %+v", st)
	}
}
