package pipeline

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"moo/internal/platform"
)

// 0.6.542：社区源升级新增必填向导字段时，必须返回结构化 WizardRequiredError
// （前端弹升级确认），而不是一句让人「去应用中心配置」的死错误。
// 真机现象：moo.livetest 1.0.0 → 1.1.0（必填 moo_test_note 无默认值）
// 每天每轮自动更新都失败一次，手动点更新同样失败。
func TestMissingWizardErrUpgradeCarriesDetail(t *testing.T) {
	content := json.RawMessage(`[{"stepTitle":"参数","items":[{"type":"text","field":"moo_test_note","label":"备注","rules":[{"required":true}]}]}]`)
	staged := &platform.StagedPackage{AppName: "moo.livetest", Version: "1.1.0", Installed: true}
	wz := &platform.AppWizard{AppName: "moo.livetest", Version: "1.1.0", HasWizard: true, Content: content}

	err := missingWizardErr(staged, wz, []string{"moo_test_note"})
	if err == nil {
		t.Fatal("升级缺必填项必须报错，却返回 nil")
	}
	var wreq *platform.WizardRequiredError
	if !errors.As(err, &wreq) {
		t.Fatalf("必须是 *platform.WizardRequiredError（前端据 detail.kind 弹升级确认），实际 %T: %v", err, err)
	}
	if len(wreq.Content) == 0 {
		t.Fatal("向导定义丢失：前端拿不到字段就无法渲染升级确认弹窗")
	}
	detail := wreq.ErrorDetail()
	if detail["kind"] != "wizard_required" {
		t.Fatalf("detail.kind = %v，期望 wizard_required", detail["kind"])
	}
	if got := detail["missing"].([]string); len(got) != 1 || got[0] != "moo_test_note" {
		t.Fatalf("detail.missing = %v，期望 [moo_test_note]", got)
	}
	wizard, _ := detail["wizard"].(map[string]any)
	if wizard == nil || wizard["content"] == nil {
		t.Fatalf("detail.wizard.content 缺失，前端无法渲染弹窗: %v", detail)
	}
	// 报错文案不能再把用户指向应用中心（社区源应用在那里没有配置入口）
	if strings.Contains(err.Error(), "请在飞牛官方应用中心安装该应用并配置") {
		t.Fatalf("升级路径文案仍在误导用户去应用中心: %v", err)
	}
}

// 新装路径保持原样：安装前前端已用 /api/apps/{key}/wizard 收集参数，
// 走到这里属异常，保留旧文案（不影响安装主流程）。
func TestMissingWizardErrInstallKeepsLegacyMessage(t *testing.T) {
	staged := &platform.StagedPackage{AppName: "demo", Version: "1.0.0", Installed: false}
	err := missingWizardErr(staged, &platform.AppWizard{HasWizard: true}, []string{"site_title"})
	if err == nil {
		t.Fatal("新装缺必填项必须报错")
	}
	var wreq *platform.WizardRequiredError
	if errors.As(err, &wreq) {
		t.Fatal("新装不应返回 WizardRequiredError（安装路径已有独立向导采集）")
	}
	if !strings.Contains(err.Error(), "site_title") {
		t.Fatalf("错误文案须点名缺失字段: %v", err)
	}
}

// 升级缺项时即使向导定义取空（daemon 异常），也不能 panic、仍要带 kind。
func TestMissingWizardErrUpgradeNilWizard(t *testing.T) {
	staged := &platform.StagedPackage{AppName: "demo", Version: "2.0.0", Installed: true}
	err := missingWizardErr(staged, nil, []string{"pin"})
	var wreq *platform.WizardRequiredError
	if !errors.As(err, &wreq) {
		t.Fatalf("期望 WizardRequiredError，实际 %T", err)
	}
	if d := wreq.ErrorDetail(); d["kind"] != "wizard_required" {
		t.Fatalf("detail.kind = %v", d["kind"])
	}
}
