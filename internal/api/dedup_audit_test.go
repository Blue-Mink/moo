package api

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"unicode"
)

// TestDedupAuditRealCatalog 离线核查（发布前人工跑，默认跳过）：把生产目录快照喂给
// 真实的 applyDedupPolicy，与线上实际可见集逐组比对，并统计「不同名的隐藏卡」。
//
//	MOO_DEDUP_AUDIT=<cards.json>     {"cards":[AppInfo...]}（按源抓取的全量卡）
//	MOO_DEDUP_VISIBLE=<apps.json>    {"apps":[...]}（/api/apps 无源过滤 = 可见集）
//	MOO_DEDUP_AUDIT_OUT=<report.json>
func TestDedupAuditRealCatalog(t *testing.T) {
	cardsPath := os.Getenv("MOO_DEDUP_AUDIT")
	if cardsPath == "" {
		t.Skip("未设置 MOO_DEDUP_AUDIT，跳过（人工审计用）")
	}
	outPath := os.Getenv("MOO_DEDUP_AUDIT_OUT")

	var raw struct {
		Cards []AppInfo `json:"cards"`
	}
	b, err := os.ReadFile(cardsPath)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("解析快照失败: %v", err)
	}
	visible := map[string]bool{}
	if visPath := os.Getenv("MOO_DEDUP_VISIBLE"); visPath != "" {
		var v struct {
			Apps []AppInfo `json:"apps"`
		}
		vb, err := os.ReadFile(visPath)
		if err != nil {
			t.Fatalf("读可见集失败: %v", err)
		}
		if err := json.Unmarshal(vb, &v); err != nil {
			t.Fatalf("解析可见集失败: %v", err)
		}
		for _, a := range v.Apps {
			visible[a.Key] = true
		}
	}

	out := raw.Cards
	// 与 buildCatalog 相同的确定性排序（平局靠顺序时保持可比）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].AppName < out[j].AppName
	})
	byName := countByAppName(out)
	applyDedupPolicy(out, "one")

	type mm struct {
		AppName string `json:"appname"`
		Want    string `json:"want"`
		Got     string `json:"got"`
	}
	var mismraw []mm
	hiddenDiffName := 0
	var samples []string
	for name, idxs := range byName {
		want := ""
		for _, i := range idxs {
			if !out[i].Hidden {
				want = out[i].Key
				break
			}
		}
		got := ""
		for _, i := range idxs {
			if visible[out[i].Key] {
				got = out[i].Key
				break
			}
		}
		if got != want {
			mismraw = append(mismraw, mm{AppName: name, Want: want, Got: got})
		}
		// 统计：被隐藏的卡里，显示名归一后与代表卡不同（潜在「同名不同应用」）
		repDN := ""
		for _, i := range idxs {
			if !out[i].Hidden {
				repDN = normName(out[i].DisplayName)
				break
			}
		}
		for _, i := range idxs {
			if out[i].Hidden && repDN != "" && normName(out[i].DisplayName) != repDN {
				hiddenDiffName++
				if len(samples) < 10 {
					samples = append(samples, name+"："+out[i].Source+"／"+out[i].DisplayName)
				}
			}
		}
	}
	sort.Slice(mismraw, func(i, j int) bool { return mismraw[i].AppName < mismraw[j].AppName })
	rep := map[string]any{
		"groups":                  len(byName),
		"cards":                   len(out),
		"mismatches":              mismraw,
		"hidden_with_diff_name":   hiddenDiffName,
		"hidden_diff_name_sample": samples,
	}
	if outPath != "" {
		rb, _ := json.MarshalIndent(rep, "", " ")
		_ = os.WriteFile(outPath, rb, 0o644)
	}
	t.Logf("组数=%d 卡数=%d 与线上可见集不一致=%d 不同名隐藏卡=%d",
		len(byName), len(out), len(mismraw), hiddenDiffName)
	if len(mismraw) > 0 {
		for i, m := range mismraw {
			if i >= 10 {
				break
			}
			t.Logf("  组 %s：期望 %s 实际 %s", m.AppName, m.Want, m.Got)
		}
		t.Fatalf("实现与线上实际可见集不一致 %d 例", len(mismraw))
	}
}

// normName 与生产口径一致：小写 + 只保留字母数字。
func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func countByAppName(out []AppInfo) map[string][]int {
	m := map[string][]int{}
	for i := range out {
		if out[i].AppName == "" {
			continue
		}
		m[out[i].AppName] = append(m[out[i].AppName], i)
	}
	return m
}
