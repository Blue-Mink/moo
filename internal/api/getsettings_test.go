package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/source"
)

func settingsPutRaw(t *testing.T, s *Server, body string) int {
	t.Helper()
	r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)).WithContext(t.Context())
	rec := httptest.NewRecorder()
	s.putSettings(rec, r)
	return rec.Code
}

func settingsGetStruct(t *testing.T, s *Server) Settings {
	t.Helper()
	var got Settings
	if err := json.Unmarshal(doSettingsGet(t, s).Body.Bytes(), &got); err != nil {
		t.Fatalf("GET /api/settings 响应解析失败: %v", err)
	}
	return got
}

// TestGetSettingsExposesCustomMirrors 0.6.325 H1(F2/F3)：
// custom_github_mirror / custom_docker_mirror / source_list_url 三个
// PUT 可写字段必须随 GET 透出——此前 GET 恒缺键，设置页水合成空串后
// 下一次全量保存把 config 里的自定义镜像静默抹掉（数据丢失级）。
func TestGetSettingsExposesCustomMirrors(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}, Pipe: &pipeline.Pipeline{}}

	// PUT 写入 → GET 必须原样透出
	putSettingsBody(t, s, `{"custom_github_mirror":"https://x.test","custom_docker_mirror":"https://dk.test","source_list_url":"https://list.test"}`)
	got := settingsGetStruct(t, s)
	if got.CustomGitHubMirror != "https://x.test" {
		t.Errorf("GET 应透出 custom_github_mirror, got %q", got.CustomGitHubMirror)
	}
	if got.CustomDockerMirror != "https://dk.test" {
		t.Errorf("GET 应透出 custom_docker_mirror, got %q", got.CustomDockerMirror)
	}
	if got.SourceListURL != "https://list.test" {
		t.Errorf("GET 应透出 source_list_url, got %q", got.SourceListURL)
	}

	// 前端全量保存模拟（handleSave 每轮无条件带上这三字段）：
	// 水合值回传后 config 必须不变——这正是修复前被抹掉的场景。
	putSettingsBody(t, s, `{"check_interval_hours":24,"custom_github_mirror":"https://x.test","custom_docker_mirror":"https://dk.test","source_list_url":"https://list.test"}`)
	if s.Cfg.CustomGitHubMirror != "https://x.test" || s.Cfg.CustomDockerMirror != "https://dk.test" || s.Cfg.SourceListURL != "https://list.test" {
		t.Errorf("全量保存回传同值后 config 不应变: gh=%q dk=%q list=%q",
			s.Cfg.CustomGitHubMirror, s.Cfg.CustomDockerMirror, s.Cfg.SourceListURL)
	}

	// 未设置时 GET 键可缺席（omitempty = 前端水合成空串，与「未设置」一致）
	s2 := &Server{Cfg: &config.Config{}, Src: source.NewManager(&config.Config{}), Pipe: &pipeline.Pipeline{}}
	raw := doSettingsGet(t, s2).Body.String()
	if strings.Contains(raw, "custom_github_mirror") {
		t.Error("未设置时 custom_github_mirror 应保持 omitempty 缺席")
	}
}

