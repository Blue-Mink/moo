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

// installedFixture 按 fnOS 1.2.05xx daemon 实测形态构造 /rpc/v1/app/installed
// 响应：host 恒为空（=当前访问主机）、serviceName 恒有值。
func installedFixture() string {
	payload := map[string]any{
		"code": 0,
		"msg":  "",
		"data": map[string]any{
			"total": 3,
			"list": []map[string]any{
				{
					"appName": "deepseek.harness", "name": "DeepSeek", "version": "1.0.0",
					"status": "running",
					"control": map[string]any{"isOpen": true, "isStartStop": true, "isUninstall": true, "upgrade": false},
					"appServiceInfo": map[string]any{
						"type": "iframe", "openType": "iframe", "serviceName": "deepseek.harness.Application",
						"urls": map[string]any{"protocol": "", "host": "", "port": "", "path": "/app/deepseek-harness/"},
					},
				},
				{
					"appName": "Gitea", "name": "Gitea", "version": "1.27.3",
					"status": "running",
					"control": map[string]any{"isOpen": true, "isStartStop": true, "isUninstall": true, "upgrade": false},
					"appServiceInfo": map[string]any{
						"type": "url", "openType": "url", "serviceName": "Gitea.Application",
						"urls": map[string]any{"protocol": "http", "host": "", "port": "3000", "path": "/"},
					},
				},
				{
					"appName": "vmweb", "name": "VM", "version": "0.1",
					"status": "running",
					"control": map[string]any{"isOpen": true, "isStartStop": false, "isUninstall": true, "upgrade": false},
					"appServiceInfo": map[string]any{
						"type": "url", "openType": "url", "serviceName": "vmweb.Application",
						"urls": map[string]any{"protocol": "http", "host": "nas.local", "port": "8080", "path": "/"},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// 回归：daemon host 为空时不得下发含 "${host}" 字面量的 URL（浏览器按
// 字面量解析 → ERR_NAME_NOT_RESOLVED，deepseek-harness 实测事故）；
// 协议/端口/path 结构化透传，serviceName 必须透传（壳内打开 bridge 依赖）。
func TestListInstalled_WebPartsNoHostTemplate(t *testing.T) {
	lis, err := net.Listen("unix", t.TempDir()+"/fake-daemon.sock")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, installedFixture())
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = lis
	srv.Start()
	defer srv.Close()

	orig := daemonSocket
	daemonSocket = lis.Addr().String()
	defer func() { daemonSocket = orig }()

	apps, err := ListInstalled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 {
		t.Fatalf("want 3 apps, got %d", len(apps))
	}
	byName := map[string]InstalledApp{}
	for _, a := range apps {
		byName[a.AppName] = a
	}

	// ① iframe 无端口：不下发 URL，path 透传，serviceName 透传
	dh := byName["deepseek.harness"]
	if dh.Web.URL != "" {
		t.Errorf("空 host 不得下发 URL，实际 %q", dh.Web.URL)
	}
	if dh.Web.URL == "" && (dh.Web.Path != "/app/deepseek-harness/" || dh.Web.Port != 0) {
		t.Errorf("path/port 透传错误: %+v", dh.Web)
	}
	if dh.ServiceName != "deepseek.harness.Application" {
		t.Errorf("serviceName 未透传: %q", dh.ServiceName)
	}

	// ② url 有端口：端口解析 + path 透传
	g := byName["Gitea"]
	if g.Web.Port != 3000 || g.Web.Path != "/" || g.Web.Proto != "http" {
		t.Errorf("Gitea 结构化字段错误: %+v", g.Web)
	}
	if g.ServiceName != "Gitea.Application" {
		t.Errorf("Gitea serviceName 未透传: %q", g.ServiceName)
	}

	// ③ 真实 host（非占位符）：拼完整 URL
	v := byName["vmweb"]
	if v.Web.URL != "http://nas.local:8080/" {
		t.Errorf("真实 host 应拼 URL，实际 %q", v.Web.URL)
	}
}

// 回归（0.6.72）：daemon upgradeInfo 是「有更新」的权威数据源（与应用中心
// UI 同源）——面板目录 app/list 的 version 可能滞后（实测 trim.preview
// 目录 0.1.16 vs 升级目标 0.2.4）。必须解析 upgradeInfo.version 与
// sourceID；无更新时 upgradeInfo.version 为空 → UpgradeInfo 保持 nil。
func TestListInstalled_UpgradeInfoParsed(t *testing.T) {
	payload := map[string]any{
		"code": 0,
		"msg":  "",
		"data": map[string]any{
			"total": 2,
			"list": []map[string]any{
				{
					"appName": "trim.preview", "name": "预览", "version": "0.1.16",
					"source": "official", "sourceID": "388", "status": "running",
					"control": map[string]any{"isOpen": false, "isStartStop": true, "isUninstall": true, "upgrade": true},
					"upgradeInfo": map[string]any{
						"version": "0.2.4", "versionID": 2689, "publishAt": 1790249980000,
						"changeLog": "新增19种格式支持预览：<br/>1. 压缩及归档文件；",
						"autoUpdateFailed": true,
					},
					"appServiceInfo": map[string]any{"type": "iframe", "serviceName": "trim.preview.Application"},
				},
				{
					"appName": "Gitea", "name": "Gitea", "version": "1.27.3",
					"source": "official", "sourceID": "48", "status": "running",
					"control": map[string]any{"isOpen": true, "isStartStop": true, "isUninstall": true, "upgrade": false},
					"upgradeInfo": map[string]any{"version": "", "versionID": 0},
					"appServiceInfo": map[string]any{"type": "url", "serviceName": "Gitea.Application"},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)

	lis, err := net.Listen("unix", t.TempDir()+"/fake-daemon.sock")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(b))
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = lis
	srv.Start()
	defer srv.Close()

	orig := daemonSocket
	daemonSocket = lis.Addr().String()
	defer func() { daemonSocket = orig }()

	apps, err := ListInstalled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]InstalledApp{}
	for _, a := range apps {
		byName[a.AppName] = a
	}

	pv := byName["trim.preview"]
	if pv.SourceID != "388" {
		t.Errorf("sourceID 未透传: %q", pv.SourceID)
	}
	if pv.UpgradeInfo == nil {
		t.Fatal("有升级目标时 UpgradeInfo 不应为 nil")
	}
	if pv.UpgradeInfo.Version != "0.2.4" {
		t.Errorf("UpgradeInfo.Version = %q, want 0.2.4", pv.UpgradeInfo.Version)
	}
	if pv.UpgradeInfo.ChangeLog == "" {
		t.Error("UpgradeInfo.ChangeLog 未透传")
	}
	if !pv.Upgrade {
		t.Error("control.upgrade 未透传")
	}

	g := byName["Gitea"]
	if g.UpgradeInfo != nil {
		t.Errorf("无升级目标时 UpgradeInfo 应为 nil，实际 %+v", g.UpgradeInfo)
	}
}
