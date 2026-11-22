package api

import (
	"strings"
	"testing"

	"moo/internal/config"
)

// 批次⑤（P2-2）：候选补位按健康准入（含 fail-open）。
// 反向验证：去掉 fillMirrorPrefixes 里 fail/limited 的跳过分支 → 本文件
// TestFillMirrorPrefixes24_SkipFail / SkipLimited 必 FAIL；去掉「未测源最多 1 个」
// 的上限 → TestFillMirrorPrefixes24_UntestedCap 必 FAIL。

// hf24Real 取当前清单里带 URL 的源（含 conversun——补位循环本来就看全清单，
// 这里保持与生产一致的集合），顺序即 GitHubMirrorOptions 顺序。
func hf24Real(t *testing.T) (keys, urls []string) {
	t.Helper()
	for _, o := range config.GitHubMirrorOptions() {
		if o.URL == "" {
			continue
		}
		keys = append(keys, o.Key)
		urls = append(urls, o.URL)
	}
	if len(urls) < 5 {
		t.Fatalf("清单源太少，测试前提不成立：%v", urls)
	}
	return keys, urls
}

// hf24Status 构造「按 key 返回固定状态」的注入函数。
func hf24Status(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func hf24Join(in []string) string { return strings.Join(in, "|") }

// (a) 无健康数据（未注入）→ 与旧行为一致：按清单序补 3 个。
func TestFillMirrorPrefixes24_NoHealthData(t *testing.T) {
	_, urls := hf24Real(t)
	config.SetGHMirrorHealth(nil)
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 3 || hf24Join(got) != hf24Join(urls[:3]) {
		t.Fatalf("无健康数据应按清单序补 3 个，want %v got %v", urls[:3], got)
	}
	again := fillMirrorPrefixes([]string{urls[0]}, 3)
	if len(again) != 3 || again[0] != urls[0] || hf24Join(again) != hf24Join(urls[:3]) {
		t.Fatalf("已有前缀不应重复且需补足，want %v got %v", urls[:3], again)
	}
}

// (a2) 注入但全部为空（从未测过）→ known=false，行为同 (a)。
func TestFillMirrorPrefixes24_AllUnknownLikeNoData(t *testing.T) {
	_, urls := hf24Real(t)
	config.SetGHMirrorHealth(func(string) string { return "" })
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 3 || hf24Join(got) != hf24Join(urls[:3]) {
		t.Fatalf("无测速数据应按清单序补 3 个，want %v got %v", urls[:3], got)
	}
}

// (b) 全部 ok → 3 个，按清单序。
func TestFillMirrorPrefixes24_AllOK(t *testing.T) {
	keys, urls := hf24Real(t)
	m := map[string]string{}
	for _, k := range keys {
		m[k] = "ok"
	}
	config.SetGHMirrorHealth(hf24Status(m))
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 3 || hf24Join(got) != hf24Join(urls[:3]) {
		t.Fatalf("全部 ok 应按清单序补 3 个，want %v got %v", urls[:3], got)
	}
}

// (c) fail 的源不进补位结果（刻意让只有 2 个非 fail，否则会被 fail-open 掩盖）。
func TestFillMirrorPrefixes24_SkipFail(t *testing.T) {
	keys, urls := hf24Real(t)
	m := map[string]string{}
	for i, k := range keys {
		if i < 2 {
			m[k] = "ok"
			continue
		}
		m[k] = "fail"
	}
	config.SetGHMirrorHealth(hf24Status(m))
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 2 || hf24Join(got) != hf24Join(urls[:2]) {
		t.Fatalf("fail 源应被跳过，want %v got %v", urls[:2], got)
	}
	for i, u := range urls {
		if i < 2 {
			continue
		}
		for _, g := range got {
			if g == u {
				t.Fatalf("fail 源 %s 不应进入补位结果：%v", u, got)
			}
		}
	}
}

// (d) limited 的源同样不进补位结果。
func TestFillMirrorPrefixes24_SkipLimited(t *testing.T) {
	keys, urls := hf24Real(t)
	m := map[string]string{}
	for i, k := range keys {
		if i == 0 {
			m[k] = "ok"
			continue
		}
		m[k] = "limited"
	}
	config.SetGHMirrorHealth(hf24Status(m))
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 1 || got[0] != urls[0] {
		t.Fatalf("limited 源应被跳过，want %v got %v", urls[:1], got)
	}
}

// (e) 从未测过的源最多补 1 个（1 个 ok + 其余未测 → ok + 1 未测就停）。
func TestFillMirrorPrefixes24_UntestedCap(t *testing.T) {
	keys, urls := hf24Real(t)
	m := map[string]string{keys[0]: "ok"}
	config.SetGHMirrorHealth(hf24Status(m))
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 2 || hf24Join(got) != hf24Join(urls[:2]) {
		t.Fatalf("未测源最多补 1 个，want %v got %v", urls[:2], got)
	}
}

// (f) 全部 fail → fail-open：仍按清单原序补到 3 个（绝不退化成只有直连）。
func TestFillMirrorPrefixes24_AllFailOpen(t *testing.T) {
	keys, urls := hf24Real(t)
	m := map[string]string{}
	for _, k := range keys {
		m[k] = "fail"
	}
	config.SetGHMirrorHealth(hf24Status(m))
	defer config.SetGHMirrorHealth(nil)

	got := fillMirrorPrefixes(nil, 3)
	if len(got) != 3 || hf24Join(got) != hf24Join(urls[:3]) {
		t.Fatalf("全部 fail 应 fail-open 按清单序补 3 个，want %v got %v", urls[:3], got)
	}
}
