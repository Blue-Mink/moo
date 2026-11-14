package api

// upgrade_deps_547_test.go —— 升级「停依赖方 → 升级 → 恢复」事务化（0.6.547）。
//
// 用假 daemon（unix socket）造出「目标应用被两个运行中的应用占用」，
// 断言 stop/task 与 start/task 的调用次数与顺序——这样「恢复」不是靠日志猜。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"moo/internal/platform"
)

// depTestServer 用临时数据目录，标记文件不会碰到真实 datadir。
func depTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	return &Server{}
}

type depTally struct {
	mu    sync.Mutex
	calls []string
}

func (c *depTally) add(s string) {
	c.mu.Lock()
	c.calls = append(c.calls, s)
	c.mu.Unlock()
}

func (c *depTally) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func (c *depTally) countOf(prefix string) int {
	n := 0
	for _, s := range c.snapshot() {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// startDepDaemon 假 daemon：targetApp 被 dep-a / dep-b 占用；
// failStopDep 指定某个依赖方的 stop/check 返回 isDependency=true（停不掉）。
func startDepDaemon(t *testing.T, failStopDep string) *depTally {
	t.Helper()
	c := &depTally{}
	var mu sync.Mutex
	stopped := map[string]bool{}

	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/stop/check", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AppName string `json:"appName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.AppName == failStopDep {
			fmt.Fprint(w, `{"code":0,"data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":true}}}`)
			return
		}
		if body.AppName == "target-app" {
			fmt.Fprint(w, `{"code":0,"data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":false,`+
				`"dependencyApps":[{"appName":"dep-a","status":"running"},{"appName":"dep-b","status":"running"}]}}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":false,"dependencyApps":[]}}}`)
	})
	mux.HandleFunc("/rpc/v1/stop/task", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AppName string `json:"appName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		stopped[body.AppName] = true
		mu.Unlock()
		c.add("stop:" + body.AppName)
		fmt.Fprintf(w, `{"code":0,"data":{"taskId":"t-stop-%s"}}`, body.AppName)
	})
	mux.HandleFunc("/rpc/v1/start/check", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"isSystemVersionMatch":true}}`)
	})
	mux.HandleFunc("/rpc/v1/start/task", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			AppName string `json:"appName"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		c.add("start:" + body.AppName)
		fmt.Fprintf(w, `{"code":0,"data":{"taskId":"t-start-%s"}}`, body.AppName)
	})
	mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"status":2,"message":"完成","progress":100}}`)
	})
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"total":3,"list":[{"appName":"target-app","version":"1.0.0","status":"running"},`+
			`{"appName":"dep-a","version":"1.0.0","status":"running"},{"appName":"dep-b","version":"1.0.0","status":"running"}]}}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/daemon.sock")
	if err != nil {
		t.Fatalf("起假 daemon 失败: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
	}))
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	platform.SetDaemonSocketForTest(lis.Addr().String())
	return c
}

// TestWithUpgradeDepsPassthrough：不带 stop_deps 时零副作用（默认路径不变）。
func TestWithUpgradeDepsPassthrough(t *testing.T) {
	c := startDepDaemon(t, "")
	s := depTestServer(t)
	called := false
	err := s.withUpgradeDeps(context.Background(), "target-app", false, func(string, float64) {}, func() error {
		called = true
		return errors.New("升级失败")
	})
	if !called || err == nil {
		t.Fatalf("直通路径应执行 fn 并原样返回错误")
	}
	if n := len(c.snapshot()); n != 0 {
		t.Fatalf("不带 stop_deps 不应调用 daemon 启停: %v", c.snapshot())
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("不应落盘标记: %#v", items)
	}
}

// TestWithUpgradeDepsStopsAndRestores：升级成功 → 两个依赖方先停后启，标记清空。
func TestWithUpgradeDepsStopsAndRestores(t *testing.T) {
	c := startDepDaemon(t, "")
	s := depTestServer(t)
	err := s.withUpgradeDeps(context.Background(), "target-app", true, func(string, float64) {}, func() error {
		// 升级进行中：标记必须在场（进程被杀时靠它恢复）
		if items := s.readDepStops(); len(items) != 1 || len(items[0].Blockers) != 2 {
			t.Errorf("升级中应有 2 个依赖方的标记，实际 %#v", items)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("升级应成功: %v", err)
	}
	calls := c.snapshot()
	if c.countOf("stop:") != 2 || c.countOf("start:") != 2 {
		t.Fatalf("应 stop×2 + start×2，实际 %v", calls)
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("结束后标记应清空: %#v", items)
	}
}

// TestWithUpgradeDepsRestoresOnFailure：升级失败也要恢复，且错误原样上抛。
func TestWithUpgradeDepsRestoresOnFailure(t *testing.T) {
	c := startDepDaemon(t, "")
	s := depTestServer(t)
	boom := errors.New("平台返回 19000")
	err := s.withUpgradeDeps(context.Background(), "target-app", true, func(string, float64) {}, func() error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("错误应原样返回: %v", err)
	}
	if c.countOf("start:") != 2 {
		t.Fatalf("升级失败后也必须恢复依赖方，实际 %v", c.snapshot())
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("标记应清空: %#v", items)
	}
}

// TestWithUpgradeDepsRollbackOnStopFailure：依赖方停不掉 → 已停的回滚，且不写标记。
func TestWithUpgradeDepsRollbackOnStopFailure(t *testing.T) {
	c := startDepDaemon(t, "dep-b")
	s := depTestServer(t)
	upgraded := false
	err := s.withUpgradeDeps(context.Background(), "target-app", true, func(string, float64) {}, func() error {
		upgraded = true
		return nil
	})
	if err == nil {
		t.Fatalf("依赖方停不掉时应报错")
	}
	if upgraded {
		t.Fatalf("停不掉依赖方时不应继续升级")
	}
	if c.countOf("start:") != 1 {
		t.Fatalf("应回滚已停用的 dep-a（start×1），实际 %v", c.snapshot())
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("失败路径不应留下标记: %#v", items)
	}
}

// TestSweepDepStopsOnce：上次进程中断留下的标记 → 启动后兜底恢复并清标记。
func TestSweepDepStopsOnce(t *testing.T) {
	c := startDepDaemon(t, "")
	s := depTestServer(t)
	s.addDepStop("target-app", []string{"dep-a", "dep-b"})
	s.sweepDepStopsOnce(context.Background())
	if c.countOf("start:") != 2 {
		t.Fatalf("sweep 应恢复 2 个依赖方，实际 %v", c.snapshot())
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("sweep 后标记应清空: %#v", items)
	}
}

// TestSweepDepStopsStale：超过 24h 的陈旧标记只清理、不动应用。
func TestSweepDepStopsStale(t *testing.T) {
	c := startDepDaemon(t, "")
	s := depTestServer(t)
	s.writeDepStops([]depStopMarker{{
		App: "target-app", Blockers: []string{"dep-a"},
		At: time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	}})
	s.sweepDepStopsOnce(context.Background())
	if n := len(c.snapshot()); n != 0 {
		t.Fatalf("陈旧标记不应启动应用，实际 %v", c.snapshot())
	}
	if items := s.readDepStops(); len(items) != 0 {
		t.Fatalf("陈旧标记应被清理: %#v", items)
	}
}