// TestPutSettingsIntervalPointerSemantics 0.6.325 H5(F4)：
// check_interval_hours / backup_interval_days / cache_clean_every_days
// 指针语义——nil=未提交不改动；0=清除回各自默认（24h/7天/1天，GET
// 透出生效值）；越界整单拒绝（check≤168；天数负数拒绝、>30 沿用既有
// 钳制）。
func TestPutSettingsIntervalPointerSemantics(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{
		CheckIntervalHours:  99,
		BackupIntervalDays:  15,
		CacheCleanEveryDays: 5,
	}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}

	// nil（未提交）不改动
	putSettingsBody(t, s, `{"log_lines":200}`)
	if cfg.CheckIntervalHours != 99 || cfg.BackupIntervalDays != 15 || cfg.CacheCleanEveryDays != 5 {
		t.Fatalf("未提交三字段不应改动: %d/%d/%d", cfg.CheckIntervalHours, cfg.BackupIntervalDays, cfg.CacheCleanEveryDays)
	}

	// 0 = 清除回默认（config 落 0，GET 透出生效值 24/7/1）
	putSettingsBody(t, s, `{"check_interval_hours":0,"backup_interval_days":0,"cache_clean_every_days":0}`)
	if cfg.CheckIntervalHours != 0 || cfg.BackupIntervalDays != 0 || cfg.CacheCleanEveryDays != 0 {
		t.Fatalf("显式 0 应落盘: %d/%d/%d", cfg.CheckIntervalHours, cfg.BackupIntervalDays, cfg.CacheCleanEveryDays)
	}
	got := settingsGetStruct(t, s)
	if got.CheckIntervalHours != 24 || got.BackupIntervalDays != 7 || got.CacheCleanEveryDays != 1 {
		t.Errorf("GET 应透出生效值 24/7/1, got %d/%d/%d", got.CheckIntervalHours, got.BackupIntervalDays, got.CacheCleanEveryDays)
	}

	// 正常值写入 + 上界钳制沿用（>30 → 30）
	putSettingsBody(t, s, `{"check_interval_hours":48,"backup_interval_days":60,"cache_clean_every_days":60}`)
	if cfg.CheckIntervalHours != 48 || cfg.BackupIntervalDays != 30 || cfg.CacheCleanEveryDays != 30 {
		t.Errorf("写入/钳制异常: %d/%d/%d", cfg.CheckIntervalHours, cfg.BackupIntervalDays, cfg.CacheCleanEveryDays)
	}

	// 越界整单拒绝（负数与 check>168），且先于此的合法字段也不许半途落盘——
	// 校验发生在任何写入之前需逐字段评估，本实现按字段顺序即拒即返，
	// 断言聚焦「拒绝 + 目标字段不变」。
	if code := settingsPutRaw(t, s, `{"check_interval_hours":169}`); code != 400 {
		t.Errorf("check_interval_hours=169 应 400, got %d", code)
	}
	if code := settingsPutRaw(t, s, `{"check_interval_hours":-1}`); code != 400 {
		t.Errorf("check_interval_hours=-1 应 400, got %d", code)
	}
	if code := settingsPutRaw(t, s, `{"backup_interval_days":-2}`); code != 400 {
		t.Errorf("backup_interval_days=-2 应 400, got %d", code)
	}
	if code := settingsPutRaw(t, s, `{"cache_clean_every_days":-1}`); code != 400 {
		t.Errorf("cache_clean_every_days=-1 应 400, got %d", code)
	}
	if cfg.CheckIntervalHours != 48 || cfg.BackupIntervalDays != 30 || cfg.CacheCleanEveryDays != 30 {
		t.Errorf("被拒请求不应改动三字段: %d/%d/%d", cfg.CheckIntervalHours, cfg.BackupIntervalDays, cfg.CacheCleanEveryDays)
	}
}

// TestDockerMirrorOptionsNoRegistryCyou 0.6.325 C12：已下线的
// registry-cyou 必须从 Docker 镜像加速源候选中移除；其余关键项
// （auto/direct/custom）仍在。候选列表唯一来源=后端 config 包，
// 前端从 API 取，无硬编码副本。
func TestDockerMirrorOptionsNoRegistryCyou(t *testing.T) {
	keys := map[string]bool{}
	for _, o := range config.DockerMirrorOptions() {
		keys[o.Key] = true
	}
	if keys["registry-cyou"] {
		t.Error("DockerMirrorOptions 不应再包含 registry-cyou（TLS 证书失效恒不可用）")
	}
	for _, k := range []string{"auto", "direct", "custom", "daocloud", "kspeeder"} {
		if !keys[k] {
			t.Errorf("DockerMirrorOptions 应仍包含 %q", k)
		}
	}
	if len(config.DockerMirrorOptions()) != 11 {
		t.Errorf("删除 registry-cyou 后应为 11 项, got %d", len(config.DockerMirrorOptions()))
	}
}

// TestGetSettingsNormalizesDeadDockerMirror 0.6.325 C12：老配置里残留的
// 已下线 key（registry-cyou）在 GET /api/settings 必须回读为 "auto"，
// 且 config 原值不被改写（读取侧归一，不主动改用户配置）。
func TestGetSettingsNormalizesDeadDockerMirror(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{DockerMirror: "registry-cyou"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}

	got := settingsGetStruct(t, s)
	if got.DockerMirror != "auto" {
		t.Errorf("已下线 key 应回读 auto, got %q", got.DockerMirror)
	}
	if cfg.DockerMirror != "registry-cyou" {
		t.Errorf("GET 不应改写 config 原值, got %q", cfg.DockerMirror)
	}

	// 前端全量保存回传（水合值 = auto）不得把死 key 重新落盘
	putSettingsBody(t, s, `{"docker_mirror":"auto"}`)
	if cfg.DockerMirror != "auto" {
		t.Errorf("显式保存 auto 应落盘, got %q", cfg.DockerMirror)
	}
}

// TestGetSettingsDockerMirrorDefaults 0.6.325 C12 回归：空值仍归一 auto；
// 合法值原样往返（不被归一逻辑误伤）。
func TestGetSettingsDockerMirrorDefaults(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())

	empty := &Server{Cfg: &config.Config{}, Src: source.NewManager(&config.Config{}), Pipe: &pipeline.Pipeline{}}
	if got := settingsGetStruct(t, empty); got.DockerMirror != "auto" {
		t.Errorf("空值应归一 auto, got %q", got.DockerMirror)
	}

	cfg := &config.Config{DockerMirror: "ratdev"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}
	if got := settingsGetStruct(t, s); got.DockerMirror != "ratdev" {
		t.Errorf("合法值应原样透出, got %q", got.DockerMirror)
	}
}
