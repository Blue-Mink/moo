//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// fakeCloudDaemon 模拟 app-center daemon 的 cloud 安装链路：
// download/task → download/status(×N) → install/task → common/status。
type fakeCloudDaemon struct {
	mux *http.ServeMux

	dlBody     map[string]any
	instBody   map[string]any
	dlFails    bool
	dlPolls    int32
	instPolls  int32
	dlPath     string
	gotStatus  int32 // common/status 收到的任务终态
}

func newFakeCloudDaemon(t *testing.T) (*fakeCloudDaemon, string) {
	t.Helper()
	d := &fakeCloudDaemon{mux: http.NewServeMux(), dlPath: "/vol1/appcenter-downloads/trim.demo/homebox-0.1.1-all.fpk"}

	d.mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&d.dlBody); err != nil {
			t.Errorf("decode download/task body: %v", err)
		}
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"downloadTaskId":"dl-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/download/status", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&d.dlPolls, 1)
		if d.dlFails {
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":3,"message":"download failed"}}`)
			return
		}
		if n < 2 {
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":1,"progress":0.5}}`)
			return
		}
		fmt.Fprintf(w, `{"code":0,"msg":"","data":{"status":2,"progress":1,"path":%q}}`, d.dlPath)
	})
	d.mux.HandleFunc("/rpc/v1/install/task", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&d.instBody); err != nil {
			t.Errorf("decode install/task body: %v", err)
		}
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"taskId":"t-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		// common/status 进度量程 0~100（实测，见 UpgradeCloud 注释）。
		n := atomic.AddInt32(&d.instPolls, 1)
		if n < 2 {
			fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":1,"progress":100}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":2,"progress":100}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/fake-cloud.sock")
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
	return d, lis.Addr().String()
}

func TestInstallCloud_DaemonFlowAndBody(t *testing.T) {
	d, _ := newFakeCloudDaemon(t)
	var steps []float64
	err := InstallCloud(context.Background(), "homebox", "188", "0.1.1", 1,
		[]WizardParam{{Key: "x", Value: "y"}}, func(p float64) { steps = append(steps, p) })
	if err != nil {
		t.Fatalf("InstallCloud: %v", err)
	}
	// download/task 请求体
	if d.dlBody["packageSourceType"] != "cloud" || d.dlBody["appName"] != "homebox" ||
		d.dlBody["sourceID"] != "188" || d.dlBody["version"] != "0.1.1" || d.dlBody["volumeID"] != float64(1) {
		t.Errorf("download/task body 不符: %v", d.dlBody)
	}
	// install/task 请求体（与应用中心 UI 同款字段）
	if d.instBody["packageType"] != "cloud" || d.instBody["appName"] != "homebox" || d.instBody["version"] != "0.1.1" {
		t.Errorf("install/task body 不符: %v", d.instBody)
	}
	sp, ok := d.instBody["systemParameters"].(map[string]any)
	if !ok {
		t.Fatalf("systemParameters 缺失: %v", d.instBody["systemParameters"])
	}
	if sp["agreedToProtocol"] != true || sp["installVolumeID"] != float64(1) || sp["dataVolumeId"] != float64(1) || sp["immediateStart"] != true {
		t.Errorf("systemParameters 不符: %v", sp)
	}
	cp, ok := d.instBody["customParameters"].([]any)
	if !ok || len(cp) != 1 {
		t.Errorf("customParameters 不符: %v", d.instBody["customParameters"])
	}
	// 进度：先下载段(≤50)后安装段(>50)
	if len(steps) == 0 {
		t.Fatal("未收到任何进度回调")
	}
	if steps[len(steps)-1] != 100 {
		t.Errorf("末段进度应为 100，实际 %v", steps[len(steps)-1])
	}
}

func TestInstallCloud_MissingSourceID(t *testing.T) {
	_, _ = newFakeCloudDaemon(t)
	if err := InstallCloud(context.Background(), "homebox", "", "0.1.1", 1, nil, nil); err == nil {
		t.Fatal("缺 sourceID 应报错")
	}
}

func TestInstallCloud_DownloadFails(t *testing.T) {
	d, _ := newFakeCloudDaemon(t)
	d.dlFails = true
	err := InstallCloud(context.Background(), "homebox", "188", "0.1.1", 1, nil, nil)
	if err == nil {
		t.Fatal("下载失败应报错")
	}
	if d.instBody != nil {
		t.Errorf("下载失败后不得提交安装任务")
	}
}

func TestDownloadCloud_ReturnsPath(t *testing.T) {
	d, _ := newFakeCloudDaemon(t)
	p, err := DownloadCloud(context.Background(), "homebox", "188", "0.1.1", 1, nil)
	if err != nil {
		t.Fatalf("DownloadCloud: %v", err)
	}
	if p != d.dlPath {
		t.Errorf("path = %q，期望 %q", p, d.dlPath)
	}
}
