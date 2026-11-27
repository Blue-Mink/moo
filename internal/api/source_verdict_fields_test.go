package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// 2026-11-27 FIXPLAN 盲点 #3 · C2：/api/sources 判定可观测字段的对外契约。
// 只读信号：不得改变 enabled / disabled_by / fail_streak 的任何语义。

func jsonMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func entryFor(t *testing.T, s *Server, m *source.Manager, name string) SourceEntry {
	t.Helper()
	for _, st := range m.Sources() {
		if st.Name == name {
			return s.sourceEntry(st)
		}
	}
	t.Fatalf("源 %s 不在 Sources() 里", name)
	return SourceEntry{}
}

func verdictFixture(t *testing.T) (*Server, *source.Manager, string, string) {
	t.Helper()
	mux := http.NewServeMux()
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	}
	mux.HandleFunc("/fnpack.json", h)
	// plain 用另一个路径：同地址会被「同源去重」拒掉。
	mux.HandleFunc("/plain/fnpack.json", h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cfg := &config.Config{}
	m := source.NewManager(cfg)
	t.Cleanup(m.WaitCareFlush)
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource tst: %v", err)
	}
	if _, err := m.AddSource("plain", ts.URL+"/plain/fnpack.json"); err != nil {
		t.Fatalf("AddSource plain: %v", err)
	}
	return &Server{Src: m, Cfg: cfg}, m, "tst", "plain"
}

// ① 一轮 alive → last_verdict:"alive" + verdict_at ≈ now；③ 无记录源两字段都不出现。
func TestSourceEntryVerdictAlive(t *testing.T) {
	s, m, name, plainName := verdictFixture(t)
	m.CareProbe = func(n, u string) source.CareVerdict { return source.CareAlive }

	before := time.Now().Unix()
	m.AutoCarePass([]source.SourceStatus{{Name: name, Error: "https://raw.example/x/moo.json: HTTP 502"}})

	e := entryFor(t, s, m, name)
	got := jsonMap(t, e)
	if got["last_verdict"] != "alive" {
		t.Fatalf("一轮 alive 后应带 last_verdict=alive, got %v", got["last_verdict"])
	}
	at, ok := got["verdict_at"].(float64)
	if !ok || int64(at) < before || int64(at) > time.Now().Unix()+2 {
		t.Fatalf("verdict_at 应 ≈ now(unix 秒), got %v", got["verdict_at"])
	}
	// ⑤ 守卫：只是多了字段，语义一个字没改。
	if !e.Enabled || e.DisabledBy != "" || e.FailStreak != 0 {
		t.Fatalf("alive 轮不得改变启用/停用来源/失败计数, got enabled=%v disabled_by=%q fail_streak=%d",
			e.Enabled, e.DisabledBy, e.FailStreak)
	}

	// ③ 从未复核过的源：JSON 里不应出现这两个键（omitempty）。
	plain := jsonMap(t, entryFor(t, s, m, plainName))
	for _, k := range []string{"last_verdict", "verdict_at"} {
		if _, ok := plain[k]; ok {
			t.Fatalf("无记录源不得出现 %s: %v", k, plain)
		}
	}
}

// ② 确定性永久失效 → last_verdict:"dead"，且 fail_streak 仍按既有规则推进。
func TestSourceEntryVerdictDead(t *testing.T) {
	s, m, name, _ := verdictFixture(t)
	m.CareProbe = func(n, u string) source.CareVerdict { return source.CarePermanentlyDead }

	m.AutoCarePass([]source.SourceStatus{{Name: name, Error: "https://raw.example/x/moo.json: HTTP 404"}})

	e := entryFor(t, s, m, name)
	got := jsonMap(t, e)
	if got["last_verdict"] != "dead" {
		t.Fatalf("确定性失效后应带 last_verdict=dead, got %v", got["last_verdict"])
	}
	if e.FailStreak != 1 || !e.Enabled {
		t.Fatalf("一轮 dead 只推进 fail_streak=1、不得停用（5 轮 + 24h 双门槛）, got %d enabled=%v",
			e.FailStreak, e.Enabled)
	}
}
