package config

import (
	"strings"
	"testing"
)

// 2026-11-21 FnDepot#24 / BACKLOG P0-3：索引拉取必须能跳过连续 fail 的镜像。
// 反向验证：把 GitHubMirrorOptionsHealthy 回退成直接返回 GitHubMirrorOptions()
// 时，TestGitHubMirrorOptionsHealthy24_FailMovedLast 必 FAIL。

func keysOf24(opts []MirrorOption) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Key)
	}
	return out
}

func TestGitHubMirrorOptionsHealthy24_NoInjection(t *testing.T) {
	SetGHMirrorHealth(nil)
	defer SetGHMirrorHealth(nil)
	base := GitHubMirrorOptions()
	got := GitHubMirrorOptionsHealthy()
	if len(got) != len(base) {
		t.Fatalf("数量变化：%d → %d", len(base), len(got))
	}
	for i := range base {
		if got[i].Key != base[i].Key {
			t.Fatalf("未注入时应保持原序，位置 %d: %s != %s", i, got[i].Key, base[i].Key)
		}
	}
}

func TestGitHubMirrorOptionsHealthy24_FailMovedLast(t *testing.T) {
	defer SetGHMirrorHealth(nil)
	base := GitHubMirrorOptions()
	victim, victimURL := "", ""
	for _, o := range base {
		if o.URL != "" {
			victim, victimURL = o.Key, o.URL
			break
		}
	}
	if victim == "" {
		t.Skip("清单里没有带 URL 的镜像")
	}
	SetGHMirrorHealth(func(k string) string {
		if k == victim {
			return "fail"
		}
		return "ok"
	})
	got := GitHubMirrorOptionsHealthy()
	if len(got) != len(base) {
		t.Fatalf("fail 源不应被删除：%d → %d", len(base), len(got))
	}
	// 判据：在所有「带 URL 的镜像」里，victim 必须排在最后（其后的 URL 空项不算）
	lastURLKey := ""
	for _, o := range got {
		if o.URL != "" {
			lastURLKey = o.Key
		}
	}
	if lastURLKey != victim {
		t.Fatalf("fail 源应排在带 URL 项的最后，got 最后=%s（全量 %v）", lastURLKey, keysOf24(got))
	}
	if got[len(got)-1].Key != "direct" && got[len(got)-1].URL != "" {
		t.Fatalf("URL 空项（custom/direct）位置应保持不动，got 末尾=%s", got[len(got)-1].Key)
	}
	// 其余带 URL 条目相对顺序不变（剔除 victim 后逐项比对）
	var want []string
	for _, o := range base {
		if o.Key != victim && o.URL != "" {
			want = append(want, o.Key)
		}
	}
	var have []string
	for _, o := range got {
		if o.Key != victim && o.URL != "" {
			have = append(have, o.Key)
		}
	}
	if len(have) != len(want) {
		t.Fatalf("条目数不符：%v vs %v", have, want)
	}
	for i := range want {
		if have[i] != want[i] {
			t.Fatalf("顺序被打乱：%v vs %v", have, want)
		}
	}
	_ = victimURL
}

func TestGitHubMirrorOptionsHealthy24_AllFailKeepsOrder(t *testing.T) {
	defer SetGHMirrorHealth(nil)
	base := GitHubMirrorOptions()
	SetGHMirrorHealth(func(string) string { return "fail" })
	got := GitHubMirrorOptionsHealthy()
	if len(got) != len(base) {
		t.Fatalf("全 fail 时数量变化：%d → %d", len(base), len(got))
	}
	for i := range base {
		if got[i].Key != base[i].Key {
			t.Fatalf("全 fail 应保持相对顺序，位置 %d: %s != %s", i, got[i].Key, base[i].Key)
		}
	}
}

func TestGitHubMirrorOptionsHealthy24_LimitedNotDemoted(t *testing.T) {
	defer SetGHMirrorHealth(nil)
	base := GitHubMirrorOptions()
	victim := ""
	for _, o := range base {
		if o.URL != "" {
			victim = o.Key
			break
		}
	}
	if victim == "" {
		t.Skip("清单里没有带 URL 的镜像")
	}
	SetGHMirrorHealth(func(k string) string {
		if k == victim {
			return "limited"
		}
		return "ok"
	})
	got := GitHubMirrorOptionsHealthy()
	idxWant, idxGot := -1, -1
	for i, o := range base {
		if o.Key == victim {
			idxWant = i
		}
	}
	for i, o := range got {
		if o.Key == victim {
			idxGot = i
		}
	}
	if idxGot != idxWant {
		t.Fatalf("limited 不应降级：位置 %d → %d（全量 %v）", idxWant, idxGot, keysOf24(got))
	}
}

// P1-6（2026-11-22 重排）：停服 / 限流 / 慢到不可用的源不得再出现在默认清单。
func TestMirrorList24_DeadMirrorsRemoved(t *testing.T) {
	banned := map[string]string{
		"gh-proxy-hk":    "全球 500",
		"cors-isteed":    "全球 403 挑战页",
		"wget-la":        "约 25KB/s，限速下截断",
		"felicity":       "全球 429/1027",
		"memory-echoes":  "全球 429/1027",
		"dpik":           "全球 429/1027",
		"gitproxy-mrhjx": "全球 429/1027",
		"ghproxy-cxkpro": "全球 429/1027",
		"gh-ddlc":        "全球 429/1027",
	}
	badHosts := []string{
		"hk.gh-proxy.org", "cors.isteed.cc", "wget.la", "gh.felicity.ac.cn",
		"memory-echoes.cn", "github.dpik.top", "gitproxy.mrhjx.cn",
		"cxkpro.top", "gh.ddlc.top", "gh.llkk.cc", "gh.b52m.cn",
	}
	for _, o := range GitHubMirrorOptions() {
		if why, ok := banned[o.Key]; ok {
			t.Errorf("不该出现在默认清单：%s（%s）", o.Key, why)
		}
		for _, bad := range badHosts {
			if strings.Contains(o.URL, bad) {
				t.Errorf("停服/不可用域名仍在清单：%s", o.URL)
			}
		}
	}
}

// 2026-11-22 重排：第一梯队按实测吞吐排序，且必须包含恢复的 cdn 与新增的 edgeone。
func TestMirrorList24_OrderAndRequired(t *testing.T) {
	want := []string{"gh-proxy", "gh-tbap", "edgeone-ghproxy", "cdn-ghproxy", "gh-proxy-org", "gh-dpik"}
	var got []string
	seen := map[string]bool{}
	for _, o := range GitHubMirrorOptions() {
		if o.URL == "" {
			continue
		}
		seen[o.Key] = true
		got = append(got, o.Key)
	}
	for _, k := range want {
		if !seen[k] {
			t.Errorf("清单缺少必需项：%s", k)
		}
	}
	i := 0
	for _, k := range got {
		if i < len(want) && k == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("第一梯队顺序不符：got %v want %v", got, want)
	}
}

// 重排时最容易引入重复 key / 重复 URL。
func TestMirrorList24_NoDuplicates(t *testing.T) {
	keys, urls := map[string]bool{}, map[string]bool{}
	for _, o := range GitHubMirrorOptions() {
		if keys[o.Key] {
			t.Errorf("重复 key：%s", o.Key)
		}
		keys[o.Key] = true
		if o.URL == "" {
			continue
		}
		if urls[o.URL] {
			t.Errorf("重复 URL：%s", o.URL)
		}
		urls[o.URL] = true
	}
}
