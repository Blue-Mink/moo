package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexUpsertListDelete(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	if err := ix.upsert(&indexedApp{
		appEntry:  appEntry{DisplayName: "应用A", Version: "1.0.0"},
		Appname:   "app.a",
		Source:    "https://github.com/o/repo",
		Publisher: "o",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ix.upsert(&indexedApp{
		appEntry:  appEntry{DisplayName: "应用B", Version: "2.0.0"},
		Appname:   "app.b",
		Publisher: "o2",
	}); err != nil {
		t.Fatal(err)
	}

	got := ix.list()
	if len(got) != 2 {
		t.Fatalf("期望 2 条，得 %d", len(got))
	}
	seen := map[string]*indexedApp{}
	for _, a := range got {
		if a.Appname == "" {
			t.Fatal("appname 未注入")
		}
		seen[a.Appname] = a
	}
	if seen["app.a"].DisplayName != "应用A" || seen["app.a"].Publisher != "o" {
		t.Fatalf("字段错误: %+v", seen["app.a"])
	}

	if err := ix.delete("app.a"); err != nil {
		t.Fatal(err)
	}
	if len(ix.list()) != 1 {
		t.Fatalf("删除后应剩 1 条，得 %d", len(ix.list()))
	}
}

func TestIndexUpsertPreservesSynced(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	_ = ix.upsert(&indexedApp{Appname: "x", Synced: true, SyncedName: "src"})
	_ = ix.upsert(&indexedApp{Appname: "x", Synced: false})
	if !ix.get("x").Synced || ix.get("x").SyncedName != "src" {
		t.Fatalf("重复发现不应抹掉 synced: %+v", ix.get("x"))
	}
}

func TestIndexPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "index.json")
	ix := newIndex(path)
	_ = ix.upsert(&indexedApp{Appname: "a", Publisher: "p"})
	ix2 := newIndex(path)
	if ix2.get("a") == nil || ix2.get("a").Publisher != "p" {
		t.Fatalf("持久化失败: %+v", ix2.list())
	}
}

func TestIndexSetSynced(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	_ = ix.upsert(&indexedApp{Appname: "a"})
	if err := ix.setSynced("a", true, "src-a"); err != nil {
		t.Fatal(err)
	}
	if !ix.get("a").Synced || ix.get("a").SyncedName != "src-a" {
		t.Fatalf("setSynced 失败")
	}
	if err := ix.setSynced("a", false, ""); err != nil {
		t.Fatal(err)
	}
	if ix.get("a").Synced || ix.get("a").SyncedName != "" {
		t.Fatalf("setSynced 清除失败")
	}
	if err := ix.setSynced("nope", true, ""); err == nil {
		t.Fatal("不存在的 appname 应报错")
	}
}

// 0.2.34：原子写——落盘后不留 .tmp，内容可完整解析。
func TestIndexSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	ix := newIndex(p)
	ix.doc.Apps["demo"] = &indexedApp{Synced: true}
	if err := ix.save(); err != nil {
		t.Fatalf("save 失败: %v", err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件未被 rename 掉: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("落盘内容不是合法 JSON: %v", err)
	}
}
