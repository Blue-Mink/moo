package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 0.6.330 WP1：记忆的写入/过期/URL 隔离/落盘。
func TestPrefStoreBasic(t *testing.T) {
	p := newPrefStore()
	if _, ok := p.Get("s1", "https://a.example/r"); ok {
		t.Fatal("空记忆不应命中")
	}
	p.Put("s1", "https://a.example/r", "fnpack.json")
	if n, ok := p.Get("s1", "https://a.example/r"); !ok || n != "fnpack.json" {
		t.Fatalf("应命中 fnpack.json，实际 %q ok=%v", n, ok)
	}
	// 同名字、不同 URL → 不串味
	if _, ok := p.Get("s1", "https://b.example/r"); ok {
		t.Fatal("URL 变了记忆必须失效")
	}
	p.Clear("s1", "https://a.example/r")
	if _, ok := p.Get("s1", "https://a.example/r"); ok {
		t.Fatal("Clear 后不应命中")
	}
}

func TestPrefStoreExpiry(t *testing.T) {
	p := newPrefStore()
	k := prefKey("s", "https://a.example/r")
	p.prefs[k] = IndexPref{Name: "moo.json", At: time.Now().Add(-indexPrefTTL - time.Minute)}
	if _, ok := p.Get("s", "https://a.example/r"); ok {
		t.Fatal("超过 TTL 应视为无记忆（强制全量重探）")
	}
}

func TestPrefStoreSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "indexpref.json")
	p := newPrefStore()
	p.Load(path) // 文件不存在也要记住路径
	p.Put("s1", "https://a.example/r", "moo.json")
	// Put 异步落盘；轮询等待（最多 2s）
	deadline := time.Now().Add(2 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			var f indexPrefFile
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("落盘 JSON 非法: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("超时：记忆未落盘")
		}
		time.Sleep(10 * time.Millisecond)
	}
	q := newPrefStore()
	if n := q.Load(path); n != 1 {
		t.Fatalf("恢复条数应为 1，实际 %d", n)
	}
	if n, ok := q.Get("s1", "https://a.example/r"); !ok || n != "moo.json" {
		t.Fatalf("恢复后应命中 moo.json，实际 %q", n)
	}
}

func TestPrefStoreExploreGap(t *testing.T) {
	p := newPrefStore()
	if !p.AllowedExplore("s", "https://a.example/r") {
		t.Fatal("首次应允许重探")
	}
	if p.AllowedExplore("s", "https://a.example/r") {
		t.Fatal("10min 内不应重复重探")
	}
	if !p.AllowedExplore("s2", "https://a.example/r") {
		t.Fatal("不同源互不影响")
	}
}
