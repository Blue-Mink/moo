package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// 2026-11-26 CARE P1-2/P1-3：/api/sources 必须真的把 empty_streak / fail_streak /
// last_error 暴露出去（修前 empty_streak 只有声明、恒 0，角标永不显示）。
func TestSourceEntryCareFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cfg := &config.Config{}
	m := source.NewManager(cfg)
	// 复核钩子注入假结论（绝不出网）。健康源那一轮不调用它；失败轮的
	// 结论必须可切：只有「确定性永久失效」才允许推进 fail_streak
	//（误判红线 2/4：暂态与「复核不确定」一律不计入），所以第 2 步把
	// verdict 切成 CarePermanentlyDead —— 修前把 uncertain 断言成 1 是
	// 自相矛盾的夹具，与红线 4 直接冲突。
	verdict := source.CareUncertain
	m.CareProbe = func(name, url string) source.CareVerdict { return verdict }
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	s := &Server{Src: m, Cfg: cfg}

	// 1) 健康源：字段恒为 0 / 空
	e := s.sourceEntry(m.Sources()[0])
	if e.EmptyStreak != 0 || e.FailStreak != 0 || e.LastError != "" {
		t.Fatalf("健康源不应带计数/错误, got %+v", e)
	}

	// 2) 确定性永久失败一轮 → fail_streak=1 + last_error 可读
	verdict = source.CarePermanentlyDead
	m.AutoCarePass([]source.SourceStatus{{Name: "tst", Error: "https://raw.example/x/moo.json: HTTP 404"}})
	e = s.sourceEntry(m.Sources()[0])
	if e.FailStreak != 1 {
		t.Fatalf("一轮失败应 fail_streak=1, got %d", e.FailStreak)
	}
	if e.LastError == "" || !strings.Contains(e.LastError, "404") {
		t.Fatalf("last_error 应说明原因（P1-3）, got %q", e.LastError)
	}

	// 2b) 复核「不确定」的失败轮：不计入（红线 4），字段回填不变
	verdict = source.CareUncertain
	m.AutoCarePass([]source.SourceStatus{{Name: "tst", Error: "所有候选地址均失败: x: HTTP 502"}})
	if e2 := s.sourceEntry(m.Sources()[0]); e2.FailStreak != 1 {
		t.Fatalf("复核不确定时不得推进 fail_streak, got %d", e2.FailStreak)
	}

	// 2c) 刷新成功但 0 应用一轮 → empty_streak=1、fail_streak 清零（字段独立）
	m.AutoCarePass([]source.SourceStatus{{Name: "tst", Count: 0}})
	e = s.sourceEntry(m.Sources()[0])
	if e.EmptyStreak != 1 {
		t.Fatalf("成功空源一轮应 empty_streak=1, got %d", e.EmptyStreak)
	}
	if e.FailStreak != 0 {
		t.Fatalf("成功轮应清零 fail_streak, got %d", e.FailStreak)
	}

	// 3) JSON 里确实有这些键（前端角标读 JSON，不读 Go 结构）
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"empty_streak", "fail_streak", "last_error"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("JSON 应含 %s: %s", k, raw)
		}
	}
}
