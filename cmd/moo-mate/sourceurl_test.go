package main

// sourceurl_test.go —— 0.2.34：端口变更后修复 Moo 源地址。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestMateHostedPath(t *testing.T) {
	cases := []struct {
		url  string
		path string
		ok   bool
	}{
		{"http://127.0.0.1:38108/moo/mate.json", "/moo/mate.json", true},
		{"http://127.0.0.1:39001/moo/moo@Blue-Mink.json", "/moo/moo@Blue-Mink.json", true},
		{"http://localhost:38108/moo/app.json", "/moo/app.json", true},
		{"https://github.com/Blue-Mink/FnDepot", "", false},
		{"http://127.0.0.1:38108/api/status", "", false},
		{"http://127.0.0.1:38108/moo/app", "", false},
		{"http://192.0.2.10:38108/moo/mate.json", "", false},
	}
	for _, c := range cases {
		p, ok := mateHostedPath(c.url)
		if ok != c.ok || p != c.path {
			t.Fatalf("mateHostedPath(%q) = (%q,%v)，期望 (%q,%v)", c.url, p, ok, c.path, c.ok)
		}
	}
}

// fakeMoo 最小 Moo admin API 模拟：源列表 + 加源 + 删源 + 同步。
// ⚠️ 严格对齐真实 Moo 的语义（internal/source/manager.go + store_routes.go）：
//   - 源的身份就是**源名**，POST /api/sources 返回 id==name（自动消歧时一起变，如 moo-mate-2）；
//   - rename 改的是名字，旧 id 随之消失 → 后续用旧 id 调用 sync/rename/delete 一律 404。
//
// 这条语义正是 0.2.34 端口修复里「改名后拿旧 id 去 sync」会静默失败的根因，必须测出来。
type fakeMoo struct {
	mu      sync.Mutex
	sources []map[string]any
	added   []string
	deleted []string
	synced  []string
	renamed []string
	failAdd bool
}

func (f *fakeMoo) handler() http.Handler {
	mux := http.NewServeMux()
	idx := func(id string) int {
		for i, s := range f.sources {
			if s["id"] == id || s["name"] == id {
				return i
			}
		}
		return -1
	}
	mux.HandleFunc("GET /api/sources", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"sources": f.sources})
	})
	mux.HandleFunc("POST /api/sources", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failAdd {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		name := body["name"]
		for i := 2; i < 10; i++ { // 撞名消歧：moo-mate → moo-mate-2（与 UniqueSourceName 同形态）
			if idx(name) < 0 {
				break
			}
			name = fmt.Sprintf("%s-%d", body["name"], i)
		}
		f.added = append(f.added, body["url"])
		f.sources = append(f.sources, map[string]any{"id": name, "name": name, "url": body["url"], "enabled": true})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"source": map[string]any{"id": name, "name": name, "url": body["url"], "enabled": true},
		})
	})
	mux.HandleFunc("DELETE /api/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		i := idx(r.PathValue("id"))
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.deleted = append(f.deleted, r.PathValue("id"))
		f.sources = append(f.sources[:i], f.sources[i+1:]...)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/sources/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		i := idx(r.PathValue("id"))
		if i < 0 { // 旧 id 在改名后已不存在
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if idx(body["name"]) >= 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.renamed = append(f.renamed, r.PathValue("id")+"->"+body["name"])
		f.sources[i]["id"], f.sources[i]["name"] = body["name"], body["name"] // 键名随新名
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": body["name"], "name": body["name"]})
	})
	mux.HandleFunc("POST /api/sources/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if idx(r.PathValue("id")) < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.synced = append(f.synced, r.PathValue("id"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"source": map[string]any{"id": r.PathValue("id"), "app_count": 1},
		})
	})
	return mux
}

