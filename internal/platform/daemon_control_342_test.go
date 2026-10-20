//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 0.6.342：daemon 秒完成的启停任务记录会被回收 → common/status 返回 5
//（unknown task），旧逻辑直接报「无法确认操作结果」，但应用实际已启动/停用。
// 新逻辑用权威已装列表复核：达到目标状态即视为成功。
type fakeControlDaemon struct {
	unknown  bool
	appName  string
	runState string
}

func newFakeControlDaemon(t *testing.T, unknown bool, appName, runState string) *fakeControlDaemon {
	t.Helper()
	d := &fakeControlDaemon{unknown: unknown, appName: appName, runState: runState}
	mux := http.NewServeMux()
	okCheck := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":false}}}`)
	}
	mux.HandleFunc("/rpc/v1/start/check", okCheck)
	mux.HandleFunc("/rpc/v1/stop/check", okCheck)
	task := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"taskId":"t-1"}}`)
	}
	mux.HandleFunc("/rpc/v1/start/task", task)
	mux.HandleFunc("/rpc/v1/stop/task", task)
	mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		st := 2
		if d.unknown {
			st = 5
		}
		fmt.Fprintf(w, `{"code":0,"msg":"","data":{"status":%d,"progress":100}}`, st)
	})
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		list, _ := json.Marshal([]map[string]any{{
			"appName": d.appName, "name": d.appName, "version": "1.0.0", "status": d.runState,
		}})
		fmt.Fprintf(w, `{"code":0,"msg":"","data":{"total":1,"list":%s}}`, list)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/fake-ctl.sock")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	orig := daemonSocket
	daemonSocket = lis.Addr().String()
	t.Cleanup(func() { daemonSocket = orig })
	return d
}

// 启动：任务记录被回收但应用已达 running → 不再误报失败。
func TestStartApp_UnknownTaskButRunning_Succeeds(t *testing.T) {
	newFakeControlDaemon(t, true, "dog-dev", "running")
	if err := StartApp(context.Background(), "dog-dev"); err != nil {
		t.Fatalf("应用已 running，结果未知应视为成功，实际: %v", err)
	}
}

// 启动：任务记录被回收且应用仍 stopped → 保留「结果未知」错误（不谎报成功）。
func TestStartApp_UnknownTaskStillStopped_Fails(t *testing.T) {
	newFakeControlDaemon(t, true, "dog-dev", "stopped")
	err := StartApp(context.Background(), "dog-dev")
	if err == nil {
		t.Fatal("应用仍 stopped，不应视为启动成功")
	}
}

// 停用：任务记录被回收但应用已 stopped → 视为成功。
func TestStopApp_UnknownTaskButStopped_Succeeds(t *testing.T) {
	newFakeControlDaemon(t, true, "dog-dev", "stopped")
	if err := StopApp(context.Background(), "dog-dev"); err != nil {
		t.Fatalf("应用已 stopped，结果未知应视为成功，实际: %v", err)
	}
}

// 任务正常成功 → 直接成功（不走复核分支）。
func TestStartApp_TaskSuccess(t *testing.T) {
	newFakeControlDaemon(t, false, "dog-dev", "stopped")
	if err := StartApp(context.Background(), "dog-dev"); err != nil {
		t.Fatalf("任务成功不应报错: %v", err)
	}
}
