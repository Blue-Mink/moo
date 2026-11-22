package api

// P1-2：自更新状态机用例。

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func journalServer(t *testing.T, version string) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	return &Server{Version: version}
}

func TestReconcileSelfUpdateConfirmed(t *testing.T) {
	s := journalServer(t, "0.6.557")
	s.recordSelfUpdateSubmitted("0.6.556", "0.6.557")
	j, changed := s.ReconcileSelfUpdateJournal()
	if !changed || j == nil || j.Status != selfUpdateStatusConfirmed {
		t.Fatalf("应为 confirmed，得到 %+v changed=%v", j, changed)
	}
	if _, changed2 := s.ReconcileSelfUpdateJournal(); changed2 {
		t.Fatalf("终态不应被重复改写")
	}
}

func TestReconcileSelfUpdateFailed(t *testing.T) {
	s := journalServer(t, "0.6.556")
	s.recordSelfUpdateSubmitted("0.6.556", "0.6.557")
	j, changed := s.ReconcileSelfUpdateJournal()
	if !changed || j.Status != selfUpdateStatusFailed {
		t.Fatalf("仍为旧版本应判 failed，得到 %+v", j)
	}
	if j.Detail == "" {
		t.Fatalf("failed 必须带可读原因")
	}
}

func TestReconcileSelfUpdateUnknown(t *testing.T) {
	s := journalServer(t, "0.6.999")
	s.recordSelfUpdateSubmitted("0.6.556", "0.6.557")
	j, _ := s.ReconcileSelfUpdateJournal()
	if j.Status != selfUpdateStatusUnknown {
		t.Fatalf("版本不匹配应判 unknown，得到 %+v", j)
	}
}

func TestSubmitFailedMarksJournal(t *testing.T) {
	s := journalServer(t, "0.6.556")
	s.recordSelfUpdateSubmitted("0.6.556", "0.6.557")
	s.markSelfUpdateSubmitFailed("提交升级失败: busy")
	j := s.readSelfUpdateJournal()
	if j == nil || j.Status != selfUpdateStatusFailed || j.Detail == "" {
		t.Fatalf("提交失败应落 failed: %+v", j)
	}
}

func TestLastSelfUpdateEndpoint(t *testing.T) {
	s := journalServer(t, "0.6.556")
	rec := httptest.NewRecorder()
	s.lastSelfUpdate(rec, httptest.NewRequest("GET", "/api/self-update/last", nil))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if got["status"] != "none" {
		t.Fatalf("无记录应 status=none，得到 %v", got)
	}
	s.recordSelfUpdateSubmitted("0.6.556", "0.6.557")
	rec = httptest.NewRecorder()
	s.lastSelfUpdate(rec, httptest.NewRequest("GET", "/api/self-update/last", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("不是合法 JSON: %v", err)
	}
	if got["to_version"] != "0.6.557" {
		t.Fatalf("应返回 journal: %v", got)
	}
}
