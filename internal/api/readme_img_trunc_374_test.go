package api

// 0.6.374 readme-img 截断缓存单测。
// 事故：moo 自己 README 的两张手机截图在 readme-img 缓存里是 0.6.372 之前
// 抓到的截断图（1,485 B / 4,125 B，缺 IEND），命中即返回 → 浏览器解码失败。
// 0.6.372 只给预览图补了「缓存命中也要校验完整性」，readme-img 这条路径漏了。

import (
	"bytes"
	"context"
	"moo/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
)

// readmeImgTestServer 构造带镜像监视器的 Server（proxyReadmeImg 会走
// readmeCandidateURLs → mirrorVariants，裸 Server 会 nil panic）。
func readmeImgTestServer() *Server {
	return &Server{Cfg: &config.Config{}, Mirrors: NewMirrorMonitor()}
}

func TestReadmeImgCacheUsable(t *testing.T) {
	if !readmeImgCacheUsable(pngBytes(4096, true)) {
		t.Fatal("完整 PNG 缓存应可直接服务")
	}
	if readmeImgCacheUsable(pngBytes(4096, false)) {
		t.Fatal("缺 IEND 的 PNG 缓存必须判不可用（事故根因）")
	}
	html := append([]byte("<html><body>rate limited</body></html>"), bytes.Repeat([]byte(" "), 2000)...)
	if !readmeImgCacheUsable(html) {
		t.Fatal("非图片内容不在此处拦（保持原有行为）")
	}
}

// 历史脏缓存（截断图）→ 命中必须忽略并重新抓取，且新内容覆盖旧条目。
func TestProxyReadmeImgIgnoresTruncatedCache(t *testing.T) {
	good := pngBytes(3000, true)
	truncated := pngBytes(3000, false)
	orig := readmeImgRaceFetch
	defer func() { readmeImgRaceFetch = orig }()
	readmeImgRaceFetch = func(*Server, context.Context, []string) ([]byte, string, bool, string, error) {
		return good, "image/png", false, "", nil
	}

	s := readmeImgTestServer()
	const img = "https://raw.githubusercontent.com/o/r/main/docs/shot.png"
	s.readmeStore().Put("readme-img|"+img, truncated, "image/png")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/x/asset?type=readme-img&u="+img, nil)
	s.proxyReadmeImg(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（应忽略坏缓存并重抓）", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), good) {
		t.Fatalf("返回 %d 字节，want 重抓的完整图 %d 字节", rec.Body.Len(), len(good))
	}
	if body, _, ok := s.readmeStore().Get("readme-img|" + img); !ok || !bytes.Equal(body, good) {
		t.Fatal("重抓成功后应覆盖缓存为完整图")
	}
}

// 上游仍返回截断图 → 502 且不得写入缓存（避免再固化 7 天）。
func TestProxyReadmeImgRejectsTruncatedFetch(t *testing.T) {
	truncated := pngBytes(3000, false)
	orig := readmeImgRaceFetch
	defer func() { readmeImgRaceFetch = orig }()
	readmeImgRaceFetch = func(*Server, context.Context, []string) ([]byte, string, bool, string, error) {
		return truncated, "image/png", false, "", nil
	}

	s := readmeImgTestServer()
	const img = "https://raw.githubusercontent.com/o/r/main/docs/trunc.png"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/x/asset?type=readme-img&u="+img, nil)
	s.proxyReadmeImg(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("截断图不能以 200 返回（前端会显示破图且长期不愈）")
	}
	if _, _, ok := s.readmeStore().Get("readme-img|" + img); ok {
		t.Fatal("截断图不得进入缓存")
	}
}

// 非 github 系 host 仍必须拒绝（防 SSRF 的既有契约不许回退）。
func TestProxyReadmeImgRejectsNonGithubHost(t *testing.T) {
	s := readmeImgTestServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/x/asset?type=readme-img&u=http://127.0.0.1/secret.png", nil)
	s.proxyReadmeImg(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
