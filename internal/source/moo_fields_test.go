package source

import "testing"

// moo.json 扩展字段（0.6.269）：desc_html / license / min_fnos / wizard。

func TestTranslateEntryMooFields(t *testing.T) {
	m := map[string]any{
		"name": "demo", "display_name": "Demo", "version": "1.0.0",
		"platform": "all", "labels": "工具",
		"download_url": "https://example.com/demo.fpk",
		"desc":         "纯文本简介",
		"desc_html":    "<p>富文本简介</p>",
		"license":      "MIT",
		"min_fnos":     "1.1.0",
		"wizard": map[string]any{
			"fields": []any{
				map[string]any{"key": "SITE_TITLE", "label": "站点标题", "default": "我的博客", "required": true},
				map[string]any{"key": "ADMIN_PWD", "label": "管理员密码", "required": true},
			},
		},
	}
	a := translateEntry("demo", m, "src", "", "")
	if a == nil {
		t.Fatal("translateEntry returned nil")
	}
	if a.DescHTML != "<p>富文本简介</p>" {
		t.Errorf("DescHTML = %q", a.DescHTML)
	}
	if a.License != "MIT" {
		t.Errorf("License = %q", a.License)
	}
	if a.MinFnos != "1.1.0" {
		t.Errorf("MinFnos = %q", a.MinFnos)
	}
	if a.Wizard == nil || len(a.Wizard.Fields) != 2 {
		t.Fatalf("Wizard = %+v", a.Wizard)
	}
	f0 := a.Wizard.Fields[0]
	if f0.Key != "SITE_TITLE" || f0.Label != "站点标题" || f0.Default != "我的博客" || !f0.Required {
		t.Errorf("field[0] = %+v", f0)
	}
	if !a.Wizard.Fields[1].Required {
		t.Errorf("field[1].Required = false")
	}
}

func TestParseSourceWizardEdgeCases(t *testing.T) {
	// 结构完全缺失 → nil
	if parseSourceWizard(nil) != nil {
		t.Error("nil input should be nil")
	}
	if parseSourceWizard("oops") != nil {
		t.Error("non-object should be nil")
	}
	// 空 fields → nil
	if parseSourceWizard(map[string]any{"fields": []any{}}) != nil {
		t.Error("empty fields should be nil")
	}
	// 无 key 的字段被丢弃，全丢后 → nil
	w := parseSourceWizard(map[string]any{"fields": []any{
		map[string]any{"label": "无键"},
	}})
	if w != nil {
		t.Errorf("keyless fields should yield nil, got %+v", w)
	}
	// 混合：一条有效 + 一条无键 → 只留有效的
	w = parseSourceWizard(map[string]any{"fields": []any{
		map[string]any{"key": "A"},
		map[string]any{"label": "无键"},
	}})
	if w == nil || len(w.Fields) != 1 || w.Fields[0].Key != "A" {
		t.Errorf("mixed fields = %+v", w)
	}
}
