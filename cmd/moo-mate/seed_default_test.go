package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newSeedTestIndex(t *testing.T) (*index, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "index.json")
	return newIndex(p), p
}

// 空索引 + 远端可用 → 用远端清单（并落盘）。
func TestSeedDefaultIfEmptyUsesRemote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"apps":{"remote-only":{"appname":"remote-only","display_name":"R","version":"1.0.0"}}}`))
	}))
	defer srv.Close()
	ix, p := newSeedTestIndex(t)
	n, src, err := ix.seedDefaultIfEmpty(srv.URL+"/mate-default-index.json", srv.Client())
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	if n != 1 || src != "remote" {
		t.Fatalf("n=%d src=%s，期望 1/remote", n, src)
	}
	if got := len(newIndex(p).list()); got != 1 {
		t.Fatalf("落盘后条数=%d，期望 1", got)
	}
}

// 远端不可达 → 回落内置快照。
func TestSeedDefaultIfEmptyFallsBackToEmbedded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	ix, _ := newSeedTestIndex(t)
	n, src, err := ix.seedDefaultIfEmpty(srv.URL+"/x.json", srv.Client())
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	if src != "embedded" || n != defaultSeedApps() || n == 0 {
		t.Fatalf("回落异常：n=%d src=%s want=%d", n, src, defaultSeedApps())
	}
}

// 非空索引 → 一条都不动（不覆盖用户数据）。
func TestSeedDefaultIfEmptySkipsNonEmpty(t *testing.T) {
	ix, _ := newSeedTestIndex(t)
	if err := ix.upsert(&indexedApp{Appname: "keep-me"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	n, src, err := ix.seedDefaultIfEmpty("", nil)
	if err != nil || n != 0 || src != "" {
		t.Fatalf("非空索引被改动：n=%d src=%s err=%v", n, src, err)
	}
	if len(ix.list()) != 1 {
		t.Fatalf("条数变了：%d", len(ix.list()))
	}
}

// 用户主动清除（留标记）→ 跳过补默认。
func TestSeedDefaultIfEmptySkipsUserCleared(t *testing.T) {
	ix, _ := newSeedTestIndex(t)
	if err := ix.markClearedByUser(); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	n, src, err := ix.seedDefaultIfEmpty("", nil)
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	if n != 0 || src != "skipped-user-cleared" {
		t.Fatalf("n=%d src=%s，期望 0/skipped-user-cleared", n, src)
	}
	if len(ix.list()) != 0 {
		t.Fatalf("被跳过后仍有 %d 条", len(ix.list()))
	}
}

// 标记只在「用户主动新增」时显式清除（handleBuild 调用 clearUserClearedFlag），
// upsert 不再自动清 —— 自动同步也走 upsert，自动清会把用户的清除意图抹掉
// （0.2.61b 缺陷 B：清除后 38s 应用被写回、标记消失）。契约变更见 index.go upsert 注释。
func TestClearUserClearedFlagExplicit(t *testing.T) {
	ix, _ := newSeedTestIndex(t)
	if err := ix.markClearedByUser(); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	if !ix.userClearedIndex() {
		t.Fatal("标记未生效")
	}
	if err := ix.upsert(&indexedApp{Appname: "new-one"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !ix.userClearedIndex() {
		t.Fatal("upsert 不应清除标记（自动同步路径会误清用户意图）")
	}
	ix.clearUserClearedFlag()
	if ix.userClearedIndex() {
		t.Fatal("显式 clearUserClearedFlag 后标记应被清除")
	}
}
