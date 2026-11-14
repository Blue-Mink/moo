package main

import (
	"path/filepath"
	"testing"
)

// 缺陷 B 回归：自动同步（upsert）不得清掉「用户主动清除」标记。
// 反向验证：把 clearUserClearedFlag() 放回 upsert → 本用例必须 FAIL。
func TestUpsertKeepsUserClearedFlag(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	if err := ix.markClearedByUser(); err != nil {
		t.Fatal(err)
	}
	if !ix.userClearedIndex() {
		t.Fatal("前提不成立：标记未创建")
	}
	// 模拟自动同步把应用写回索引
	if err := ix.upsert(&indexedApp{
		appEntry: appEntry{DisplayName: "自同步应用", Version: "1.0.0"},
		Appname:  "auto.sync",
		Source:   "https://github.com/o/repo",
	}); err != nil {
		t.Fatal(err)
	}
	if !ix.userClearedIndex() {
		t.Fatal("upsert 把「用户主动清除」标记清掉了（缺陷 B 回归）")
	}
	if ix.get("auto.sync") == nil {
		t.Fatal("upsert 本身应写入成功")
	}
}

// 缺陷 A 回归：清除（推进代次）后，清除前发起的那一轮检查必须放弃写回。
// 反向验证：把 upsertIfGen 改回 upsert（不看代次）→ 本用例必须 FAIL。
func TestUpsertIfGenSkipsAfterClear(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	if err := ix.upsert(&indexedApp{
		appEntry: appEntry{DisplayName: "旧应用", Version: "1.0.0"},
		Appname:  "old.app",
	}); err != nil {
		t.Fatal(err)
	}
	gen := ix.generation() // 模拟「同步开始时」取到的代次

	if err := ix.clear(); err != nil { // 用户点了「清除同步索引」
		t.Fatal(err)
	}

	wrote, err := ix.upsertIfGen(&indexedApp{
		appEntry: appEntry{DisplayName: "旧应用", Version: "1.1.0"},
		Appname:  "old.app",
	}, gen)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("清除后仍写回旧快照（缺陷 A 回归：清除不彻底）")
	}
	if got := len(ix.list()); got != 0 {
		t.Fatalf("清除后索引应保持为空，实际 %d 条", got)
	}

	// 代次未变时（正常路径）必须照常写入 —— 不能把功能改死。
	gen2 := ix.generation()
	wrote, err = ix.upsertIfGen(&indexedApp{
		appEntry: appEntry{DisplayName: "新应用", Version: "2.0.0"},
		Appname:  "new.app",
	}, gen2)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || ix.get("new.app") == nil {
		t.Fatal("代次未变时应正常写入")
	}
}
