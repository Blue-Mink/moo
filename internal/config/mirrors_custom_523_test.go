package config

import (
	"strings"
	"testing"
)

// 0.6.523（用户令）：加速源清单同步只替换「默认清单」，
// 不得覆盖 / 吞掉用户手动添加的自定义加速源。
func TestRemoteMirrorSyncKeepsCustomMirrors(t *testing.T) {
	SetHiddenMirrors(nil)
	SetCustomMirrors(
		[]string{"https://mine.example/gh/"},
		[]string{"https://mine.example/dk/"},
	)
	t.Cleanup(func() {
		SetCustomMirrors(nil, nil)
		SetRemoteMirrorOptions(nil, nil)
		SetHiddenMirrors(nil)
	})

	// 远端清单里即便出现 custom / custom-N 也不能顶掉用户自己的地址
	SetRemoteMirrorOptions([]MirrorOption{
		{Key: "auto", Label: "智能"},
		{Key: "remote-gh", Label: "Remote GH", URL: "https://remote.example/gh/"},
		{Key: "custom", Label: "自定义", URL: "https://remote.example/custom/"},
		{Key: "custom-2", Label: "自定义 2", URL: "https://remote.example/custom-2/"},
		{Key: "direct", Label: "直连"},
	}, []MirrorOption{
		{Key: "auto", Label: "智能"},
		{Key: "remote-dk", Label: "Remote DK", URL: "https://remote.example/dk/"},
		{Key: "custom", Label: "自定义", URL: "https://remote.example/dkcustom/"},
		{Key: "direct", Label: "直连"},
	})

	gh := GitHubMirrorOptions()
	gotGH, countCustom := "", 0
	for _, o := range gh {
		if o.Key == "custom" {
			gotGH = o.URL
		}
		if o.Key == "custom" || strings.HasPrefix(o.Key, "custom-") {
			countCustom++
		}
	}
	if gotGH != "https://mine.example/gh/" {
		t.Fatalf("远端清单覆盖了自定义 GitHub 源：custom.URL=%q", gotGH)
	}
	if countCustom != 1 {
		t.Fatalf("自定义 GitHub 源数量 = %d，期望 1（远端 custom-N 应被忽略）", countCustom)
	}

	gotDK := ""
	for _, o := range DockerMirrorOptions() {
		if o.Key == "custom" {
			gotDK = o.URL
		}
	}
	if gotDK != "https://mine.example/dk/" {
		t.Fatalf("远端清单覆盖了自定义 Docker 源：custom.URL=%q", gotDK)
	}

	// 再同步一次（远端清单完全换一批）——自定义源仍需原样保留
	SetRemoteMirrorOptions([]MirrorOption{
		{Key: "auto", Label: "智能"},
		{Key: "remote-2", Label: "Remote 2", URL: "https://remote2.example/"},
		{Key: "direct", Label: "直连"},
	}, nil)
	gotGH = ""
	for _, o := range GitHubMirrorOptions() {
		if o.Key == "custom" {
			gotGH = o.URL
		}
	}
	if gotGH != "https://mine.example/gh/" {
		t.Fatalf("第二次同步后自定义 GitHub 源丢失：%q", gotGH)
	}

	// 「删除默认源」只作用于默认清单条目，不影响自定义源
	SetHiddenMirrors([]string{"remote-2"})
	gh = GitHubMirrorOptions()
	kept := false
	for _, o := range gh {
		if o.Key == "remote-2" {
			t.Fatalf("被屏蔽的默认源仍在列表：%v", gh)
		}
		if o.Key == "custom" && o.URL == "https://mine.example/gh/" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("屏蔽默认源后自定义源丢失：%v", gh)
	}
}
