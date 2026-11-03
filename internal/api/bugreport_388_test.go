package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// TestBugReportURLDetailOnly_388：源声明的 bug_report_url 只在详情下发。
//
// 背景：0.6.388 之前该字段已解析进 source.App（fndepot.go translateEntry），
// 但 AppInfo 没有对应字段 → /api/apps 与 /api/apps/{key} 都不下发，详情页
// 因此没有「问题反馈」入口。补齐时刻意与 homepage 同口径：列表 3000 张卡
// 每张多 ~50B ≈ +150KB，故列表瘦身剥离、仅详情保留。
//
// 反向验证：删掉 store_routes.go 里的 `a.BugReportURL = ""` → 本用例第一段必红。
func TestBugReportURLDetailOnly_388(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示",` +
			`"download_url":"https://example.com/demo.fpk",` +
			`"bug_report_url":"https://example.com/demo/issues"}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := &config.Config{}
	m := source.NewManager(cfg)
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	s := &Server{Src: m, Cfg: cfg}

	listMux := http.NewServeMux()
	listMux.HandleFunc("GET /api/apps", s.appsWithEtag)
	recL := httptest.NewRecorder()
	listMux.ServeHTTP(recL, httptest.NewRequest("GET", "/api/apps", nil).WithContext(t.Context()))
	if recL.Code != http.StatusOK {
		t.Fatalf("list 应 200, got %d", recL.Code)
	}
	if strings.Contains(recL.Body.String(), "bug_report_url") {
		t.Error("列表载荷不得下发 bug_report_url（应随列表瘦身剥离，详情按 key 补齐）")
	}

	detailMux := http.NewServeMux()
	detailMux.HandleFunc("GET /api/apps/{key}", s.appDetail)
	recD := httptest.NewRecorder()
	detailMux.ServeHTTP(recD, httptest.NewRequest("GET", "/api/apps/demo", nil).WithContext(t.Context()))
	if recD.Code != http.StatusOK {
		t.Fatalf("detail 应 200, got %d", recD.Code)
	}
	if !strings.Contains(recD.Body.String(), `"bug_report_url":"https://example.com/demo/issues"`) {
		t.Errorf("详情应带 bug_report_url，实际：%s", recD.Body.String())
	}
}
