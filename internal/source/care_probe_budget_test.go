package source

// 2026-11-27（F10 / 审计 N6）：复核探测的单源总预算。
//
// 审计实测：11 个镜像组逐个探（8s/请求），一轮复核 ~77s，把刷新轮拖成分钟级。
// 期望：预算用尽立刻放弃本轮（结论=不确定，绝不判死），而不是把剩下的通道打完。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeCareVerdictDeadlineAbortsOnBudget(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gone.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer slow.Close()

	// 直连确定性 404 + 11 个镜像组各慢 300ms：无预算时最坏 11×300ms ≈ 3.3s
	//（生产是 11×8s ≈ 96s），预算 250ms 必须在第一个慢镜像之后就放弃。
	groups := []probeGroup{{Name: "direct", URLs: []string{gone.URL + "/moo.json"}}}
	for i := 0; i < 11; i++ {
		groups = append(groups, probeGroup{
			Name: fmt.Sprintf("mirror:m%d", i),
			URLs: []string{slow.URL + "/moo.json"},
		})
	}
	start := time.Now()
	v, timedOut := probeCareVerdictDeadline(slow.Client(), groups, time.Now().Add(250*time.Millisecond))
	elapsed := time.Since(start)

	if v != CareUncertain {
		t.Fatalf("超预算必须记不确定（不得判死），实际 verdict=%v", v)
	}
	if !timedOut {
		t.Error("超预算必须回报 timedOut=true（调用方据此记日志）")
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("预算没生效：耗时 %s（应 ≈250ms）", elapsed)
	}
}

// 预算充裕时行为必须与旧口径一致（否则等于把「判死」能力关掉了）。
func TestProbeCareVerdictDeadlineStillDecidesWhenBudgetLeft(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer gone.Close()
	groups := []probeGroup{
		{Name: "direct", URLs: []string{gone.URL + "/moo.json"}},
		{Name: "mirror:m0", URLs: []string{gone.URL + "/moo.json"}},
	}
	v, timedOut := probeCareVerdictDeadline(gone.Client(), groups, time.Now().Add(5*time.Second))
	if v != CarePermanentlyDead || timedOut {
		t.Fatalf("直连+镜像双 404 仍应判死（误判红线不放松），got %v timedOut=%v", v, timedOut)
	}
}
