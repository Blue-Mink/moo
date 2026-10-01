package api

import "testing"

func TestParseChangelogEntries_Versioned(t *testing.T) {
	// Blue-Mink/FnDepot 真实格式：版本段 + ；分隔 + 无版本前缀尾注并入前一条
	raw := "1.20.16: 图标更换 CS-3D A70「柔光」卡片堆叠方案；1.20.15: 列表三元素统一蓝框徽章；1.20.15c: 端口/服务登记 DB 同步修复；1.20.6: 设置页简化（账号预填）。当前仅发布 x86 版，arm64 测试中。"
	got := parseChangelogEntries(raw, "1.20.16", nil)
	if len(got) != 4 {
		t.Fatalf("期望 4 条，得到 %d: %+v", len(got), got)
	}
	if got[0].Version != "1.20.16" || got[1].Version != "1.20.15" || got[2].Version != "1.20.15c" || got[3].Version != "1.20.6" {
		t.Fatalf("版本顺序错误: %+v", got)
	}
	if want := "设置页简化（账号预填）。当前仅发布 x86 版，arm64 测试中。"; got[3].Text != want {
		t.Fatalf("尾注未并入前一条: %q", got[3].Text)
	}
}

func TestParseChangelogEntries_ReleaseOverride(t *testing.T) {
	raw := "1.2.0: 旧说明；1.1.0: 更早"
	rel := map[string]string{"1.2.0": "新版干净说明"}
	got := parseChangelogEntries(raw, "1.2.0", rel)
	if len(got) != 2 || got[0].Version != "1.2.0" || got[0].Text != "新版干净说明" {
		t.Fatalf("releases 覆盖失败: %+v", got)
	}
}

func TestParseChangelogEntries_ReleaseMissingVersionPrepend(t *testing.T) {
	raw := "1.1.0: 旧版说明"
	rel := map[string]string{"1.3.0": "releases 里的最新版"}
	got := parseChangelogEntries(raw, "1.3.0", rel)
	if len(got) != 2 || got[0].Version != "1.3.0" || got[1].Version != "1.1.0" {
		t.Fatalf("缺版本条目未补首位: %+v", got)
	}
}

func TestParseChangelogEntries_PlainTextFallback(t *testing.T) {
	got := parseChangelogEntries("一堆没有版本号的说明文字", "2.0.0", nil)
	if len(got) != 1 || got[0].Version != "2.0.0" || got[0].Text != "一堆没有版本号的说明文字" {
		t.Fatalf("纯文本回退失败: %+v", got)
	}
}

func TestParseChangelogEntries_Empty(t *testing.T) {
	if got := parseChangelogEntries("", "1.0.0", nil); got != nil {
		t.Fatalf("空输入应返回 nil: %+v", got)
	}
}

func TestParseChangelogEntries_OnlyReleases(t *testing.T) {
	rel := map[string]string{"1.1.0": "旧", "1.10.0": "新"}
	got := parseChangelogEntries("", "1.10.0", rel)
	if len(got) != 2 || got[0].Version != "1.10.0" {
		t.Fatalf("releases 排序失败（1.10.0 应高于 1.1.0）: %+v", got)
	}
}
