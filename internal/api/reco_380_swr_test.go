package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.380（用户令「发现页的阻塞怎么回事」）：TTL 过期后**不得**再让请求
// 路径同步等一个 15s 级超时链。以下用例锁死新契约（全部 httptest，不触外网）。

// blockingListServer 一个「连上但永不回包」的清单源，release 关闭即放行。
func blockingListServer(t *testing.T) (url string, release chan struct{}) {
	t.Helper()
	release = make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})
	return ts.URL + "/recommend_list.txt", release
}

// TestRecoStaleReturnsImmediately_380 有旧值 + TTL 过期 → 立刻回旧值（SWR）。
func TestRecoStaleReturnsImmediately_380(t *testing.T) {
	old := recoColdFetchBudget
	recoColdFetchBudget = 200 * time.Millisecond
	defer func() { recoColdFetchBudget = old }()

	url, _ := blockingListServer(t)
	s := &Server{Cfg: &config.Config{RecommendListURL: url}}
	s.recoMu.Lock()
	s.recoEntries = []recommendEntry{{AppName: "stale-app", Source: "x"}}
	s.recoAt = time.Now().Add(-recoListTTL - time.Minute)
	s.recoLoaded, s.recoURL = true, url
	s.recoMu.Unlock()

	start := time.Now()
	got, fresh := s.recommendEntries(context.Background())
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("有旧值时请求路径不得阻塞，实际耗时 %s", elapsed)
	}
	if fresh {
		t.Fatalf("旧值不算新鲜，应返回 fresh=false")
	}
	if len(got) != 1 || got[0].AppName != "stale-app" {
		t.Fatalf("应返回上一次成功值，实际 %+v", got)
	}
}

// TestRecoColdStartBounded_380 冷启动（无任何缓存）也必须有硬预算，
// 超预算立刻回内嵌清单，页面不等网络。
func TestRecoColdStartBounded_380(t *testing.T) {
	old := recoColdFetchBudget
	recoColdFetchBudget = 200 * time.Millisecond
	defer func() { recoColdFetchBudget = old }()

	url, _ := blockingListServer(t)
	s := &Server{Cfg: &config.Config{RecommendListURL: url}}

	start := time.Now()
	got, fresh := s.recommendEntries(context.Background())
	elapsed := time.Since(start)
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("冷启动超预算后必须立刻返回，实际耗时 %s", elapsed)
	}
	if fresh {
		t.Fatalf("超时回内嵌兜底不算新鲜")
	}
	if len(got) == 0 {
		t.Fatalf("必须回内嵌清单兜底（不能空列表）")
	}
}

// TestRecoCandidatesMirrorFirst_380 清单候选：健康镜像在前、直连最后，
// limited 镜像不参与。
func TestRecoCandidatesMirrorFirst_380(t *testing.T) {
	const u = "https://raw.githubusercontent.com/Blue-Mink/moo/main/recommend_list.txt"
	if got := recoListCandidates(u, nil); len(got) != 1 || got[0] != u {
		t.Fatalf("无监控时只应回直连，实际 %v", got)
	}
	m := &mirrorMonitor{ghStats: []MirrorStat{
		{Key: "gh-proxy-hk", LatencyMS: 200, Status: "ok"},
		{Key: "gh-proxy", LatencyMS: 300, Status: "ok"},
		{Key: "ghproxy-cxkpro", LatencyMS: 100, Status: "limited"},
	}}
	got := recoListCandidates(u, m)
	if len(got) != 3 {
		t.Fatalf("应 2 镜像 + 1 直连，实际 %v", got)
	}
	if !strings.HasSuffix(got[0], u) || got[0] == u {
		t.Fatalf("第一个候选应是镜像前缀 + 原地址，实际 %s", got[0])
	}
	if !strings.Contains(got[0], "gh-proxy.org") {
		t.Fatalf("延迟最低的 ok 镜像应排第一，实际 %s", got[0])
	}
	if got[len(got)-1] != u {
		t.Fatalf("直连必须排最后，实际 %v", got)
	}
	for _, c := range got {
		if strings.Contains(c, "cxkpro") {
			t.Fatalf("limited 镜像不该参与，实际 %v", got)
		}
	}
}

// TestRecoFallbackBackoff_380 首次即失败（回退内嵌清单）后，后续请求必须秒回，
// 不能每个请求都走冷启动同步抓取（否则发现页每次都要等满预算）。
func TestRecoFallbackBackoff_380(t *testing.T) {
	oldB, oldF := recoColdFetchBudget, recoFallbackTTL
	recoColdFetchBudget, recoFallbackTTL = 300*time.Millisecond, 2*time.Minute
	defer func() { recoColdFetchBudget, recoFallbackTTL = oldB, oldF }()

	url, _ := blockingListServer(t)
	s := &Server{Cfg: &config.Config{RecommendListURL: url}}
	_, _ = s.recommendEntries(context.Background()) // 第一次：最多 300ms

	start := time.Now()
	got, fresh := s.recommendEntries(context.Background())
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("退避期内应秒回内嵌清单，实际耗时 %s", elapsed)
	}
	if fresh || len(got) == 0 {
		t.Fatalf("应回内嵌兜底且 fresh=false，实际 len=%d fresh=%v", len(got), fresh)
	}
}

// TestRecommendedRefreshForcesFetch_380 管理端「强制刷新」必须真去拉（同步），
// 不能因为 SWR 命中旧值就把旧值当结果返回。
func TestRecommendedRefreshForcesFetch_380(t *testing.T) {
	cat := []AppInfo{recCard("new-app", "s")}
	body := "new-app\n"
	s := recommendTestServer(t, cat, "", &body)
	// 预置一份「旧地址下的旧值」，模拟 TTL 内缓存
	s.recoMu.Lock()
	s.recoEntries = []recommendEntry{{AppName: "old-app"}}
	s.recoAt = time.Now()
	s.recoLoaded, s.recoURL = true, s.recommendListURL()
	s.recoMu.Unlock()

	rec := httptest.NewRecorder()
	s.recommendedRefresh(rec, httptest.NewRequest(http.MethodPost, "/api/recommended/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", rec.Code)
	}
	var out struct {
		OK      bool     `json:"ok"`
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	if len(out.Entries) != 1 || out.Entries[0] != "new-app" {
		t.Fatalf("强制刷新必须返回远程最新清单，实际 %+v", out.Entries)
	}
	if !out.OK {
		t.Fatalf("ok 应为 true")
	}
}

// TestRecoCandidatesNonGitHub_380 非 GitHub 地址不做镜像改写。
func TestRecoCandidatesNonGitHub_380(t *testing.T) {
	const u = "https://example.com/recommend_list.txt"
	m := &mirrorMonitor{ghStats: []MirrorStat{{Key: "gh-proxy-hk", LatencyMS: 100, Status: "ok"}}}
	if got := recoListCandidates(u, m); len(got) != 1 || got[0] != u {
		t.Fatalf("非 GitHub 地址只应回原地址，实际 %v", got)
	}
}
