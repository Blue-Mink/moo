package api

import (
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// newVisibleTestServer F9 测试环境：3 个源应用（a@x / b@x / c@y），
// 目录缓存预置（跳过 buildCatalog 的 daemon RPC）。
func newVisibleTestServer(t *testing.T, policy string) *Server {
	t.Helper()
	// cfg.Sources 必须声明：Manager.Apps("") 按已配置（启用）源名聚合缓存
	cfg := &config.Config{DedupPolicy: policy, Sources: []config.SourceRef{
		{Name: "x", URL: "https://example.com/x/fnpack.json"},
		{Name: "y", URL: "https://example.com/y/fnpack.json"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"x": {
			"a": {Name: "a", Source: "x", Version: "1.0", IconURL: "https://i/a.png", ReadmeURL: "https://i/a.md"},
			"b": {Name: "b", Source: "x", Version: "1.0", IconURL: "https://i/b.png", ReadmeURL: "https://i/b.md"},
		},
		"y": {
			"c": {Name: "c", Source: "y", Version: "1.0", IconURL: "https://i/c.png"},
		},
	})
	s := &Server{Cfg: cfg, Src: m}
	// c 的社区卡已并入官方目录（Source=fnos-official，IconURL 未变）
	s.catalogData = []AppInfo{
		{AppName: "a", Source: "x", IconURL: "https://i/a.png"},
		{AppName: "b", Source: "x", IconURL: "https://i/b.png", Hidden: policy != "all"},
		{AppName: "c", Source: OfficialSourceID, IconURL: "https://i/c.png"},
	}
	s.catalogAt = time.Now()
	return s
}

func visibleNames(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, a := range s.visibleSourceApps() {
		names[a.Name] = true
	}
	return names
}

// TestVisibleSourceApps_F9Scope F9 可见集过滤：去重档（one）下隐藏卡的源应用不进
// 预热范围；官方合并卡（Source 已改 fnos-official）经 IconURL 匹配仍在
// 范围；全部档全量；目录未就绪回退全量。
func TestVisibleSourceApps_F9Scope(t *testing.T) {
	// 去重档（one）：b 隐藏
	s := newVisibleTestServer(t, "one")
	names := visibleNames(t, s)
	if !names["a"] || !names["c"] {
		t.Errorf("可见卡 a 与官方合并卡 c 应在预热范围: %v", names)
	}
	if names["b"] {
		t.Error("去重档（one）下隐藏卡 b 不应在预热范围")
	}
	// 全部档：全量（含 b）
	s2 := newVisibleTestServer(t, "all")
	names = visibleNames(t, s2)
	if !names["a"] || !names["b"] || !names["c"] {
		t.Errorf("全部档应全量预热: %v", names)
	}
	// 同名跨源组：a@x 可见 / a@z 隐藏 → a@z 不进范围（去重档真实场景）
	cfg := &config.Config{DedupPolicy: "one", Sources: []config.SourceRef{
		{Name: "x", URL: "https://example.com/x/fnpack.json"},
		{Name: "z", URL: "https://example.com/z/fnpack.json"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"x": {"a": {Name: "a", Source: "x", Version: "1.0", IconURL: "https://i/a.png"}},
		"z": {"a": {Name: "a", Source: "z", Version: "1.0", IconURL: "https://i/az.png"}},
	})
	s3 := &Server{Cfg: cfg, Src: m}
	s3.catalogData = []AppInfo{
		{AppName: "a", Source: "x", IconURL: "https://i/a.png"},
		{AppName: "a", Source: "z", IconURL: "https://i/az.png", Hidden: true},
	}
	s3.catalogAt = time.Now()
	names3 := map[string]bool{}
	for _, a := range s3.visibleSourceApps() {
		names3[a.Source+"|"+a.Name] = true
	}
	if !names3["x|a"] || names3["z|a"] {
		t.Errorf("同名组应只预热可见卡 x|a（z|a 隐藏）: %v", names3)
	}
}

// TestVisibleSourceApps_Fallback F9 目录未就绪回退：源已有应用但目录缓存
// 为空（构建窗口/刚失效）→ 可见集为空 → 回退全量预热（旧行为兜底，保
// 冷启动预热不缺图）。
func TestVisibleSourceApps_Fallback(t *testing.T) {
	s := newVisibleTestServer(t, "one")
	// 模拟目录未就绪：缓存存在但为空（fresh，不触发 buildCatalog 重建）
	s.catalogData = []AppInfo{}
	s.catalogAt = time.Now()
	got := s.visibleSourceApps()
	if len(got) != 3 {
		names := make([]string, 0, len(got))
		for _, a := range got {
			names = append(names, a.Source+"/"+a.Name)
		}
		t.Fatalf("目录未就绪应回退全量 3 个源应用, got %d: %v", len(got), names)
	}
}
