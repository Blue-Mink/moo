package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// 0.6.331 P1：预览图「首次抓取慢 / 同步风暴期 502」的两条修复
//   A. 单请求预算从 25s 收到 15s，且只跑一轮竞速（不在请求线程里重试两轮）；
//   B. 用户请求与后台预热各自限并发（8 / 4），并把预览图纳入预热轮次，
//      让详情页首开命中缓存（毫秒级）而不是现抓。

func TestPreviewBudgetAndSemaphores_P1(t *testing.T) {
	if previewBudget > 15*time.Second {
		t.Fatalf("预览图单请求预算应 ≤15s（旧 25s 实测最坏等 21.9s），got %v", previewBudget)
	}
	if previewBudget < 8*time.Second {
		t.Fatalf("预算过紧会让正常上游（8.8s 链路）稳定失败，got %v", previewBudget)
	}
	if previewUserSem <= 0 || previewWarmSem <= 0 {
		t.Fatalf("并发闸门必须是正数：user=%d warm=%d", previewUserSem, previewWarmSem)
	}
	if previewWarmSem > previewUserSem {
		t.Fatalf("后台预热不应比用户请求更宽：warm=%d user=%d", previewWarmSem, previewUserSem)
	}
	if cap(previewUserSemCh) != previewUserSem {
		t.Fatalf("信号量容量与常量不一致：%d vs %d", cap(previewUserSemCh), previewUserSem)
	}
}

// 预热一轮必须把可见应用的预览图灌进两级缓存（页面最多展示 4 张，
// 第 5 张不预热），已命中/负缓存的跳过。
func TestWarmPreviewsPassFillsCache_P1(t *testing.T) {
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "fnpack.json") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"demo":{
			"display_name":"Demo","version":"1.0",
			"download_url":"https://example.com/demo.fpk",
			"preview_urls":["https://example.com/p1.png","https://example.com/p2.png",
			                "https://example.com/p3.png","https://example.com/p4.png",
			                "https://example.com/p5.png"]}}`)
	}))
	defer idx.Close()

	cfg := config.Default()
	cfg.Sources = []config.SourceRef{{Name: "t", URL: idx.URL}}
	m := source.NewManager(cfg)
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("索引刷新失败: %v", err)
	}

	var calls int32
	withPreviewFetcher(t, func(_ *Server, _ context.Context, u string) ([]byte, string, bool, string, error) {
		atomic.AddInt32(&calls, 1)
		return testPng, "image/png", false, u, nil
	})

	s := &Server{Cfg: cfg, Src: m}
	s.warmPreviewsPass(context.Background(), 10*time.Second)

	st := s.readmeStore()
	for i := 1; i <= 4; i++ {
		key := "preview|https://example.com/p" + string(rune('0'+i)) + ".png"
		if !st.Has(key) {
			t.Fatalf("第 %d 张预览图应已预热入缓存（key=%s）", i, key)
		}
	}
	if st.Has("preview|https://example.com/p5.png") {
		t.Fatal("第 5 张超出一页展示上限，不应预热（浪费带宽）")
	}
	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Fatalf("预热抓取次数应为 4（去重后），实际 %d", got)
	}
}

// 已有缓存 / 负缓存的图不重复抓取。
func TestWarmPreviewsPassSkipsCached_P1(t *testing.T) {
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "fnpack.json") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"demo":{
			"display_name":"Demo","version":"1.0",
			"download_url":"https://example.com/demo.fpk",
			"preview_urls":["https://example.com/hit.png","https://example.com/neg.png"]}}`)
	}))
	defer idx.Close()

	cfg := config.Default()
	cfg.Sources = []config.SourceRef{{Name: "t", URL: idx.URL}}
	m := source.NewManager(cfg)
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("索引刷新失败: %v", err)
	}

	var calls int32
	withPreviewFetcher(t, func(_ *Server, _ context.Context, u string) ([]byte, string, bool, string, error) {
		atomic.AddInt32(&calls, 1)
		return testPng, "image/png", false, u, nil
	})

	s := &Server{Cfg: cfg, Src: m}
	st := s.readmeStore()
	st.Put("preview|https://example.com/hit.png", testPng, "image/png")
	st.MarkNegative("preview|https://example.com/neg.png")

	s.warmPreviewsPass(context.Background(), 5*time.Second)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("命中/负缓存的预览图不应再抓，实际抓了 %d 次", got)
	}
}
