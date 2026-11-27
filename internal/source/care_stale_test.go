package source

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moo/internal/config"
)

// 误判红线 7：空源通道同等严格——「刷新失败」绝不能被当成「成功但 0 应用」，
// 且「疑似陈旧副本 + 复核不可达」必须保留旧缓存、不缩表、不计空。
//
// 说明：单机 httptest 下「记忆路径 = 陈旧镜像、raw 复核不可达」的双通道差异
// 无法构造（同一台假服务器既当镜像又当 raw），该分支由 decideStale 判定表
// 覆盖（TestDecideStale：复核不可用 → staleKeepOld）。本用例覆盖红线 7 的
// 另一条硬要求：抓取失败的轮次不得计入空源。
func TestCareStaleKeepOldNoEmptyCount(t *testing.T) {
	var phase int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&phase) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "moo.json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"a0":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a1":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a2":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a3":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a4":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a5":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a6":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a7":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a8":{"version":"1.0","download_url":"https://example.com/a.fpk"},` +
			`"a9":{"version":"1.0","download_url":"https://example.com/a.fpk"}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Sources: []config.SourceRef{{Name: "t", URL: srv.URL}}}
	m := NewManager(cfg)
	m.CareProbe = func(name, url string) CareVerdict { return CareUncertain }

	if err := m.Refresh("t"); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	if got := m.cacheCount("t"); got != 10 {
		t.Fatalf("首轮应有 10 条, got %d", got)
	}

	// 第二轮：全通道 500（等同「复核不可达」）→ 刷新失败，但不得清缓存
	atomic.StoreInt32(&phase, 1)
	if err := m.Refresh("t"); err == nil {
		t.Fatal("全通道 500 时刷新应失败")
	}
	if got := m.cacheCount("t"); got != 10 {
		t.Fatalf("失败轮不得清缓存/缩表, got %d", got)
	}

	// 策略轮：失败轮（Error 非空）既不计空也不沉底
	sts := m.Sources()
	sts[0].Error = "所有候选地址均失败: x: HTTP 500"
	m.AutoCarePass(sts)
	m.careMu.Lock()
	empty := m.careEmpty["t"]
	m.careMu.Unlock()
	if empty != 0 {
		t.Fatalf("失败轮不得计入空源（红线 7）, got %d", empty)
	}
	if !cfg.Sources[0].IsEnabled() {
		t.Fatal("失败轮不得停用源")
	}
	if cfg.Sources[0].Name != "t" {
		t.Fatal("失败轮不得沉底")
	}
}
