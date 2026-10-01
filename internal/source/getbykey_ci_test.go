package source

import (
	"testing"

	"moo/internal/config"
)

// TestGetByKeyCaseInsensitive 0.6.208：裸 key 大小写不敏感回退。
// 场景：已装应用规范卡 key 取 daemon appname（"Gitea"），源 feed appname
// 为 "gitea"——精确匹配恒 404，回退必须能解析出 feed 条目。
func TestGetByKeyCaseInsensitive(t *testing.T) {
	cfg := &config.Config{
		Sources: []config.SourceRef{
			{Name: "fnos-store", URL: "https://github.com/conversun/fnos-apps"},
			{Name: "other", URL: "https://github.com/o/p"},
		},
	}
	m := NewManager(cfg)
	m.cache["fnos-store"] = map[string]*App{
		"gitea":  {Name: "gitea", Source: "fnos-store", Version: "28.0.0"},
		"1Panel": {Name: "1Panel", Source: "fnos-store", Version: "1.10.34-lts"},
	}
	m.cache["other"] = map[string]*App{
		"nodejs_v22": {Name: "nodejs_v22", Source: "other", Version: "22.23.2"},
	}

	// 精确命中不受影响
	if a, err := m.GetByKey("1Panel"); err != nil || a.Version != "1.10.34-lts" {
		t.Errorf("精确命中应成功: a=%v err=%v", a, err)
	}
	// 大小写回退：daemon "Gitea" → feed "gitea"
	if a, err := m.GetByKey("GITEA"); err != nil || a.Name != "gitea" {
		t.Errorf("大小写回退应命中 feed 条目: a=%v err=%v", a, err)
	}
	// 完全不存在仍 404
	if _, err := m.GetByKey("Nope"); err == nil {
		t.Error("不存在的 key 应报错")
	}
}

// TestGetByKeyCaseInsensitiveAmbiguous 回退歧义（无精确命中、两源同名仅
// 大小写不同）→ 诚实报错；有精确命中时优先精确、不走回退。
func TestGetByKeyCaseInsensitiveAmbiguous(t *testing.T) {
	cfg := &config.Config{
		Sources: []config.SourceRef{
			{Name: "s1", URL: "https://github.com/a/b"},
			{Name: "s2", URL: "https://github.com/c/d"},
		},
	}
	m := NewManager(cfg)
	m.cache["s1"] = map[string]*App{"Gitea": {Name: "Gitea", Source: "s1"}}
	m.cache["s2"] = map[string]*App{"gitea": {Name: "gitea", Source: "s2"}}
	// 无精确命中 + 两个大小写变体 → 歧义报错
	if _, err := m.GetByKey("GITEA"); err == nil {
		t.Error("歧义回退应报错")
	}
	// 精确命中优先于回退
	if a, err := m.GetByKey("gitea"); err != nil {
		t.Errorf("s2 精确命中应成功: %v", err)
	} else if a.Source != "s2" {
		t.Errorf("精确命中应取 s2: got %s", a.Source)
	}
	if a, err := m.GetByKey("Gitea"); err != nil {
		t.Errorf("s1 精确命中应成功: %v", err)
	} else if a.Source != "s1" {
		t.Errorf("精确命中应取 s1: got %s", a.Source)
	}
}
