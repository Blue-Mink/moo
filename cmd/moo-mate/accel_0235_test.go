package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 0.2.35（用户令「moo 的 github 加速给 mate 侧复用」）：mate 自更新下载走
// 镜像候选 + 停滞切换；镜像表从 Moo 取（mateMirrorProvider）。

// TestMateDownloadCandidates_0235 候选顺序：镜像在前、直连最后；非 GitHub 原样。
func TestMateDownloadCandidates_0235(t *testing.T) {
	old := mateMirrorProvider
	defer func() { mateMirrorProvider = old }()
	const raw = "https://github.com/Blue-Mink/moo/releases/download/mate-v0.2.35/moo-mate_0.2.35_x86.fpk"

	mateMirrorProvider = func() []string { return []string{"https://m.example/"} }
	got := mateDownloadCandidates(raw)
	if len(got) != 2 || got[0] != "https://m.example/"+raw || got[1] != raw {
		t.Fatalf("应 [镜像, 直连]，实际 %v", got)
	}

	// Moo 不可达（provider 返回 nil）→ 回落硬编码镜像表，仍然是镜像优先。
	mateMirrorProvider = func() []string { return nil }
	got = mateDownloadCandidates(raw)
	if len(got) < 2 {
		t.Fatalf("应含硬编码镜像 + 直连，实际 %v", got)
	}
	if !strings.HasPrefix(got[0], githubMirrorPrefixes[0]) {
		t.Fatalf("首个候选应是硬编码首个镜像，实际 %s", got[0])
	}
	if got[len(got)-1] != raw {
		t.Fatalf("直连必须最后，实际 %v", got)
	}

	if c := mateDownloadCandidates("https://example.com/a.fpk"); len(c) != 1 || c[0] != "https://example.com/a.fpk" {
		t.Fatalf("非 GitHub 地址只应有原地址，实际 %v", c)
	}
}

// hostRT 按 host 分流：bad 返回 500，其余返回固定内容（不发真实网络）。
type hostRT struct {
	bad  map[string]bool
	body []byte
	hit  []string
}

func (h *hostRT) RoundTrip(r *http.Request) (*http.Response, error) {
	h.hit = append(h.hit, r.URL.Host)
	hd := http.Header{}
	if h.bad[r.URL.Host] {
		return &http.Response{StatusCode: http.StatusInternalServerError, Status: "500",
			Body: io.NopCloser(strings.NewReader("boom")), Header: hd, Request: r}, nil
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Body:          io.NopCloser(bytes.NewReader(h.body)),
		ContentLength: int64(len(h.body)),
		Header:        hd,
		Request:       r,
	}, nil
}

// TestFetchAnyMirrorFallback_0235 首个镜像 500 → 自动切下一个通道并落盘。
func TestFetchAnyMirrorFallback_0235(t *testing.T) {
	old := mateMirrorProvider
	defer func() { mateMirrorProvider = old }()
	mateMirrorProvider = func() []string { return []string{"https://bad.example/", "https://good.example/"} }

	const raw = "https://github.com/Blue-Mink/moo/releases/download/mate-v0.2.35/moo-mate_0.2.35_x86.fpk"
	payload := []byte("fpk-payload-0235")
	rt := &hostRT{bad: map[string]bool{"bad.example": true}, body: payload}
	client := &http.Client{Transport: rt, Timeout: 5 * time.Second}

	dst := filepath.Join(t.TempDir(), "out.fpk")
	var tried []int
	n, err := fetchAnyMirror(client, raw, dst, 1<<20, func(i, total int) {
		tried = append(tried, i)
		if total < 3 {
			t.Fatalf("候选数应 ≥3，实际 %d", total)
		}
	})
	if err != nil {
		t.Fatalf("第二个通道应成功，实际 %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("落盘字节数 %d，期望 %d", n, len(payload))
	}
	b, _ := os.ReadFile(dst)
	if !bytes.Equal(b, payload) {
		t.Fatalf("落盘内容不符")
	}
	if len(tried) != 2 {
		t.Fatalf("应尝试 2 个通道，实际 %v", tried)
	}
	if len(rt.hit) != 2 || rt.hit[0] != "bad.example" || rt.hit[1] != "good.example" {
		t.Fatalf("应先镜像后镜像/直连，实际 %v", rt.hit)
	}
}

// TestMateFetchFirstByteTimeout_0235 连上不吐字节 → 看护提前放弃（不等 5 分钟）。
func TestMateFetchFirstByteTimeout_0235(t *testing.T) {
	oldF, oldS := mateFirstByteTimeout, mateStallWindow
	mateFirstByteTimeout, mateStallWindow = 150*time.Millisecond, 150*time.Millisecond
	defer func() { mateFirstByteTimeout, mateStallWindow = oldF, oldS }()

	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(release)

	dst := filepath.Join(t.TempDir(), "out.bin")
	start := time.Now()
	_, err := fetchLimited(&http.Client{Timeout: 30 * time.Second}, ts.URL+"/a.fpk", dst, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "首字节") {
		t.Fatalf("应报首字节超时，实际 %v", err)
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("看护太慢：%s", el)
	}
}
