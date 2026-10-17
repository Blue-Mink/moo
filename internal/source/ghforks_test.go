package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestGHForksLookupAndRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/chordi0124/fndepot":
			w.Header().Set("ETag", `"e1"`)
			w.Header().Set("X-RateLimit-Remaining", "50")
			_, _ = w.Write([]byte(`{"fork":true,"parent":{"full_name":"EWEDLCM/FnDepot"}}`))
		case "/repos/ewedlcm/fndepot":
			_, _ = w.Write([]byte(`{"fork":false}`))
		default:
			w.WriteHeader(http.StatusNotFound) // 已删/私有/改名 → 负缓存
		}
	}))
	defer srv.Close()

	g := NewGHForks(filepath.Join(t.TempDir(), "ghforks.json"))
	g.apiBase = srv.URL

	// 未拉过：known=false（调用方据此保守放过）
	if _, _, known := g.Lookup("chordi0124/fndepot"); known {
		t.Fatal("未拉过的仓库应 known=false")
	}
	g.Enqueue("Chordi0124/FnDepot") // 大小写归一
	g.Enqueue("EWEDLCM/FnDepot")
	g.Enqueue("gone/FnDepot")
	if n, err := g.RefreshRound(context.Background(), 10); err != nil || n != 3 {
		t.Fatalf("RefreshRound = (%d,%v), want (3,nil)", n, err)
	}
	if parent, isFork, known := g.Lookup("chordi0124/fndepot"); !known || !isFork || parent != "ewedlcm/fndepot" {
		t.Errorf("fork 仓库 Lookup = (%q,%v,%v), want (ewedlcm/fndepot,true,true)", parent, isFork, known)
	}
	if _, isFork, known := g.Lookup("ewedlcm/fndepot"); !known || isFork {
		t.Error("根仓库应为 known=true / fork=false（作者自建仓不判上游的前提）")
	}
	if _, isFork, known := g.Lookup("gone/fndepot"); !known || isFork {
		t.Error("404 应负缓存为 known=true / fork=false")
	}

	// 新鲜结果与 404 负缓存都不应重复入队（省额度）
	g.Enqueue("chordi0124/fndepot")
	g.Enqueue("gone/fndepot")
	g.mu.Lock()
	q := len(g.queue)
	g.mu.Unlock()
	if q != 0 {
		t.Errorf("新鲜/负缓存项不应重复入队, 队列 = %d", q)
	}
}

func TestGHForksRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	g := NewGHForks(filepath.Join(t.TempDir(), "ghforks.json"))
	g.apiBase = srv.URL
	g.Enqueue("o/r")
	n, err := g.RefreshRound(context.Background(), 10)
	if err != ErrRateLimited || n != 0 {
		t.Fatalf("限流时应 (%d,ErrRateLimited), got (%d,%v)", 0, n, err)
	}
	g.mu.Lock()
	queued := len(g.queue)
	g.mu.Unlock()
	if queued != 1 {
		t.Errorf("限流后该仓库应回队待重试, got %d", queued)
	}
}

func TestGHForksPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghforks.json")
	g := NewGHForks(path)
	g.SetForTest("A/B", "up/Parent") // parent 写入时归一为小写
	g.save()

	g2 := NewGHForks(path)
	parent, isFork, known := g2.Lookup("a/b")
	if !known || !isFork || parent != "up/parent" {
		t.Errorf("重启后 Lookup = (%q,%v,%v), want (up/parent,true,true)", parent, isFork, known)
	}
	if _, _, known := g2.Lookup("nope/nope"); known {
		t.Error("未记录的仓库在重启后仍应 known=false")
	}
}

