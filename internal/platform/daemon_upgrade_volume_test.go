//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeUpgradeDaemon 模拟 app-center daemon 的升级链路：
// （cloud: download/task → download/status）→ update/info → update/task → common/status。
// wizardInfoRaw 为 update/info 返回的 data.wizardInfo（取自真机实测原始结构）。
type fakeUpgradeDaemon struct {
	mux *http.ServeMux

	dlBody   map[string]any
	infoBody map[string]any
	updBody  map[string]any
	updCalls int32
}

func newFakeUpgradeDaemon(t *testing.T, wizardInfoRaw string) *fakeUpgradeDaemon {
	t.Helper()
	d := &fakeUpgradeDaemon{mux: http.NewServeMux()}

	d.mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&d.dlBody)
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"downloadTaskId":"dl-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/download/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":2,"progress":1,"path":"/tmp/x.fpk"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/update/info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&d.infoBody)
		fmt.Fprintf(w, `{"code":0,"msg":"","data":{"wizardInfo":%s,"relation":{},"constraint":{}}}`, wizardInfoRaw)
	})
	d.mux.HandleFunc("/rpc/v1/update/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.updCalls, 1)
		_ = json.NewDecoder(r.Body).Decode(&d.updBody)
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"taskId":"t-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"","data":{"status":2,"progress":100}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/fake-upg.sock")
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

// 真机实测结构（trim.preview 0.2.5，fnOS 1.2.06xx，2026-10-10）：
// 系统空间应用 installedVolumeID=0 + installedType="root"，应用中心 UI 携 0 升级成功。
const rootVol0Wizard = `{"source":"official","sourceID":"388","appName":"trim.preview","version":"0.2.5","installType":"root","installedType":"root","installedVolumeID":0,"hasWizard":false}`

// 系统空间（root）应用：volume=0 属平台正常态，必须放行并照实传 0。
func TestUpgradeCloud_RootVol0_ProceedsZero(t *testing.T) {
	d := newFakeUpgradeDaemon(t, rootVol0Wizard)
	if err := UpgradeCloud(context.Background(), "trim.preview", "388", "0.2.5", nil, nil); err != nil {
		t.Fatalf("系统空间应用 volume=0 应放行升级，实际: %v", err)
	}
	if d.updBody == nil {
		t.Fatal("未提交 update/task")
	}
	sp, _ := d.updBody["systemParameters"].(map[string]any)
	if sp == nil {
		t.Fatalf("systemParameters 缺失: %v", d.updBody)
	}
	if sp["installVolumeID"] != float64(0) || sp["dataVolumeId"] != float64(0) {
		t.Errorf("系统空间应用应传 volume=0，实际: %v", sp)
	}
	if d.updBody["packageType"] != "cloud" {
		t.Errorf("packageType 应为 cloud: %v", d.updBody["packageType"])
	}
}

// 卷应用取不到卷号：必须中止保护（原保护目标保留）。
func TestUpgradeCloud_NonRootVol0_Aborts(t *testing.T) {
	d := newFakeUpgradeDaemon(t, `{"appName":"homebox","version":"0.2.0","installType":"user","installedType":"user","installedVolumeID":0}`)
	err := UpgradeCloud(context.Background(), "homebox", "188", "0.2.0", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "无法确定") {
		t.Fatalf("卷应用卷号=0 应中止保护，实际: %v", err)
	}
	if atomic.LoadInt32(&d.updCalls) != 0 {
		t.Error("中止后不得提交升级任务")
	}
}

// 老 daemon 不下发 installedType 字段：保守回落旧行为（volume=0 中止），零行为变化。
func TestUpgradeCloud_MissingInstalledType_Vol0_Aborts(t *testing.T) {
	d := newFakeUpgradeDaemon(t, `{"appName":"homebox","version":"0.2.0","installedVolumeID":0}`)
	err := UpgradeCloud(context.Background(), "homebox", "188", "0.2.0", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "无法确定") {
		t.Fatalf("字段缺失且卷号=0 应中止（回落旧行为），实际: %v", err)
	}
	if atomic.LoadInt32(&d.updCalls) != 0 {
		t.Error("中止后不得提交升级任务")
	}
}

// 卷应用正常路径：卷号照传（回归保护，确保修复不误伤既有用法）。
func TestUpgradeCloud_Vol2_Proceeds(t *testing.T) {
	d := newFakeUpgradeDaemon(t, `{"appName":"homebox","version":"0.2.0","installType":"user","installedType":"user","installedVolumeID":2}`)
	if err := UpgradeCloud(context.Background(), "homebox", "188", "0.2.0", nil, nil); err != nil {
		t.Fatalf("卷应用 volume=2 应正常升级: %v", err)
	}
	sp, _ := d.updBody["systemParameters"].(map[string]any)
	if sp["installVolumeID"] != float64(2) || sp["dataVolumeId"] != float64(2) {
		t.Errorf("应传 volume=2，实际: %v", sp)
	}
}

// FPK 通道同规则：系统空间应用放行传 0。
func TestUpgradeFpk_RootVol0_ProceedsZero(t *testing.T) {
	d := newFakeUpgradeDaemon(t, rootVol0Wizard)
	err := UpgradeFpk(context.Background(), &StagedPackage{
		AppName: "trim.preview", Version: "0.2.5", PackageType: "fpk", Installed: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("系统空间应用 FPK 升级应放行 volume=0: %v", err)
	}
	sp, _ := d.updBody["systemParameters"].(map[string]any)
	if sp["installVolumeID"] != float64(0) || sp["dataVolumeId"] != float64(0) {
		t.Errorf("FPK 通道也应传 volume=0，实际: %v", sp)
	}
}

// FPK 通道卷应用取不到卷号：中止。
func TestUpgradeFpk_NonRootVol0_Aborts(t *testing.T) {
	d := newFakeUpgradeDaemon(t, `{"appName":"homebox","version":"0.2.0","installedType":"user","installedVolumeID":0}`)
	err := UpgradeFpk(context.Background(), &StagedPackage{
		AppName: "homebox", Version: "0.2.0", PackageType: "fpk", Installed: true,
	}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "无法确定") {
		t.Fatalf("卷应用 FPK 卷号=0 应中止，实际: %v", err)
	}
	if atomic.LoadInt32(&d.updCalls) != 0 {
		t.Error("中止后不得提交升级任务")
	}
}
