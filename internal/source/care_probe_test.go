package source

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 2026-11-26 CARE：通道级复核（误判红线 1/2 的判定核心）。
// 全部用 httptest 假服务，绝不访问 GitHub（配额与稳定性）。

// probeSrv 按 status 回应一切请求；sleep>0 时先睡（模拟超时）。
func probeSrv(t *testing.T, status int, sleep time.Duration) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sleep > 0 {
			time.Sleep(sleep)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func testProbeClient(d time.Duration) *http.Client { return &http.Client{Timeout: d} }

// 直连与 ≥1 独立镜像同时 404/410 → 确定性失效。
func TestProbeVerdictBothChannelsGone(t *testing.T) {
	for _, code := range []int{404, 410} {
		direct := probeSrv(t, code, 0)
		mirror := probeSrv(t, code, 0)
		groups := []probeGroup{
			{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
			{Name: "mirror:m1", URLs: []string{mirror.URL + "/moo.json"}},
		}
		if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CarePermanentlyDead {
			t.Fatalf("双通道 %d 应判确定性失效, got %v", code, got)
		}
	}
}

// 误判侧③：镜像 404 但 raw 直连 200 → 视为健康（不是失败）。
func TestProbeVerdictMirrorGoneDirectOK(t *testing.T) {
	direct := probeSrv(t, 200, 0)
	mirror := probeSrv(t, 404, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{mirror.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CareAlive {
		t.Fatalf("直连 200 即健康, got %v", got)
	}
}

// 直连超时 + 镜像 200 → 仍算健康（镜像能证明源活着）。
func TestProbeVerdictDirectSlowMirrorOK(t *testing.T) {
	slow := probeSrv(t, 200, 400*time.Millisecond)
	ok := probeSrv(t, 200, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{slow.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{ok.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(120*time.Millisecond), groups); got != CareAlive {
		t.Fatalf("镜像可达即健康, got %v", got)
	}
}

// 直连 404 + 镜像 5xx/429/403/超时 → 一律不确定（红线 2：暂态永不计入）。
func TestProbeVerdictTransientMirrorUncertain(t *testing.T) {
	direct := probeSrv(t, 404, 0)
	cases := []struct {
		name   string
		status int
		sleep  time.Duration
		noURL  bool
	}{
		{"5xx", 502, 0, false},
		{"429", 429, 0, false},
		{"403 私有仓", 403, 0, false},
		{"超时", 200, 400 * time.Millisecond, false},
		{"无镜像可查", 0, 0, true},
	}
	for _, c := range cases {
		groups := []probeGroup{{Name: "direct", URLs: []string{direct.URL + "/moo.json"}}}
		if !c.noURL {
			m := probeSrv(t, c.status, c.sleep)
			groups = append(groups, probeGroup{Name: "mirror:m1", URLs: []string{m.URL + "/moo.json"}})
		}
		// 0.6.575（PATCH_0575 A）：直连已确认 404/410、镜像全不可达时不再永远
		// 停在「不确定」，改为计入失败轮（CareDirectGoneMirrorsUnreachable），
		// 仍由 5 轮 + 24h 双门槛兜住抖动（见 care_gap_0575_test.go）。
		// 没有任何镜像组时无从交叉确认，保持不确定。
		want := CareDirectGoneMirrorsUnreachable
		if c.noURL {
			want = CareUncertain
		}
		if got := probeCareVerdictWith(testProbeClient(120*time.Millisecond), groups); got != want {
			t.Fatalf("%s：期望 %v, got %v", c.name, want, got)
		}
	}
}

// 组内多候选：任一 200 即活；全部 404 才算 gone（索引名回退语义）。
func TestProbeGroupStatusCandidates(t *testing.T) {
	ok := probeSrv(t, 200, 0)
	gone := probeSrv(t, 404, 0)
	c := testProbeClient(2 * time.Second)
	g := probeGroup{Name: "direct", URLs: []string{gone.URL + "/moo.json", ok.URL + "/fnpack.json"}}
	if got := probeGroupStatus(c, g); got != probeOK {
		t.Fatalf("组内任一 200 应为 OK, got %v", got)
	}
	g2 := probeGroup{Name: "direct", URLs: []string{gone.URL + "/moo.json", gone.URL + "/fnpack.json"}}
	if got := probeGroupStatus(c, g2); got != probeGone {
		t.Fatalf("组内全 404 应为 gone, got %v", got)
	}
}
