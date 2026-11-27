package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// 0.6.574（复核 F1）：数据目录自监控端点必须真的把计数暴露出来 ——
// 反向验证：删掉 route 里的注册或 handler 里删掉 save_rejected 字段 → FAIL。
func TestDatadirHealthEndpoint(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/datadir-health", nil)
	req = req.WithContext(withUserCtx(req.Context(), User{Username: "t", UserID: "1", Trusted: true, IsAdmin: true}))
	w := httptest.NewRecorder()
	s.requireAdmin(s.datadirHealth)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d，body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是 JSON: %v（%s）", err, w.Body.String())
	}
	for _, k := range []string{"data_dir", "data_dir_src", "app_name", "save_rejected", "save_failed", "ok"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("缺少字段 %q: %s", k, w.Body.String())
		}
	}
	if got["data_dir"] != os.Getenv("MOO_DATA") {
		t.Fatalf("data_dir=%v，期望 %s", got["data_dir"], dir)
	}
}

// 非管理员必须被拦（端点不外泄目录与计数）。
func TestDatadirHealthRequiresAdmin(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/datadir-health", nil)
	req = req.WithContext(withUserCtx(req.Context(), User{Username: "guest", UserID: "2", Trusted: true, IsAdmin: false}))
	w := httptest.NewRecorder()
	s.requireAdmin(s.datadirHealth)(w, req)
	if w.Code == http.StatusOK {
		t.Fatalf("非管理员不应拿到 200: %s", w.Body.String())
	}
}
