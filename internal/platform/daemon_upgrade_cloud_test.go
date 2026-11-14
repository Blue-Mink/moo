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
	"time"
)

// fakeCloudUpg 模拟官方云升级链路（真机当前 pending=0，无法复现）：
// download/task → download/status → stop/check → update/info →
// update/task → common/status。每个环节都可注入故障。
type fakeCloudUpg struct {
	mux *http.ServeMux

	wizardInfo      map[string]any // update/info 的 data.wizardInfo
	updateInfoCode  int            // 非 0 → update/info 直接返回该 code（10100=包未下载）
	dependents      []map[string]any
	statusScript    []string // common/status 依次返回的 data 原文
	statusIdx       int32
	updateTaskCode  int    // 非 0 → update/task 直接返回该 code
	updateTaskCode2 string // updateTaskCode 的 msg
	taskBody        map[string]any

	dlCalls   int32
	depCalls  int32
	infoCalls int32
	taskCalls int32
	statCalls int32
}

func newFakeCloudUpg(t *testing.T) *fakeCloudUpg {
	t.Helper()
	d := &fakeCloudUpg{mux: http.NewServeMux()}

	d.mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.dlCalls, 1)
		fmt.Fprint(w, `{"code":0,"data":{"downloadTaskId":"dl-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/download/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"status":2,"progress":1}}`)
	})
	d.mux.HandleFunc("/rpc/v1/stop/check", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.depCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"dependentApps": map[string]any{
				"isDependency":   len(d.dependents) > 0,
				"dependencyApps": d.dependents,
			}},
		})
	})
	d.mux.HandleFunc("/rpc/v1/update/info", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.infoCalls, 1)
		if d.updateInfoCode != 0 {
			fmt.Fprintf(w, `{"code":%d,"msg":""}`, d.updateInfoCode)
			return
		}
		wi := d.wizardInfo
		if wi == nil {
			wi = map[string]any{"installedVolumeID": 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0, "data": map[string]any{"wizardInfo": wi},
		})
	})
	d.mux.HandleFunc("/rpc/v1/update/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&d.taskCalls, 1)
		_ = json.NewDecoder(r.Body).Decode(&d.taskBody)
		if d.updateTaskCode != 0 {
			fmt.Fprintf(w, `{"code":%d,"msg":%q}`, d.updateTaskCode, d.updateTaskCode2)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"taskId":"t-1"}}`)
	})
	d.mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&d.statCalls, 1))
		if len(d.statusScript) == 0 {
			fmt.Fprint(w, `{"code":0,"data":{"status":2}}`)
			return
		}
		i := n - 1
		if i >= len(d.statusScript) {
			i = len(d.statusScript) - 1
		}
		fmt.Fprint(w, d.statusScript[i])
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

func wizardWithRequiredCheckbox() map[string]any {
	return map[string]any{
		"hasWizard":         true,
		"hasLicense":        false,
		"installedVolumeID": 1,
		"wizardContent": []any{map[string]any{
			"stepTitle": "升级须知与确认",
			"items": []any{
				map[string]any{"type": "tips", "helpText": "<b>1.x 任务与续传状态不兼容</b>"},
				map[string]any{
					"type": "checkbox", "field": "wizard_upgrade_acknowledged",
					"rules":   []any{map[string]any{"required": true, "message": "请先阅读并同意升级说明"}},
					"options": []any{map[string]any{"label": "我已阅读并同意", "value": "true"}},
				},
			},
		}},
	}
}

// 1. 有必填勾选项且用户未提供 → WizardRequiredError，且不得提交 update/task
func TestUpgradeCloud_WizardRequired(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.wizardInfo = wizardWithRequiredCheckbox()
	err := UpgradeCloud(context.Background(), "motrix", "340", "2.0.3", nil, nil)
	var we *WizardRequiredError
	if !errors.As(err, &we) {
		t.Fatalf("应返回 WizardRequiredError，实际 %v", err)
	}
	if len(we.Missing) != 1 || we.Missing[0] != "wizard_upgrade_acknowledged" {
		t.Fatalf("缺项不符: %v", we.Missing)
	}
	if len(we.Content) == 0 {
		t.Fatal("应带回向导定义供前端渲染")
	}
	if n := atomic.LoadInt32(&d.taskCalls); n != 0 {
		t.Fatalf("缺必填项时不得提交 update/task（实际 %d 次）", n)
	}
}

// 2. 必填字段有 initValue → 自动填充后提交（不替用户勾选同意项）
func TestUpgradeCloud_WizardInitValueAutofilled(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.wizardInfo = map[string]any{
		"hasWizard": true, "installedVolumeID": 1,
		"wizardContent": []any{map[string]any{"items": []any{
			map[string]any{"type": "switch", "field": "enable_beta", "initValue": "true",
				"rules": []any{map[string]any{"required": true}}},
		}}},
	}
	if err := UpgradeCloud(context.Background(), "demo", "1", "1.0.1", nil, nil); err != nil {
		t.Fatalf("有默认值的必填项应自动填充: %v", err)
	}
	cp, _ := d.taskBody["customParameters"].([]any)
	if len(cp) != 1 {
		t.Fatalf("customParameters = %v", d.taskBody["customParameters"])
	}
	m, _ := cp[0].(map[string]any)
	if m["key"] != "enable_beta" || m["value"] != "true" {
		t.Fatalf("自动填值不符: %v", m)
	}
}

// 3. 依赖方在运行 → 下载前就拦住（省一次整包下载）
func TestUpgradeCloud_BlocksOnRunningDependents(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.dependents = []map[string]any{{"appName": "marvis-app", "displayName": "Marvis", "status": "running"}}
	err := UpgradeCloud(context.Background(), "nodejs_v22", "98", "22.23.5-1", nil, nil)
	var de *DependencyBlockedError
	if !errors.As(err, &de) {
		t.Fatalf("应返回 DependencyBlockedError，实际 %v", err)
	}
	if !strings.Contains(de.Error(), "Marvis") {
		t.Fatalf("应列出依赖方名称: %v", de.Error())
	}
	if n := atomic.LoadInt32(&d.dlCalls); n != 0 {
		t.Fatalf("依赖未停时不得开始下载（实际 %d 次）", n)
	}
}

// 4. 已停止的依赖方不算阻塞
func TestUpgradeCloud_StoppedDependentsDontBlock(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.dependents = []map[string]any{{"appName": "marvis-app", "status": "stopped"}}
	if err := UpgradeCloud(context.Background(), "nodejs_v22", "98", "22.23.5-1", nil, nil); err != nil {
		t.Fatalf("依赖已停止不应阻塞: %v", err)
	}
	if n := atomic.LoadInt32(&d.taskCalls); n != 1 {
		t.Fatalf("应提交一次 update/task，实际 %d", n)
	}
}

// 5. hasLicense → 引导用户去应用中心，不代同意
func TestUpgradeCloud_LicenseRequired(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.wizardInfo = map[string]any{"hasLicense": true, "installedVolumeID": 1}
	err := UpgradeCloud(context.Background(), "demo", "1", "1.0.1", nil, nil)
	var le *LicenseRequiredError
	if !errors.As(err, &le) {
		t.Fatalf("应返回 LicenseRequiredError，实际 %v", err)
	}
	if n := atomic.LoadInt32(&d.taskCalls); n != 0 {
		t.Fatalf("未接受许可不得提交 update/task（实际 %d）", n)
	}
}

// 6. 平台确定性拒绝（19000）必须透传，不能报成「不再持有任务」
func TestWaitTask_DeterministicRejectSurfaced(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.statusScript = []string{
		`{"code":19000,"msg":"wizard required field x not found","data":{"status":1}}`,
	}
	err := WaitTask(context.Background(), "t-1", "升级", 20*time.Second, nil)
	if err == nil {
		t.Fatal("应失败")
	}
	if !strings.Contains(err.Error(), "19000") {
		t.Fatalf("必须透传平台错误码: %v", err)
	}
	if strings.Contains(err.Error(), "不再持有") {
		t.Fatalf("不得报成任务丢失: %v", err)
	}
	if n := atomic.LoadInt32(&d.statCalls); n > 3 {
		t.Fatalf("确定性拒绝应在 3 次内定论，实际轮询 %d 次", n)
	}
}

// 7. 拒绝后任务被回收（status=5）仍报真实原因
func TestWaitTask_RejectThenRecycled(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.statusScript = []string{
		`{"code":19000,"msg":"wizard required field x not found","data":{"status":1}}`,
		`{"code":19000,"msg":"wizard required field x not found","data":{"status":1}}`,
		`{"code":0,"data":{"status":5}}`,
	}
	err := WaitTask(context.Background(), "t-1", "升级", 20*time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "19000") {
		t.Fatalf("任务被回收时应报最近一次拒绝: %v", err)
	}
}

// 8. 没有任何拒绝的 status=5 → 保持「结果未知」（上层用已装版本复核）
func TestWaitTask_UnknownWithoutReject(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.statusScript = []string{`{"code":0,"data":{"status":5}}`}
	err := WaitTask(context.Background(), "t-1", "升级", 5*time.Second, nil)
	if !errors.Is(err, ErrTaskOutcomeUnknown) {
		t.Fatalf("应返回 ErrTaskOutcomeUnknown，实际 %v", err)
	}
}

// 9. 偶发一次拒绝不应打断（平台瞬时返回不应直接判死）
func TestWaitTask_TransientRejectThenSuccess(t *testing.T) {
	d := newFakeCloudUpg(t)
	d.statusScript = []string{
		`{"code":19000,"msg":"x","data":{"status":1}}`,
		`{"code":0,"data":{"status":2}}`,
	}
	if err := WaitTask(context.Background(), "t-1", "升级", 20*time.Second, nil); err != nil {
		t.Fatalf("单次拒绝后成功应视为成功: %v", err)
	}
}
