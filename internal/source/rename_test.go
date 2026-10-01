package source

import (
	"errors"
	"testing"
	"time"

	"moo/internal/config"
)

func TestRenameSource(t *testing.T) {
	cfg := &config.Config{
		Sources: []config.SourceRef{
			{Name: "alpha", URL: "https://github.com/a/b"},
			{Name: "beta", URL: "https://github.com/c/d"},
		},
	}
	m := NewManager(cfg)

	// 正常改名 + 缓存/fresh/空源计数迁移
	m.cache["alpha"] = map[string]*App{"x": {Name: "x", Source: "alpha"}}
	m.fresh["alpha"] = time.Now()
	m.careEmpty["alpha"] = 3

	if err := m.RenameSource("alpha", "alpha2"); err != nil {
		t.Fatalf("RenameSource: %v", err)
	}
	if _, ok := m.cache["alpha2"]; !ok {
		t.Error("缓存应迁移到新名")
	}
	if _, ok := m.cache["alpha"]; ok {
		t.Error("旧名缓存应删除")
	}
	if _, ok := m.fresh["alpha2"]; !ok {
		t.Error("拉取时间应迁移到新名")
	}
	if a := m.cache["alpha2"]["x"]; a == nil || a.Source != "alpha2" {
		t.Errorf("缓存内 App.Source 应改为新名: got %q", func() string {
			if a == nil {
				return "<nil>"
			}
			return a.Source
		}())
	}
	if m.careEmpty["alpha2"] != 3 {
		t.Errorf("空源计数应迁移: got %d", m.careEmpty["alpha2"])
	}
	if m.careEmpty["alpha"] != 0 {
		t.Error("旧名空源计数应删除")
	}
	found := false
	for _, s := range m.cfg.Sources {
		if s.Name == "alpha2" && s.URL == "https://github.com/a/b" {
			found = true
		}
	}
	if !found {
		t.Error("配置应改为新名且 URL 不变")
	}

	// 与现有源重名 → ErrExists
	var eExists *ErrExists
	if err := m.RenameSource("beta", "alpha2"); !errors.As(err, &eExists) {
		t.Errorf("重名应报 ErrExists: %v", err)
	}

	// 不存在的源
	var eNoSuch *ErrNoSuchSource
	if err := m.RenameSource("ghost", "g2"); !errors.As(err, &eNoSuch) {
		t.Errorf("不存在的源应报 ErrNoSuchSource: %v", err)
	}

	// 空名
	if err := m.RenameSource("beta", "  "); err == nil {
		t.Error("空名应报错")
	}

	// 同名 = 无操作
	if err := m.RenameSource("beta", "beta"); err != nil {
		t.Errorf("同名应无操作: %v", err)
	}
}
