package api

// 2026-11-26 B-SRC-LISTSYNC 缺陷 1/2：日志与退避的收尾用例
//（成功清退避；每轮恰一行摘要；失败逐条一行）。零外网。

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestListSyncSuccessClearsBackoff 退避到期后重试成功 → 记录整条清除，
// 下一轮起照常参与（不会因为历史失败被永久拉黑）。
func TestListSyncSuccessClearsBackoff(t *testing.T) {
	f := newBackoffFixture(t)
	state := filepath.Join(t.TempDir(), "source_list_sync.json")
	s := f.newServer(t, state)
	if _, err := s.sourceListSyncOnce(t.Context()); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	// 模拟退避到期（等价于 6h 后）
	s.listSyncMu.Lock()
	for k, v := range s.listSync.Fails {
		v.NextAt = 0
		s.listSync.Fails[k] = v
	}
	s.saveListSyncLocked()
	s.listSyncMu.Unlock()

	f.fail.Store(false)
	res, err := s.sourceListSyncOnce(t.Context())
	if err != nil {
		t.Fatalf("重试轮: %v", err)
	}
	if res.Added != 1 || res.Failed != 0 {
		t.Fatalf("恢复后应新增 1 源, got %+v", res)
	}
	s.listSyncMu.Lock()
	n := len(s.listSync.Fails)
	s.listSyncMu.Unlock()
	if n != 0 {
		t.Fatalf("成功应清除退避记录, 剩余 %d 条", n)
	}
}

// captureLog 捕获一段 log 输出（包内既有单测同款做法）。
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	fn()
	return buf.String()
}

// TestListSyncSummaryExactlyOneLine 每轮恰一行摘要（成功轮无「跳过」行）。
func TestListSyncSummaryExactlyOneLine(t *testing.T) {
	f := newBackoffFixture(t)
	f.fail.Store(false)
	s := f.newServer(t, "")
	out := captureLog(t, func() {
		if _, err := s.sourceListSyncOnce(t.Context()); err != nil {
			t.Errorf("sourceListSyncOnce: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Fatalf("一轮应恰 1 行日志, got %d: %q", len(lines), out)
	}
	for _, want := range []string{"[list-sync] 本轮完成", "列表 1 条", "新增 1", "失败 0"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("摘要行应含 %q, got %q", want, lines[0])
		}
	}
}

// TestListSyncFailureLogsSkipLine 失败轮：1 行摘要 + 逐条「跳过 <URL>：<原因>」。
func TestListSyncFailureLogsSkipLine(t *testing.T) {
	f := newBackoffFixture(t)
	s := f.newServer(t, "")
	out := captureLog(t, func() {
		if _, err := s.sourceListSyncOnce(t.Context()); err != nil {
			t.Errorf("sourceListSyncOnce: %v", err)
		}
	})
	lines := nonEmptyLines(out)
	if len(lines) != 2 {
		t.Fatalf("失败轮应 1 摘要 + 1 跳过 = 2 行, got %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "失败 1") {
		t.Errorf("摘要行应记失败 1, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "[list-sync] 跳过 "+f.srv.URL+"/src：") {
		t.Errorf("应逐条记「跳过 <URL>：<原因>」, got %q", lines[1])
	}
	if got := atomic.LoadInt32(&f.hits); got == 0 {
		t.Error("失败轮应真抓死链（零请求说明没走到加源逻辑）")
	}
}

// nonEmptyLines 拆分日志行（去掉尾部空行）。
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestListSourcesExposeLastSync /api/sources 必须带「上次同步时刻 + 上轮新增数」
// （缺陷 1 的对外接口；前端据它渲染「上次同步 X 前 · 新增 N」）。
func TestListSourcesExposeLastSync(t *testing.T) {
	f := newBackoffFixture(t)
	f.fail.Store(false)
	s := f.newServer(t, "")
	if _, err := s.sourceListSyncOnce(t.Context()); err != nil {
		t.Fatalf("sourceListSyncOnce: %v", err)
	}
	rec := httptest.NewRecorder()
	s.listSources(rec, httptest.NewRequest(http.MethodGet, "/api/sources", nil).WithContext(t.Context()))
	if rec.Code != http.StatusOK {
		t.Fatalf("listSources 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Sources []SourceEntry `json:"sources"`
		LastAt  string        `json:"last_list_sync_at"`
		Added   int           `json:"last_list_sync_added"`
		Fetched int           `json:"last_list_sync_fetched"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 /api/sources: %v", err)
	}
	if out.LastAt == "" {
		t.Fatal("/api/sources 应下发 last_list_sync_at")
	}
	if out.Added != 1 || out.Fetched != 1 {
		t.Fatalf("应下发上轮计数 added=1 fetched=1, got added=%d fetched=%d", out.Added, out.Fetched)
	}
	if len(out.Sources) != 1 {
		t.Fatalf("应 1 个源, got %d", len(out.Sources))
	}
}
