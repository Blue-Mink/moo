package source

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"moo/internal/config"
)

// newBudgetTestManager 造 10 个已启用源 s01..s10，按表注入 fresh 时间。
func newBudgetTestManager(t *testing.T, fresh map[string]time.Time) *Manager {
	t.Helper()
	cfg := &config.Config{}
	for i := 1; i <= 10; i++ {
		name := fmt.Sprintf("s%02d", i)
		cfg.Sources = append(cfg.Sources, config.SourceRef{Name: name, URL: "https://example.com/" + name})
	}
	m := NewManager(cfg)
	for name, ts := range fresh {
		m.fresh[name] = ts
	}
	return m
}

// TestSelectBudgetedRefresh_PriorityAndOrder F5 选源：优先级集必刷（在前，
// 配置序），其余按新鲜度升序取 budget 个；从未拉取的源最旧；同旧按名称决胜。
func TestSelectBudgetedRefresh_PriorityAndOrder(t *testing.T) {
	base := time.Now()
	fresh := map[string]time.Time{
		"s01": base.Add(-1 * time.Hour),
		"s02": base.Add(-2 * time.Hour),
		"s03": base.Add(-3 * time.Hour),
		"s04": base.Add(-4 * time.Hour),
		"s05": base.Add(-5 * time.Hour),
		"s06": base.Add(-6 * time.Hour),
		"s07": base.Add(-7 * time.Hour),
		"s08": base.Add(-8 * time.Hour),
		// s09 / s10 从未拉取 → 视为最旧（名称决胜）
	}
	m := newBudgetTestManager(t, fresh)
	got := m.SelectBudgetedRefresh([]string{"s05"}, 4)
	want := []string{"s05", "s09", "s10", "s08", "s07"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("选源 = %v, 期望 %v", got, want)
	}
}

// TestSelectBudgetedRefresh_BudgetEdges 预算边界：budget=0 只刷优先级集；
// 预算大于补刷集时全补；停用源（优先级/补刷）均不参与；未知优先级名无害。
func TestSelectBudgetedRefresh_BudgetEdges(t *testing.T) {
	m := newBudgetTestManager(t, nil)
	off := false
	m.cfg.Sources[0].Enabled = &off // s01 停用

	got := m.SelectBudgetedRefresh([]string{"s01"}, 0)
	if !reflect.DeepEqual(got, []string{}) {
		t.Errorf("停用优先级源 + budget=0 应不刷任何源, got %v", got)
	}
	got = m.SelectBudgetedRefresh([]string{"s05", "s99"}, 1)
	// 优先级 s05（s99 不存在无害）+ 补刷 1 个（全部从未拉取 → 名称最旧的非优先级源 s01? 停用 → s02）
	want := []string{"s05", "s02"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("优先级+补刷1 = %v, 期望 %v", got, want)
	}
	got = m.SelectBudgetedRefresh([]string{"s05"}, 99)
	if len(got) != 9 { // 10 源 − s01 停用（s05 在其中，不重复计）
		t.Errorf("大预算应覆盖全部已启用源, got %d", len(got))
	}
}

// TestRefreshBudgeted_OnlySelected 功能路径：RefreshBudgeted 只刷新选中的
// 源（被预算排掉的源 fresh 不变、不发起抓取）。
func TestRefreshBudgeted_OnlySelected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &config.Config{}
	m := NewManager(cfg)
	for _, n := range []string{"p1", "a", "b", "c"} {
		if _, err := m.AddSource(n, ts.URL+"/"+n+"/fnpack.json"); err != nil {
			t.Fatalf("AddSource %s: %v", n, err)
		}
	}
	// 重置新鲜度：a 最新（1 分钟前）、b 2 小时前、c 从未拉取
	m.fresh["a"] = time.Now().Add(-1 * time.Minute)
	m.fresh["b"] = time.Now().Add(-2 * time.Hour)
	delete(m.fresh, "c")

	beforeA := m.fresh["a"]
	sts := m.RefreshBudgeted([]string{"p1"}, 2, 8)
	names := map[string]bool{}
	for _, st := range sts {
		names[st.Name] = true
	}
	for _, n := range []string{"p1", "b", "c"} {
		if !names[n] {
			t.Errorf("预算轮应刷新 %s（实际 %v）", n, sts)
		}
	}
	if names["a"] {
		t.Error("a 是最新源且预算=2，不应被本轮刷新")
	}
	if !m.fresh["a"].Equal(beforeA) {
		t.Error("未被选中的源 fresh 不应变化")
	}
}

// TestRefreshBudgeted_ConcurrencyParam 0.6.315 M2：n 参数透传到抓取工作池
// （启动首轮错峰 4 并发、周期轮 8 并发）。gate 打开后 handler 阻塞在
// release 上，测试读观测到的最大并发——n=2 生效时必恰好为 2；若 n 被忽略
// （用 8），6 个源会冲到 6 并发而失败。注意：AddSource 会即时抓取，所以
// gate 必须在全部 AddSource 完成后才打开（否则首个抓取被 gate 挡住超时）。
func TestRefreshBudgeted_ConcurrencyParam(t *testing.T) {
	var mu sync.Mutex
	conc, maxConc := 0, 0
	gate := false
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if gate {
			conc++
			if conc > maxConc {
				maxConc = conc
			}
			mu.Unlock()
			<-release
			mu.Lock()
			conc--
			mu.Unlock()
		} else {
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		// AddSource 会即时抓取并拒绝空索引 → 返回一个最小非空 fnpack
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","download_url":"https://example.com/demo.fpk"}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &config.Config{}
	m := NewManager(cfg)
	for i := 1; i <= 6; i++ {
		nm := fmt.Sprintf("c%02d", i)
		if _, err := m.AddSource(nm, ts.URL+"/"+nm+"/fnpack.json"); err != nil {
			t.Fatalf("AddSource %s: %v", nm, err)
		}
	}
	mu.Lock()
	gate = true
	mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.RefreshBudgeted(nil, 99, 2) // budget 覆盖全部 6 源，n=2
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := maxConc
		mu.Unlock()
		if got >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release) // 放行全部阻塞 handler
	<-done
	mu.Lock()
	got := maxConc
	mu.Unlock()
	if got != 2 {
		t.Errorf("n=2 应把抓取并发限到 2，观测最大并发 = %d", got)
	}
}
