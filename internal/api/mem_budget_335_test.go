package api

// mem_budget_335_test.go —— 0.6.335 B/C 批次单测：
//   B1/B2 图标与 README 内存层字节预算（超大条目只落磁盘、总量按最旧淘汰）
//   C1   预览图快判死判定（仅 raw 直链 + 首候选 404）
//   C2   detail-store 逐源日志按窗口聚合
//   C3   [iconfail] 同一应用 6h 内只记一条

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/config"
)

// ---------- B1：图标内存层字节预算 ----------

func TestIconMemByteBudget_335(t *testing.T) {
	st := newIconStore("")
	st.memMax = 100
	st.memMaxBytes = 1000
	st.memEntryMax = 400

	for i := 0; i < 10; i++ {
		st.Put(kvKey(i), make([]byte, 300), "image/png")
	}
	if st.memBytes > st.memMaxBytes {
		t.Fatalf("内存层字节 %d 超预算 %d", st.memBytes, st.memMaxBytes)
	}
	if len(st.mem) > 3 {
		t.Fatalf("300B × %d 条已超 1000B 预算（应 ≤3）", len(st.mem))
	}
	// 超大条目（> memEntryMax）只落磁盘：不进内存，且不影响已有条目
	before := st.memBytes
	st.Put("big@app", make([]byte, 500), "image/png")
	if _, ok := st.mem["big@app"]; ok {
		t.Fatal("超过单条上限的图标不应进内存层")
	}
	if st.memBytes != before {
		t.Fatalf("超大条目不应改变 memBytes: %d → %d", before, st.memBytes)
	}
	// 记账自洽：memBytes == 各条 len 之和
	var sum int64
	for _, e := range st.mem {
		sum += int64(len(e.data))
	}
	if sum != st.memBytes {
		t.Fatalf("memBytes 记账不一致: 字段 %d, 实测 %d", st.memBytes, sum)
	}
}

// ---------- B2：README 内存层字节预算 ----------

func TestReadmeMemByteBudget_335(t *testing.T) {
	st := newReadmeStore("")
	st.memMaxBytes = 900
	st.memEntryMax = 400

	for i := 0; i < 10; i++ {
		st.Put(kvKey(i), make([]byte, 300), "text/markdown")
	}
	if st.memBytes > st.memMaxBytes {
		t.Fatalf("README 内存层字节 %d 超预算 %d", st.memBytes, st.memMaxBytes)
	}
	if len(st.mem) > 3 {
		t.Fatalf("300B × %d 条已超 900B 预算（应 ≤3）", len(st.mem))
	}
	before := st.memBytes
	st.Put("big-readme", make([]byte, 500), "text/markdown")
	if _, ok := st.mem["big-readme"]; ok {
		t.Fatal("超过单条上限的 README 不应进内存层（只落磁盘）")
	}
	if st.memBytes != before {
		t.Fatalf("超大 README 不应改变 memBytes: %d → %d", before, st.memBytes)
	}
	var sum int64
	for _, e := range st.mem {
		sum += int64(len(e.body))
	}
	if sum != st.memBytes {
		t.Fatalf("memBytes 记账不一致: 字段 %d, 实测 %d", st.memBytes, sum)
	}
}

// ---------- C1：预览图快判死 ----------

func TestFastDeadOnDeclared_335(t *testing.T) {
	raw := "https://raw.githubusercontent.com/o/r/main/a.png"
	cases := []struct {
		name   string
		decl   string
		resURL string
		status int
		want   bool
	}{
		{"raw 首候选 404", raw, raw, 404, true},
		{"镜像 404 不算", raw, "https://ghfast.top/" + raw, 404, false},
		{"非 404 不算", raw, raw, 403, false},
		{"jsDelivr 声明不算", "https://cdn.jsdelivr.net/gh/o/r@main/a.png", "https://cdn.jsdelivr.net/gh/o/r@main/a.png", 404, false},
		{"非 raw 主机不算", "https://example.com/a.png", "https://example.com/a.png", 404, false},
	}
	for _, c := range cases {
		if got := fastDeadOnDeclared(c.decl, c.resURL, c.status); got != c.want {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// ---------- C2：detail-store 日志聚合 ----------

func TestDetailSinkAggregation_335(t *testing.T) {
	s := &Server{}
	s.detailSinkNote(3)
	s.detailSinkNote(2)
	if s.detailSinkApps != 5 || s.detailSinkSources != 2 {
		t.Fatalf("窗口计数 = %d 应用 / %d 源, want 5/2", s.detailSinkApps, s.detailSinkSources)
	}
	s.flushDetailSink()
	if s.detailSinkApps != 0 || s.detailSinkSources != 0 {
		t.Fatalf("flush 后应清零, got %d/%d", s.detailSinkApps, s.detailSinkSources)
	}
}

// ---------- C3：[iconfail] 限频 ----------

func TestIconFailThrottle_335(t *testing.T) {
	oldEvery := iconFailEvery
	oldSeen := iconFailSeen
	iconFailEvery = 30 * time.Millisecond
	iconFailSeen = map[string]time.Time{}
	defer func() {
		iconFailEvery = oldEvery
		iconFailSeen = oldSeen
	}()

	if !iconFailAllow("src@app") {
		t.Fatal("首次应允许记录")
	}
	if iconFailAllow("src@app") {
		t.Fatal("窗口内应被限频")
	}
	if !iconFailAllow("other@app") {
		t.Fatal("不同 key 不应互相限频")
	}
	time.Sleep(40 * time.Millisecond)
	if !iconFailAllow("src@app") {
		t.Fatal("窗口过后应允许再次记录")
	}
}

// ---------- C1：声明地址挂住 + 其余候选全失败 → 快判死 ----------

func TestRaceFetchFastDeadGrace_335(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // 模拟 raw 挂起：直到客户端取消才返回
		case <-time.After(12 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer fast.Close()

	s := &Server{Cfg: &config.Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cands := []string{slow.URL + "/p1.png", fast.URL + "/p1.png"}
	t0 := time.Now()
	_, _, dead, _, err := s.raceFetchWith(ctx, cands, true)
	elapsed := time.Since(t0)
	cancel()

	if !dead || err == nil {
		t.Fatalf("应判死（dead=true, err!=nil），got dead=%v err=%v", dead, err)
	}
	if elapsed > previewFastDeadGrace+2*time.Second {
		t.Fatalf("应在 %v 宽限内判死，实际 %v（旧逻辑会陪满 13s）", previewFastDeadGrace, elapsed)
	}
	if elapsed < previewFastDeadGrace-500*time.Millisecond {
		t.Fatalf("不应早于宽限就判死（其余候选可能尚未全败）: %v", elapsed)
	}
}
