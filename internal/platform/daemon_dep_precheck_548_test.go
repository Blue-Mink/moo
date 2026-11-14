//go:build linux

package platform

// daemon_dep_precheck_548_test.go —— RunningDependentsForInstalled（0.6.548）。
//
// 社区 FPK 通道补依赖预检的依据：已装升级才预检；未装/预检失败一律 false，
// 不阻断正常安装。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func startDepPrecheckDaemon(t *testing.T, installed []string, depApps string, failCheck bool) *int32 {
	t.Helper()
	var calls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		items := ""
		for i, n := range installed {
			if i > 0 {
				items += ","
			}
			items += fmt.Sprintf(`{"appName":%q,"version":"1.0.0","status":"running"}`, n)
		}
		fmt.Fprintf(w, `{"code":0,"data":{"total":%d,"list":[%s]}}`, len(installed), items)
	})
	mux.HandleFunc("/rpc/v1/stop/check", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if failCheck {
			fmt.Fprint(w, `{"code":500,"msg":"boom"}`)
			return
		}
		fmt.Fprintf(w, `{"code":0,"data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":true,"dependencyApps":[%s]}}}`, depApps)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{}}`)
	})
	lis, err := net.Listen("unix", t.TempDir()+"/d.sock")
	if err != nil {
		t.Fatalf("起假 daemon 失败: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	SetDaemonSocketForTest(lis.Addr().String())
	t.Cleanup(func() { SetDaemonSocketForTest("/var/run/com.trim.app.center.sock") })
	return &calls
}

// 已装（大小写不同）+ 有运行中依赖 → 返回依赖方，且真的查了 stop/check。
func TestRunningDependentsForInstalledHit(t *testing.T) {
	calls := startDepPrecheckDaemon(t, []string{"TargetApp"},
		`{"appName":"dep-a","displayName":"Dep A","status":"running"}`, false)
	deps, checked := RunningDependentsForInstalled(context.Background(), "targetapp")
	if !checked || len(deps) != 1 || deps[0].AppName != "dep-a" {
		t.Fatalf("checked=%v deps=%v", checked, deps)
	}
	if *calls != 1 {
		t.Fatalf("stop/check 调用次数 = %d, want 1", *calls)
	}
}

// 依赖方全部 stopped → 不算阻塞。
func TestRunningDependentsForInstalledStoppedNotBlocking(t *testing.T) {
	startDepPrecheckDaemon(t, []string{"demo"},
		`{"appName":"dep-a","status":"stopped"}`, false)
	deps, checked := RunningDependentsForInstalled(context.Background(), "demo")
	if !checked || len(deps) != 0 {
		t.Fatalf("checked=%v deps=%v（stopped 不应计入）", checked, deps)
	}
}

// 未安装 → 不预检（checked=false），新装路径零额外调用。
func TestRunningDependentsForInstalledNotInstalled(t *testing.T) {
	calls := startDepPrecheckDaemon(t, []string{"other"}, ``, false)
	if _, checked := RunningDependentsForInstalled(context.Background(), "demo"); checked {
		t.Fatal("未装应用不应预检")
	}
	if *calls != 0 {
		t.Fatalf("未装不应调 stop/check，实际 %d 次", *calls)
	}
}

// 预检失败 → checked=false（调用方忽略，不阻断升级）。
func TestRunningDependentsForInstalledPrecheckError(t *testing.T) {
	startDepPrecheckDaemon(t, []string{"demo"}, ``, true)
	if deps, checked := RunningDependentsForInstalled(context.Background(), "demo"); checked || deps != nil {
		t.Fatalf("预检失败应 checked=false，得到 %v %v", checked, deps)
	}
}
