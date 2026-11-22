package api

// SSE_RESUME（B 方案）用例：GET /api/operations/{id}/events 可续传跟随，
// sseRunOp 的每条事件都带 opID。历史现状用例见 sse_client_disconnect_test.go。

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/operation"
)

// newFollowServer 起一个只挂续传路由的测试服务。
func newFollowServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s := &Server{Ops: operation.NewQueue()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/operations/{id}/events", s.followOperationSSE)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

// getStream 发一个带超时的 GET 并返回 body（测试收尾时自动关闭）。
func getStream(t *testing.T, ctx context.Context, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// readEvent 读一条 data: 事件并解成 map。
func readEvent(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("事件不是合法 JSON: %v (%s)", err, line)
		}
		return ev
	}
	t.Fatalf("流已结束但没读到事件（err=%v）", sc.Err())
	return nil
}

func waitOpDone(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v := s.Ops.Status(id); v != nil && v.State == operation.StateDone {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("操作 %s 未在 3s 内完成: %+v", id, s.Ops.Status(id))
}

// 终态副作用按 opID 幂等：断线后续传的第二个跟随连接不得重复执行
// （否则本机安装计数会重复累加）。
func TestFinalizeOpDedupesByOpID(t *testing.T) {
	s := &Server{Ops: operation.NewQueue()}
	v := operation.View{ID: "op-x", Kind: "uninstall", Target: "gitea", State: operation.StateDone}
	if !s.finalizeOp(v) {
		t.Fatalf("首次结算应执行副作用")
	}
	if s.finalizeOp(v) {
		t.Fatalf("同一 opID 二次结算不应再执行副作用")
	}
	if !s.finalizeOp(operation.View{ID: "op-y", Kind: "uninstall", Target: "gitea"}) {
		t.Fatalf("不同 opID 应各自结算一次")
	}
}

// (a) 操作运行中：follow 端点先推一条「当前在跑」的事件（step=kind）。
// 用 kind=uninstall 避免终态副作用写测试机真实配置目录（bumpLocalInstalls）。
func TestFollowOperationRunningFirstEvent(t *testing.T) {
	s, srv := newFollowServer(t)
	release := make(chan struct{})
	view, err := s.Ops.Start("uninstall", "fnmonitor", func(_ context.Context, progress func(string, float64)) error {
		progress("卸载中…", 40)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer close(release) // 收尾：让后台操作结束

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := getStream(t, ctx, srv.URL+"/api/operations/"+view.ID+"/events")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q，不是 SSE", ct)
	}
	ev := readEvent(t, bufio.NewScanner(resp.Body))
	if ev["step"] != "uninstall" {
		t.Fatalf("首条事件应为运行中状态 step=uninstall，实际 %v", ev)
	}
	if ev["id"] != view.ID {
		t.Fatalf("事件 id = %v，期望 %s", ev["id"], view.ID)
	}
}

// (b) 操作已完成后再 follow：立刻收到 done 并结束流。
func TestFollowOperationFinishedEmitsDone(t *testing.T) {
	s, srv := newFollowServer(t)
	view, err := s.Ops.Start("uninstall", "gitea", func(_ context.Context, progress func(string, float64)) error {
		progress("卸载完成", 100)
		return nil
	})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitOpDone(t, s, view.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := getStream(t, ctx, srv.URL+"/api/operations/"+view.ID+"/events")
	sc := bufio.NewScanner(resp.Body)
	ev := readEvent(t, sc)
	if ev["step"] != "done" || ev["id"] != view.ID {
		t.Fatalf("期望 done 事件带 id，实际 %v", ev)
	}
	for sc.Scan() {
		if strings.HasPrefix(strings.TrimSuffix(sc.Text(), "\r"), "data: ") {
			t.Fatalf("done 之后不应再有事件: %s", sc.Text())
		}
	}
}

// (c) 未知 / 已过期的 id：404 + JSON 错误体（前端据此退回兜底匹配）。
func TestFollowOperationUnknownID404(t *testing.T) {
	_, srv := newFollowServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp := getStream(t, ctx, srv.URL+"/api/operations/deadbeefdeadbeef/events")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", resp.StatusCode)
	}
	buf := new(strings.Builder)
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		buf.WriteString(sc.Text())
	}
	if !strings.Contains(buf.String(), "操作不存在或已过期") {
		t.Fatalf("错误体应说明操作不存在，实际 %s", buf.String())
	}
}

// (d) sseRunOp 发出的每条事件都带非空 id（客户端据此续传）。
func TestSSERunOpEventsCarryOpID(t *testing.T) {
	s := &Server{Ops: operation.NewQueue()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/apps/gitea/uninstall", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sseRunOp(req, rec, rec, "uninstall", "gitea", func(_ context.Context, progress func(string, float64)) error {
			progress("卸载中…", 50)
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("sseRunOp 5s 未结束")
	}
	body := rec.Body.String()
	ids := map[string]bool{}
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("事件非法 JSON: %v (%s)", err, line)
		}
		n++
		id, _ := ev["id"].(string)
		if id == "" {
			t.Fatalf("事件缺 id: %s", line)
		}
		ids[id] = true
	}
	// 操作在 Start 后由 goroutine 异步开跑：订阅建立前可能已经进入 running/done，
	// 中间态事件有概率收不到（生产同理，客户端靠 /api/operations/{id} 兜底）。
	// 因此这里只要求「启动 + 至少一条终态」，不再以固定 3 条断言（-race 下必红）。
	if n < 2 {
		t.Fatalf("事件太少（至少 启动+终态）: %s", body)
	}
	// 进度/中间态事件同样必须带 id（不依赖时序，直接验报文构造）。
	if ev := opEvent("deadbeefdeadbeef", map[string]any{"step": "running", "progress": 50, "message": "卸载中…"}); ev["id"] != "deadbeefdeadbeef" {
		t.Fatalf("进度事件缺 id: %v", ev)
	}
	if len(ids) != 1 {
		t.Fatalf("同一操作的事件 id 应一致，实际 %v", ids)
	}
	if !strings.Contains(body, `"step":"done"`) {
		t.Fatalf("缺少 done 事件: %s", body)
	}
}

