package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
)

func doSettingsPut(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	return rec
}

func doSettingsGet(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/settings", nil).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.getSettings(rec, r)
	return rec
}

// TestTabOrderRoundTrip dock/设置 tab 顺序：缺省默认 → 保存全排列 → 回读一致
// → 非法（未知 key / 缺 key / 重复）拒绝。
func TestTabOrderRoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}, Pipe: &pipeline.Pipeline{}}

	// 缺省 = 默认顺序
	rec := doSettingsGet(t, s)
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rec.Code)
	}
	var got struct {
		DockOrder        []string `json:"dock_order"`
		SettingsTabOrder []string `json:"settings_tab_order"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.DockOrder) != 4 || got.DockOrder[0] != "recommended" {
		t.Errorf("dock 缺省应为默认 4 项: %v", got.DockOrder)
	}
	if len(got.SettingsTabOrder) != 6 || got.SettingsTabOrder[0] != "system" {
		t.Errorf("设置 tab 缺省应为默认 6 项: %v", got.SettingsTabOrder)
	}

	// 保存自定义顺序
	rec = doSettingsPut(t, s, `{"dock_order":["installed","recommended","update_available","all"],"settings_tab_order":["about","notify","backup","source","accel","system"]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if s.Cfg.DockOrder[0] != "installed" || s.Cfg.SettingsTabOrder[5] != "system" {
		t.Errorf("顺序未保存: %v / %v", s.Cfg.DockOrder, s.Cfg.SettingsTabOrder)
	}

	// 回读一致
	rec = doSettingsGet(t, s)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.DockOrder[0] != "installed" || got.DockOrder[3] != "all" {
		t.Errorf("回读 dock 顺序不符: %v", got.DockOrder)
	}
	if got.SettingsTabOrder[0] != "about" {
		t.Errorf("回读设置 tab 顺序不符: %v", got.SettingsTabOrder)
	}

	// 非法：未知 key
	rec = doSettingsPut(t, s, `{"dock_order":["recommended","all","installed","nope"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知 key 应 400, got %d", rec.Code)
	}
	// 非法：缺 key（非全排列）
	rec = doSettingsPut(t, s, `{"dock_order":["recommended","all","installed"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 key 应 400, got %d", rec.Code)
	}
	// 非法：重复 key
	rec = doSettingsPut(t, s, `{"settings_tab_order":["system","system","source","backup","notify","about"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("重复 key 应 400, got %d", rec.Code)
	}
	// 空数组拒绝
	rec = doSettingsPut(t, s, `{"dock_order":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空数组应 400, got %d", rec.Code)
	}

	// 拒绝后配置不受影响
	if s.Cfg.DockOrder[0] != "installed" {
		t.Errorf("非法提交不应改动已存顺序: %v", s.Cfg.DockOrder)
	}

	// 未提交 = 不改动（只改间隔）
	rec = doSettingsPut(t, s, `{"check_interval_hours":12}`)
	if rec.Code != 200 || s.Cfg.DockOrder[0] != "installed" {
		t.Errorf("部分提交不应波及顺序: %d %v", rec.Code, s.Cfg.DockOrder)
	}
}

// TestResolveOrder 脏数据（非法持久化值）回退默认，不 500。
func TestResolveOrder(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		DockOrder:        []string{"broken"},
		SettingsTabOrder: []string{"about", "about"},
	}, Pipe: &pipeline.Pipeline{}}
	rec := doSettingsGet(t, s)
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rec.Code)
	}
	var got struct {
		DockOrder        []string `json:"dock_order"`
		SettingsTabOrder []string `json:"settings_tab_order"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.DockOrder[0] != "recommended" || len(got.DockOrder) != 4 {
		t.Errorf("脏 dock 数据应回退默认: %v", got.DockOrder)
	}
	if got.SettingsTabOrder[0] != "system" || len(got.SettingsTabOrder) != 6 {
		t.Errorf("脏设置 tab 数据应回退默认: %v", got.SettingsTabOrder)
	}
}
