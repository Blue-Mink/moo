package api

// P1-1：手动更新队列接口 + unknown 负缓存的用例。

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
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

// 用户「忽略更新」后，需手动处理队列必须同步减一（2026-11-27 用户令）。
// 忽略判定与 applyIgnoredUpdates 同源（config.UpdateIgnored 按 AppName 精确匹配）。
func TestListManualUpdatesSkipsIgnoredApps(t *testing.T) {
	s := negCacheServer(t)
	s.markAutoUpdateSkip("app.w", "1.0.0", "", autoUpdateSkipReasonWizard, nil)
	s.markAutoUpdateSkip("app.u", "2.0.0", "abc", autoUpdateSkipReasonUnknown, nil)

	read := func() (int, []manualUpdateItem) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.listManualUpdates(rec, httptest.NewRequest("GET", "/api/updates/manual", nil))
		var got struct {
			Count int                `json:"count"`
			Items []manualUpdateItem `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
		}
		return got.Count, got.Items
	}

	if n, _ := read(); n != 2 {
		t.Fatalf("忽略前 count=%d，期望 2", n)
	}
	s.Cfg = &config.Config{UpdateIgnored: []string{"app.w"}}
	n, items := read()
	if n != 1 || len(items) != 1 || items[0].App != "app.u" {
		t.Fatalf("忽略 app.w 后 count=%d items=%+v，期望只剩 app.u", n, items)
	}
	// 取消忽略 → 条目仍在负缓存里，数字自动回来
	s.Cfg = &config.Config{}
	if n, _ := read(); n != 2 {
		t.Fatalf("取消忽略后 count=%d，期望 2", n)
	}
}

// 批次 1 F6（N11）：channel（10371「需在官方应用中心升级」）此前落到
// default「需人工处理」，用户看不出为什么自动通道跑不通。
// 反向验证：删掉 manualReasonLabel 的 channel 分支 → 本用例 FAIL。
func TestManualReasonLabel(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{autoUpdateSkipReasonWizard, "升级需先在界面确认向导"},
		{autoUpdateSkipReasonBlocked, "平台标记需人工处理（依赖占用等）"},
		{autoUpdateSkipReasonUnknown, "平台任务结果未知（可能仍在执行）"},
		{autoUpdateSkipReasonChannel, "该应用需在官方应用中心升级"},
		{"something-else", "需人工处理"},
	}
	for _, c := range cases {
		if got := manualReasonLabel(c.reason); got != c.want {
			t.Errorf("reason=%q 文案应为 %q, got %q", c.reason, c.want, got)
		}
	}
}

// 批次 3a（D1）：channel 判定要连续两次命中才算数，首轮（Hits=1）不该出现在
// 「需人工处理」横幅里——否则一次瞬时 10371 就让用户看到「请去应用中心升级」。
func TestListManualUpdatesChannelNeedsTwoHits(t *testing.T) {
	s := negCacheServer(t)
	read := func() []manualUpdateItem {
		t.Helper()
		rec := httptest.NewRecorder()
		s.listManualUpdates(rec, httptest.NewRequest("GET", "/api/updates/manual", nil))
		var got struct {
			Count int                `json:"count"`
			Items []manualUpdateItem `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
		}
		if got.Count != len(got.Items) {
			t.Fatalf("count=%d 与 items=%d 不一致", got.Count, len(got.Items))
		}
		return got.Items
	}

	s.markAutoUpdateSkip("app.c", "2.0.0", "sha", autoUpdateSkipReasonChannel, nil)
	if items := read(); len(items) != 0 {
		t.Fatalf("首轮（未连击）不该进手动队列，实际 %+v", items)
	}
	s.markAutoUpdateSkip("app.c", "2.0.0", "sha", autoUpdateSkipReasonChannel, nil)
	items := read()
	if len(items) != 1 || items[0].App != "app.c" {
		t.Fatalf("连击两次后应进手动队列，实际 %+v", items)
	}
	if items[0].ReasonLabel != "该应用需在官方应用中心升级" {
		t.Fatalf("reason_label 不符：%q", items[0].ReasonLabel)
	}
}
