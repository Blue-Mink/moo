package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 0.6.377：夹具改成「尾部完整」的极小 PNG。旧值只有 12 B 且没有 IEND，
// 靠 imageComplete 的「<1024 B 一律放行」豁免才被判完整；该豁免按 F5b 盲点
// 去掉后，它会被正确判成截断 —— 夹具失真，不是产品回归。
var testPng = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x00},
	'I', 'E', 'N', 'D', 0xAE, 'B', '`', 0x82)

func withPreviewFetcher(t *testing.T, fn func(*Server, context.Context, string) ([]byte, string, bool, string, error)) {
	t.Helper()
	old := previewRaceFetch
	previewRaceFetch = fn
	t.Cleanup(func() { previewRaceFetch = old })
}

// 0.6.331 C：同 URL 并发只抓一次。
func TestPreviewSingleFlight(t *testing.T) {
	var calls int32
	withPreviewFetcher(t, func(_ *Server, _ context.Context, u string) ([]byte, string, bool, string, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(80 * time.Millisecond)
		return testPng, "image/png", false, u, nil
	})
	s := &Server{}
	const url = "https://example.com/sf-preview.png"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, _, _, err := s.fetchPreviewShared(context.Background(), url)
			if err != nil || len(body) == 0 {
				t.Errorf("并发取图失败: err=%v len=%d", err, len(body))
			}
		}()
	}
	wg.Wait()
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("single-flight 失效：上游被调用 %d 次，期望 1", n)
	}
}

// 0.6.331 C：瞬态失败记 60s 负缓存，第二次请求不再重抓。
func TestProxyPreviewTransientNegative(t *testing.T) {
	var calls int32
	withPreviewFetcher(t, func(*Server, context.Context, string) ([]byte, string, bool, string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, "", false, "", errors.New("context deadline exceeded")
	})
	s := &Server{}
	const url = "https://example.com/neg-transient.png"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	rec := httptest.NewRecorder()
	s.proxyPreview(rec, req, url)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("首次应 502，实际 %d", rec.Code)
	}
	if !s.readmeStore().IsNegative("preview|" + url) {
		t.Fatalf("瞬态失败未写负缓存")
	}
	rec2 := httptest.NewRecorder()
	s.proxyPreview(rec2, req, url)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("负缓存窗口内应 404，实际 %d", rec2.Code)
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("负缓存未生效：上游被调用 %d 次", n)
	}
}

// 0.6.331 C：确定性 404 → MarkNegative，缓存窗口内不再抓。
func TestProxyPreviewDeadNegative(t *testing.T) {
	var calls int32
	withPreviewFetcher(t, func(*Server, context.Context, string) ([]byte, string, bool, string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, "", true, "", errors.New("源仓库返回 404")
	})
	s := &Server{}
	const url = "https://example.com/neg-dead.png"
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	s.proxyPreview(rec, req, url)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dead 应 404，实际 %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	s.proxyPreview(rec2, req, url)
	if rec2.Code != http.StatusNotFound || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("dead 负缓存未生效：code=%d calls=%d", rec2.Code, calls)
	}
}

// 0.6.331 C：抓到非图片内容 → 404 且记负缓存（不当 502 重试）。
func TestProxyPreviewNotImage(t *testing.T) {
	withPreviewFetcher(t, func(*Server, context.Context, string) ([]byte, string, bool, string, error) {
		return []byte("<html>not an image</html>"), "text/html", false, "", nil
	})
	s := &Server{}
	const url = "https://example.com/not-image.png"
	rec := httptest.NewRecorder()
	s.proxyPreview(rec, httptest.NewRequest(http.MethodGet, "/x", nil), url)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非图片应 404，实际 %d", rec.Code)
	}
	if !s.readmeStore().IsNegative("preview|" + url) {
		t.Fatalf("非图片未记负缓存")
	}
}
