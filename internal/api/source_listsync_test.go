package api

// 0.6.325 G3（BUG-A1）「源列表自动同步」接线单测：
// UI 开关此前后端零消费者（开关纯摆设）。现接入周期刷新循环 autoSyncSourceList，
// 与手动「同步官源」按钮共用 sourceListSyncOnce。全程 httptest 本地源，零外网。

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// newListSyncServer 一个 httptest 服务器兼任「清单文件」与「应用源」：
// /list 返回 list(base) 文本（base=服务器自身地址，可拼指向 /any 的源 URL）；
// 其余路径回一份合法扁平 fnpack（AddSource 校验可过）。
func newListSyncServer(t *testing.T, list func(base string) string, hits *int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(list(srv.URL)))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestAutoSyncSourceListSwitchOff 开关关（SourceListOff=true）→ 直接跳过：
// 连清单都不抓（hits=0）、零源新增。
func TestAutoSyncSourceListSwitchOff(t *testing.T) {
	var hits int32
	srv := newListSyncServer(t, func(b string) string { return b + "/any\n" }, &hits)
	cfg := &config.Config{SourceListOff: true, SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.autoSyncSourceList(t.Context())
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("开关关不应发起任何抓取, hits=%d", got)
	}
	if len(cfg.Sources) != 0 {
		t.Fatalf("开关关不应新增源, got %d", len(cfg.Sources))
	}
}

// TestAutoSyncSourceListEmptyList 清单解析后 0 条 URL（空清单短路）→ 零新增、
// 不 panic、不产生任何源变更。
func TestAutoSyncSourceListEmptyList(t *testing.T) {
	srv := newListSyncServer(t, func(string) string { return "# 只有注释\n\n  \nnot-a-url\n" }, nil)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.autoSyncSourceList(t.Context())
	if len(cfg.Sources) != 0 {
		t.Fatalf("空清单不应新增源, got %d", len(cfg.Sources))
	}
}

// TestAutoSyncSourceListAddsNew 正向接线（BUG-A1 核心：改造前永远不会执行）：
// 开关开 + 清单含 1 个缺失源 → 添加成功入 cfg.Sources。
func TestAutoSyncSourceListAddsNew(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListSyncServer(t, func(b string) string { return b + "/any # 演示源\n" }, nil)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.autoSyncSourceList(t.Context())
	if len(cfg.Sources) != 1 {
		t.Fatalf("开关开且清单含缺失源 → 应新增 1 源, got %d (%+v)", len(cfg.Sources), cfg.Sources)
	}
}

// TestAutoSyncSourceListFetchFailSkips 抓取失败（清单地址=已关闭的服务器）→
// 单行日志跳过，绝不 panic、零变更（周期轮不被中断的语义保证）。
func TestAutoSyncSourceListFetchFailSkips(t *testing.T) {
	dead := newListSyncServer(t, func(string) string { return "" }, nil)
	deadURL := dead.URL // t.Cleanup 关之前先取地址；Close 后连接必拒
	dead.Close()
	cfg := &config.Config{SourceListURL: deadURL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	s.autoSyncSourceList(t.Context())
	if len(cfg.Sources) != 0 {
		t.Fatalf("抓取失败不应有源变更, got %d", len(cfg.Sources))
	}
}

// TestSyncListHandlerRefactorRegression 手动按钮 refactor 后契约不变：
// 200 + 新增入列（fetched/added 语义保留）。
func TestSyncListHandlerRefactorRegression(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	srv := newListSyncServer(t, func(b string) string { return b + "/any\n" + b + "/any2\n" }, nil)
	cfg := &config.Config{SourceListURL: srv.URL + "/list"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	r := httptest.NewRequest("POST", "/api/sources/sync-list", nil).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.syncList(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("syncList 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("应新增 2 源, got %d", len(cfg.Sources))
	}
}
