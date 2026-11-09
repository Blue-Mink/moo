package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 解析 SSE 响应体为事件对象序列（严格校验每条都以 "data: " 开头、以空行结束）。
func parseSSE(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, chunk := range strings.Split(body, "\n\n") {
		chunk = strings.TrimRight(chunk, "\n")
		if chunk == "" {
			continue
		}
		if !strings.HasPrefix(chunk, "data: ") {
			t.Fatalf("非 SSE data 行: %q", chunk)
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(chunk, "data: ")), &ev); err != nil {
			t.Fatalf("事件不是合法 JSON: %v / %q", err, chunk)
		}
		out = append(out, ev)
	}
	return out
}

// stream=1：响应头必须是 event-stream，事件顺序必须是 discover → discover(done) → parse → done。
// 用注入的假 discover + 关闭端口的下载 URL，全程不触网、不真下载。
func TestBuildSSEEventOrder(t *testing.T) {
	c := &companion{httpc: &http.Client{Timeout: 2 * time.Second}}
	rec := httptest.NewRecorder()
	s := startSSE(rec)
	defer s.close()

	if got := rec.Header().Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q", got)
	}

	const name = "demo_1.0.0_x86.fpk"
	fake := func(context.Context) ([]discoveredApp, error) {
		return []discoveredApp{{Name: name, Version: "1.0.0",
			DownloadURL: "http://127.0.0.1:1/" + name}}, nil
	}
	added, skipped, err := c.runBuild(context.Background(), s, "https://example.com/o/r", "o", fake)
	respondBuild(rec, s, added, skipped, err)

	evs := parseSSE(t, rec.Body.String())
	// 进度事件之间会穿插 phase=log 的短日志；单测只校验进度阶段顺序。
	var phases []string
	logCount := 0
	for _, ev := range evs {
		switch ev["phase"] {
		case "log":
			logCount++
		case "ping":
		default:
			phases = append(phases, ev["phase"].(string))
		}
	}
	want := []string{"discover", "discover", "parse", "done"}
	if len(phases) != len(want) {
		t.Fatalf("进度事件 = %v, want %v（全部事件 %v）", phases, want, evs)
	}
	for i, w := range want {
		if phases[i] != w {
			t.Fatalf("第 %d 个进度事件 phase = %v, want %v", i, phases[i], w)
		}
	}
	if logCount == 0 {
		t.Fatal("缺少 phase=log 短日志事件")
	}
	var discoverDone, parseEv, doneEv map[string]any
	for _, ev := range evs {
		switch {
		case ev["phase"] == "discover" && ev["done"] != nil:
			discoverDone = ev
		case ev["phase"] == "parse":
			parseEv = ev
		case ev["phase"] == "done":
			doneEv = ev
		}
	}
	if discoverDone["done"] != float64(1) {
		t.Fatalf("discover.done = %v, want 1", discoverDone["done"])
	}
	if parseEv["total"] != float64(1) || parseEv["app"] != name {
		t.Fatalf("parse 事件 = %v", parseEv)
	}
	if doneEv["count"] != float64(0) {
		t.Fatalf("done.count = %v, want 0", doneEv["count"])
	}
	// skipped 条目形如 "<文件名>: <失败原因>"，只要首项命中该文件即可。
	if sk, _ := doneEv["skipped"].([]any); len(sk) != 1 || !strings.HasPrefix(sk[0].(string), name) {
		t.Fatalf("done.skipped = %v, want [%s]", doneEv["skipped"], name)
	}
	if doneEv["skipped_count"] != float64(1) {
		t.Fatalf("done.skipped_count = %v, want 1", doneEv["skipped_count"])
	}
}

// 失败分支：流式下发 phase=error；非流式保持原有状态码 + JSON 契约。
func TestBuildErrorBothPaths(t *testing.T) {
	empty := func(context.Context) ([]discoveredApp, error) { return nil, nil }

	rec := httptest.NewRecorder()
	s := startSSE(rec)
	defer s.close()
	_, _, err := (&companion{httpc: http.DefaultClient}).runBuild(context.Background(), s, "https://example.com/o/r", "o", empty)
	if err == nil {
		t.Fatal("空仓库应当报错")
	}
	respondBuild(rec, s, nil, nil, err)
	evs := parseSSE(t, rec.Body.String())
	last := evs[len(evs)-1]
	if last["phase"] != "error" || last["msg"] == "" {
		t.Fatalf("尾部事件 = %v, want phase=error", last)
	}

	rec2 := httptest.NewRecorder()
	respondBuild(rec2, nil, nil, nil, err)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("非流式状态码 = %d, want 404", rec2.Code)
	}
	if ct := rec2.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("非流式 Content-Type = %q, want JSON", ct)
	}
}

// 手动「同步更新」：POST /api/sync-now 立即返回 ok 并唤醒同步循环。
func TestHandleSyncNow(t *testing.T) {
	c := &companion{syncWake: make(chan struct{}, 1)}
	rec := httptest.NewRecorder()
	c.handleSyncNow(rec, httptest.NewRequest(http.MethodPost, "/api/sync-now", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("响应体 = %q", rec.Body.String())
	}
	select {
	case <-c.syncWake:
	default:
		t.Fatal("未唤醒同步循环")
	}
}
