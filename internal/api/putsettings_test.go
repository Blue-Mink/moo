package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

func putSettingsBody(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	if rec.Code != 200 {
		t.Fatalf("putSettings 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	return rec
}

// TestPutSettingsClearSemantics 指针语义：
//   - 提交空串 = 显式清除（此前 `!= ""` 判断让「清空后保存」无效，
//     旧值留存、重开设置页又看到旧值 → 用户观感「保存没作用」）
//   - 未提交（JSON 缺字段）= 不改动
//   - 开关 false 可显式保存（此前 url 为空时 source_list 切不开）
//
// 0.6.255：面板账号（panel_*）已从设置彻底移除，相关断言一并删除；
// 旧版提交的 panel_* 字段被静默忽略（不再落盘）。
func TestPutSettingsClearSemantics(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{
		CustomGitHubMirror: "https://old.gh.example",
		SourceListURL:      "https://old.list.example",
		SourceListOff:      true,
		Mirror:             "gh-proxy",
	}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	// 空串清除可清空字段
	putSettingsBody(t, s, `{"custom_github_mirror": ""}`)
	if cfg.CustomGitHubMirror != "" {
		t.Errorf("custom_github_mirror 空串应清除, got %q", cfg.CustomGitHubMirror)
	}
	// 未提交字段不改动
	if cfg.Mirror != "gh-proxy" {
		t.Error("未提交 mirror 不应改动")
	}

	// 空串清除后回写新值
	putSettingsBody(t, s, `{"custom_github_mirror": "https://new.gh.example"}`)
	if cfg.CustomGitHubMirror != "https://new.gh.example" {
		t.Errorf("新值应写入: gh=%q", cfg.CustomGitHubMirror)
	}

	// 0.6.255 回归：旧客户端仍会提交 panel_* —— 必须被静默忽略（不报错、
	// 不落盘、不影响其它字段）。
	cfg.PanelBaseURL = ""
	putSettingsBody(t, s, `{"panel_enabled": false, "panel_username": "fnos", "panel_password": "x", "panel_base_url": "http://127.0.0.1:9666", "custom_github_mirror": "https://keep.gh.example"}`)
	if cfg.PanelBaseURL != "" || cfg.PanelUsername != "" || cfg.PanelPassword != "" {
		t.Errorf("panel_* 字段应被忽略不落盘: base=%q user=%q pass=%q", cfg.PanelBaseURL, cfg.PanelUsername, cfg.PanelPassword)
	}
	if cfg.CustomGitHubMirror != "https://keep.gh.example" {
		t.Errorf("panel_* 提交不应影响其它字段: gh=%q", cfg.CustomGitHubMirror)
	}

	// 源列表：url 空串清除 + 自动同步显式开启（此前 url 空时开不动）
	putSettingsBody(t, s, `{"source_list_url": "", "source_list_disabled": false}`)
	if cfg.SourceListURL != "" {
		t.Errorf("source_list_url 空串应清除, got %q", cfg.SourceListURL)
	}
	if cfg.SourceListOff {
		t.Error("source_list_disabled=false 应保存（url 为空时也要能开）")
	}

	// 自动监测开关
	putSettingsBody(t, s, `{"source_auto_care_disabled": true}`)
	if !cfg.SourceAutoCareOff {
		t.Error("source_auto_care_disabled=true 应保存")
	}
	putSettingsBody(t, s, `{"source_auto_care_disabled": false}`)
	if cfg.SourceAutoCareOff {
		t.Error("source_auto_care_disabled=false 应保存")
	}

	// 常规字段不受指针改造影响
	putSettingsBody(t, s, `{"check_interval_hours": 12, "auto_update": true}`)
	if cfg.CheckIntervalHours != 12 || !cfg.AutoUpdate {
		t.Errorf("常规字段应保存: interval=%d auto=%v", cfg.CheckIntervalHours, cfg.AutoUpdate)
	}
}

func putSettingsBodyExpect400(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("期望 400/403, got %d (%s)", rec.Code, rec.Body.String())
	}
	return rec
}
