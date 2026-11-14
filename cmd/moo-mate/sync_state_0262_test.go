package main

import (
	"path/filepath"
	"testing"
)

// 铁律：补默认清单只补「索引里没有的」，已有一律不动（用户改过版本/自己加的不覆盖）。
func TestMergeDefaultListOnlyAddsMissing(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	// 用户自己维护的 motrix：版本与默认清单不同，必须原样保留
	if err := ix.upsert(&indexedApp{
		appEntry: appEntry{DisplayName: "我自己改的", Version: "9.9.9"},
		Appname:  "motrix",
		Source:   "https://github.com/user/own",
	}); err != nil {
		t.Fatal(err)
	}
	added, src, err := ix.mergeDefaultList("", nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if src != "embedded" {
		t.Fatalf("远程为空应回落内置，得 %q", src)
	}
	d, derr := decodeDefaultIndex()
	if derr != nil {
		t.Fatal(derr)
	}
	if want := len(d.Apps) - 1; added != want {
		t.Fatalf("应补 %d 条（默认 %d 条里已有 1 条），实得 %d", want, len(d.Apps), added)
	}
	got := ix.get("motrix")
	if got == nil || got.Version != "9.9.9" || got.DisplayName != "我自己改的" || got.Source != "https://github.com/user/own" {
		t.Fatalf("用户条目被覆盖了：%+v", got)
	}
}

// 铁律：用户主动删除过的默认应用，后续补清单不能又加回来。
func TestMergeDefaultListSkipsRemoved(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	d, _ := decodeDefaultIndex()
	var victim string
	for k := range d.Apps {
		victim = k
		break
	}
	if err := ix.markRemovedDefault(victim); err != nil {
		t.Fatal(err)
	}
	added, _, err := ix.mergeDefaultList("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(d.Apps) - 1; added != want {
		t.Fatalf("删过的应跳过，应补 %d 条，实得 %d", want, added)
	}
	if ix.get(victim) != nil {
		t.Fatalf("删过的 %s 又被补回来了", victim)
	}
	// 用户重新添加同名应用后，排除记录撤销，才允许再次参与补全
	if err := ix.unmarkRemovedDefault(victim); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := ix.mergeDefaultList("", nil); n != 1 {
		t.Fatalf("撤销排除后应补回 1 条，实得 %d", n)
	}
}

// 用户点过「清除同步索引」时，补清单整体跳过（清除语义优先）。
func TestMergeDefaultListSkipsUserCleared(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	if err := ix.markClearedByUser(); err != nil {
		t.Fatal(err)
	}
	added, src, err := ix.mergeDefaultList("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 || src != "skipped-user-cleared" {
		t.Fatalf("应跳过，得 added=%d src=%s", added, src)
	}
	if len(ix.list()) != 0 {
		t.Fatal("跳过后不应写入任何条目")
	}
}

// 检查结果写入：成功/失败都要留痕，界面据此显示红点/感叹号。
func TestApplyCheckResults(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	for _, n := range []string{"ok.app", "up.app", "bad.app"} {
		if err := ix.upsert(&indexedApp{appEntry: appEntry{Version: "1.0.0"}, Appname: n}); err != nil {
			t.Fatal(err)
		}
	}
	gen := ix.generation()
	wrote, err := ix.applyCheckResults([]checkOutcome{
		{Appname: "ok.app", Latest: "1.0.0"},
		{Appname: "up.app", Latest: "1.2.0"},
		{Appname: "bad.app", ErrMsg: "HTTP 403"},
	}, gen)
	if err != nil || !wrote {
		t.Fatalf("写入失败 wrote=%v err=%v", wrote, err)
	}
	if a := ix.get("ok.app"); a.LatestVersion != "1.0.0" || a.LastCheck == 0 || a.LastCheckErr != "" {
		t.Fatalf("ok.app 状态错误：%+v", a)
	}
	if a := ix.get("up.app"); a.LatestVersion != "1.2.0" {
		t.Fatalf("up.app 最新版本错误：%+v", a)
	}
	if a := ix.get("bad.app"); a.LastCheckErr != "HTTP 403" || a.LastCheck == 0 {
		t.Fatalf("bad.app 失败原因未记录：%+v", a)
	}
}

// 清除后（代次变化）检查状态也不得写回。
func TestApplyCheckResultsSkipsAfterClear(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	if err := ix.upsert(&indexedApp{appEntry: appEntry{Version: "1.0.0"}, Appname: "a"}); err != nil {
		t.Fatal(err)
	}
	gen := ix.generation()
	if err := ix.clear(); err != nil {
		t.Fatal(err)
	}
	wrote, err := ix.applyCheckResults([]checkOutcome{{Appname: "a", Latest: "2.0.0"}}, gen)
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("清除后不应写入检查状态")
	}
	if len(ix.list()) != 0 {
		t.Fatal("清除后索引应保持为空")
	}
}
