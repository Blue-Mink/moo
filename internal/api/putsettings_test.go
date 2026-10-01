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
func TestPutSettingsClearSemantics(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{
		CustomGitHubMirror: "https://old.gh.example",
		PanelEnabled:       true,
		PanelUsername:      "fnos",
		PanelBaseURL:       "http://127.0.0.1:9666",
		SourceListURL:      "https://old.list.example",
		SourceListOff:      true,
		Mirror:             "gh-proxy",
	}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	// 空串清除三个可清空字段
	putSettingsBody(t, s, `{"custom_github_mirror": "", "panel_base_url": "", "panel_username": ""}`)
	if cfg.CustomGitHubMirror != "" {
		t.Errorf("custom_github_mirror 空串应清除, got %q", cfg.CustomGitHubMirror)
	}
	if cfg.PanelBaseURL != "" {
		t.Errorf("panel_base_url 空串应清除, got %q", cfg.PanelBaseURL)
	}
	if cfg.PanelUsername != "" {
		t.Errorf("panel_username 空串应清除, got %q", cfg.PanelUsername)
	}
	// 未提交字段不改动
	if !cfg.PanelEnabled {
		t.Error("未提交 panel_enabled 不应改动")
	}
	if cfg.Mirror != "gh-proxy" {
		t.Error("未提交 mirror 不应改动")
	}

	// 空串清除后回写新值
	// 面板地址仅接受本地回环（2026-09-27 安全审核：防口令外泄）
	putSettingsBody(t, s, `{"custom_github_mirror": "https://new.gh.example", "panel_base_url": "http://127.0.0.1:9667"}`)
	if cfg.CustomGitHubMirror != "https://new.gh.example" || cfg.PanelBaseURL != "http://127.0.0.1:9667" {
		t.Errorf("新值应写入: gh=%q base=%q", cfg.CustomGitHubMirror, cfg.PanelBaseURL)
	}

	// 面板开关显式关闭（其它 panel 字段均未提交）
	putSettingsBody(t, s, `{"panel_enabled": false}`)
	if cfg.PanelEnabled {
		t.Error("panel_enabled=false 应保存")
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

	// 安全回归（2026-09-27 审核）：外网面板地址必须拒绝（面板口令
	// 会进 WS 登录帧，目标限定本机回环）
	cfg.PanelBaseURL = ""
	rec := putSettingsBodyExpect400(t, s, `{"panel_base_url": "http://evil.example:8080"}`)
	if cfg.PanelBaseURL != "" {
		t.Errorf("外网面板地址应被拒且不留痕, got %q", cfg.PanelBaseURL)
	}
	_ = rec
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
