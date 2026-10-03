package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/panel"
	"moo/internal/source"
)

// newOfficialSyncTestServer 构造带官方面板通道的最小 Server。
func newOfficialSyncTestServer(fail *atomic.Bool, calls *int32) *Server {
	cfg := &config.Config{}
	p := &Panel{
		client: panel.NewClient("", "BlueMink", "test"),
		listApps: func(ctx context.Context) ([]panel.PanelApp, error) {
			atomic.AddInt32(calls, 1)
			if fail.Load() {
				return nil, context.DeadlineExceeded
			}
			return []panel.PanelApp{{AppName: "homebox", Name: "Homebox", Version: "0.26.2"}}, nil
		},
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
	return &Server{Cfg: cfg, Src: source.NewManager(cfg), Panel: p}
}

// TestSyncSourceOfficial 官方源手动同步端点（0.6.252 修复）：
// 1) 面板通道启用时不再 404 空转，走 AppsForce 并返回源条目；
// 2) 失败退避窗口内手动同步仍可重试（force 绕过退避）；
// 3) 面板未启用时保持 404「源不存在」。
func TestSyncSourceOfficial(t *testing.T) {
	var fail atomic.Bool
	var calls int32
	fail.Store(true)
	s := newOfficialSyncTestServer(&fail, &calls)

	// 先制造一次失败：退避窗口开启（failCount=1，30min）
	if _, err := s.Panel.Apps(context.Background()); err == "" {
		t.Fatal("首次失败应返回错误")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("首次应尝试登录 1 次，实际 %d", calls)
	}

	// 退避窗口内手动同步：必须绕过退避、重新尝试（calls 1→2）
	fail.Store(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/sources/fnos-official/sync", nil)
	req.SetPathValue("id", OfficialSourceID)
	s.syncSource(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("官方源手动同步应 200（0.6.251 误返回 404），实际 %d: %s", rec.Code, rec.Body.String())
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("退避窗口内 force 应触发登录重试（1→2），实际 %d", calls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fnos-official") {
		t.Errorf("响应应含官方源条目，实际: %s", body)
	}

	// 手动成功后退避清零：把缓存龄过 30 分钟，自动路径应重新拉取（2→3）
	s.Panel.mu.Lock()
	s.Panel.appsAt = time.Now().Add(-31 * time.Minute)
	s.Panel.mu.Unlock()
	if _, err := s.Panel.Apps(context.Background()); err != "" {
		t.Fatalf("成功后自动路径应恢复，err=%q", err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("退避清零后自动路径应正常拉取（2→3），实际 %d", calls)
	}
}

// TestSyncSourceOfficialPanelDisabled 面板未启用 → 404 源不存在。
func TestSyncSourceOfficialPanelDisabled(t *testing.T) {
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Panel: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/sources/fnos-official/sync", nil)
	req.SetPathValue("id", OfficialSourceID)
	s.syncSource(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("面板未启用应 404，实际 %d: %s", rec.Code, rec.Body.String())
	}
}
