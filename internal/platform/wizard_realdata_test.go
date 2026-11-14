package platform

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// 真实 FPK 的 wizard/install 定义回归（取自本机下载缓存的官方包）：
// 锁住「升级前置校验」的两个方向——
//  1. 不能漏判：必填且无 initValue 的字段必须出现在 Missing 里（否则平台 19000 又变成天书）；
//  2. 不能误判：有 initValue 的必填字段由 AutoFill 自动填，不得算缺（否则升级被无辜拦下）。
//
// 现实分布（2026-11-12 扫 164 个本机 FPK / 98 个含向导）：形状一律是「步骤数组」，
// 18 个包存在「必填无默认」——说明这不是假想场景。
func TestWizardPrecheckOnRealPackages(t *testing.T) {
	cases := []struct {
		file        string
		wantMissing []string
		wantFilled  int
	}{
		{"wizard_1panel.json", []string{"wizard_entrance", "wizard_username", "wizard_password"}, 0},
		{"wizard_moo.json", []string{"wizard_license_agree"}, 3},
		{"wizard_fnchromium.json", []string{"wizard_password"}, 2},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", c.file))
			if err != nil {
				t.Fatalf("读取 fixture 失败: %v", err)
			}

			params, missing, err := AutoFillWizardParams(raw)
			if err != nil {
				t.Fatalf("真实向导定义解析失败（形状变了？）: %v", err)
			}
			if !reflect.DeepEqual(missing, c.wantMissing) {
				t.Fatalf("Missing = %v，期望 %v", missing, c.wantMissing)
			}
			if len(params) != c.wantFilled {
				t.Fatalf("自动填充 %d 个参数，期望 %d（%v）", len(params), c.wantFilled, params)
			}

			// 自动填充的结果再判一次：真实缺口不能因为「有默认值的字段被填上」而消失
			if got := unsatisfiedRequired(raw, params); !reflect.DeepEqual(got, c.wantMissing) {
				t.Fatalf("自动填充后仍应缺 %v，实际 %v", c.wantMissing, got)
			}

			// 重试契约：用户把缺的字段都填上后，不得再判缺（否则会无限弹窗）
			filled := append([]WizardParam{}, params...)
			for _, m := range c.wantMissing {
				filled = append(filled, WizardParam{Key: m, Value: "user-input"})
			}
			if got := unsatisfiedRequired(raw, filled); len(got) != 0 {
				t.Fatalf("用户填完后仍判缺 %v", got)
			}
		})
	}
}

// 空值不算「已提供」：只有空白字符的 value 仍属缺失（前端清空输入框后重试不得被放行）。
func TestUnsatisfiedRequiredIgnoresBlankValues(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "wizard_1panel.json"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	blank := []WizardParam{
		{Key: "wizard_entrance", Value: "   "},
		{Key: "wizard_username", Value: ""},
		{Key: "wizard_password", Value: "\t"},
	}
	if got := unsatisfiedRequired(raw, blank); len(got) != 3 {
		t.Fatalf("空白值应仍判缺 3 项，实际 %v", got)
	}
}
