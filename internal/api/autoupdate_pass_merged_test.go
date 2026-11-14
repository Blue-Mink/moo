package api

// autoUpdatePass / blockedReason / 通知聚合 的单测（合并版）。
//
// 覆盖：
//   - blockedReason 的逐字文案与四种放行情形；
//   - buildAutoUpdateRoundMsg 的「平台标记失败只算跳过、不算失败」聚合语义；
//   - autoUpdatePass 的三道闸门（开关关闭 / 平台权威跳过 / 手动操作让路）与一条正控
//     （去掉平台标记就必须真进升级通道）。
//
// 关键做法：用 platform.SetDaemonSocketForTest 起假 daemon（unix socket，路由串直接写字面量），
// 造出「已装 1.0.0 + 目录 2.0.0」的真实 job，再用 download/task 调用计数断言
// ——这样「0 次」不会被「jobs 为空」冒充成通过。
//
// 仍不可测：升级成功路径（UpdateApp/install 真正成功后的计数与外部渠道 fan-out），
// 需要把 updater 与通知发送抽成 Server 可注入字段才能端到端断言。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/operation"
	"moo/internal/panel"
	"moo/internal/platform"
	"moo/internal/source"
)

// auTally 记录假 daemon 收到的请求：upgrade = 真进入升级通道（download/task），
// writes = 全部非 GET 请求（预检/下载等写动作），paths 供失败时定位。
type auTally struct {
	upgrade int32
	writes  int32
	mu      sync.Mutex
	paths   []string
}

func (c *auTally) rec(r *http.Request) {
	if r.Method != http.MethodGet {
		atomic.AddInt32(&c.writes, 1)
	}
	c.mu.Lock()
	c.paths = append(c.paths, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
}

func (c *auTally) dump() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

// startAutoUpdateDaemon 起假 daemon：已装 demo 1.0.0，平台报告可升级 2.0.0；
// autoUpdateFailed 控制平台「自动更新已失败」权威标记。download/task 计数后立即失败，
// 让「确实尝试过」这一点快速可见（不消耗升级预算）。
func startAutoUpdateDaemon(t *testing.T, autoUpdateFailed bool) *auTally {
	t.Helper()
	c := &auTally{}
	installed := fmt.Sprintf(`{"code":0,"data":{"total":1,"list":[{"appName":"demo",`+
		`"name":"演示","version":"1.0.0","sourceID":"src-1","status":"running",`+
		`"upgradeInfo":{"version":"2.0.0","autoUpdateFailed":%t}}]}}`, autoUpdateFailed)

	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, installed)
	})
	mux.HandleFunc("/rpc/v1/stop/check", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"dependentApps":{"isDependency":false,"dependencyApps":[]}}}`)
	})
	mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.upgrade, 1)
		fmt.Fprint(w, `{"code":16000,"msg":"测试注入：下载拒绝"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/daemon.sock")
	if err != nil {
		t.Fatalf("起假 daemon 失败：%v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.rec(r)
		mux.ServeHTTP(w, r)
	}))
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	platform.SetDaemonSocketForTest(lis.Addr().String())
	return c
}

