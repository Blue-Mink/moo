package platform

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// 真机只读探针：用**生产代码**（ListInstalled + routeUpdateInfo +
// AutoFillWizardParams/unsatisfiedRequired）去看「本机现在哪些应用的升级会被
// 要求先确认」。不下载、不安装、不改任何应用状态——只读 daemon 的 update-info。
//
// 用法（必须 root，daemon socket 0660 root:root）：
//
//	MOO_LIVE_PROBE=1 go test -count=1 -run TestLiveUpdateInfoProbe -v ./internal/platform/
//
// 不设环境变量时自动 Skip，CI 与日常 `go test ./...` 不受影响。
func TestLiveUpdateInfoProbe(t *testing.T) {
	if os.Getenv("MOO_LIVE_PROBE") != "1" {
		t.Skip("设置 MOO_LIVE_PROBE=1 才跑（真机只读探测）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	apps, err := ListInstalled(ctx)
	if err != nil {
		t.Fatalf("读取已装应用失败（daemon socket 权限？需 root）: %v", err)
	}
	pending, hit := 0, 0
	for _, a := range apps {
		if a.UpgradeInfo == nil || a.UpgradeInfo.Version == "" {
			continue
		}
		pending++
		var info infoResponse
		err := daemonCall(ctx, routeUpdateInfo, map[string]any{
			"appName":       a.AppName,
			"updateVersion": a.UpgradeInfo.Version,
			"packageType":   "cloud",
			"language":      "zh-CN",
		}, &info)
		if err != nil {
			t.Logf("SKIP  %-22s %s -> %s  info 调用失败: %v", a.AppName, a.Version, a.UpgradeInfo.Version, err)
			continue
		}
		w := info.WizardInfo
		if !w.HasWizard || len(w.WizardContent) == 0 {
			t.Logf("OK    %-22s %s -> %s  无向导（升级不需要确认）", a.AppName, a.Version, a.UpgradeInfo.Version)
			continue
		}
		params, autoMissing, perr := AutoFillWizardParams(w.WizardContent)
		if perr != nil {
			t.Logf("BAD   %-22s %s -> %s  向导解析失败: %v", a.AppName, a.Version, a.UpgradeInfo.Version, perr)
			continue
		}
		// 与 UpgradeCloud 里完全相同的判定
		stillMissing := unsatisfiedRequired(w.WizardContent, params)
		if len(stillMissing) > 0 {
			hit++
			t.Logf("NEED  %-22s %s -> %s  升级需确认: %v（自动填充 %d 项）",
				a.AppName, a.Version, a.UpgradeInfo.Version, stillMissing, len(params))
			e := &WizardRequiredError{App: a.AppName, Version: a.UpgradeInfo.Version,
				Missing: stillMissing, Content: w.WizardContent}
			if b, err := json.MarshalIndent(e.ErrorDetail(), "      ", "  "); err == nil {
				t.Logf("      detail = %s", b)
			}
			continue
		}
		t.Logf("OK    %-22s %s -> %s  有向导但必填项都有默认值（自动填充 %d 项，缺省 %v，无需确认）",
			a.AppName, a.Version, a.UpgradeInfo.Version, len(params), autoMissing)
	}
	t.Logf("汇总：已装 %d 个 / 待升级 %d 个 / 需确认 %d 个", len(apps), pending, hit)
}
