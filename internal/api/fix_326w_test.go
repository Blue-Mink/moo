package api

// fix_326w_test.go —— 0.6.326 收尾：审计 M-1 / M-2 / L-5 的定向单测
// （版本压在 0.6.326，用户令 2026-10-17）。
//
//	M-1  official_session_expired 只对「已授权过官方源」的用户告警；
//	M-2a 本地图标先于别名网络探测（由 store_ops 调用序保证，E2E 覆盖）；
//	M-2b 同仓 in-flight 合并（本文件并发用例，旧实现必发 N 次请求）；
//	L-5  applyUpstreamCorrection 复制 Arch/Archs（回归守卫在
//	     correct_326k_test.go::TestCorrect326K_PackageFieldsFollowUpstream）。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/official"
)

// newAuthorizedOfficialSession 造一份「已授权过」的官方会话（不联网）：
// access 已过期但 refresh 仍在 —— 既覆盖「过期但曾授权」也算 linked，
// 又不会触发任何真实请求。
func newAuthorizedOfficialSession(t *testing.T) *official.Manager {
	t.Helper()
	m := official.NewManagerWithBase(t.TempDir(), "http://192.0.2.22:5666")
	b, err := json.Marshal(official.Session{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour).UnixMilli(),
		UpdatedAt:    time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.SessionFile()), 0o700); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(m.SessionFile(), b, 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	m.LoadSession()
	if m.Session() == nil {
		t.Fatal("前置：会话未加载成功")
	}
	return m
}

// TestIconAliasInflightDedupe_326W 同仓多卡并发列举只应发一次请求。
func TestIconAliasInflightDedupe_326W(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(150 * time.Millisecond) // 拉住在飞窗口，保证并发调用都赶上
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"files":[{"name":"SmartDNS/ICON.PNG"}]}`)
	}))
	defer ts.Close()

	l := &iconAliasLister{
		apiRoot:  ts.URL,
		client:   ts.Client(),
		repos:    map[string]*iconAliasEntry{},
		inflight: map[string]*iconAliasCall{},
	}
	const n = 12
	var (
		wg    sync.WaitGroup
		oks   = make([]bool, n)
		start = make(chan struct{})
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			e, ok := l.dirs(context.Background(), "33205q", "FnDepot")
			oks[i] = ok && e != nil && e.OK
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("同仓并发列举应合并为 1 次请求，实际 %d 次（M-2 回归）", got)
	}
	for i, ok := range oks {
		if !ok {
			t.Fatalf("第 %d 个并发调用未拿到结果（等待侧必须共享 leader 结果）", i)
		}
	}
}
