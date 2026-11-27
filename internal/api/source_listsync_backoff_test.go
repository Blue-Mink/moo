package api

// 2026-11-26 B-SRC-LISTSYNC 缺陷 2：失败退避（6h→24h 递增、落盘、成功即清）。
// 全程 httptest 本地假源，零外网。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// backoffFixture 清单固定 1 条指向 /src；/src 按 fail 标志返回 500 或合法
// 扁平 fnpack（AddSource 校验可过），并统计被真抓的次数。
type backoffFixture struct {
	srv  *httptest.Server
	hits int32
	fail atomic.Bool
}

func newBackoffFixture(t *testing.T) *backoffFixture {
	t.Helper()
	f := &backoffFixture{}
	f.fail.Store(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.srv.URL + "/src\n"))
	})
	// 兜底：除 /list 外任意候选路径都回同一份 fnpack（AddSource 会依次
	// 试 apps.json / moo.json / raw/… 等候选，测试不关心具体命中哪个）。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.hits, 1)
		if f.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// newServer 造一个隔离的 Server（独立 MOO_DATA 与退避状态文件）。
func (f *backoffFixture) newServer(t *testing.T, statePath string) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{SourceListURL: f.srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.SetListSyncPath(statePath)
	return s
}

// TestListSyncBackoffLadder 退避阶梯：6h → 12h → 24h 封顶。
func TestListSyncBackoffLadder(t *testing.T) {
	cases := []struct {
		count int
		want  int64
	}{{0, 6}, {1, 6}, {2, 12}, {3, 24}, {4, 24}, {9, 24}}
	for _, c := range cases {
		if got := int64(listSyncBackoffOf(c.count).Hours()); got != c.want {
			t.Errorf("count=%d 退避应为 %dh, got %dh", c.count, c.want, got)
		}
	}
}

// TestListSyncDeadLinkBacksOffAcrossRestart 死链：第一轮真抓 1 次；
// 第二轮（新进程，状态从磁盘载入）直接跳过，不再发请求。
func TestListSyncDeadLinkBacksOffAcrossRestart(t *testing.T) {
	f := newBackoffFixture(t)
	state := filepath.Join(t.TempDir(), "source_list_sync.json")
	s1 := f.newServer(t, state)
	res1, err := s1.sourceListSyncOnce(t.Context())
	if err != nil {
		t.Fatalf("sourceListSyncOnce: %v", err)
	}
	if res1.Failed != 1 || res1.Added != 0 {
		t.Fatalf("首轮应失败 1 条, got %+v", res1)
	}
	hits1 := atomic.LoadInt32(&f.hits)
	if hits1 == 0 {
		t.Fatal("首轮应真抓死链（含候选索引名重试）")
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("退避状态应落盘: %v", err)
	}
	// 模拟重启：新 Server 从同一状态文件载入
	s2 := f.newServer(t, state)
	res2, err := s2.sourceListSyncOnce(t.Context())
	if err != nil {
		t.Fatalf("第二轮 sourceListSyncOnce: %v", err)
	}
	if res2.Backoff != 1 {
		t.Fatalf("第二轮应因退避跳过 1 条, got %+v", res2)
	}
	if got := atomic.LoadInt32(&f.hits); got != hits1 {
		t.Fatalf("退避期内不应再抓死链, hits %d → %d", hits1, got)
	}
}

// 批次 1 F3（N7）：退避表超上限时淘汰的必须是「最晚到期」（最不紧急）的键，
// 而不是「最早到期、最该重试」的键。
// 反向验证：把 pruneListSyncFailsLocked 的排序改回升序 → 本用例 FAIL。
func TestPruneListSyncFailsKeepsUrgent(t *testing.T) {
	s := &Server{}
	// 499 条普通条目（NextAt 递增）+ 2 条极端条目 = cap+1 条（超上限 1 条）。
	s.listSync.Fails = make(map[string]listSyncFail, listSyncFailCap+1)
	for i := 0; i < listSyncFailCap-1; i++ {
		s.listSync.Fails[fmt.Sprintf("k%03d", i)] = listSyncFail{NextAt: int64(1000 + i)}
	}
	s.listSync.Fails["most-urgent"] = listSyncFail{NextAt: 1}        // 马上到期：绝不能删
	s.listSync.Fails["least-urgent"] = listSyncFail{NextAt: 1 << 40} // 一天多以后：该删
	n := len(s.listSync.Fails) - listSyncFailCap
	if n != 1 {
		t.Fatalf("构造错误：超出上限 %d 条（期望 1）", n)
	}
	s.pruneListSyncFailsLocked(n)
	if len(s.listSync.Fails) != listSyncFailCap {
		t.Fatalf("淘汰后应恰好剩 %d 条, got %d", listSyncFailCap, len(s.listSync.Fails))
	}
	if _, ok := s.listSync.Fails["least-urgent"]; ok {
		t.Error("最晚到期的键应被淘汰（旧实现把最早的删了，方向反了）")
	}
	if _, ok := s.listSync.Fails["most-urgent"]; !ok {
		t.Error("最早到期（最该重试）的键绝不能被淘汰")
	}
	if _, ok := s.listSync.Fails["k000"]; !ok {
		t.Error("NextAt 最小的普通条目不该被淘汰")
	}
}
