//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeInstallDaemon 模拟 app-center daemon 的 FPK 新装链路：
// install/task → common/status（status=2 成功）。记录 install/task 请求体
// 用于断言「应用安装位置」卷号透传（0.6.325 F1）。
type fakeInstallDaemon struct {
	mux       *http.ServeMux
	instBody  map[string]any
	instCalls int32
}

func newFakeInstallDaemon(t *testing.T) *fakeInstallDaemon {
	t.Helper()
	d := &fakeInstallDaemon{mux: http.NewServeMux()}

	d.mux.HandleFunc("/rpc/v1/install/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.instCalls, 1)
		_ = json.NewDecoder(r.Body).Decode(&d.instBody)
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"taskId":"t-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":2,"progress":100}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/fake-inst.sock")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(d.mux)
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	orig := daemonSocket
	daemonSocket = lis.Addr().String()
	t.Cleanup(func() { daemonSocket = orig })
	return d
}

// withDefaultVolumeFn 临时替换默认卷探测缝（模拟多卷/单卷/探测失败环境）。
func withDefaultVolumeFn(t *testing.T, fn func() (int, error)) {
	t.Helper()
	orig := defaultVolumeFn
	defaultVolumeFn = fn
	t.Cleanup(func() { defaultVolumeFn = orig })
}

func fpkStaged() *StagedPackage {
	return &StagedPackage{AppName: "demo-app", Version: "1.0.0", PackageType: "fpk", Installed: false}
}

func instSystemParams(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	if body == nil {
		t.Fatal("未提交 install/task")
	}
	sp, _ := body["systemParameters"].(map[string]any)
	if sp == nil {
		t.Fatalf("systemParameters 缺失: %v", body)
	}
	return sp
}

// 配置卷 >0（设置「应用安装位置」）：必须原样透传，且不得触发默认卷探测。
func TestInstallFpk_ConfiguredVolume_PassedThrough(t *testing.T) {
	d := newFakeInstallDaemon(t)
	withDefaultVolumeFn(t, func() (int, error) {
		t.Error("配置卷 >0 时不得触发默认卷探测")
		return 0, nil
	})
	if err := InstallFpk(context.Background(), fpkStaged(), 2, nil, nil); err != nil {
		t.Fatalf("配置卷=2 应正常安装: %v", err)
	}
	sp := instSystemParams(t, d.instBody)
	if sp["installVolumeID"] != float64(2) || sp["dataVolumeId"] != float64(2) {
		t.Errorf("应透传 volume=2，实际: %v", sp)
	}
}

// 未配置（volume=0）：回落 DefaultVolume 探测结果（回归保护 = 既有行为）。
func TestInstallFpk_Unset_FallsBackToDefaultVolume(t *testing.T) {
	d := newFakeInstallDaemon(t)
	withDefaultVolumeFn(t, func() (int, error) { return 7, nil })
	if err := InstallFpk(context.Background(), fpkStaged(), 0, nil, nil); err != nil {
		t.Fatalf("未配置卷应回落默认卷: %v", err)
	}
	sp := instSystemParams(t, d.instBody)
	if sp["installVolumeID"] != float64(7) || sp["dataVolumeId"] != float64(7) {
		t.Errorf("应传默认探测卷 7，实际: %v", sp)
	}
}

// 多卷且未配置：DefaultVolume 诚实报错必须原样透出，且不得提交安装任务。
func TestInstallFpk_Unset_MultiVolume_HonestError(t *testing.T) {
	d := newFakeInstallDaemon(t)
	withDefaultVolumeFn(t, func() (int, error) {
		return 0, errors.New("系统有 2 个存储卷，无法确定默认安装卷（appcenter-cli default-volume 不可用）")
	})
	err := InstallFpk(context.Background(), fpkStaged(), 0, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "无法确定默认安装卷") {
		t.Fatalf("多卷未配置应诚实报错，实际: %v", err)
	}
	if atomic.LoadInt32(&d.instCalls) != 0 {
		t.Error("报错后不得提交安装任务")
	}
}