// autoUpdateTestServer 造 Server：目录源广告 demo 2.0.0（httptest），官方目录注入为不可用
// （否则 runOfficialUpgrade 会走 panelApp → s.Panel nil 风险）。
func autoUpdateTestServer(t *testing.T, autoUpdate bool) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"demo":{"version":"2.0.0","display_name":"演示",`+
			`"download_url":"https://example.com/demo.fpk"}}`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cfg := config.Default()
	cfg.AutoUpdate = autoUpdate
	m := source.NewManager(cfg)
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	return &Server{
		Cfg: cfg, Src: m, Ops: operation.NewQueue(),
		Panel: &Panel{listApps: func(context.Context) ([]panel.PanelApp, error) {
			return nil, fmt.Errorf("测试：不查官方目录")
		}},
	}
}

// requirePending 断言目录里确有「已装且有更新」的 demo——没有它，闸门用例会因为
// jobs 为空而假通过（这正是本文件要防的坑）。
func requirePending(t *testing.T, s *Server) {
	t.Helper()
	for _, a := range s.buildCatalog(s.bgLang()) {
		if a.AppName == "demo" && a.Installed && a.HasUpdate {
			return
		}
	}
	t.Fatalf("前置条件不成立：目录里没有「已装且有更新」的 demo，本用例会假通过")
}

// runAutoUpdatePass 跑一轮，超时即失败。
func runAutoUpdatePass(t *testing.T, s *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() { s.autoUpdatePass(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("一轮自动更新超时未返回")
	}
}

// TestBlockedReasonAutoUpdateFailed 平台标记自动更新失败 → 给出可读的逐字跳过原因。
func TestBlockedReasonAutoUpdateFailed(t *testing.T) {
	got := blockedReason("1Panel", map[string]bool{"1Panel": true})
	want := "平台自动更新已失败（需手动确认向导或先停用依赖）"
	if got != want {
		t.Fatalf("blockedReason 失败原因不符：got %q want %q", got, want)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("跳过原因不得为空（否则通知里会显示空括号）")
	}
}

// TestBlockedReasonNoFailureReason 无失败标记 → 空串（默认放行）。
func TestBlockedReasonNoFailureReason(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]bool
		app  string
	}{
		{"nil map", nil, "1Panel"},
		{"空 map", map[string]bool{}, "1Panel"},
		{"别的应用失败", map[string]bool{"Gitea": true}, "1Panel"},
		{"显式 false", map[string]bool{"1Panel": false}, "1Panel"},
	}
	for _, c := range cases {
		if got := blockedReason(c.app, c.m); got != "" {
			t.Fatalf("%s：应放行（空原因），got %q", c.name, got)
		}
	}
}

// TestBuildAutoUpdateRoundMsgSkippedNotCountedAsFailed 「平台已标记失败」只进 skipped 段，
// 不计入 failed（否则每小时一条假失败通知、通知常驻）。
func TestBuildAutoUpdateRoundMsgSkippedNotCountedAsFailed(t *testing.T) {
	content, table, rows, variants := buildAutoUpdateRoundMsg(nil, nil, []string{"demo"}, nil)
	if variants == nil {
		t.Fatal("variants 不应为 nil")
	}
	if !strings.Contains(content, "跳过 1") {
		t.Fatalf("摘要应含「跳过 1」，got %q", content)
	}
	if !strings.Contains(content, "失败 0") {
		t.Fatalf("跳过的应用不得计入失败：摘要应含「失败 0」，got %q", content)
	}
	if !strings.Contains(content, "demo") {
		t.Fatalf("跳过名单应列出应用名，got %q", content)
	}
	if strings.Contains(content, "失败 1") {
		t.Fatalf("跳过被误算成失败：%q", content)
	}
	if table == "" || len(rows) == 0 {
		t.Fatalf("表格/卡片行不应为空：table=%q rows=%d", table, len(rows))
	}

	// 反向对照：真失败才计失败。
	fc, _, _, _ := buildAutoUpdateRoundMsg(nil, []string{"demo"}, nil, nil)
	if !strings.Contains(fc, "失败 1") {
		t.Fatalf("真失败应计为「失败 1」，got %q", fc)
	}
	if strings.Contains(fc, "跳过") {
		t.Fatalf("无跳过项时不应出现跳过段：%q", fc)
	}
}

// TestAutoUpdatePassDisabledDoesNothing 开关关闭 → 有活也不干（先自证「有活」）。
func TestAutoUpdatePassDisabledDoesNothing(t *testing.T) {
	c := startAutoUpdateDaemon(t, false)
	s := autoUpdateTestServer(t, false)
	requirePending(t, s)

	runAutoUpdatePass(t, s)
	if n := atomic.LoadInt32(&c.upgrade); n != 0 {
		t.Fatalf("自动更新关闭时不应进入升级通道，实际 %d 次；请求 %v", n, c.dump())
	}
	if cur := s.Ops.Current(); cur != nil {
		t.Fatalf("关闭时不应占用操作队列，got %+v", cur)
	}
}

// TestAutoUpdatePassSkipsPlatformAutoFailedApp 平台权威标记 → 跳过、不重试、不计失败。
func TestAutoUpdatePassSkipsPlatformAutoFailedApp(t *testing.T) {
	c := startAutoUpdateDaemon(t, true)
	s := autoUpdateTestServer(t, true)
	requirePending(t, s)

	runAutoUpdatePass(t, s)
	if n := atomic.LoadInt32(&c.upgrade); n != 0 {
		t.Fatalf("已标记失败的应用不应再试：进入升级通道 %d 次；请求 %v", n, c.dump())
	}
	if cur := s.Ops.Current(); cur != nil {
		t.Fatalf("跳过时不应遗留进行中操作，got %+v", cur)
	}
	if got := blockedReason("demo", map[string]bool{"demo": true}); got == "" {
		t.Fatal("跳过原因不应为空（通知里会显示空括号）")
	}
}

// TestAutoUpdatePassAttemptsWhenPlatformAllows 正控：同一夹具去掉平台标记，
// 必须真的进入升级通道（否则「0 次」可能只是空转）。
func TestAutoUpdatePassAttemptsWhenPlatformAllows(t *testing.T) {
	c := startAutoUpdateDaemon(t, false)
	s := autoUpdateTestServer(t, true)
	requirePending(t, s)

	runAutoUpdatePass(t, s)
	if n := atomic.LoadInt32(&c.upgrade); n == 0 {
		t.Fatalf("平台未标记失败时应尝试升级，实际一次都没进升级通道；请求 %v", c.dump())
	}
}

// TestAutoUpdatePassYieldsToRunningManualOp 手动操作进行中 → 整轮让路：
// 不接管队列、不进升级通道。
func TestAutoUpdatePassYieldsToRunningManualOp(t *testing.T) {
	c := startAutoUpdateDaemon(t, false)
	s := autoUpdateTestServer(t, true)
	requirePending(t, s)
	q := s.Ops

	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = q.Start("upgrade", "busy-app", func(ctx context.Context, progress func(string, float64)) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("准备阶段：手动操作未能进入 running")
	}
	defer close(release)
	cur := q.Current()
	if cur == nil {
		t.Fatal("准备阶段：Current 应为进行中的操作")
	}
	busyID := cur.ID

	runAutoUpdatePass(t, s)
	if n := atomic.LoadInt32(&c.upgrade); n != 0 {
		t.Fatalf("手动操作进行中应让路，实际进入升级通道 %d 次；请求 %v", n, c.dump())
	}
	if c2 := q.Current(); c2 == nil || c2.ID != busyID {
		t.Fatalf("让路失败：队列状态被改，got %+v want %s", c2, busyID)
	}
}
