package api

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/pipeline"
)

// 321R7 E：本地 FPK 上传端点（handler 直调，鉴权走 requireAdmin 网关层）。
func TestUploadFpkDownload(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Pipe: pipeline.New(nil, t.TempDir(), dir)}

	mkReq := func(filename string, content []byte) *http.Request {
		var b strings.Builder
		w := multipart.NewWriter(&b)
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
		w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/fpk-downloads/upload", strings.NewReader(b.String()))
		req.Header.Set("Content-Type", w.FormDataContentType())
		return req
	}

	// 1) 正常上传：.FPK 大写归一小写；../../ 路径穿越被 Base 拦在下载目录内
	content := []byte("fake-fpk-bytes-321r7")
	rec := httptest.NewRecorder()
	s.uploadFpkDownload(rec, mkReq("../../evil.FPK", content))
	if rec.Code != http.StatusOK {
		t.Fatalf("正常上传应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	p := filepath.Join(dir, "evil.fpk")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("应归一落盘为 evil.fpk: %v", err)
	}
	if fi.Size() != int64(len(content)) {
		t.Fatalf("内容大小不符: %d != %d", fi.Size(), len(content))
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".upload-tmp") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}

	// 2) 同名覆盖 → replaced=true
	rec2 := httptest.NewRecorder()
	s.uploadFpkDownload(rec2, mkReq("evil.fpk", []byte("new")))
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"replaced":true`) {
		t.Fatalf("覆盖应返回 replaced=true: %d %s", rec2.Code, rec2.Body.String())
	}

	// 3) 非 .fpk 拒 400
	rec3 := httptest.NewRecorder()
	s.uploadFpkDownload(rec3, mkReq("notes.txt", []byte("x")))
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("非 .fpk 应 400，实际 %d", rec3.Code)
	}

	// 4) 空文件拒 400
	rec4 := httptest.NewRecorder()
	s.uploadFpkDownload(rec4, mkReq("empty.fpk", nil))
	if rec4.Code != http.StatusBadRequest {
		t.Fatalf("空文件应 400，实际 %d", rec4.Code)
	}

	// 5) 绝对路径文件名 → Base 归一落下载目录
	rec5 := httptest.NewRecorder()
	s.uploadFpkDownload(rec5, mkReq("/abs/path/x.fpk", []byte("y")))
	if rec5.Code != http.StatusOK {
		t.Fatalf("绝对路径文件名应成功（Base 归一）: %d %s", rec5.Code, rec5.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "x.fpk")); err != nil {
		t.Fatalf("应落下载目录 x.fpk: %v", err)
	}
}
