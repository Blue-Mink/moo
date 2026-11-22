package api

// P1-1：手动更新队列接口 + unknown 负缓存的用例。

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// unknown（任务结果未知）必须静默 6 小时：openclaw 每小时重试导致后台占用高的回归。
func TestUnknownSkipTTL(t *testing.T) {
	s := negCacheServer(t)
	s.markAutoUpdateSkip("app.u", "2.0.0", "abc", autoUpdateSkipReasonUnknown, nil)
	m := s.autoUpdateSkips()
	if !autoUpdateSkipHit(m, "app.u", "2.0.0", "abc", autoUpdateSkipReasonUnknown) {
		t.Fatalf("unknown 条目应命中: %#v", m)
	}
	if !autoUpdateSkipHitAny(m, "app.u", "2.0.0", "abc") {
		t.Fatalf("hitAny 必须包含 unknown（否则每小时重试）")
	}
	for _, e := range m {
		until, err := time.Parse(time.RFC3339, e.Until)
		if err != nil {
			t.Fatalf("until 不可解析: %q", e.Until)
		}
		left := time.Until(until)
		if left < 5*time.Hour || left > 7*time.Hour {
			t.Fatalf("unknown TTL = %v，期望约 6h", left)
		}
	}
}

func TestListManualUpdates(t *testing.T) {
	s := negCacheServer(t)
	s.markAutoUpdateSkip("app.w", "1.0.0", "", autoUpdateSkipReasonWizard, []string{"note"})
	s.markAutoUpdateSkip("app.u", "2.0.0", "abc", autoUpdateSkipReasonUnknown, nil)
	s.markAutoUpdateSkip("app.b", "3.0.0", "", autoUpdateSkipReasonBlocked, nil)

	rec := httptest.NewRecorder()
	s.listManualUpdates(rec, httptest.NewRequest("GET", "/api/updates/manual", nil))
	var got struct {
		Count int                `json:"count"`
		Items []manualUpdateItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	if got.Count != 3 || len(got.Items) != 3 {
		t.Fatalf("count=%d items=%d，期望 3/3", got.Count, len(got.Items))
	}
	labels := map[string]string{}
	for _, it := range got.Items {
		if it.ReasonLabel == "" {
			t.Fatalf("%s 缺 reason_label", it.App)
		}
		labels[it.App] = it.ReasonLabel
	}
	if labels["app.u"] == "" || labels["app.w"] == "" || labels["app.b"] == "" {
		t.Fatalf("条目缺失: %+v", got.Items)
	}
	for i := 1; i < len(got.Items); i++ {
		if got.Items[i-1].Until > got.Items[i].Until {
			t.Fatalf("未按 until 升序: %+v", got.Items)
		}
	}
}
