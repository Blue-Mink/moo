//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 10500 = 平台「同一应用已有任务在跑」（每应用单任务槽）。平台在**建任务之前**
// 就拒绝（响应 data:null，无 taskId），所以提交层重试是安全的。
// fakeBusyDaemon 在 busyPath 上前 N 次返回 10500，其余请求正常。
type fakeBusyDaemon struct {
	mu       sync.Mutex
	busyPath string
	failures int // 注入 10500 的次数（<0 = 永远忙）
	code     int
	calls    map[string]int
}

func newFakeBusyDaemon(t *testing.T, busyPath string, failures int) *fakeBusyDaemon {
	t.Helper()
	d := &fakeBusyDaemon{
		busyPath: busyPath,
		failures: failures,
		code:     daemonCodeStatusUnsupported,
		calls:    map[string]int{},
	}
	mux := http.NewServeMux()
	task := func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		d.calls[r.URL.Path]++
		n, code, limit := d.calls[r.URL.Path], d.code, d.failures
		d.mu.Unlock()
		if r.URL.Path == d.busyPath && (limit < 0 || n <= limit) {
			fmt.Fprintf(w, `{"code":%d,"msg":"","data":null}`, code)
			return
		}
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"taskId":"t-1"}}`)
	}
	for _, p := range []string{
		routeInstallTask, routeUpdateTask, routeUninstallTask,
		routeStartTask, routeStopTask,
	} {
		mux.HandleFunc(p, task)
	}
	ok := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"isSystemVersionMatch":true,"dependentApps":{"isDependency":false}}}`)
	}
	mux.HandleFunc(routeStartCheck, ok)
	mux.HandleFunc(routeStopCheck, ok)
	info := func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"wizardInfo":{"installedVolumeID":1,"hasWizard":false,"hasLicense":false}}}`)
	}
	mux.HandleFunc(routeUpdateInfo, info)
	mux.HandleFunc(routeInstallInfo, info)
	mux.HandleFunc(routeCommonStatus, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":2,"progress":100}}`)
	})
	lis, err := net.Listen("unix", t.TempDir()+"/fake-busy.sock")
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

func (d *fakeBusyDaemon) callCount(path string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls[path]
}

// shortBusyBackoff 把 1s/2s/4s 缩短到毫秒级，避免单测真等 7 秒。
func shortBusyBackoff(t *testing.T, ds ...time.Duration) {
	t.Helper()
	if len(ds) == 0 {
		ds = []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond}
	}
	orig := busyRetryBackoff
	busyRetryBackoff = ds
	t.Cleanup(func() { busyRetryBackoff = orig })
}

// a) 第 1、2 次 10500、第 3 次成功 → 提交成功，调用计数 = 3。
func TestBusyRetry_StartSucceedsAfterTwoBusy(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeStartTask, 2)
	if err := StartApp(context.Background(), "demo"); err != nil {
		t.Fatalf("两次 10500 后应重试成功: %v", err)
	}
	if n := d.callCount(routeStartTask); n != 3 {
		t.Fatalf("start/task 调用次数应为 3，实际 %d", n)
	}
}

// b) 连续 4 次 10500 → 放弃重试，错误保留错误码且带精简文案。
func TestBusyRetry_GivesUpAfterThreeRetries(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeStartTask, -1)
	err := StartApp(context.Background(), "demo")
	if err == nil {
		t.Fatal("持续忙应报错")
	}
	msg := err.Error()
	for _, want := range []string{"10500", "应用中心正忙"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误缺少 %q: %q", want, msg)
		}
	}
	if n := d.callCount(routeStartTask); n != 4 {
		t.Fatalf("应共调用 4 次（首次 + 3 次重试），实际 %d", n)
	}
}

// c) 非 10500（如 10030）→ 只调用 1 次，不重试。
func TestBusyRetry_NoRetryOnOtherErrors(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeStartTask, -1)
	d.code = daemonCodeValidation
	err := StartApp(context.Background(), "demo")
	if err == nil || !strings.Contains(err.Error(), "10030") {
		t.Fatalf("应保留 10030 错误，实际 %v", err)
	}
	if n := d.callCount(routeStartTask); n != 1 {
		t.Fatalf("非 10500 不应重试，实际调用 %d 次", n)
	}
}

// d) ctx 在退避期间取消 → 快速返回 ctx.Err()（不等满退避）。
func TestBusyRetry_CancelDuringBackoff(t *testing.T) {
	shortBusyBackoff(t, 5*time.Second)
	d := newFakeBusyDaemon(t, routeStartTask, -1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := StartApp(ctx, "demo")
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 ctx 取消错误，实际 %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("退避中取消应快速返回，实际耗时 %v", elapsed)
	}
	if n := d.callCount(routeStartTask); n != 1 {
		t.Fatalf("取消后不应再提交，实际调用 %d 次", n)
	}
}

// e1) install/task：忙一次 → 重试成功。
func TestBusyRetry_InstallTaskRetries(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeInstallTask, 1)
	st := &StagedPackage{AppName: "demo", Version: "1.0.0", PackageType: "fpk", Path: "/tmp/demo.fpk"}
	if err := InstallFpk(context.Background(), st, 1, nil, nil); err != nil {
		t.Fatalf("install 应重试成功: %v", err)
	}
	if n := d.callCount(routeInstallTask); n != 2 {
		t.Fatalf("install/task 应调用 2 次，实际 %d", n)
	}
}

// e2) update/task：忙一次 → 重试成功。
func TestBusyRetry_UpdateTaskRetries(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeUpdateTask, 1)
	st := &StagedPackage{AppName: "demo", Version: "2.0.0", PackageType: "fpk", Installed: true, Path: "/tmp/demo.fpk"}
	if err := UpgradeFpk(context.Background(), st, nil, nil); err != nil {
		t.Fatalf("update 应重试成功: %v", err)
	}
	if n := d.callCount(routeUpdateTask); n != 2 {
		t.Fatalf("update/task 应调用 2 次，实际 %d", n)
	}
}

// e3) uninstall/task：忙一次 → 重试成功（卸载同样只提交一次任务）。
func TestBusyRetry_UninstallRetries(t *testing.T) {
	shortBusyBackoff(t)
	d := newFakeBusyDaemon(t, routeUninstallTask, 1)
	if err := Uninstall(context.Background(), "demo", false, nil); err != nil {
		t.Fatalf("uninstall 应重试成功: %v", err)
	}
	if n := d.callCount(routeUninstallTask); n != 2 {
		t.Fatalf("uninstall/task 应调用 2 次，实际 %d", n)
	}
}