// 0.6.326d（H2/H7）：404 负缓存必须带 TTL —— 源仓改名/转公开/短暂 404 之后，
// 原实现（NotFound 永久跳过）会让该源的整仓 fork 溯源静默失效且无人可察。
// 本用例走完整自愈路径：404 → 立即重排被拒 → 时间戳老化 → 重排 → 重拉成功。
func TestGHForksNotFoundTTLExpiryAndSelfHeal(t *testing.T) {
	var hits int32
	var renamed atomic.Bool // true = 仓库已转公开/改名成功
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if renamed.Load() {
			_, _ = w.Write([]byte(`{"fork":true,"parent":{"full_name":"up/Parent"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	g := NewGHForks(filepath.Join(t.TempDir(), "ghforks.json"))
	g.apiBase = srv.URL

	g.Enqueue("gone/repo")
	if n, err := g.RefreshRound(context.Background(), 5); err != nil || n != 1 {
		t.Fatalf("首次 404 轮次 = (%d,%v), want (1,nil)", n, err)
	}
	if _, _, known := g.Lookup("gone/repo"); !known {
		t.Fatal("404 应落负缓存（known=true / fork=false）")
	}

	// 负缓存有效期内：不重排（省额度）
	g.Enqueue("gone/repo")
	if q := queueLen(g); q != 0 {
		t.Fatalf("负缓存有效期内不应重排, 队列 = %d", q)
	}

	// 时间戳老化到 TTL 之外（模拟 7 天后）：必须重排
	g.mu.Lock()
	g.repos["gone/repo"].FetchedAt = time.Now().Add(-ghForkNotFoundTTL - time.Hour).Format(time.RFC3339)
	g.mu.Unlock()
	g.Enqueue("gone/repo")
	if q := queueLen(g); q != 1 {
		t.Fatalf("负缓存过期后应重排, 队列 = %d", q)
	}

	// 上游仓改名/转公开 → 重拉成功，负缓存被真实结果覆盖（自愈）
	renamed.Store(true)
	if n, err := g.RefreshRound(context.Background(), 5); err != nil || n != 1 {
		t.Fatalf("自愈轮次 = (%d,%v), want (1,nil)", n, err)
	}
	parent, isFork, known := g.Lookup("gone/repo")
	if !known || !isFork || parent != "up/parent" {
		t.Fatalf("自愈后 Lookup = (%q,%v,%v), want (up/parent,true,true)", parent, isFork, known)
	}
	if hits != 2 {
		t.Fatalf("自愈应恰好重拉一次, 请求数 = %d", hits)
	}
}

// 队列上限（防御）：ghForksRound 会顺带排 fork parent，加上失败回队，队列有
// 多条增长来源 —— 超限即拒新入队（丢的只是「本轮没排上」，下轮源列表仍会重排）。
func TestGHForksQueueCap(t *testing.T) {
	g := NewGHForks("")
	for i := 0; i < ghForkMaxQueue+50; i++ {
		g.Enqueue(fmt.Sprintf("o/r%d", i))
	}
	if q := queueLen(g); q != ghForkMaxQueue {
		t.Fatalf("队列应被上限裁到 %d, got %d", ghForkMaxQueue, q)
	}
}

// 预算裁剪：单轮只拉 maxRepos 个，其余原样回队（不丢、不重复）。
func TestGHForksBudgetTrimRequenues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"fork":false}`))
	}))
	defer srv.Close()

	g := NewGHForks(filepath.Join(t.TempDir(), "ghforks.json"))
	g.apiBase = srv.URL
	for i := 0; i < 10; i++ {
		g.Enqueue(fmt.Sprintf("o/r%d", i))
	}
	n, err := g.RefreshRound(context.Background(), 3)
	if err != nil || n != 3 {
		t.Fatalf("预算 3 应只拉 3 个, got (%d,%v)", n, err)
	}
	if q := queueLen(g); q != 7 {
		t.Fatalf("其余 7 个应回队, got %d", q)
	}
	pulled := 0
	for i := 0; i < 10; i++ {
		if _, _, known := g.Lookup(fmt.Sprintf("o/r%d", i)); known {
			pulled++
		}
	}
	if pulled != 3 {
		t.Fatalf("本轮应恰好落下 3 条结果, got %d", pulled)
	}
	if n2, _ := g.RefreshRound(context.Background(), 100); n2 != 7 {
		t.Fatalf("下轮应把回队的 7 个拉完, got %d", n2)
	}
}

// 限流（整轮预算耗尽）：已拉到的结果照常落库，**未拉的不得落任何错数据**
// （不能把 unknown 当成已知 false 放过），其余全部回队等下轮。
func TestGHForksRateLimitMidRoundNoBadData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r0" {
			_, _ = w.Write([]byte(`{"fork":true,"parent":{"full_name":"up/x"}}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	g := NewGHForks(filepath.Join(t.TempDir(), "ghforks.json"))
	g.apiBase = srv.URL
	for i := 0; i < 5; i++ {
		g.Enqueue(fmt.Sprintf("o/r%d", i))
	}
	n, err := g.RefreshRound(context.Background(), 10)
	if err != ErrRateLimited || n != 1 {
		t.Fatalf("限流整轮应返回 (1,ErrRateLimited), got (%d,%v)", n, err)
	}
	if parent, isFork, known := g.Lookup("o/r0"); !known || !isFork || parent != "up/x" {
		t.Fatalf("限流前已拉到的结果必须落库：(%q,%v,%v)", parent, isFork, known)
	}
	for i := 1; i < 5; i++ {
		if _, _, known := g.Lookup(fmt.Sprintf("o/r%d", i)); known {
			t.Fatalf("限流未拉到的 o/r%d 不得落任何结果（会误判为「非 fork」）", i)
		}
	}
	if q := queueLen(g); q != 4 {
		t.Fatalf("限流后其余 4 个应回队, got %d", q)
	}
}

func queueLen(g *GHForks) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.queue)
}
