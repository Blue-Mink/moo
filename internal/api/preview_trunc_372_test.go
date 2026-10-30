package api

// 0.6.372 预览图截断修复单测。
// 事故：上游/镜像传输中断仍回 200 + 半截 body，老逻辑只看魔数 →
// 截断图进缓存 → 前端 <img> 解码失败（「预览图加载不出来」长期不愈）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 构造一张"完整"PNG：魔数 + 填充 + IEND 尾块；截断版去掉 IEND。
func pngBytes(payload int, complete bool) []byte {
	b := make([]byte, 0, payload+16)
	b = append(b, 0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A)
	b = append(b, bytes.Repeat([]byte{0x00}, payload)...)
	if complete {
		b = append(b, 0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xAE, 'B', '`', 0x82)
	}
	return b
}

func TestImageCompletePNG(t *testing.T) {
	if !imageComplete(pngBytes(4096, true)) {
		t.Fatal("完整 PNG 应判定完整")
	}
	if imageComplete(pngBytes(4096, false)) {
		t.Fatal("缺 IEND 的 PNG 应判定截断（这是事故根因）")
	}
}

func TestImageCompleteJPEGAndWebP(t *testing.T) {
	jpgOK := append([]byte{0xFF, 0xD8}, bytes.Repeat([]byte{1}, 4096)...)
	jpgOK = append(jpgOK, 0xFF, 0xD9)
	if !imageComplete(jpgOK) {
		t.Fatal("完整 JPEG 应判定完整")
	}
	jpgBad := jpgOK[:len(jpgOK)-2]
	if imageComplete(jpgBad) {
		t.Fatal("缺 EOI 的 JPEG 应判定截断")
	}
	webpOK := make([]byte, 4096)
	copy(webpOK, []byte("RIFF"))
	binary.LittleEndian.PutUint32(webpOK[4:8], uint32(len(webpOK)-8))
	copy(webpOK[8:], []byte("WEBPVP8 "))
	if !imageComplete(webpOK) {
		t.Fatal("长度自洽的 WebP 应判定完整")
	}
	if imageComplete(webpOK[:2000]) {
		t.Fatal("RIFF 声明长度与实际不符应判定截断")
	}
}

// 上游返回截断图 → fetchPreviewShared 必须报瞬态错误、且不落地缓存。
func TestFetchPreviewSharedRejectsTruncated(t *testing.T) {
	truncated := pngBytes(3000, false)
	withPreviewFetcher(t, func(*Server, context.Context, string) ([]byte, string, bool, string, error) {
		return truncated, "image/png", false, "", nil
	})
	s := &Server{}
	body, _, dead, err := s.fetchPreviewShared(context.Background(), "https://example.com/trunc-372.png")
	if err == nil {
		t.Fatal("截断图必须报错（否则会写进缓存）")
	}
	if !errors.Is(err, errPreviewTruncated) {
		t.Fatalf("err = %v, want errPreviewTruncated（瞬态，允许 60s 后重试）", err)
	}
	if dead {
		t.Fatal("截断不是确定性缺失，dead 应为 false")
	}
	if len(body) != 0 {
		t.Fatalf("失败时不应返回 body，got %d bytes", len(body))
	}
	st := s.readmeStore()
	if _, _, ok := st.Get("preview|https://example.com/trunc-372.png"); ok {
		t.Fatal("截断图不得进入缓存")
	}
}

// 缓存里已有截断图（历史脏数据）→ 命中时必须忽略并重新抓取。
func TestProxyPreviewIgnoresTruncatedCache(t *testing.T) {
	good := pngBytes(3000, true)
	truncated := pngBytes(3000, false)
	withPreviewFetcher(t, func(*Server, context.Context, string) ([]byte, string, bool, string, error) {
		return good, "image/png", false, "", nil
	})
	s := &Server{}
	const raw = "https://example.com/cached-trunc-372.png"
	s.readmeStore().Put("preview|"+raw, truncated, "image/png")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/apps/x/asset?type=preview&index=0", nil)
	s.proxyPreview(rec, req, raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（应忽略坏缓存并重抓）", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), good) {
		t.Fatalf("返回内容 %d 字节，want 重抓的完整图 %d 字节", rec.Body.Len(), len(good))
	}
}

// 根因层：竞速读 body 时中断 / 声明长度不足，该候选必须判失败，
// 不能把半截内容当 200 成功返回（老逻辑丢弃 io.ReadAll 的 err）。
func TestRaceFetchRejectsTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "5000") // 谎报：实际只写 128 字节
		_, _ = w.Write(bytes.Repeat([]byte("A"), 128))
	}))
	defer srv.Close()

	s := &Server{}
	body, _, _, _, err := s.raceFetchOnce(context.Background(), []string{srv.URL + "/t.png"}, true)
	if err == nil {
		t.Fatalf("截断响应应判失败，却返回了 %d 字节", len(body))
	}
}
