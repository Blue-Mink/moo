package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// 0.2.73-fix（B-1）回归：协议字段回填只能通过持写锁的 updateFields 落地。
func TestBackfillMustUseLockedUpdate(t *testing.T) {
	b, err := os.ReadFile("backfill_0273.go")
	if err != nil {
		t.Skipf("读不到 backfill_0273.go：%v", err)
	}
	src := string(b)
	if strings.Contains(src, "c.idx.put(") {
		t.Fatalf("回填不得直接调用 c.idx.put —— put 要求调用方已持写锁，后台 goroutine 无锁调用会与 HTTP 侧 list() 形成 map 竞态（fatal error）。请改用 c.idx.updateFields")
	}
	if !strings.Contains(src, "c.idx.updateFields(") {
		t.Fatalf("回填必须经由 c.idx.updateFields 写回协议字段")
	}
}

// updateFields 只改 fn 指定的字段：版本号 / 下载地址 / 校验值 / 包信息必须原样保留。
func TestUpdateFieldsOnlyTouchesProtocolFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	ix := newIndex(path)
	ix.doc.Apps["demo"] = &indexedApp{
		appEntry: appEntry{
			Version:     "1.2.3",
			Sha256:      "deadbeef",
			DownloadURL: "https://example.invalid/demo.fpk",
		},
		Appname:  "demo",
		Source:   "https://github.com/o/r",
		Packages: map[string]*appPackage{"x86": {DownloadURL: "https://example.invalid/demo.fpk", Sha256: "deadbeef", SizeBytes: 42}},
	}
	ok, err := ix.updateFields("demo", func(a *indexedApp) bool {
		a.Changelog = "日志"
		a.ReadmeURL = "http://127.0.0.1:38108/moo/demo/README.md"
		a.Releases = map[string]*appRelease{"1.2.3": {Changelog: "日志"}}
		return true
	})
	if err != nil || !ok {
		t.Fatalf("updateFields 应写入成功：ok=%v err=%v", ok, err)
	}
	got := newIndex(path).get("demo")
	if got == nil {
		t.Fatal("落盘后读不到 demo")
	}
	if got.Version != "1.2.3" || got.Sha256 != "deadbeef" || got.DownloadURL != "https://example.invalid/demo.fpk" {
		t.Fatalf("协议字段以外的值被改动：%+v", got)
	}
	if p := got.Packages["x86"]; p == nil || p.Sha256 != "deadbeef" || p.SizeBytes != 42 {
		t.Fatalf("包信息被改动：%+v", got.Packages)
	}
	if got.Changelog != "日志" || got.ReadmeURL == "" || len(got.Releases) != 1 {
		t.Fatalf("协议字段未写入：%+v", got)
	}
}

// 反向断言：fn 未改动时不得落盘（避免每轮无谓写盘）。
func TestUpdateFieldsNoChangeNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	ix := newIndex(path)
	ix.doc.Apps["demo"] = &indexedApp{Appname: "demo", Source: "https://github.com/o/r"}
	ok, err := ix.updateFields("demo", func(a *indexedApp) bool { return false })
	if err != nil || ok {
		t.Fatalf("无改动时应返回 false/nil：ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("无改动不应写盘")
	}
	// 不存在的条目：安全返回 false。
	if ok, err := ix.updateFields("nope", func(a *indexedApp) bool { return true }); err != nil || ok {
		t.Fatalf("不存在的条目应返回 false/nil：ok=%v err=%v", ok, err)
	}
}

// 并发读写（-race 下必须有意义）：回填写 vs HTTP 侧 list() 读。
func TestUpdateFieldsConcurrentWithList(t *testing.T) {
	dir := t.TempDir()
	ix := newIndex(filepath.Join(dir, "index.json"))
	ix.doc.Apps["demo"] = &indexedApp{Appname: "demo", Source: "https://github.com/o/r"}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			for _, a := range ix.list() {
				_ = a.Changelog
				_ = a.Releases
				_ = a.ReadmeURL
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			_, _ = ix.updateFields("demo", func(a *indexedApp) bool {
				a.Changelog = "x"
				a.ReadmeURL = "y"
				return true
			})
		}
	}()
	wg.Wait()
}
