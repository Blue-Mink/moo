package official

// 0.6.349 WS2：rebase 重试语义（面板端口迁移 → 连接失败 → 重解析 → 重试一次成功）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"moo/internal/panel"
)

// listenStub 在 127.0.0.1:0 上起假面板，返回端口与关闭函数（端口可写进
// 网关 conf，用真实 TCP 监听以通过 probeOK）。
func listenStub(t *testing.T, name string, hits *[]string, mu *sync.Mutex) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				"expires_at":    4102444800000,
			},
		})
	})
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = srv.Close() }
}

func writeConfPort(t *testing.T, port int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gw.conf")
	if err := os.WriteFile(p, []byte(fmt.Sprintf(`{"schema":{"http":{"port":%d}}}`, port)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestManagerRebaseOnConnectionFailure_350：面板端口迁移（A 下线、conf 改 B）
// 后，换 token 链路第一次打到死端口 A 连接失败 → rebase 重读 conf → 重试
// 命中 B 成功（issue moo#7 的核心闭环，且不触发真实 OAuth，仅验证基址解析）。
func TestManagerRebaseOnConnectionFailure_350(t *testing.T) {
	var hits []string
	var mu sync.Mutex
	aPort, closeA := listenStub(t, "A", &hits, &mu)
	bPort, closeB := listenStub(t, "B", &hits, &mu)
	defer closeB()

	confPath := writeConfPort(t, aPort)
	t.Setenv("MOO_PANEL_CONF", confPath)
	t.Setenv("MOO_PANEL_BASE", "")
	panel.InvalidateBaseCache()
	t.Cleanup(panel.InvalidateBaseCache)

	m := NewManager(t.TempDir())
	if got := m.currentBase(); got != "http://127.0.0.1:"+strconv.Itoa(aPort) {
		t.Fatalf("首解析应落到 A（网关配置端口），得到 %s", got)
	}

	// 面板端口迁移：A 下线，conf 改写成 B（B 已监听）
	closeA()
	if err := os.WriteFile(confPath, []byte(fmt.Sprintf(`{"schema":{"http":{"port":%d}}}`, bPort)), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAuthorization(context.Background(), "c1"); err != nil {
		t.Fatalf("连接失败后应 rebase 重试成功: %v", err)
	}
	if s := m.Session(); s == nil || s.AccessToken != "tok-B" {
		t.Fatalf("未切到新端口 B: %+v", s)
	}
	// 确认重试只多打一次（A 首次失败 + B 重试成功），不会无限循环
	mu.Lock()
	defer mu.Unlock()
	for _, h := range hits {
		if h != "A /oauthapi/third-part/token" && h != "B /oauthapi/third-part/token" {
			t.Fatalf("意外命中: %v", h)
		}
	}
}

// TestManagerFixedBaseNoRebase_350：固定基址（测试/多实例）连接失败不 rebase、
// 直接返回错误（rebase() 对 dynamic=false 恒返回 false）。
func TestManagerFixedBaseNoRebase_350(t *testing.T) {
	m := NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1") // 必连接失败
	if _, err := m.BeginAuthorization(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompleteAuthorization(context.Background(), "c1"); err == nil {
		t.Fatal("固定基址连接失败应直接报错，不应静默成功")
	}
}
