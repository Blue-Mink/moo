package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// 0.6.531：关于页「内存占用」实时端点——返回 RSS / 堆 / 运行时总量 / 协程数。
func TestAboutMemGet(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.aboutMemGet(rec, httptest.NewRequest("GET", "/api/about/mem", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	for _, k := range []string{"rss_bytes", "heap_bytes", "sys_bytes", "goroutines"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("missing field %q: %v", k, body)
		}
	}
	if body["heap_bytes"].(float64) <= 0 {
		t.Fatalf("heap_bytes should be > 0: %v", body)
	}
}
