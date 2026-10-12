package api

import (
	"fmt"
	"testing"
)

// 321R7：发现页推荐卡稳定随机选卡（同一天内刷新不换卡）。
func TestPickWithDailySeed_Deterministic(t *testing.T) {
	mk := func() []AppInfo {
		out := make([]AppInfo, 0, 100)
		for i := 0; i < 100; i++ {
			out = append(out, AppInfo{AppName: fmt.Sprintf("app-%03d", i), Key: fmt.Sprintf("app-%03d", i)})
		}
		return out
	}
	a := pickWithDailySeed(mk(), 5)
	b := pickWithDailySeed(mk(), 5)
	if len(a) != 5 {
		t.Fatalf("应选出 5 个，实际 %d", len(a))
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Fatalf("同一天两次选卡结果不一致: pos=%d a=%s b=%s", i, a[i].Key, b[i].Key)
		}
	}
}

func TestPickWithDailySeed_SubsetNoDup(t *testing.T) {
	pool := mkPoolForSeedTest(60)
	got := pickWithDailySeed(pool, 7)
	seen := map[string]bool{}
	for _, a := range got {
		if seen[a.Key] {
			t.Fatalf("选出重复应用: %s", a.Key)
		}
		seen[a.Key] = true
		if !containsKey(pool, a.Key) {
			t.Fatalf("选出了池外应用: %s", a.Key)
		}
	}
}

func TestDaySeedInt_Stable(t *testing.T) {
	if daySeedInt("moo-reco:2026-10-12") != daySeedInt("moo-reco:2026-10-12") {
		t.Fatal("同输入种子必须相同")
	}
	if daySeedInt("moo-reco:2026-10-12") == daySeedInt("moo-reco:2026-10-13") {
		t.Fatal("不同输入不应撞种子（本例）")
	}
}

func mkPoolForSeedTest(n int) []AppInfo {
	out := make([]AppInfo, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, AppInfo{AppName: fmt.Sprintf("seed-app-%02d", i), Key: fmt.Sprintf("seed-app-%02d", i)})
	}
	return out
}

func containsKey(pool []AppInfo, k string) bool {
	for _, a := range pool {
		if a.Key == k {
			return true
		}
	}
	return false
}
