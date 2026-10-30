package config

// 回归测试：自定义加速源合并逻辑。
// 对应功能：设置页可增删自定义 GitHub/Docker 加速源，与内置清单合并后
// 供源同步竞速（buildCandidates）、健康探测、下载候选三处消费。

import (
	"strings"
	"testing"
)

func resetHook(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetCustomMirrorProvider(nil) })
}

func TestMergeWithoutProvider(t *testing.T) {
	resetHook(t)
	opts := mergeCustomMirrors(GitHubMirrorOptions())
	if len(opts) != len(GitHubMirrorOptions()) {
		t.Fatalf("无 provider 时不应改变清单：got %d want %d", len(opts), len(GitHubMirrorOptions()))
	}
}

func TestMergeAppendsCustomBeforeSpecial(t *testing.T) {
	resetHook(t)
	base := len(GitHubMirrorOptions()) // provider 为 nil 时 = 纯内置数量
	SetCustomMirrorProvider(func() []MirrorOption {
		return []MirrorOption{
			{Key: "custom-1", Label: "我的源A", URL: "https://a.example.com/"},
			{Key: "custom-2", Label: "我的源B", URL: "https://b.example.com/"},
		}
	})
	opts := mergeCustomMirrors(GitHubMirrorOptions())

	// 总数 = 内置 + 2
	if len(opts) != base+2 {
		t.Fatalf("数量不对：got %d want %d", len(opts), base+2)
	}
	// 自定义源应在 auto 之后、custom/direct 之前（特殊项沉底）
	var idxCustom1, idxAuto, idxDirect = -1, -1, -1
	for i, o := range opts {
		switch o.Key {
		case "custom-1":
			idxCustom1 = i
		case "auto":
			idxAuto = i
		case "direct":
			idxDirect = i
		}
	}
	if idxCustom1 < 0 {
		t.Fatal("custom-1 未出现在合并结果中")
	}
	if !(idxAuto < idxCustom1 && idxCustom1 < idxDirect) {
		t.Fatalf("排序错误：auto=%d custom-1=%d direct=%d（期望 auto < custom < direct）", idxAuto, idxCustom1, idxDirect)
	}
}

func TestMergeDedupsKeyAndURL(t *testing.T) {
	resetHook(t)
	SetCustomMirrorProvider(func() []MirrorOption {
		return []MirrorOption{
			{Key: "gh-proxy", Label: "重复key", URL: "https://x.example.com/"}, // 与内置 key 撞
			{Key: "custom-1", Label: "重复URL", URL: "https://GH-Proxy.com"},   // 与内置 URL 撞（大小写+尾斜杠归一）
			{Key: "custom-2", Label: "正常", URL: "https://ok.example.com/"},
		}
	})
	opts := mergeCustomMirrors(GitHubMirrorOptions())
	// 只有 custom-2 进得来（去重后 = 纯内置 + 1）
	base := len(opts) - 1
	if len(opts) != base+1 {
		t.Fatalf("去重失败：got %d want %d", len(opts), base+1)
	}
	for _, o := range opts {
		if o.Key == "custom-1" {
			t.Fatal("URL 撞车的条目不应进入")
		}
		if o.Key == "gh-proxy" && o.Label == "重复key" {
			t.Fatal("key 撞车的条目不应覆盖内置项")
		}
	}
}

func TestNilProviderStopsServing(t *testing.T) {
	resetHook(t)
	SetCustomMirrorProvider(func() []MirrorOption {
		return []MirrorOption{{Key: "custom-1", Label: "x", URL: "https://x.example.com/"}}
	})
	SetCustomMirrorProvider(nil)
	opts := mergeCustomMirrors(GitHubMirrorOptions())
	if len(opts) != len(GitHubMirrorOptions()) {
		t.Fatal("清除 provider 后应回到纯内置清单")
	}
}

func TestNormalizeMirrorURL(t *testing.T) {
	if normalizeMirrorURL("https://GH-Proxy.com/") != normalizeMirrorURL("https://gh-proxy.com") {
		t.Fatal("大小写与尾斜杠应归一")
	}
	if normalizeMirrorURL("https://A.Example.COM/x") != strings.ToLower(normalizeMirrorURL("https://A.Example.COM/x")) {
		t.Fatal("应转小写")
	}
}
