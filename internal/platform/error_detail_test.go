package platform

import (
	"encoding/json"
	"testing"
)

// 0.6.5xx：升级向导缺项的结构化细节——前端靠它直接渲染向导弹窗，
// 不再新增 /update-wizard 端点，也不必为探测向导先下载目标包。
func TestWizardRequiredErrorDetail(t *testing.T) {
	content := json.RawMessage(`[{"stepTitle":"升级设置","items":[{"type":"input","field":"dir","label":"下载目录","rules":[{"required":true}]}]}]`)
	e := &WizardRequiredError{
		App:     "motrix",
		Version: "1.6.11",
		Missing: []string{"下载目录"},
		Content: content,
	}

	d := e.ErrorDetail()
	if d["kind"] != "wizard_required" {
		t.Fatalf("kind = %v", d["kind"])
	}
	if d["appname"] != "motrix" || d["version"] != "1.6.11" {
		t.Fatalf("appname/version 不对: %v %v", d["appname"], d["version"])
	}
	if m, ok := d["missing"].([]string); !ok || len(m) != 1 || m[0] != "下载目录" {
		t.Fatalf("missing 不对: %v", d["missing"])
	}

	w, ok := d["wizard"].(map[string]any)
	if !ok {
		t.Fatalf("wizard 不是对象: %T", d["wizard"])
	}
	if w["has_wizard"] != true || w["appname"] != "motrix" || w["version"] != "1.6.11" {
		t.Fatalf("wizard 摘要不对: %v", w)
	}
	steps, ok := w["content"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("content 不是「步骤数组」: %T %v", w["content"], w["content"])
	}
	step, _ := steps[0].(map[string]any)
	if step["stepTitle"] != "升级设置" {
		t.Fatalf("步骤标题丢失: %v", step)
	}
}

// 无向导内容时不得凭空造 content 键（前端据此判断「无可渲染字段」）。
func TestWizardRequiredErrorDetailWithoutContent(t *testing.T) {
	e := &WizardRequiredError{App: "x", Version: "1", Missing: []string{"a"}}
	d := e.ErrorDetail()
	w, _ := d["wizard"].(map[string]any)
	if _, has := w["content"]; has {
		t.Fatalf("空 Content 不应带 content 键: %v", w)
	}
}

// 依赖占用：前端要按 appname 去停用，所以每个 blocker 必须带可执行的标识。
func TestDependencyBlockedErrorDetail(t *testing.T) {
	e := &DependencyBlockedError{
		App: "fnmusic",
		Blockers: []DepBlocker{
			{AppName: "fnmedia-ext", DisplayName: "媒体扩展", Status: "running"},
			{AppName: "orphan", Status: "unknown"},
		},
	}
	d := e.ErrorDetail()
	if d["kind"] != "dependency_blocked" || d["appname"] != "fnmusic" {
		t.Fatalf("kind/appname 不对: %v", d)
	}
	bs, ok := d["blockers"].([]map[string]any)
	if !ok || len(bs) != 2 {
		t.Fatalf("blockers 结构不对: %T %v", d["blockers"], d["blockers"])
	}
	if bs[0]["appname"] != "fnmedia-ext" || bs[0]["display_name"] != "媒体扩展" || bs[0]["status"] != "running" {
		t.Fatalf("第 1 个 blocker 不对: %v", bs[0])
	}
	// 没有 display_name 时前端回落 appname，所以字段允许为空但不能丢 appname
	if bs[1]["appname"] != "orphan" {
		t.Fatalf("第 2 个 blocker 缺 appname: %v", bs[1])
	}
}

// 许可协议：只能由用户在应用中心交互同意，前端只给指引不做自动重试。
func TestLicenseRequiredErrorDetail(t *testing.T) {
	e := &LicenseRequiredError{App: "dpanel", Version: "1.2.3"}
	d := e.ErrorDetail()
	if d["kind"] != "license_required" || d["appname"] != "dpanel" || d["version"] != "1.2.3" {
		t.Fatalf("detail 不对: %v", d)
	}
}
