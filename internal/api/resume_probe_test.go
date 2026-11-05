package api

// 回归测试：续传深测（2026-11-04，PR #14 纯收益部分）。
// 用 httptest 造三类假镜像：完美支持 Range / 忽略 Range / 每次内容不同。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"moo/internal/config"
)

const fakeAssetSize = 3 * resumeChunkBytes

// fakeByte 确定性内容（不占内存，按偏移生成）。
func fakeByte(i int64) byte { return byte('a' + i%26) }

// allowLoopbackForTest 让深测放行 httptest 的本机地址。
// 只替换 resumeGuard 这一处钩子，不动 netguard 全局豁免表
// （同包有源列表 SSRF 测试依赖那张表的原状）。
func allowLoopbackForTest(t *testing.T) {
	t.Helper()
	old := resumeGuard
	resumeGuard = func(string) (string, error) { return "test.local", nil }
	t.Cleanup(func() { resumeGuard = old })
}

func writeRange(w http.ResponseWriter, start, end int64) {
	w.Header().Set("Content-Range",
		fmt.Sprintf("bytes %d-%d/%d", start, end, fakeAssetSize))
	w.WriteHeader(http.StatusPartialContent)
	buf := make([]byte, 0, 64*1024)
	for i := start; i <= end; i++ {
		buf = append(buf, fakeByte(i))
		if len(buf) == cap(buf) {
			_, _ = w.Write(buf)
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		_, _ = w.Write(buf)
	}
}

// goodMirror 正确处理 Range：206 + 精确区间 + 内容确定。
func goodMirror() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		if !strings.HasPrefix(rng, "bytes=") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := int64(fakeAssetSize - 1)
		if len(parts) == 2 && parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if end >= fakeAssetSize {
			end = fakeAssetSize - 1
		}
		writeRange(w, start, end)
	}))
}

// noRangeMirror 忽略 Range，永远 200 返回整份内容。
func noRangeMirror() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		for i := int64(0); i < fakeAssetSize; i += 64 * 1024 {
			end := i + 64*1024 - 1
			if end >= fakeAssetSize {
				end = fakeAssetSize - 1
			}
			buf := make([]byte, 0, end-i+1)
			for j := i; j <= end; j++ {
				buf = append(buf, fakeByte(j))
			}
			_, _ = w.Write(buf)
		}
	}))
}

// flakyMirror 支持 206，但同一区间每次返回不同内容（假续传）。
func flakyMirror() *httptest.Server {
	var seq atomic.Int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := seq.Add(1)
		parts := strings.SplitN(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-", 2)
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := int64(fakeAssetSize - 1)
		if len(parts) == 2 && parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if end >= fakeAssetSize {
			end = fakeAssetSize - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fakeAssetSize))
		w.WriteHeader(http.StatusPartialContent)
		buf := make([]byte, 0, 64*1024)
		for i := start; i <= end; i++ {
			buf = append(buf, fakeByte(i)+byte(n%7)) // 每次内容漂移
			if len(buf) == cap(buf) {
				_, _ = w.Write(buf)
				buf = buf[:0]
			}
		}
		if len(buf) > 0 {
			_, _ = w.Write(buf)
		}
	}))
}

func optOf(srv *httptest.Server, key string) config.MirrorOption {
	return config.MirrorOption{Key: key, Label: key, URL: srv.URL + "/"}
}

func TestProbeResumeGoodMirror(t *testing.T) {
	allowLoopbackForTest(t)
	srv := goodMirror()
	defer srv.Close()

	res := probeResume(context.Background(), optOf(srv, "test-good"), "assets/test.bin")
	if !res.OK {
		t.Fatalf("支持 Range 的镜像应通过深测，reason=%s", res.Reason)
	}
}

func TestProbeResumeNoRangeMirror(t *testing.T) {
	allowLoopbackForTest(t)
	srv := noRangeMirror()
	defer srv.Close()

	res := probeResume(context.Background(), optOf(srv, "test-norange"), "assets/test.bin")
	if res.OK {
		t.Fatal("忽略 Range（恒 200）的镜像不应通过深测")
	}
	if !strings.Contains(res.Reason, "206") {
		t.Fatalf("失败原因应提示期望 206，got: %s", res.Reason)
	}
}

func TestProbeResumeFlakyMirror(t *testing.T) {
	allowLoopbackForTest(t)
	srv := flakyMirror()
	defer srv.Close()

	res := probeResume(context.Background(), optOf(srv, "test-flaky"), "assets/test.bin")
	if res.OK {
		t.Fatal("同一区间内容漂移的镜像不应通过深测")
	}
	if !strings.Contains(res.Reason, "不一致") {
		t.Fatalf("应命中「内容不一致」分支，got: %s", res.Reason)
	}
}

// TestTopSpeedKeysResumeTieBreak 深测结论必须真的参与优选：
// 吞吐不同 → 吞吐主序不变；同吞吐 → 续传通过者优先。
// （反向验证：把 topSpeedKeys 里的 resumeOKOf 分支去掉，本用例必红。）
func TestTopSpeedKeysResumeTieBreak(t *testing.T) {
	SetResumeResult("fast-no-resume", resumeResult{OK: false})
	SetResumeResult("slow-with-resume", resumeResult{OK: true})
	t.Cleanup(func() {
		SetResumeResult("fast-no-resume", resumeResult{})
		SetResumeResult("slow-with-resume", resumeResult{})
	})

	stats := []MirrorStat{
		{Key: "fast-no-resume", Status: "ok", SpeedBPS: 900, LatencyMS: 50},
		{Key: "slow-with-resume", Status: "ok", SpeedBPS: 800, LatencyMS: 60},
	}
	got := topSpeedKeys(stats)
	if len(got) != 2 || got[0] != "fast-no-resume" {
		t.Fatalf("不同吞吐时应保持吞吐主序: %v", got)
	}

	stats[0].SpeedBPS, stats[1].SpeedBPS = 800, 800
	got = topSpeedKeys(stats)
	if len(got) != 2 || got[0] != "slow-with-resume" {
		t.Fatalf("同吞吐时应让续传通过的源优先: %v", got)
	}
}

func TestTopResumeCandidates(t *testing.T) {
	stats := []MirrorStat{
		{Key: "gh-proxy", Status: "ok", SpeedBPS: 900},
		{Key: "ghfast", Status: "ok", SpeedBPS: 800},
		{Key: "dpik", Status: "fail"},
	}
	got := topResumeCandidates(stats, 2)
	if len(got) != 2 {
		t.Fatalf("应取 2 个可深测源，got %d", len(got))
	}
	var keys []string
	for _, o := range got {
		keys = append(keys, o.Key)
		if o.URL == "" {
			t.Fatalf("候选必须带前缀 URL（空 URL 项不可深测）: %+v", o)
		}
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "gh-proxy,ghfast" {
		t.Fatalf("应按吞吐 TopN 且只含可探测源: %v", keys)
	}
}
