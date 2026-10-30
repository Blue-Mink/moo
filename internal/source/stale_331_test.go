package source

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"moo/internal/config"
)

// 0.6.331 P0：陈旧索引拦截。
//
// 触发条件（深度测试实锤 2026-10-18）：重启后健康数据冷，fast 镜像/记忆
// 4s 预算下「快但陈旧」的副本先回 → 卡片数骤降（2156→2081 / 1836→1814）。
// 三档处置：复核更多→采信真源；复核不通→保留旧缓存；复核确认变少→允许下架。

func TestStaleDropThreshold(t *testing.T) {
	cases := []struct {
		prev, cur int
		want      bool
	}{
		{100, 81, false}, // 降 19%：噪声，不拦
		{100, 80, false}, // 恰好 20%：不拦（>20% 才拦）
		{100, 79, true},  // 降 21%：拦
		{31, 9, true},    // RROrg 实锤（31→9）
		{109, 58, true},  // wabisabi926 实锤（109→58）
		{4, 1, false},    // 小源（基数 <5）不拦
		{5, 1, true},     // 基数 5 起拦
		{0, 0, false},    // 冷启动无基线
		{10, 10, false},  // 持平
		{10, 12, false},  // 增长
	}
	for _, c := range cases {
		if got := staleDrop(c.prev, c.cur); got != c.want {
			t.Fatalf("staleDrop(%d,%d)=%v want %v", c.prev, c.cur, got, c.want)
		}
	}
}

// withRawOnly 必须同时：剔除全部 jsDelivr 候选、丢掉记忆限制、放开全部镜像。
func TestRawOnlyCandidates(t *testing.T) {
	fd := NewFnDepot("s", "https://github.com/Blue-Mink/FnDepot")
	fd.withPref("moo.json")
	before := fd.candidateURLs()
	if len(before) == 0 {
		t.Fatal("记忆命中路径不该为空")
	}
	for _, u := range before {
		if indexNameOf(u) != "moo.json" {
			t.Fatalf("记忆路径混入了其它索引名: %s", u)
		}
	}
	fd.withFullMirrors().withRawOnly()
	after := fd.candidateURLs()
	if len(after) <= len(before) {
		t.Fatalf("复核路径应放开全量候选（忽略记忆 + 全部镜像）：before=%d after=%d", len(before), len(after))
	}
	hasOtherName := false
	for _, u := range after {
		if strings.Contains(u, "cdn.jsdelivr.net") {
			t.Fatalf("复核路径仍含 jsDelivr（陈旧来源不能再参与复核）: %s", u)
		}
		if indexNameOf(u) != "moo.json" {
			hasOtherName = true
		}
	}
	if !hasOtherName {
		t.Fatal("复核路径应回到全量索引名探测")
	}
}

// 索引服务三态：
//
//	0 = 全路径回「新鲜」条目（首轮）；
//	1 = 全路径回「陈旧」条目（模拟缓存滞后，复核也确认变少=真实下架）；
//	2 = 记忆命中路径回陈旧条目，其余路径 500（模拟复核不可达）。
type staleIndexSrv struct {
	*httptest.Server
	mode           int32
	freshN, staleN int
	reqs           int32
	// 0.6.377：firstFile 由 mu 保护 —— httptest.Handler 在多个连接协程里并发
	// 执行，无锁读写会在 -race 下报 DATA RACE（测试自身缺陷，非产品问题）。
	mu        sync.Mutex
	firstFile string // 首轮命中的索引文件名（第二轮的「记忆命中」路径）
}

func newStaleIndexSrv(t *testing.T, freshN, staleN int) *staleIndexSrv {
	t.Helper()
	s := &staleIndexSrv{freshN: freshN, staleN: staleN}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s.reqs, 1)
		cnt := 0
		switch atomic.LoadInt32(&s.mode) {
		case 0:
			s.mu.Lock()
			if s.firstFile == "" {
				s.firstFile = path.Base(r.URL.Path)
			}
			s.mu.Unlock()
			cnt = s.freshN
		case 1:
			cnt = s.staleN
		default:
			// 记忆命中路径（首轮命中的索引名）给陈旧条目，其余一律 500
			s.mu.Lock()
			first := s.firstFile
			s.mu.Unlock()
			if strings.HasSuffix(r.URL.Path, first) {
				cnt = s.staleN
			} else {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
		}
		var b strings.Builder
		b.WriteString(`{"app0":{"display_name":"A","version":"1.0","download_url":"https://example.com/a.fpk"}`)
		for i := 1; i < cnt; i++ {
			fmt.Fprintf(&b, `,"app%d":{"display_name":"A%d","version":"1.0","download_url":"https://example.com/a.fpk"}`, i, i)
		}
		b.WriteString("}")
		_, _ = io.WriteString(w, b.String())
	}))
	t.Cleanup(s.Close)
	return s
}

// 复核结果的三种处置（纯判定表）：这是「保留旧缓存」分支的确定性证据——
// 真机上复核失败的场景无法用单机 httptest 复现（陈旧副本与真源同主机）。
func TestDecideStale(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur int
		verifyOK  bool
		verifyCnt int
		want      staleDecision
	}{
		{"未触发复核", 100, 100, false, 0, staleAccept},
		{"降幅不足不触发", 100, 90, false, 0, staleAccept},
		{"复核更多→采信真源", 31, 9, true, 31, staleUseVerify},
		{"复核更多但小源不拦", 4, 1, true, 4, staleAccept},
		{"复核确认变少→允许下架", 10, 3, true, 3, staleAccept},
		{"复核不可用→保留旧缓存", 109, 58, false, 0, staleKeepOld},
		{"复核更多但同数→采信本轮", 10, 5, true, 5, staleAccept},
	}
	for _, c := range cases {
		if got := decideStale(c.prev, c.cur, c.verifyOK, c.verifyCnt); got != c.want {
			t.Fatalf("%s: decideStale(%d,%d,ok=%v,cnt=%d)=%v want %v",
				c.name, c.prev, c.cur, c.verifyOK, c.verifyCnt, got, c.want)
		}
	}
}

// 复核成功且确认确实变少（真实下架）→ 采信较小的结果。
func TestRefreshAcceptsRealShrink(t *testing.T) {
	srv := newStaleIndexSrv(t, 10, 3)
	cfg := &config.Config{Sources: []config.SourceRef{{Name: "t", URL: srv.URL}}}
	m := NewManager(cfg)
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("首轮刷新失败: %v", err)
	}
	atomic.StoreInt32(&srv.mode, 1)
	if err := m.Refresh("t"); err != nil {
		t.Fatalf("第二轮刷新失败: %v", err)
	}
	if got := m.cacheCount("t"); got != 3 {
		t.Fatalf("复核确认的下架应被采信（3 条），实际 %d", got)
	}
	if atomic.LoadInt32(&srv.reqs) < 3 {
		t.Fatalf("应额外发出复核请求（总请求数 >2），实际 %d", srv.reqs)
	}
}