func TestReconcileSourceURLsRebuildsStalePort(t *testing.T) {
	f := &fakeMoo{sources: []map[string]any{
		{"id": "s1", "name": "moo-mate", "url": "http://127.0.0.1:38108/moo/mate.json", "enabled": true},
		{"id": "s2", "name": "moo", "url": "https://github.com/Blue-Mink/FnDepot", "enabled": true},
		{"id": "s3", "name": "already", "url": "http://127.0.0.1:39001/moo/app.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://127.0.0.1:39001", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	if fixed != 1 {
		t.Fatalf("fixed=%d，期望 1", fixed)
	}
	if len(f.added) != 1 || !strings.HasSuffix(f.added[0], ":39001/moo/mate.json") {
		t.Fatalf("新源地址异常: %v", f.added)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "s1" {
		t.Fatalf("旧源未删除: %v", f.deleted)
	}
	if len(f.synced) != 1 || f.synced[0] != "moo-mate" {
		t.Fatalf("改名后没用新名 sync（旧 id 会 404）: %v", f.synced)
	}
	if len(f.renamed) != 1 || f.renamed[0] != "moo-mate-2->moo-mate" {
		t.Fatalf("新源名未改回原名: %v", f.renamed)
	}
	// 最终只剩一条 moo-mate 源，且地址已指到新端口
	var mates []map[string]any
	for _, s := range f.sources {
		if s["name"] == "moo-mate" {
			mates = append(mates, s)
		}
	}
	if len(mates) != 1 || mates[0]["url"] != "http://127.0.0.1:39001/moo/mate.json" {
		t.Fatalf("最终源列表异常: %v", f.sources)
	}
}

// base 带路径前缀时也必须拼出 /moo/xxx.json（旧实现 c.base+p 会拼成 /app/moo/moo/…）。
func TestReconcileBaseWithPathPrefix(t *testing.T) {
	f := &fakeMoo{sources: []map[string]any{
		{"id": "moo-mate", "name": "moo-mate", "url": "http://127.0.0.1:38108/moo/mate.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://127.0.0.1:39001/app/moo", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil || fixed != 1 {
		t.Fatalf("fixed=%d err=%v，期望 1/nil", fixed, err)
	}
	if len(f.added) != 1 || f.added[0] != "http://127.0.0.1:39001/moo/mate.json" {
		t.Fatalf("加源地址被 base 前缀污染: %v", f.added)
	}
}

// 用户自建的「本机回环 + /moo/*.json」源（名字不是 Moo Mate）必须原样保留。
func TestReconcileIgnoresUserOwnedLoopbackSource(t *testing.T) {
	f := &fakeMoo{sources: []map[string]any{
		{"id": "s9", "name": "my-local-index", "url": "http://127.0.0.1:39999/moo/app.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://127.0.0.1:39001", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil || fixed != 0 {
		t.Fatalf("fixed=%d err=%v，期望 0/nil（用户源不应被改动）", fixed, err)
	}
	if len(f.added) != 0 || len(f.deleted) != 0 {
		t.Fatalf("用户源被动了: added=%v deleted=%v", f.added, f.deleted)
	}
}

// 加新源失败时必须保留旧源（不能先删后加）。
func TestReconcileKeepsOldSourceWhenAddFails(t *testing.T) {
	f := &fakeMoo{failAdd: true, sources: []map[string]any{
		{"id": "s1", "name": "moo-mate", "url": "http://127.0.0.1:38108/moo/mate.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://127.0.0.1:39001", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil || fixed != 0 {
		t.Fatalf("fixed=%d err=%v，期望 0/nil", fixed, err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("加源失败却删了旧源: %v", f.deleted)
	}
}

// self-base 非回环（配置异常）时一个源都不该改。
func TestReconcileSkipsWhenBaseNotLoopback(t *testing.T) {
	f := &fakeMoo{sources: []map[string]any{
		{"id": "s1", "name": "moo-mate", "url": "http://127.0.0.1:38108/moo/mate.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://192.0.2.10:39001", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil || fixed != 0 {
		t.Fatalf("fixed=%d err=%v，期望 0/nil（base 非回环应跳过）", fixed, err)
	}
	if len(f.added) != 0 || len(f.deleted) != 0 {
		t.Fatalf("不应有增删: added=%v deleted=%v", f.added, f.deleted)
	}
}

func TestMateURLForPath(t *testing.T) {
	cases := []struct {
		base string
		path string
		want string
		ok   bool
	}{
		{"http://127.0.0.1:38108", "/moo/mate.json", "http://127.0.0.1:38108/moo/mate.json", true},
		// base 带前缀时整体替换 path，绝不能拼成 /app/moo/moo/xxx.json
		{"http://127.0.0.1:38108/app/moo", "/moo/mate.json", "http://127.0.0.1:38108/moo/mate.json", true},
		{"http://localhost:38108/", "/moo/moo@Blue-Mink.json", "http://localhost:38108/moo/moo@Blue-Mink.json", true},
		{"http://192.0.2.10:39001", "/moo/mate.json", "", false},
		{"/app/moo", "/moo/mate.json", "", false},
		{"http://127.0.0.1:39001", "/api/status", "", false},
	}
	for _, cs := range cases {
		c := &companion{base: cs.base}
		got, ok := c.mateURLForPath(cs.path)
		if ok != cs.ok || got != cs.want {
			t.Fatalf("mateURLForPath(%q,%q) = (%q,%v)，期望 (%q,%v)", cs.base, cs.path, got, ok, cs.want, cs.ok)
		}
	}
}

func TestIsMateSourceName(t *testing.T) {
	yes := []string{"moo-mate", "Moo-Mate", " moo-mate ", "moo-mate.json", "moo-mate-2"}
	no := []string{"", "moo", "my-moo-mate", "moo-mate-backup", "moo-mate-", "moo-mate-2x"}
	for _, n := range yes {
		if !isMateSourceName(n) {
			t.Fatalf("isMateSourceName(%q) = false，期望 true", n)
		}
	}
	for _, n := range no {
		if isMateSourceName(n) {
			t.Fatalf("isMateSourceName(%q) = true，期望 false", n)
		}
	}
}

func TestReconcileSourceURLsNoopWhenCurrent(t *testing.T) {
	f := &fakeMoo{sources: []map[string]any{
		{"id": "s1", "name": "moo-mate", "url": "http://127.0.0.1:39001/moo/mate.json", "enabled": true},
	}}
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	c := newCompanion(ts.URL, "http://127.0.0.1:39001", "", http.NotFoundHandler(), newIndex(t.TempDir()+"/index.json"))
	fixed, err := c.reconcileSourceURLs()
	if err != nil || fixed != 0 {
		t.Fatalf("fixed=%d err=%v，期望 0/nil", fixed, err)
	}
	if len(f.added) != 0 || len(f.deleted) != 0 {
		t.Fatalf("不应有任何增删: added=%v deleted=%v", f.added, f.deleted)
	}
}
