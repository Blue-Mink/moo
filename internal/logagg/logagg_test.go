package logagg

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// logCapture 带锁捕获标准 log 输出。
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw := c.buf.String()
	if raw == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(raw, "\n"), "\n")
}

func withLogCapture(t *testing.T) *logCapture {
	t.Helper()
	cap := &logCapture{}
	old := log.Writer()
	log.SetOutput(cap)
	t.Cleanup(func() { log.SetOutput(old) })
	return cap
}

// 60s 窗口内同键 1247 次 = 2 行（首条完整 + 窗口末聚合行）。
func TestAggregatorSameKeyFirstPlusAggregate(t *testing.T) {
	a := New(time.Hour) // 窗口拉大，手动 Flush，避免 ticker 竞态
	defer a.Close()
	cap := withLogCapture(t)

	const n = 1247
	for i := 0; i < n; i++ {
		a.Record("icon", "raw.githubusercontent.com", "跳过（安全策略）https://raw.githubusercontent.com/x: 主机解析失败（拒绝抓取）: i/o timeout")
	}
	got := cap.lines()
	if len(got) != 1 {
		t.Fatalf("窗口内应只有首条 1 行: %v", got)
	}
	for _, want := range []string{"[icon]", "raw.githubusercontent.com", "i/o timeout"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("首条应完整: %q 缺 %q", got[0], want)
		}
	}
	a.Flush()
	got = cap.lines()
	if len(got) != 2 {
		t.Fatalf("Flush 后应共 2 行（首条+聚合）: %v", got)
	}
	agg := got[1]
	for _, want := range []string{"[icon]", "聚合", "1247 次", "首次="} {
		if !strings.Contains(agg, want) {
			t.Errorf("聚合行缺 %q: %s", want, agg)
		}
	}
}

// 异键独立计数；同主机大小写归一；窗口重开后计数清零（单次不再出聚合）。
func TestAggregatorDistinctKeysAndWindowReset(t *testing.T) {
	a := New(time.Hour)
	defer a.Close()
	cap := withLogCapture(t)

	a.Record("race", "A.example", "ERR x")
	a.Record("race", "B.example", "ERR y")
	a.Record("race", "a.example", "ERR z") // 大小写归一同键 → 计入 A 的第 2 次
	a.Flush()
	got := cap.lines()
	if len(got) != 3 { // 2 首条（x/y）+ 1 聚合（A/a×2，z 被计数不出行）
		t.Fatalf("应 3 行（2 首条+1 聚合）: %v", got)
	}
	if !strings.Contains(got[2], "2 次") {
		t.Errorf("A/a 应聚合成 2 次: %s", got[2])
	}
	// 第二窗口：同键只命中 1 次 → 只出首条，不出聚合
	a.Record("race", "A.example", "ERR again")
	a.Flush()
	if got := cap.lines(); len(got) != 4 {
		t.Fatalf("第二窗口单次命中应只加 1 行: %v", got)
	}
}

// Close 后 ticker 协程干净退出（不泄漏）。
func TestAggregatorClose(t *testing.T) {
	a := New(time.Hour)
	a.Record("dl", "h.example", "x")
	done := make(chan struct{})
	go func() {
		a.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未返回（flush 协程泄漏）")
	}
}
