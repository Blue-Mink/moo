package api

import (
	"net/http/httptest"
	"testing"
)

// 0.6.5xx：`?wizard=` 是安装与升级共用的契约。解析失败必须显式报错——
// 此前官方通道静默吞掉坏 JSON，用户会在「升级确认」里无限循环。
func TestParseWizardQuery(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantLen int
		wantErr bool
	}{
		{"无参数", "", 0, false},
		{"单个字段", `?wizard=%5B%7B%22key%22%3A%22dir%22%2C%22value%22%3A%22%2Fvol1%2Fd%22%7D%5D`, 1, false},
		{"多个字段", `?wizard=%5B%7B%22key%22%3A%22a%22%2C%22value%22%3A%221%22%7D%2C%7B%22key%22%3A%22b%22%2C%22value%22%3A%222%22%7D%5D`, 2, false},
		{"空数组", `?wizard=%5B%5D`, 0, false},
		{"坏 JSON 必须报错", `?wizard=not-json`, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/apps/x/update"+c.query, nil)
			got, err := parseWizardQuery(req)
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际解析出 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if len(got) != c.wantLen {
				t.Fatalf("字段数 = %d，期望 %d（%v）", len(got), c.wantLen, got)
			}
		})
	}
}

// 解析结果必须原样保留 key/value（升级重试要靠它填 customParameters）。
func TestParseWizardQueryKeepsValues(t *testing.T) {
	req := httptest.NewRequest("POST",
		`/api/apps/motrix/update?wizard=%5B%7B%22key%22%3A%22download_dir%22%2C%22value%22%3A%22%2Fvol1%2F1000%2FDownloads%22%7D%5D`, nil)
	got, err := parseWizardQuery(req)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got) != 1 || got[0].Key != "download_dir" || got[0].Value != "/vol1/1000/Downloads" {
		t.Fatalf("参数被改写: %+v", got)
	}
}
