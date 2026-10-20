package api

// ghforks_340_test.go —— 0.6.340 溯源提速批次的目录失效回归点。
//
// 背景（用户 2026-10-19 实测）：fork 关系同步完成后**目录缓存不失效**，
// 溯源字段要等下一次目录重建（源同步/安装/重启，常几十分钟）才出现在卡片上，
// 用户视角就是「溯源很慢」。修复 = ghForksRound 拉到新数据即 invalidateCatalog。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

func TestGHForksRoundInvalidatesCatalog_340(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"fork":false}`)
	}))
	defer srv.Close()

	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "A", URL: "https://github.com/a/fndepot"},
	}}
	f := source.NewGHForks("")
	f.SetAPIBaseForTest(srv.URL)
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Forks: f}

	// 预置一份「已有内容」的目录缓存
	s.catalogMu.Lock()
	s.catalogData = []AppInfo{{Key: "keep@A"}}
	s.catalogAt = time.Now()
	s.catalogMu.Unlock()

	if n := s.ghForksRound(context.Background(), 10); n != 1 {
		t.Fatalf("本轮应更新 1 个仓库, got %d", n)
	}

	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalogData != nil {
		t.Fatalf("fork 关系更新后目录缓存未失效（溯源要等下一次重建才生效）")
	}
}

// 源清单为空时 ghForkRepos 返回空：StartGHForksRefresh 应短等重试而不是
// 按「本轮 0 个」退避 6 小时（0.6.340 修复的启动早期空转）。
func TestGHForkReposEmptyWhenNoSources_340(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Forks: source.NewGHForks("")}
	if got := s.ghForkRepos(); len(got) != 0 {
		t.Fatalf("无源时应返回空, got %v", got)
	}
}
