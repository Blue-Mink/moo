package api

// 2026-11-27（F9 / 审计 N3）：putSettings 的半提交回归。
//
// 审计原话：先 SetEncryption（改磁盘会话文件）再校验其它字段，后续 18 处
// 400 出口不回滚 → 界面「开」、config「关」，重启后设置静默回退。
// 本文件的断言口径：**被 400 拒绝的请求不得留下任何可观察副作用**——
// 内存配置、config.json、会话文件形态三者都必须停在提交前。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/secret"
	"moo/internal/source"
)

// putSettingsRaw 发一个 PUT /api/settings 并返回原始响应（不预设状态码）。
func putSettingsRaw(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body)).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	return rec
}

// TestPutSettingsEncryptRejectedByLaterFieldIsNotHalfCommitted：
// 同一请求里既切会话加密、又带非法 catalog_language（旧前端缓存表单）→ 400，
// 且：①内存开关仍是旧值 ②会话文件字节不变（未被按新形态重写）
// ③config.json 里没有 official_session_encrypt=true。
func TestPutSettingsEncryptRejectedByLaterFieldIsNotHalfCommitted(t *testing.T) {
	s, _ := newSessionEncryptServer(t, true, sessionFixture())

	sessionFile := s.Official.SessionFile()
	before, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("读会话夹具失败: %v", err)
	}
	if strings.Contains(string(before), secret.Prefix) {
		t.Fatal("前置条件：夹具会话文件应是明文")
	}
	cfgPath := config.Path(os.Getenv("MOO_DATA"))

	rec := putSettingsRaw(t, s, `{"official_session_encrypt":true,"catalog_language":"fr-FR"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 catalog_language 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}

	// ① 内存：开关与运行态都不得被改
	if s.Cfg.OfficialSessionEncrypt || s.Official.EncryptionEnabled() {
		t.Errorf("被拒绝的请求不得改内存开关: cfg=%v runtime=%v",
			s.Cfg.OfficialSessionEncrypt, s.Official.EncryptionEnabled())
	}
	// GET /api/settings 也必须回读到旧值
	var got Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/settings 解析失败: %v", err)
	}
	if got.OfficialSessionEncrypt {
		t.Error("GET /api/settings 不得显示「开」（配置未落盘，界面与磁盘必须一致）")
	}
	// ② 会话文件形态未变（这是审计里的「不可撤销磁盘副作用」）
	after, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("复读会话文件失败: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("被拒绝的请求改写了会话文件（半提交）：before=%q after=%q", before, after)
	}
	// ③ 配置未落盘该标志
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if v, ok := m["official_session_encrypt"]; ok && v == true {
				t.Errorf("config.json 被写入了 official_session_encrypt=true（但请求 400）: %s", raw)
			}
		}
	}
}

// TestPutSettingsTokenRolledBackOnLaterValidation：GitHub token 是「改内存即
// 生效」的第二处副作用，被后续 400 拒绝时同样必须回滚（含查询器运行态）。
func TestPutSettingsTokenRolledBackOnLaterValidation(t *testing.T) {
	s, _ := newSessionEncryptServer(t, true, "")
	s.Forks = source.NewGHForks(filepath.Join(t.TempDir(), "forks.json"))

	rec := putSettingsRaw(t, s, `{"github_token":"ghp_newtoken","catalog_language":"fr-FR"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 catalog_language 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if s.Cfg.GithubToken != "" {
		t.Errorf("被拒绝的请求不得改内存 token: %q", s.Cfg.GithubToken)
	}
	if s.Forks.HasToken() {
		t.Error("Forks 查询器的 token 未回滚（运行态已带 token，配置却是空）")
	}
}
