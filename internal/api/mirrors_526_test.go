package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.526（用户实报「自动选最快有时候没选到最快」）：probeGh 必须分批探测——
// 全部源同时各拉 2MB 会把出口带宽打满，测得「谁抢到」而不是「谁真的快」
// （实测同一源相邻两轮 7.28MB/s ↔ 1.0MB/s、直连 7.4MB/s ↔ 超时）。
//
// 反向验证：把 probeGh 的步长改成 len(opts)（全并发一批），peak 会 > 批大小、本用例必红。
func TestProbeGhBatchesLimitConcurrency(t *testing.T) {
	var cur, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(60 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", 1600*1024)))
		atomic.AddInt32(&cur, -1)
	}))
	defer srv.Close()
	withGhAssets(t, []string{"github.com/o/r/releases/download/v1/probe.bin"})

	n := ghProbeBatchSize*2 + 1
	opts := make([]config.MirrorOption, 0, n)
	for i := 0; i < n; i++ {
		opts = append(opts, config.MirrorOption{Key: fmt.Sprintf("m%02d", i), Label: "m", URL: srv.URL + "/"})
	}
	config.SetRemoteMirrorOptions(opts, nil)
	t.Cleanup(func() { config.SetRemoteMirrorOptions(nil, nil) })

	m := NewMirrorMonitor()
	m.probeGh()
	stats := m.gh()
	if len(stats) != n {
		t.Fatalf("分批探测漏源：got %d want %d", len(stats), n)
	}
	for _, st := range stats {
		if st.Status != "ok" {
			t.Fatalf("%s 应为 ok, got %q (%s)", st.Key, st.Status, st.Error)
		}
	}
	if peak > ghProbeBatchSize {
		t.Fatalf("并发未受限：峰值 %d > 批大小 %d（分批失效）", peak, ghProbeBatchSize)
	}
}
