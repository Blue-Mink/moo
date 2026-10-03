package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/panel"
)

// newBackoffTestPanel 构造可注入 listApps 的 Panel（client=nil，
// ensureBackfill 对 nil client 直接返回，安全）。
func newBackoffTestPanel(fail *atomic.Bool, calls *int32) *Panel {
	return &Panel{
		listApps: func(ctx context.Context) ([]panel.PanelApp, error) {
			atomic.AddInt32(calls, 1)
			if fail.Load() {
				return nil, errors.New("模拟面板登录失败")
			}
			return []panel.PanelApp{{AppName: "homebox", Name: "Homebox", Version: "0.26.2"}}, nil
		},
		iconCache:   map[string]iconEntry{},
		detailCache: map[string]detailEntry{},
	}
}

func TestFailBackoffDuration(t *testing.T) {
	for _, tc := range []struct {
		count int
		want  time.Duration
	}{
		{0, 0},
		{1, 30 * time.Minute},
		{2, 1 * time.Hour},
		{3, 2 * time.Hour},
		{4, 2 * time.Hour}, // 封顶
		{10, 2 * time.Hour},
	} {
		p := &Panel{failCount: tc.count}
		if got := p.failBackoff(); got != tc.want {
			t.Errorf("failCount=%d: 退避=%v，期望 %v", tc.count, got, tc.want)
		}
	}
}

func TestAppsBackoffSuppressesAutoRetry(t *testing.T) {
	var fail atomic.Bool
	var calls int32
	fail.Store(true)
	p := newBackoffTestPanel(&fail, &calls)
	ctx := context.Background()

	// 第一次：失败（触发登录尝试 1 次）
	apps, err := p.Apps(ctx)
	if len(apps) != 0 || err == "" {
		t.Fatalf("首次失败应返回空+错误: apps=%d err=%q", len(apps), err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("首次应调用 1 次 listApps，实际 %d", calls)
	}

	// 退避窗口内（30min）：自动路径不得再触发登录
	apps, _ = p.Apps(ctx)
	if len(apps) != 0 {
		t.Fatalf("退避期内应返回空缓存")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("退避期内 Apps() 不得调用 listApps（避免限流续命），实际 %d 次", calls)
	}

	// 手动强制（AppsForce）：绕过退避，允许重试
	_, _ = p.AppsForce(ctx)
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("AppsForce 应绕过退避触发 1 次，实际累计 %d", calls)
	}
	// 第二次失败 → 退避升级为 1h
	p.mu.Lock()
	b := p.failBackoff()
	p.mu.Unlock()
	if b != time.Hour {
		t.Errorf("第二次失败后退避应为 1h，实际 %v", b)
	}
}

func TestAppsBackoffResetOnSuccess(t *testing.T) {
	var fail atomic.Bool
	var calls int32
	fail.Store(true)
	p := newBackoffTestPanel(&fail, &calls)
	ctx := context.Background()

	_, _ = p.Apps(ctx) // 失败 → 退避 30min
	// 退避期自然到点后（模拟 31 分钟过去）：自动路径恢复重试
	p.mu.Lock()
	p.lastFailAt = time.Now().Add(-31 * time.Minute)
	p.mu.Unlock()
	_, _ = p.Apps(ctx) // 仍失败（fail 仍 true）→ 累计 2 次
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("退避到期后应恢复自动重试，累计应 2 次，实际 %d", calls)
	}

	// 成功 → 退避清零
	fail.Store(false)
	apps, err := p.AppsForce(ctx)
	if err != "" || len(apps) != 1 {
		t.Fatalf("强制同步应成功: apps=%d err=%q", len(apps), err)
	}
	p.mu.Lock()
	zero := p.failCount == 0 && p.lastFailAt.IsZero()
	p.mu.Unlock()
	if !zero {
		t.Fatalf("成功后退避状态应清零")
	}
	// 成功后的 30 分钟缓存：自动路径直接命中缓存，不再登录
	_, _ = p.Apps(ctx)
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("缓存命中不得触发 listApps，累计应 3 次，实际 %d", calls)
	}
}
