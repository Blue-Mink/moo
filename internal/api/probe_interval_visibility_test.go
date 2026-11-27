package api

// 2026-11-26 B-MIRROR-PROBE-VISIBILITY P1-3：测速间隔参数不再静默回落。
//
// 反向验证目标：
//   - 删掉 putSettings 里的 probeFallback 计算 → 响应无 probe_interval_fallback → FAIL；
//   - 删掉 GET 的 gh/dk_probe_interval_minutes + probe_interval_fallback → FAIL。
// 门控阈值/降频策略本批不改（只做可见性），故这里只断言「参数 → 生效值」的可观测性。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/source"
)

func newProbeSettingsServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	return &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}
}

// readSettings GET /api/settings 解成 Settings。
func readSettings(t *testing.T, s *Server) Settings {
	t.Helper()
	rec := httptest.NewRecorder()
	s.getSettings(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("getSettings 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var set Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &set); err != nil {
		t.Fatalf("解析 settings: %v", err)
	}
	return set
}

// TestProbeIntervalZeroFallbackVisible 0h0m（未设置）→ 实际按 5 分钟跑，
// 保存响应与 GET 都必须把这次回落显式告诉前端（不再静默）。
func TestProbeIntervalZeroFallbackVisible(t *testing.T) {
	s := newProbeSettingsServer(t)
	rec := putSettingsBody(t, s, `{"gh_probe_hours":0,"gh_probe_minutes":0}`)
	var out struct {
		OK       bool           `json:"ok"`
		Fallback map[string]int `json:"probe_interval_fallback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 PUT 响应: %v", err)
	}
	if !out.OK {
		t.Fatalf("PUT 应 ok, got %+v", out)
	}
	if out.Fallback["gh"] != config.DefaultProbeIntervalMinutes {
		t.Fatalf("0h0m 应回传「已回落 %d 分钟」, got %+v", config.DefaultProbeIntervalMinutes, out.Fallback)
	}

	set := readSettings(t, s)
	if set.GhProbeIntervalMinutes != config.DefaultProbeIntervalMinutes {
		t.Fatalf("GET 应下发生效值 %d, got %d", config.DefaultProbeIntervalMinutes, set.GhProbeIntervalMinutes)
	}
	if !set.GhProbeFallback {
		t.Fatal("GET gh_probe_fallback 应为 true（0h0m ≠ 5 分钟）")
	}
	if set.ProbeIntervalDefault != config.DefaultProbeIntervalMinutes {
		t.Fatalf("GET 应下发默认值 %d, got %d", config.DefaultProbeIntervalMinutes, set.ProbeIntervalDefault)
	}
}

// TestProbeIntervalValidNoFallback 合法值：生效值 == 提交值，且不带回落提示。
func TestProbeIntervalValidNoFallback(t *testing.T) {
	s := newProbeSettingsServer(t)
	rec := putSettingsBody(t, s, `{"dk_probe_hours":0,"dk_probe_minutes":17}`)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 PUT 响应: %v", err)
	}
	if _, has := out["probe_interval_fallback"]; has {
		t.Fatalf("合法值不应带回落提示, got %v", out)
	}
	if set := readSettings(t, s); set.DkProbeIntervalMinutes != 17 || set.DkProbeFallback {
		t.Fatalf("GET 应生效 17 分钟且无回落, got %d fallback=%v",
			set.DkProbeIntervalMinutes, set.DkProbeFallback)
	}
}

// TestProbeIntervalOutOfRangeRejected 越界（99 小时）整单拒绝，绝不静默入库。
func TestProbeIntervalOutOfRangeRejected(t *testing.T) {
	s := newProbeSettingsServer(t)
	putSettingsBodyExpect400(t, s, `{"gh_probe_hours":99,"gh_probe_minutes":0}`)
	if s.Cfg.GhProbeHours == 99 {
		t.Fatal("越界值不得入库")
	}
}
