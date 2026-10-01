package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"moo/internal/config"
)

func cfgForCacheTest() *config.Config {
	return &config.Config{Sources: []config.SourceRef{{Name: "src", URL: "https://example.com"}}}
}

// TestSaveCacheConcurrent 回归：120 源并发刷新曾触发多个 SaveCache goroutine
// 交错写同一 tmp，产出「完整 JSON + 脏尾巴」的损坏快照。
func TestSaveCacheConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")

	for round := 0; round < 3; round++ {
		m := NewManager(cfgForCacheTest())
		m.cache["src"] = map[string]*App{
			"appx": {Name: "appx", Source: "src", Version: "1.0.0"},
		}
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := m.SaveCache(path); err != nil {
					t.Errorf("SaveCache: %v", err)
				}
			}()
		}
		wg.Wait()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if err := json.Unmarshal(raw, &cacheFile{}); err != nil {
			t.Fatalf("第 %d 轮并发写后快照不是合法 JSON: %v（size=%d）", round+1, err, len(raw))
		}
	}
}

// TestLoadCacheCorruptedTail 回归：损坏快照（合法文档 + 脏尾巴）仍应恢复。
func TestLoadCacheCorruptedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.json")
	m := NewManager(cfgForCacheTest())
	m.cache["src"] = map[string]*App{"appx": {Name: "appx", Source: "src", Version: "1.0.0"}}
	if err := m.SaveCache(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	// 模拟并发交错损坏：文档后追加一段未闭合的 JSON 尾巴
	if err := os.WriteFile(path, append(raw, []byte(`,"changelog":"x","updated_at":""}`)[:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(cfgForCacheTest())
	n := m2.LoadCache(path)
	if n != 1 {
		t.Fatalf("损坏快照恢复源数 = %d, want 1", n)
	}
}
