package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// G3：探测器只读 + 未知即入队；命中本仓索引 → 可加。
func TestAddProbeLookupAndRefresh(t *testing.T) {
	a := NewAddProbe(filepath.Join(t.TempDir(), "addprobe.json"))
	if _, _, known := a.Lookup("acme/tool"); known {
		t.Fatal("未探测的仓库应 unknown")
	}
	a.probe = func(u string) string {
		if strings.HasSuffix(u, "/acme/tool") {
			return "fnpack.json"
		}
		return ""
	}
	a.Enqueue("acme/tool")
	if n := a.RefreshRound(context.Background(), 10); n != 1 {
		t.Fatalf("应探测 1 个仓库，实际 %d", n)
	}
	idx, owner, known := a.Lookup("acme/tool")
	if !known || idx != "fnpack.json" || owner != "" {
		t.Fatalf("Lookup = (%q,%q,%v)，期望 (fnpack.json,,true)", idx, owner, known)
	}
}

// G3：本仓无索引但 owner 有 <owner>/FnDepot → 记 OwnerIndex（按钮改指它）。
func TestAddProbeOwnerIndexFallback(t *testing.T) {
	a := NewAddProbe("")
	a.probe = func(u string) string {
		if strings.HasSuffix(u, "/bob/FnDepot") {
			return "fnpack.json"
		}
		return ""
	}
	a.Enqueue("bob/pack-only") // 本仓是打包仓，无索引；owner 有 FnDepot
	a.RefreshRound(context.Background(), 10)
	idx, owner, known := a.Lookup("bob/pack-only")
	if !known {
		t.Fatal("应已探测")
	}
	if idx != "" || owner != "bob/FnDepot" {
		t.Fatalf("期望 (,'bob/FnDepot')，实际 (%q,%q)", idx, owner)
	}
}

// G3：两处都无索引 → 结果已知且全空（前端据此不出按钮、显示原因）。
func TestAddProbeNoIndexAnywhere(t *testing.T) {
	a := NewAddProbe("")
	a.probe = func(string) string { return "" }
	a.Enqueue("nobody/nothing")
	a.RefreshRound(context.Background(), 10)
	idx, owner, known := a.Lookup("nobody/nothing")
	if !known || idx != "" || owner != "" {
		t.Fatalf("期望已知且全空，实际 (%q,%q,%v)", idx, owner, known)
	}
}

// G3：队列卫生 —— 同一仓库重复入队只探一次；非 owner/repo 形态不入队。
func TestAddProbeEnqueueDedup(t *testing.T) {
	a := NewAddProbe("")
	calls := 0
	a.probe = func(string) string { calls++; return "" }
	a.Enqueue("x/y")
	a.Enqueue("x/y")
	a.Enqueue("bad")   // 不是 owner/repo
	a.Enqueue("a/b/c") // 段数不对
	if n := a.RefreshRound(context.Background(), 10); n != 1 {
		t.Fatalf("应只探测 1 个，实际 %d", n)
	}
	// 已知且新鲜 → 不再入队
	a.Enqueue("x/y")
	if n := a.RefreshRound(context.Background(), 10); n != 0 {
		t.Fatalf("新鲜结果不应重复探测，实际 %d", n)
	}
}

// G3：Enqueue 必须唤醒正在等下一轮的循环（目录只在 /api/apps 时构建，
// 纯定时会让装完不打开应用的场景白等一整个周期）。
func TestAddProbeKickWakesWait(t *testing.T) {
	a := NewAddProbe("")
	done := make(chan struct{})
	go func() { a.WaitNext(context.Background(), time.Hour); close(done) }()
	time.Sleep(30 * time.Millisecond)
	a.Enqueue("acme/late") // 队列空 → 入队 + kick
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue 入队后应立刻唤醒 WaitNext")
	}
	// 到期路径仍要生效（没有 kick 也不能永久阻塞）
	start := time.Now()
	a.WaitNext(context.Background(), 120*time.Millisecond)
	if d := time.Since(start); d < 80*time.Millisecond {
		t.Fatalf("WaitNext 应等到期，实际 %v", d)
	}
	// ctx 取消路径
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.WaitNext(ctx, time.Hour) // 立即返回即通过
}

// G3：落盘/加载往返（重启不丢，详情页秒级命中）。
func TestAddProbePersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "addprobe.json")
	a := NewAddProbe(p)
	a.SetForTest("acme/tool", "moo.json", "")
	a.save()
	b := NewAddProbe(p)
	idx, owner, known := b.Lookup("acme/tool")
	if !known || idx != "moo.json" || owner != "" {
		t.Fatalf("落盘往返失败：(%q,%q,%v)", idx, owner, known)
	}
}
