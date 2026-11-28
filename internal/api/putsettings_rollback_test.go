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
	"reflect"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/netx"
	"moo/internal/pipeline"
	"moo/internal/secret"
	"moo/internal/source"
	"moo/internal/task"
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

// ── 2026-11-28（BACKLOG B-PUTSETTINGS-HALFCOMMIT）──
//
// 点位 1 / 点位 2：putSettings 中段就先写 cfg().ProxyEnabled/ProxyURL 并
// netx.SetProxy（改内存即生效），并在中段调 applyDownloadDir（MkdirAll + 搬缓存 +
// Tasks/Pipe/cfg 全改），而后面还有 8 处 400 出口不回滚 →
// 「代理已生效 / 缓存已被搬走，config.json 未变」，重启后设置静默回退。
// 本组用例的口径：被 400 拒绝的请求不得留下任何可观察副作用；
// 合法请求则必须仍然即时生效（不许为了修半提交而牺牲「无需重启」）。

// writeFpkCache 在 dir 下写一个假 FPK 缓存文件（下载目录迁移用例的夹具）。
func writeFpkCache(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("写测试 FPK 失败: %v", err)
	}
}

// snapshotDir 返回目录下「文件名 → 内容」的快照（断言缓存清单/内容不变）。
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s 失败: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("读 %s 失败: %v", e.Name(), rerr)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// TestPutSettingsProxyHalfCommitRollback（点位 1）：proxy_url 合法 +
// 后续字段非法（log_lines=0）→ 400，cfg 与 netx 生效代理都必须是旧值。
func TestPutSettingsProxyHalfCommitRollback(t *testing.T) {
	const oldURL = "http://127.0.0.1:7890"
	const newURL = "http://127.0.0.1:7891"
	prevEffective := netx.ProxyURL()
	netx.SetProxy(oldURL)
	t.Cleanup(func() { netx.SetProxy(prevEffective) })

	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{ProxyEnabled: true, ProxyURL: oldURL}
	s := &Server{Cfg: cfg, Pipe: &pipeline.Pipeline{}}

	rec := putSettingsRaw(t, s, `{"proxy_enabled":true,"proxy_url":"`+newURL+`","log_lines":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 log_lines 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if !cfg.ProxyEnabled || cfg.ProxyURL != oldURL {
		t.Errorf("被拒绝的请求改了内存代理配置: enabled=%v url=%q", cfg.ProxyEnabled, cfg.ProxyURL)
	}
	if got := netx.ProxyURL(); got != oldURL {
		t.Errorf("netx 生效代理未回滚: got %q want %q（400 时代理已在内存生效 = 半提交）", got, oldURL)
	}
	if raw, err := os.ReadFile(config.Path(os.Getenv("MOO_DATA"))); err == nil {
		if strings.Contains(string(raw), newURL) {
			t.Errorf("被拒绝的请求把新代理写进了 config.json: %s", raw)
		}
	}
}

// TestPutSettingsDownloadDirHalfCommitRollback（点位 2）：download_dir 合法 +
// 后续字段非法 → 400，旧目录缓存清单/内容不变，cfg/Pipe/Tasks 仍是旧目录。
func TestPutSettingsDownloadDirHalfCommitRollback(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	oldDir := t.TempDir()
	writeFpkCache(t, oldDir, "demo_1.0.0_all.fpk", "FPK-PAYLOAD-A")
	before := snapshotDir(t, oldDir)

	cfg := &config.Config{DownloadDir: oldDir}
	tm, err := task.NewManager(oldDir, nil)
	if err != nil {
		t.Fatalf("建任务管理器失败: %v", err)
	}
	s := &Server{Cfg: cfg, Pipe: pipeline.New(source.NewManager(cfg), t.TempDir(), oldDir), Tasks: tm}
	t.Cleanup(func() { fpkLocalInstance.SetExtraDirs(nil) })

	target := defaultDownloadDir() // $MOO_DATA/downloads：白名单内的合法目标
	body, _ := json.Marshal(map[string]any{"download_dir": target, "log_lines": 0})
	rec := putSettingsRaw(t, s, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 log_lines 应 400，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if after := snapshotDir(t, oldDir); !reflect.DeepEqual(before, after) {
		t.Errorf("被拒绝的请求搬走了旧目录缓存: before=%v after=%v", before, after)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		t.Errorf("被拒绝的请求把缓存搬到了新目录 %s（%d 项）", target, len(entries))
	}
	if cfg.DownloadDir != oldDir {
		t.Errorf("被拒绝的请求改了 cfg.DownloadDir: %q", cfg.DownloadDir)
	}
	if s.Pipe.Downloads != oldDir || tm.DownloadDir() != oldDir {
		t.Errorf("运行态目录未保持旧值: pipe=%q tasks=%q", s.Pipe.Downloads, tm.DownloadDir())
	}
}

// TestPutSettingsProxyAndDownloadDirApplyOnSuccess：回归 —— 合法请求必须仍然
// 「即时生效」：代理切到新值（netx 同步），缓存搬到新目录且 Tasks/Pipe/cfg 跟上。
func TestPutSettingsProxyAndDownloadDirApplyOnSuccess(t *testing.T) {
	const newURL = "http://127.0.0.1:7892"
	prevEffective := netx.ProxyURL()
	t.Cleanup(func() { netx.SetProxy(prevEffective) })

	t.Setenv("MOO_DATA", t.TempDir())
	oldDir := t.TempDir()
	writeFpkCache(t, oldDir, "demo_1.0.0_all.fpk", "FPK-PAYLOAD-B")

	cfg := &config.Config{DownloadDir: oldDir}
	tm, err := task.NewManager(oldDir, nil)
	if err != nil {
		t.Fatalf("建任务管理器失败: %v", err)
	}
	s := &Server{Cfg: cfg, Pipe: pipeline.New(source.NewManager(cfg), t.TempDir(), oldDir), Tasks: tm}
	t.Cleanup(func() { fpkLocalInstance.SetExtraDirs(nil) })

	target := defaultDownloadDir()
	body, _ := json.Marshal(map[string]any{
		"proxy_enabled": true, "proxy_url": newURL, "download_dir": target,
	})
	rec := putSettingsRaw(t, s, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("合法请求应 200，实际 %d body=%s", rec.Code, rec.Body.String())
	}
	if got := netx.ProxyURL(); got != newURL {
		t.Errorf("合法保存后代理未即时生效: netx=%q want %q", got, newURL)
	}
	if !cfg.ProxyEnabled || cfg.ProxyURL != newURL {
		t.Errorf("代理未写入内存配置: enabled=%v url=%q", cfg.ProxyEnabled, cfg.ProxyURL)
	}
	if cfg.DownloadDir != target || s.Pipe.Downloads != target || tm.DownloadDir() != target {
		t.Errorf("下载目录未即时生效: cfg=%q pipe=%q tasks=%q want=%q",
			cfg.DownloadDir, s.Pipe.Downloads, tm.DownloadDir(), target)
	}
	moved, err := os.ReadFile(filepath.Join(target, "demo_1.0.0_all.fpk"))
	if err != nil || string(moved) != "FPK-PAYLOAD-B" {
		t.Errorf("缓存未迁移到新目录: err=%v content=%q", err, moved)
	}
	if left, _ := os.ReadDir(oldDir); len(left) != 0 {
		t.Errorf("旧目录仍残留 %d 个文件", len(left))
	}
}
