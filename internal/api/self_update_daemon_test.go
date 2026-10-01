package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// startMockDaemon 在临时 unix socket 上起一个模拟 appcenter daemon，
// 按真实信封 {"code":0,"msg":"","data":{...}} 应答，返回 (socketPath, close)。
func startMockDaemon(t *testing.T, routes map[string]http.HandlerFunc) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		h, ok := routes[r.URL.Path]
		if !ok {
			w.Write([]byte(`{"code":404,"msg":"no route","data":null}`))
			return
		}
		h(w, r)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return sock, func() {
		srv.Close()
		ln.Close()
	}
}

func writeEnvelope(w http.ResponseWriter, data any) {
	b, _ := json.Marshal(data)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"code":0,"msg":"","data":%s}`, b)
}

func TestDaemonCallEnvelope(t *testing.T) {
	sock, closeDaemon := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/common/status": func(w http.ResponseWriter, r *http.Request) {
			writeEnvelope(w, map[string]any{"status": 5, "taskId": ""})
		},
	})
	defer closeDaemon()

	old := selfUpdateSocketPath
	selfUpdateSocketPath = sock
	defer func() { selfUpdateSocketPath = old }()

	if !daemonReachable() {
		t.Fatal("daemonReachable 应为 true")
	}
	var st struct {
		Status int `json:"status"`
	}
	if err := daemonCall(t.Context(), "/rpc/v1/common/status", map[string]any{"taskId": "x"}, &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != 5 {
		t.Errorf("status=%d want 5", st.Status)
	}

	// code!=0 必须报错
	sock2, close2 := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/update/task": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"code":10237,"msg":"boom","data":null}`))
		},
	})
	defer close2()
	selfUpdateSocketPath = sock2
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(t.Context(), "/rpc/v1/update/task", map[string]any{}, &task); err == nil {
		t.Error("code!=0 应返回错误")
	}
}

func TestStageSelfUpdateFpk(t *testing.T) {
	statuses := []int{1, 2} // 第一次 running，第二次 success
	calls := 0
	sock, closeDaemon := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/download/task": func(w http.ResponseWriter, r *http.Request) {
			writeEnvelope(w, map[string]any{"downloadTaskId": "dl-1"})
		},
		"/rpc/v1/download/status": func(w http.ResponseWriter, r *http.Request) {
			i := min(calls, len(statuses)-1)
			calls++
			writeEnvelope(w, map[string]any{
				"status":      statuses[i],
				"appName":     "moo",
				"version":     "0.6.112",
				"packageType": "fpk",
				"installed":   true,
			})
		},
	})
	defer closeDaemon()
	old := selfUpdateSocketPath
	selfUpdateSocketPath = sock
	defer func() { selfUpdateSocketPath = old }()

	staged, err := stageSelfUpdateFpk(t.Context(), "/tmp/whatever.fpk")
	if err != nil {
		t.Fatal(err)
	}
	if staged.AppName != "moo" || staged.Version != "0.6.112" || !staged.Installed || staged.PackageType != "fpk" {
		t.Errorf("staged=%+v", staged)
	}

	// 失败状态
	calls = 0
	failed := true
	sock2, close2 := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/download/task": func(w http.ResponseWriter, r *http.Request) {
			writeEnvelope(w, map[string]any{"downloadTaskId": "dl-2"})
		},
		"/rpc/v1/download/status": func(w http.ResponseWriter, r *http.Request) {
			if failed {
				writeEnvelope(w, map[string]any{"status": 3, "message": "解压失败"})
			} else {
				writeEnvelope(w, map[string]any{"status": 2, "appName": "moo", "version": "1"})
			}
		},
	})
	defer close2()
	selfUpdateSocketPath = sock2
	if _, err := stageSelfUpdateFpk(t.Context(), "/tmp/whatever.fpk"); err == nil {
		t.Error("非 running/success 状态应报错")
	}
}

func TestWaitSelfUpdateTask(t *testing.T) {
	// 第一次 running，第二次 success
	steps := []int{1, 2}
	calls := 0
	sock, closeDaemon := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/common/status": func(w http.ResponseWriter, r *http.Request) {
			i := min(calls, len(steps)-1)
			calls++
			writeEnvelope(w, map[string]any{"status": steps[i]})
		},
	})
	defer closeDaemon()
	old := selfUpdateSocketPath
	selfUpdateSocketPath = sock
	defer func() { selfUpdateSocketPath = old }()

	if err := waitSelfUpdateTask(t.Context(), "t-1"); err != nil {
		t.Fatal(err)
	}

	// unknown → 视为已移交（nil）
	sock2, close2 := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/common/status": func(w http.ResponseWriter, r *http.Request) {
			writeEnvelope(w, map[string]any{"status": 5})
		},
	})
	defer close2()
	selfUpdateSocketPath = sock2
	if err := waitSelfUpdateTask(t.Context(), "t-2"); err != nil {
		t.Errorf("unknown 应视为已移交: %v", err)
	}

	// 真实失败（状态 99 + message）
	sock3, close3 := startMockDaemon(t, map[string]http.HandlerFunc{
		"/rpc/v1/common/status": func(w http.ResponseWriter, r *http.Request) {
			writeEnvelope(w, map[string]any{"status": 99, "message": "版本冲突"})
		},
	})
	defer close3()
	selfUpdateSocketPath = sock3
	if err := waitSelfUpdateTask(t.Context(), "t-3"); err == nil {
		t.Error("失败状态应报错")
	}
}

// TestDaemonReachableFalse socket 不存在时返回 false（不 panic）。
func TestDaemonReachableFalse(t *testing.T) {
	old := selfUpdateSocketPath
	selfUpdateSocketPath = filepath.Join(os.TempDir(), "moo-nosuch-daemon.sock")
	defer func() { selfUpdateSocketPath = old }()
	if daemonReachable() {
		t.Error("无 socket 时 daemonReachable 应为 false")
	}
}
