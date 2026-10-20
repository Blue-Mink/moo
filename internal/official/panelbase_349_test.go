package official

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"moo/internal/panel"
)

// stubPanel 假面板：只实现换 token 端点（0.6.349 回归用）。
func stubPanel(t *testing.T, name string, hits *[]string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*hits = append(*hits, name+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/oauthapi/third-part/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"access_token":  "tok-" + name,
				"refresh_token": "rft-" + name,
				"expires_at":    4102444800000, // 2100-01-01
				"scopes":        []string{"trim.appcenter.all"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 自定义面板端口（MOO_PANEL_BASE）必须被换 token 链路采用。
func TestManagerUsesResolvedPanelBase(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	srv := stubPanel(t, "A", &hits, &mu)
	t.Setenv("MOO_PANEL_BASE", srv.URL)
	panel.InvalidateBaseCache()
	t.Cleanup(panel.InvalidateBaseCache)

	m := NewManager(t.TempDir())
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatalf("BeginAuthorization: %v", err)
	}
	if err := m.CompleteAuthorization(context.Background(), "code-1"); err != nil {
		t.Fatalf("CompleteAuthorization 应打到解析出的端口: %v", err)
	}
	if s := m.Session(); s == nil || s.AccessToken != "tok-A" {
		t.Fatalf("会话未建立: %+v", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || hits[0] != "A /oauthapi/third-part/token" {
		t.Fatalf("面板端点命中异常: %v", hits)
	}
}

// 面板端口变更（env 变化）后下一次请求自动切到新基址，无需重启。
func TestManagerRebasesAfterPanelMove(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	a := stubPanel(t, "A", &hits, &mu)
	b := stubPanel(t, "B", &hits, &mu)
	t.Setenv("MOO_PANEL_BASE", a.URL)
	panel.InvalidateBaseCache()
	t.Cleanup(panel.InvalidateBaseCache)

	m := NewManager(t.TempDir())
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAuthorization(context.Background(), "c1"); err != nil {
		t.Fatalf("A 阶段失败: %v", err)
	}

	t.Setenv("MOO_PANEL_BASE", b.URL)
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAuthorization(context.Background(), "c2"); err != nil {
		t.Fatalf("B 阶段失败: %v", err)
	}
	if s := m.Session(); s == nil || s.AccessToken != "tok-B" {
		t.Fatalf("未切到新端口: %+v", s)
	}
}

// 固定基址（测试/多实例）不受 env 影响。
func TestManagerFixedBaseIgnoresEnv(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	a := stubPanel(t, "A", &hits, &mu)
	b := stubPanel(t, "B", &hits, &mu)
	t.Setenv("MOO_PANEL_BASE", b.URL)
	panel.InvalidateBaseCache()
	t.Cleanup(panel.InvalidateBaseCache)

	m := NewManagerWithBase(t.TempDir(), a.URL)
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAuthorization(context.Background(), "c1"); err != nil {
		t.Fatalf("固定基址失败: %v", err)
	}
	if s := m.Session(); s == nil || s.AccessToken != "tok-A" {
		t.Fatalf("固定基址被 env 覆盖: %+v", s)
	}
}
