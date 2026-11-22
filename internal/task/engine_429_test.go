package task

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// 429 + Retry-After 的测试服务器：hits 计真实收到的请求数。
func new429Server(t *testing.T, retryAfter string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// P2-1：同一 host 第一次 429（Retry-After: 60）后，第二次 Download 不得再发请求。
// 反向验证：去掉 Download 里的「限流期跳过」或去掉 runHTTP 的 mark → 本用例 FAIL。
func TestEngineDownload429SkipsSecondAttempt(t *testing.T) {
	ResetRateLimitsForTest()
	t.Cleanup(ResetRateLimitsForTest)
	srv, hits := new429Server(t, "60")
	u := srv.URL + "/x.fpk"
	dir := t.TempDir()
	e := NewEngine("") // 无 aria2 → 必走内置单连接（httptest 固定发 429）

	err1 := e.Download(context.Background(), DownloadOpts{
		DestPath:   filepath.Join(dir, "x.fpk"),
		Candidates: []string{u},
	})
	if err1 == nil {
		t.Fatal("429 应失败")
	}
	if !strings.Contains(err1.Error(), "Retry-After 60s") {
		t.Errorf("错误应带 Retry-After 60s: %v", err1)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("首轮应发 1 次请求, got %d", got)
	}

	var stage string
	err2 := e.Download(context.Background(), DownloadOpts{
		DestPath:   filepath.Join(dir, "x.fpk"),
		Candidates: []string{u},
		OnFail:     func(candidate, st string, err error) { stage = st },
	})
	if err2 == nil {
		t.Fatal("限流期内应直接失败")
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("限流期内不得再发请求, got %d 次（限流表未生效）", got)
	}
	if !strings.Contains(err2.Error(), "429 限流中") {
		t.Errorf("限流跳过错误应含「429 限流中」: %v", err2)
	}
	if stage != "限流跳过" {
		t.Errorf("OnFail stage 应为「限流跳过」, got %q", stage)
	}
}

// P2-1：错误汇总必须是线路级的（标签 + Retry-After），并给出限流结论。
func TestEngineDownload429SummaryIsLineLevel(t *testing.T) {
	ResetRateLimitsForTest()
	t.Cleanup(ResetRateLimitsForTest)
	srv, hits := new429Server(t, "60")
	dir := t.TempDir()
	e := NewEngine("")
	// 两个候选同 host：第一个真收到 429，第二个应被限流表跳过（不再发请求）
	err := e.Download(context.Background(), DownloadOpts{
		DestPath:   filepath.Join(dir, "x.fpk"),
		Candidates: []string{srv.URL + "/a.fpk", srv.URL + "/b.fpk"},
	})
	if err == nil {
		t.Fatal("全部 429 应失败")
	}
	msg := err.Error()
	for _, want := range []string{"[直连/源站]", "HTTP 429", "Retry-After 60s", "429 限流中", "所有候选都被限流（429）"} {
		if !strings.Contains(msg, want) {
			t.Errorf("汇总应含 %q: %s", want, msg)
		}
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("同 host 第二候选应跳过, 请求数应为 1, got %d", got)
	}
}

// P2-1：镜像壳地址的 429 要打「[加速镜像]」标签（与源站区分）。
func TestEngineDownload429MirrorLabel(t *testing.T) {
	ResetRateLimitsForTest()
	t.Cleanup(ResetRateLimitsForTest)
	srv, _ := new429Server(t, "")
	wrapped := srv.URL + "/https://raw.githubusercontent.com/o/r/main/x.fpk"
	e := NewEngine("")
	err := e.Download(context.Background(), DownloadOpts{
		DestPath:   filepath.Join(t.TempDir(), "x.fpk"),
		Candidates: []string{wrapped},
	})
	if err == nil {
		t.Fatal("429 应失败")
	}
	if !strings.Contains(err.Error(), "[加速镜像]") {
		t.Errorf("镜像壳候选应标 [加速镜像]: %v", err)
	}
	// 无 Retry-After 头 → 保持原提示（不加 Retry-After 后缀），也不能被误判成「地址不存在」
	if strings.Contains(err.Error(), "Retry-After") {
		t.Errorf("无 Retry-After 头不应编造等待时长: %v", err)
	}
}
