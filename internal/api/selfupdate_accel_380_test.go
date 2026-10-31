package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.380（用户令：「点更新用不了 GitHub 加速、进度很慢」+「加速给 mate 复用」）：
// 自更新下载候选改成**镜像优先、直连兜底**，并给下载加首字节/停滞看护；
// 同时向 Mate 暴露健康镜像表（GET /api/mate/mirrors）。

// TestSelfUpdateCandidatesMirrorFirst_380 候选顺序：镜像在前（最多 3 个）、
// 直连最后；三件套（FPK/.sha256/.minisig）随同一前缀改写。
func TestSelfUpdateCandidatesMirrorFirst_380(t *testing.T) {
	old := selfUpdateMirrorPrefixes
	selfUpdateMirrorPrefixes = func(*Server) []string {
		return []string{"https://m1.example/", "https://m2.example/"}
	}
	defer func() { selfUpdateMirrorPrefixes = old }()

	const asset = "https://github.com/Blue-Mink/moo/releases/download/v9.9.9/moo_9.9.9_x86.fpk"
	cands := selfUpdateDownloadCandidates(nil, asset, asset+".sha256", asset+".minisig")
	if len(cands) != 3 {
		t.Fatalf("应 2 镜像 + 1 直连，实际 %d 条", len(cands))
	}
	if cands[0].asset != "https://m1.example/"+asset {
		t.Fatalf("第一个候选应是镜像，实际 %s", cands[0].asset)
	}
	if cands[0].sha == "" || cands[0].minisig == "" {
		t.Fatalf("镜像候选必须同时给出 .sha256 与 .minisig（fail-closed 需要）")
	}
	if !strings.HasPrefix(cands[0].sha, "https://m1.example/") || !strings.HasPrefix(cands[0].minisig, "https://m1.example/") {
		t.Fatalf("侧车必须与包同前缀，实际 sha=%s sig=%s", cands[0].sha, cands[0].minisig)
	}
	if cands[2].asset != asset || cands[2].sha != asset+".sha256" {
		t.Fatalf("直连必须是最后一个候选，实际 %+v", cands[2])
	}
}

// TestSelfUpdateFirstByteTimeout_380 连上却不吐字节 → 12s（测试压到 ~1s）
// 内主动放弃，不拖到 8 分钟总超时。
func TestSelfUpdateFirstByteTimeout_380(t *testing.T) {
	oldF, oldS := selfUpdateFirstByteTimeout, selfUpdateStallWindow
	selfUpdateFirstByteTimeout, selfUpdateStallWindow = 150*time.Millisecond, 150*time.Millisecond
	defer func() { selfUpdateFirstByteTimeout, selfUpdateStallWindow = oldF, oldS }()

	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "5242880")
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

	rec := httptest.NewRecorder()
	start := time.Now()
	_, err := downloadSelfUpdateOne(rec, rec, context.Background(), &http.Client{}, ts.URL+"/moo.fpk", "9.9.9")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("卡死通道必须报错")
	}
	if !strings.Contains(err.Error(), "首字节") {
		t.Fatalf("应报首字节超时，实际：%v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("看护太慢（%s），应在秒级切换通道", elapsed)
	}
}

// TestMateMirrorsAuth_380 缺 X-Moo-Mate 头必须 403（与 /api/mate/github-token 同口径）。
func TestMateMirrorsAuth_380(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	s.mateMirrors(rec, httptest.NewRequest(http.MethodGet, "/api/mate/mirrors", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺头应 403，实际 %d", rec.Code)
	}
}

// TestMateMirrorsPayload_380 带齐头 → 返回健康镜像前缀（无健康数据时按声明顺序回退）。
func TestMateMirrorsPayload_380(t *testing.T) {
	s := &Server{Cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "/api/mate/mirrors", nil)
	req.Header.Set("X-Moo-Mate", "1")
	rec := httptest.NewRecorder()
	s.mateMirrors(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", rec.Code)
	}
	var out struct {
		Prefixes []string `json:"prefixes"`
		Count    int      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if out.Count == 0 || out.Count != len(out.Prefixes) {
		t.Fatalf("count 与 prefixes 不一致：%+v", out)
	}
	if out.Count > mateMirrorMax {
		t.Fatalf("前缀数不得超过 %d，实际 %d", mateMirrorMax, out.Count)
	}
	for _, p := range out.Prefixes {
		if !strings.HasPrefix(p, "https://") || !strings.HasSuffix(p, "/") {
			t.Fatalf("前缀格式应为 https://…/，实际 %q", p)
		}
	}
}
