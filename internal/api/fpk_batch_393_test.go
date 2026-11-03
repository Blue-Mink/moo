package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/pipeline"
)

// 0.6.393：已下载 FPK 批量删除端点。
// 口径：非法名逐项报 failed（不整批拒绝）、重复项只处理一次、
// 文件已不存在=目标状态已达成（计入 deleted）、其它文件不受影响。
func TestBatchRemoveDownloads393(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a_1.0_x86.fpk", "b_2.0_x86.fpk", "keep.fpk"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Pipe: &pipeline.Pipeline{Downloads: dir}}

	body := `{"names":["a_1.0_x86.fpk","a_1.0_x86.fpk","b_2.0_x86.fpk","gone.fpk","../../etc/passwd"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/fpk-downloads/batch-delete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.batchRemoveDownloads(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		OK      bool `json:"ok"`
		Deleted int  `json:"deleted"`
		Failed  []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	// a（重复只算一次）+ b + gone（本就不存在）= 3
	if got.Deleted != 3 {
		t.Fatalf("deleted=%d, want 3 (body=%s)", got.Deleted, rec.Body.String())
	}
	if len(got.Failed) != 1 || got.Failed[0].Name != "../../etc/passwd" {
		t.Fatalf("failed=%+v, want 仅非法名", got.Failed)
	}
	for _, n := range []string{"a_1.0_x86.fpk", "b_2.0_x86.fpk"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Fatalf("%s 应已删除（err=%v）", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.fpk")); err != nil {
		t.Fatalf("未提交的文件被误删: %v", err)
	}
}

// 空名单 / 超上限 → 400（前端不会发，但契约要卡住）。
func TestBatchRemoveDownloadsRejectsBadInput393(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Pipe: &pipeline.Pipeline{Downloads: dir}}
	cases := []struct {
		name string
		body string
	}{
		{"empty", `{"names":[]}`},
		{"too-many", func() string {
			names := make([]string, 501)
			for i := range names {
				names[i] = "x.fpk"
			}
			b, _ := json.Marshal(map[string]any{"names": names})
			return string(b)
		}()},
		{"bad-json", `{"names":`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/fpk-downloads/batch-delete", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			s.batchRemoveDownloads(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}
