package main

import (
	"path/filepath"
	"testing"
)

// TestSeedSkipsRemovedDefaults：出厂兜底补齐必须跳过用户主动删过的条目；
// 全被删光时返回 (0, "all-removed") 且不写盘。
func TestSeedSkipsRemovedDefaults(t *testing.T) {
	keys := defaultIndexKeys()
	if len(keys) < 2 {
		t.Skipf("内置默认清单过短（%d 条），无法区分部分/全部", len(keys))
	}

	// ① 全部 key 都删过 → 一条都不补，且给出 all-removed
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	for _, k := range keys {
		if err := ix.markRemovedDefault(k); err != nil {
			t.Fatalf("markRemovedDefault(%s): %v", k, err)
		}
	}
	n, src, err := ix.seedDefaultIfEmpty("", nil)
	if err != nil {
		t.Fatalf("seedDefaultIfEmpty: %v", err)
	}
	if n != 0 || src != "all-removed" {
		t.Fatalf("全删光时应为 (0, all-removed)，实际 (%d, %q)", n, src)
	}
	if got := len(ix.list()); got != 0 {
		t.Fatalf("全删光后索引仍被补齐 %d 条", got)
	}

	// ② 只删一条 → 其余照补，被删那条不得复活
	ix2 := newIndex(filepath.Join(t.TempDir(), "index.json"))
	gone := keys[0]
	if err := ix2.markRemovedDefault(gone); err != nil {
		t.Fatalf("markRemovedDefault: %v", err)
	}
	n2, src2, err := ix2.seedDefaultIfEmpty("", nil)
	if err != nil {
		t.Fatalf("seedDefaultIfEmpty: %v", err)
	}
	if src2 != "embedded" {
		t.Fatalf("应走内置快照，实际来源 %q", src2)
	}
	if n2 != len(keys)-1 {
		t.Fatalf("应补 %d 条，实际 %d 条", len(keys)-1, n2)
	}
	if ix2.get(gone) != nil {
		t.Fatalf("被删除的 %q 被兜底补齐复活了", gone)
	}
}

// TestClearByUserKeepsRemovedDefaults：「清除同步索引」必须记住被清掉的应用名，
// 否则下一次「同步更新」/ 重启兜底会把它们原样加回来。
func TestClearByUserKeepsRemovedDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	ix := newIndex(path)
	for _, name := range []string{"app.one", "app.two"} {
		if err := ix.upsert(&indexedApp{Appname: name, Source: "https://example.com/x"}); err != nil {
			t.Fatalf("upsert(%s): %v", name, err)
		}
	}
	if err := ix.clearByUser([]string{"extra.removed"}); err != nil {
		t.Fatalf("clearByUser: %v", err)
	}
	if got := len(ix.list()); got != 0 {
		t.Fatalf("清除后索引应为空，实际 %d 条", got)
	}
	rm := ix.removedSet()
	for _, want := range []string{"app.one", "app.two", "extra.removed"} {
		if !rm[want] {
			t.Fatalf("排除名单缺 %q：%v", want, rm)
		}
	}

	// 落盘后重读仍在（重启不会忘）
	again := newIndex(path)
	rm2 := again.removedSet()
	for _, want := range []string{"app.one", "app.two", "extra.removed"} {
		if !rm2[want] {
			t.Fatalf("重读索引后排除名单缺 %q", want)
		}
	}
	if got := len(again.list()); got != 0 {
		t.Fatalf("重读后索引应为空，实际 %d 条", got)
	}

	// 默认清单也不会把它们补回来（当且仅当它们出现在默认清单里）
	if _, _, err := again.seedDefaultIfEmpty("", nil); err != nil {
		t.Fatalf("seedDefaultIfEmpty: %v", err)
	}
	if again.get("app.one") != nil && rm2["app.one"] {
		t.Fatalf("被用户清除的 app.one 又被默认清单补回来了")
	}
}
