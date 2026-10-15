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

type orderResp struct {
	DockOrder        []string `json:"dock_order"`
	SettingsTabOrder []string `json:"settings_tab_order"`
}

func getOrders(t *testing.T, s *Server) orderResp {
	t.Helper()
	rec := doSettingsGet(t, s)
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rec.Code)
	}
	var got orderResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return got
}

func eqKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestTabOrderRoundTrip dock/设置 tab 顺序：缺省默认（dock 5 项，settings 末位）
// → 保存 5 键全排列 → 回读一致 → 非法（未知 key / 缺 key / 重复 / 空）拒绝。
func TestTabOrderRoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}, Pipe: &pipeline.Pipeline{}}

	// ① 缺省 = 默认顺序（dock 5 项，settings 末位）
	got := getOrders(t, s)
	if !eqKeys(got.DockOrder, []string{"recommended", "all", "installed", "update_available", "settings"}) {
		t.Errorf("dock 缺省应为默认 5 项(settings 末位): %v", got.DockOrder)
	}
	if len(got.SettingsTabOrder) != 7 || got.SettingsTabOrder[0] != "system" {
		t.Errorf("设置 tab 缺省应为默认 7 项: %v", got.SettingsTabOrder)
	}

	// ② 保存 5 键全排列自定义顺序
	rec := doSettingsPut(t, s, `{"dock_order":["settings","installed","recommended","update_available","all"],"settings_tab_order":["about","notify","backup","log","source","accel","system"]}`)
	if rec.Code != 200 {
		t.Fatalf("PUT 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !eqKeys(s.Cfg.DockOrder, []string{"settings", "installed", "recommended", "update_available", "all"}) {
		t.Errorf("dock 顺序未原样保存: %v", s.Cfg.DockOrder)
	}
	if s.Cfg.SettingsTabOrder[6] != "system" {
		t.Errorf("设置 tab 顺序未保存: %v", s.Cfg.SettingsTabOrder)
	}

	// 回读一致
	got = getOrders(t, s)
	if !eqKeys(got.DockOrder, []string{"settings", "installed", "recommended", "update_available", "all"}) {
		t.Errorf("回读 dock 顺序不符: %v", got.DockOrder)
	}
	if got.SettingsTabOrder[0] != "about" {
		t.Errorf("回读设置 tab 顺序不符: %v", got.SettingsTabOrder)
	}

	// ⑤ 非法：未知 key
	rec = doSettingsPut(t, s, `{"dock_order":["recommended","all","installed","update_available","nope"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知 key 应 400, got %d", rec.Code)
	}
	// ⑤ 非法：缺 key（既非 5 键全排列，也非 legacy 4 键全排列）
	rec = doSettingsPut(t, s, `{"dock_order":["recommended","all","installed","settings"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 key 应 400, got %d", rec.Code)
	}
	// ⑤ 非法：重复 key
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
	if s.Cfg.DockOrder[0] != "settings" {
		t.Errorf("非法提交不应改动已存顺序: %v", s.Cfg.DockOrder)
	}

	// 未提交 = 不改动（只改间隔）
	rec = doSettingsPut(t, s, `{"check_interval_hours":12}`)
	if rec.Code != 200 || s.Cfg.DockOrder[0] != "settings" {
		t.Errorf("部分提交不应波及顺序: %d %v", rec.Code, s.Cfg.DockOrder)
	}
}

// TestDockOrderLegacyMigration ③ 老配置迁移：Cfg 存量为 legacy 4 键自定义序 →
// GET 保留原序 + settings 追加末位（不被重置为默认）。
func TestDockOrderLegacyMigration(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		DockOrder: []string{"installed", "recommended", "update_available", "all"},
	}, Pipe: &pipeline.Pipeline{}}
	got := getOrders(t, s)
	want := []string{"installed", "recommended", "update_available", "all", "settings"}
	if !eqKeys(got.DockOrder, want) {
		t.Errorf("legacy 迁移应保留原序+settings 末位, got %v want %v", got.DockOrder, want)
	}
}

// TestDockOrderLegacyPutNormalizes ④ PUT legacy 4 键 → 200 且存储为 5 键（末位 settings），
// 回读一致（兼容旧前端/旧脚本）。
func TestDockOrderLegacyPutNormalizes(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}, Pipe: &pipeline.Pipeline{}}
	rec := doSettingsPut(t, s, `{"dock_order":["installed","recommended","update_available","all"]}`)
	if rec.Code != 200 {
		t.Fatalf("legacy 4 键 PUT 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	want := []string{"installed", "recommended", "update_available", "all", "settings"}
	if !eqKeys(s.Cfg.DockOrder, want) {
		t.Errorf("legacy PUT 应归一化为 5 键, got %v want %v", s.Cfg.DockOrder, want)
	}
	got := getOrders(t, s)
	if !eqKeys(got.DockOrder, want) {
		t.Errorf("legacy PUT 回读不符: %v", got.DockOrder)
	}
}

// TestResolveOrder 脏数据（非法持久化值）回退默认，不 500。
func TestResolveOrder(t *testing.T) {
	s := &Server{Cfg: &config.Config{
		DockOrder:        []string{"broken"},
		SettingsTabOrder: []string{"about", "about"},
	}, Pipe: &pipeline.Pipeline{}}
	got := getOrders(t, s)
	if got.DockOrder[0] != "recommended" || len(got.DockOrder) != 5 {
		t.Errorf("脏 dock 数据应回退默认 5 项: %v", got.DockOrder)
	}
	if got.SettingsTabOrder[0] != "system" || len(got.SettingsTabOrder) != 7 {
		t.Errorf("脏设置 tab 数据应回退默认: %v", got.SettingsTabOrder)
	}
}
