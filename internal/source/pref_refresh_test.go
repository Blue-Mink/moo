package source

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.330 WP1 端到端：首轮全量命中后写记忆，第二轮只打记忆里的索引名。
func TestRefreshUsesIndexPref(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		mu.Lock()
		hits[p]++
		mu.Unlock()
		if strings.HasSuffix(p, "/fnpack.json") {
			_, _ = io.WriteString(w, `{"app1":{"display_name":"A","version":"1.0","download_url":"https://example.com/a.fpk"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := &config.Config{Sources: []config.SourceRef{{Name: "t", URL: srv.URL}}}
	m := NewManager(cfg)

	// 第一轮：无记忆 → 全量探测，命中 fnpack.json
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	name, ok := m.prefs.Get("t", srv.URL)
	if !ok || name != "fnpack.json" {
		t.Fatalf("应写入记忆 fnpack.json，实际 %q ok=%v", name, ok)
	}

	// 第二轮：只应请求 fnpack.json
	mu.Lock()
	hits = map[string]int{}
	mu.Unlock()
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("第二轮刷新失败: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	total := 0
	for p, c := range hits {
		total += c
		if !strings.Contains(p, "fnpack.json") {
			t.Fatalf("第二轮不应请求非记忆索引：%s", p)
		}
	}
	if total > 2 {
		t.Fatalf("第二轮请求数应 ≤2（[url]/fnpack.json 与 [url]/raw/main/fnpack.json），实际 %d", total)
	}
}

// 记忆命中但源已失效：当轮返回错误 + 清记忆（下轮回到全量）。
func TestPrefClearedOnFailure(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/fnpack.json") {
			_, _ = io.WriteString(w, `{"a":{"display_name":"A","version":"1.0"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := &config.Config{Sources: []config.SourceRef{{Name: "t", URL: srv.URL}}}
	m := NewManager(cfg)
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("首轮应成功: %v", err)
	}
	if _, ok := m.prefs.Get("t", srv.URL); !ok {
		t.Fatal("首轮后应有记忆")
	}

	fail.Store(true)
	_ = m.Refresh("t") // 期望失败（httptest 会回 404）
	if _, ok := m.prefs.Get("t", srv.URL); ok {
		t.Fatal("失败后应清掉记忆（下轮回到全量探测）")
	}
}

// 有记忆时阶段 1 预算收紧到 4s：用挂起的 handler 验证「不会等满 12s」。
// 这里只做轻量断言（<9s），避免 CI 上跑太久。
func TestPrefBudgetIsShort(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过耗时用例")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 挂起直到客户端超时，避免 Close 时阻塞
	}))
	defer srv.Close()

	cfg := &config.Config{Sources: []config.SourceRef{{Name: "t", URL: srv.URL}}}
	m := NewManager(cfg)
	m.prefs.Put("t", srv.URL, "fnpack.json")
	start := time.Now()
	_ = m.Refresh("t")
	if d := time.Since(start); d > 9*time.Second {
		t.Fatalf("有记忆时应 4s 量级失败，实际 %v", d)
	}
}
