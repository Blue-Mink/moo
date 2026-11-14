package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// TestRunFullSyncDoesNotCreateSource：完整同步绝不能替用户「建源」。
// 用户删掉 moo-mate 源之后，同步再把它建回来等于「删除」按钮失效（0.2.62 修）。
func TestRunFullSyncDoesNotCreateSource(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			posts = append(posts, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/api/about":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.0.0-test"})
		case "/api/sources":
			_ = json.NewEncoder(w).Encode(map[string]any{"sources": []sourceInfo{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &companion{
		idx:    newIndex(filepath.Join(t.TempDir(), "index.json")),
		moo:    newMooClient(srv.URL),
		hosted: map[string][]byte{},
		base:   srv.URL,
	}
	if _, _, err := c.runFullSync(); err != nil {
		t.Fatalf("runFullSync: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(posts) != 0 {
		t.Fatalf("同步时发生了写请求（应只读+只同步已存在源）：%v", posts)
	}
}
