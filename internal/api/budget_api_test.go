package api

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// TestPrioritySourceNames F5 优先级集：收藏源 + 已装应用所在源（含官方
// 合并卡的原社区源），不含官方虚拟源。
func TestPrioritySourceNames(t *testing.T) {
	cfg := &config.Config{FavoriteSources: []string{"fav1"}}
	m := source.NewManager(cfg)
	s := &Server{Cfg: cfg, Src: m}
	// 预置目录缓存（跳过 buildCatalog 的 daemon RPC）
	s.catalogData = []AppInfo{
		{AppName: "app1", Source: "srcA", Installed: true},
		{AppName: "app2", Source: "srcB", Installed: true},
		{AppName: "app2", Source: "srcZ"}, // 已装 app2 的同名社区卡（非规范）
		{AppName: "app3", Source: OfficialSourceID, Installed: true},
		{AppName: "app3", Source: "srcC"}, // 官方合并卡的社区来源
		{AppName: "app4", Source: "srcD"}, // 未装 → 不算
	}
	s.catalogAt = time.Now()
	got := s.prioritySourceNames()
	// srcZ 只是 app2 的同名镜像卡（非已装来源、非官方卡）→ 不进优先级集
	want := []string{"fav1", "srcA", "srcB", "srcC"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("优先级集 = %v, 期望 %v", got, want)
	}
}

// TestBuildSourceSummary_FingerprintStable F5 配套：预算轮 sts 每轮不同
// （轮转刷新子集），但目录状态不变时摘要指纹必须跨轮稳定（否则源同步
// 摘要每小时重推）；失败源标记来自本轮 sts。
func TestBuildSourceSummary_FingerprintStable(t *testing.T) {
	all := []source.SourceStatus{
		{Name: "a", Count: 10}, {Name: "b", Count: 20}, {Name: "c", Count: 30},
	}
	sts1 := []source.SourceStatus{{Name: "a", Count: 10}, {Name: "b", Count: 20}}
	sts2 := []source.SourceStatus{{Name: "c", Count: 30}, {Name: "a", Count: 10}}
	_, full1 := buildSourceSummary(sts1, all, 60, 0, 0)
	_, full2 := buildSourceSummary(sts2, all, 60, 0, 0)
	if full1 != full2 {
		t.Errorf("状态不变时指纹应跨预算轮稳定：\n%q\nvs\n%q", full1, full2)
	}
	// 本轮失败源在明细里标「同步失败」（即使旧缓存还有计数）
	sts3 := []source.SourceStatus{{Name: "b", Error: "boom"}}
	_, full3 := buildSourceSummary(sts3, all, 60, 0, 0)
	if !strings.Contains(full3, "b：同步失败") {
		t.Errorf("失败源应标记同步失败:\n%s", full3)
	}
	if strings.Contains(full3, "b：20 个应用") {
		t.Errorf("失败源不应同时按旧计数展示:\n%s", full3)
	}
	// 总数变化 → 指纹变化
	_, full4 := buildSourceSummary(sts3, append([]source.SourceStatus{}, all...), 59, 0, 0)
	all4 := []source.SourceStatus{{Name: "a", Count: 10}, {Name: "b", Count: 20}, {Name: "c", Count: 29}}
	_, full5 := buildSourceSummary(sts3, all4, 59, 0, 0)
	if full4 == full5 {
		t.Error("状态变化时指纹应变化")
	}
}

// TestSourceRoundNotify_D2Rotation F5 配套：轮转预算下 D2 恢复判定——上轮
// 失败的源须本轮**真的重新刷新且成功**才算恢复。轮转未排到的失败源不得
// 出假「源恢复」通知，且失败态必须跨轮保留（否则下轮真恢复时迁移丢失、
// 恢复通知永不触发）。
func TestSourceRoundNotify_D2Rotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	cfg := config.Default()
	// source_recovered 事件目录缺省=关，测试显式打开（判定逻辑与开关正交）
	cfg.NotifyEvents = map[string]bool{"source_recovered": true}
	cfg.Sources = []config.SourceRef{
		{Name: "x", URL: "https://example.com/x/fnpack.json"},
		{Name: "y", URL: "https://example.com/y/fnpack.json"},
	}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"x": {"a": {Name: "a", Source: "x", Version: "1.0.0"}},
		"y": {"b": {Name: "b", Source: "y", Version: "1.0.0"}},
	})
	s := &Server{Cfg: cfg, Src: m}
	favSeed(s,
		AppInfo{Key: "a@x", AppName: "a", Source: "x", LatestVersion: "1.0.0"},
		AppInfo{Key: "b@y", AppName: "b", Source: "y", LatestVersion: "1.0.0"},
	)
	recovered := func() (n int, content string) {
		for _, e := range cfg.NotifyLog {
			if e.Event == "source_recovered" {
				n++
				content = e.Content
			}
		}
		return
	}
	// 上一轮基线：x 失败（y 正常）
	cfg.NotifyPrevFailed = map[string]bool{"x": true}
	cfg.NotifyPrevTotal = 2

	// ① 轮转轮没排到 x（只刷了 y 且成功）→ 不得出恢复通知，x 失败态保留
	s.sourceRoundNotify([]source.SourceStatus{{Name: "y", Count: 1}})
	if n, c := recovered(); n != 0 {
		t.Fatalf("未被轮转排到的失败源 x 不得假恢复: %d 条 %q", n, c)
	}
	if !cfg.NotifyPrevFailed["x"] {
		t.Fatalf("未排到的失败源必须保留失败态: %v", cfg.NotifyPrevFailed)
	}
	if cfg.NotifyPrevFailed["y"] {
		t.Fatalf("成功的 y 不得留在失败集: %v", cfg.NotifyPrevFailed)
	}

	// ② 下轮排到 x 且成功 → 恰好一条恢复通知（含源名），失败集清空
	s.sourceRoundNotify([]source.SourceStatus{{Name: "x", Count: 1}, {Name: "y", Count: 1}})
	n, c := recovered()
	if n != 1 {
		t.Fatalf("x 真恢复应恰好推 1 次: got %d", n)
	}
	if !strings.Contains(c, "x") {
		t.Fatalf("恢复通知应含源名 x: %q", c)
	}
	if len(cfg.NotifyPrevFailed) != 0 {
		t.Fatalf("恢复后失败集应清空: %v", cfg.NotifyPrevFailed)
	}

	// ③ 再失败 → 不恢复、写回失败集
	s.sourceRoundNotify([]source.SourceStatus{{Name: "x", Error: "boom"}, {Name: "y", Count: 1}})
	if n, _ := recovered(); n != 1 {
		t.Fatalf("再失败的源不得出恢复通知: 累计 %d", n)
	}
	if !cfg.NotifyPrevFailed["x"] {
		t.Fatalf("再失败的 x 应写回失败集: %v", cfg.NotifyPrevFailed)
	}
}