// (e) 客户端中途断开后，操作照旧跑完；新的 follow（续传连接）仍能把 done 送到。
func TestFollowOperationSurvivesClientDisconnect(t *testing.T) {
	s, srv := newFollowServer(t)
	release := make(chan struct{})
	view, err := s.Ops.Start("uninstall", "motrix", func(_ context.Context, progress func(string, float64)) error {
		progress("卸载中…", 10)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}

	// 第一条连接：拿到首条事件后模拟手机切网（取消请求）
	ctx, cancel := context.WithCancel(context.Background())
	resp := getStream(t, ctx, srv.URL+"/api/operations/"+view.ID+"/events")
	first := readEvent(t, bufio.NewScanner(resp.Body))
	if first["id"] != view.ID {
		t.Fatalf("首条事件 id = %v，期望 %s", first["id"], view.ID)
	}
	cancel()
	_ = resp.Body.Close()

	close(release) // 客户端走了，操作在后台跑完
	waitOpDone(t, s, view.ID)

	// 第二条连接（续传）：立即收到 done
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	resp2 := getStream(t, ctx2, srv.URL+"/api/operations/"+view.ID+"/events")
	ev := readEvent(t, bufio.NewScanner(resp2.Body))
	if ev["step"] != "done" {
		t.Fatalf("续传连接应立刻收到 done，实际 %v", ev)
	}
}

// P0-1（终态轮询兜底）：GET /api/operations/{id} 返回单条状态；终态时执行
// 幂等结算（目录失效/本机计数），重复查询不重复执行。
func TestGetOperationJSONFinalizesOnTerminal(t *testing.T) {
	s := &Server{Ops: operation.NewQueue()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/operations/{id}", s.getOperationJSON)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 用 uninstall 避免依赖本机安装计数（最小 Server 未初始化 Cfg/计数表）。
	view, err := s.Ops.Start("uninstall", "demo", func(_ context.Context, progress func(string, float64)) error {
		progress("卸载中…", 50)
		return nil
	})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitOpDone(t, s, view.ID)

	for i := 0; i < 2; i++ {
		resp, err := http.Get(srv.URL + "/api/operations/" + view.ID)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		var got operation.View
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			_ = resp.Body.Close()
			t.Fatalf("响应不是合法 JSON: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || got.State != operation.StateDone {
			t.Fatalf("第 %d 次查询: status=%d state=%q", i+1, resp.StatusCode, got.State)
		}
	}
	if _, ok := s.opFinalized.Load(view.ID); !ok {
		t.Fatalf("终态查询未执行结算（opFinalized 无 %s）", view.ID)
	}

	// 不存在的 id → 404 JSON（前端据此退化为兜底匹配/后台提示）。
	resp, err := http.Get(srv.URL + "/api/operations/does-not-exist")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在 id 应 404，实际 %d", resp.StatusCode)
	}
}
