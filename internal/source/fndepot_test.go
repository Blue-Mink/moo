package source

import (
	"testing"
)

// mustJSON 测试小工具：把 map 条目转成条目 map（模拟 fnpack.json 条目）。
func mustJSON(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	return m
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.2", "1.10", true},
		{"4.8.8-1", "4.8.9", true},
		{"v1.0.1", "1.0.2", true},
		{"1.0.1", "1.0.1", false},
		{"2.0", "1.9.9", false},
		{"4.8.8-1", "4.8.8-2", true},
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestTranslateEntryV2Releases(t *testing.T) {
	// RROrg 风格：无顶层 version/download_url，版本包全在 releases；
	// 字段名变体 maintainer/categories 数组/platform 数组。
	m := mustJSON(t, map[string]any{
		"display_name": "VirtualHereServer",
		"desc":         "VirtualHere USB 服务端",
		"platform":     []any{"all"},
		"categories":   []any{"系统工具"},
		"maintainer":   "VirtualHere Pty. Ltd.",
		"maintainer_url": "https://www.virtualhere.com",
		"distributor":  "RROrg",
		"releases": map[string]any{
			"4.8.8-1": map[string]any{
				"changelog": "Initial release",
				"packages": map[string]any{
					"all": map[string]any{
						"download_url": "https://github.com/RROrg/fn-apps/releases/download/2026.09.21-1639/fn-VirtualHereServer_all_v4.8.8-1.fpk",
						"sha256":       "163f89653b84",
						"size":         3306626,
					},
				},
			},
			"4.8.7": map[string]any{
				"changelog": "old",
				"packages": map[string]any{
					"all": map[string]any{
						"download_url": "https://example.com/old.fpk",
					},
				},
			},
		},
	})
	a := translateEntry("fn-VirtualHereServer", m, "RROrg", "https://github.com/RROrg/fn-apps", "")
	if a.Version != "4.8.8-1" {
		t.Errorf("Version = %q, want 4.8.8-1（应取最高版本）", a.Version)
	}
	if a.DownloadURL == "" || a.DownloadURL == "https://example.com/old.fpk" {
		t.Errorf("DownloadURL = %q, 应取 4.8.8-1 的包", a.DownloadURL)
	}
	if a.Sha256 != "163f89653b84" {
		t.Errorf("Sha256 = %q", a.Sha256)
	}
	if a.SizeBytes != 3306626 {
		t.Errorf("SizeBytes = %d", a.SizeBytes)
	}
	if a.Author != "VirtualHere Pty. Ltd." {
		t.Errorf("Author = %q, 应从 maintainer 变体取", a.Author)
	}
	if a.Labels != "系统工具" {
		t.Errorf("Labels = %q, 应从 categories 数组取", a.Labels)
	}
	if a.Platform != "all" {
		t.Errorf("Platform = %q, 应从 platform 数组取", a.Platform)
	}
	// 顶层 Changelog 不回填（避免与 ReleaseChangelogs 在详情里重复成
	// 版本为空的条目）；版本化说明全部在 ReleaseChangelogs。
	if a.Changelog != "" {
		t.Errorf("Changelog 应为空（releases 说明不进顶层）: %q", a.Changelog)
	}
	if a.ReleaseChangelogs["4.8.8-1"] != "Initial release" {
		t.Errorf("ReleaseChangelogs 缺失最新版说明")
	}
	if a.ReleaseChangelogs["4.8.7"] != "old" {
		t.Errorf("ReleaseChangelogs 缺失旧版本说明")
	}
}

func TestTranslateEntryV1Flat(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"display_name": "code-server",
		"desc":         "VS Code 在线版",
		"version":      "1.0.1",
		"download_url": "https://github.com/RROrg/fn-apps/releases/download/x/fn-codeserver_v1.0.1.fpk",
		"labels":       "工具",
		"author":       "Ing",
		"size":         "0.026",
		"isdocker":     "false",
		"download_count": 1234,
	})
	a := translateEntry("fn-codeserver", m, "RROrg", "https://github.com/RROrg/fn-apps", "")
	if a.Version != "1.0.1" || a.DownloadURL == "" {
		t.Errorf("V1 平铺版本/链接丢失: %q %q", a.Version, a.DownloadURL)
	}
	if a.DownloadCount != 1234 {
		t.Errorf("DownloadCount = %d", a.DownloadCount)
	}
	if a.SizeMB != "0.026" {
		t.Errorf("SizeMB = %q", a.SizeMB)
	}
	if a.IsDocker == "true" {
		t.Errorf("IsDocker 误判: %q", a.IsDocker)
	}
}

func TestTranslateEntryArchDiff(t *testing.T) {
	m := mustJSON(t, map[string]any{
		"version": "1.2.3",
		"arch_diff": map[string]any{
			"x86": map[string]any{"download_url": "https://example.com/x86.fpk", "sha256": "abc"},
			"arm": map[string]any{"download_url": "https://example.com/arm.fpk"},
		},
	})
	a := translateEntry("mediahub", m, "FnDepot", "https://github.com/DinDing1/FnDepot", "")
	if a.DownloadURL == "" {
		t.Fatal("arch_diff 回退未生效")
	}
	// 当前架构包优先；无论 x86/arm 构建都应拿到对应链接（不为空）
	if a.DownloadURL != "https://example.com/x86.fpk" && a.DownloadURL != "https://example.com/arm.fpk" {
		t.Errorf("arch_diff 选包错误: %q", a.DownloadURL)
	}
}

func TestTranslateEntryRepoConvention(t *testing.T) {
	// 无 releases/平铺链接/arch_diff，但有版本 → 仓库内 <appname>/<appname>.fpk
	m := mustJSON(t, map[string]any{"version": "0.9.0"})
	a := translateEntry("fpk-demo", m, "S", "https://github.com/moxyis/FnDepot", "")
	want := "https://raw.githubusercontent.com/moxyis/FnDepot/main/fpk-demo/fpk-demo.fpk"
	if a.DownloadURL != want {
		t.Errorf("约定直链 = %q, want %q", a.DownloadURL, want)
	}
	// Docker 应用不走约定
	m2 := mustJSON(t, map[string]any{"version": "0.9.0", "is_docker": true})
	a2 := translateEntry("docker-demo", m2, "S", "https://github.com/moxyis/FnDepot", "")
	if a2.DownloadURL != "" {
		t.Errorf("Docker 应用不应构造约定直链: %q", a2.DownloadURL)
	}
	// 非 GitHub 源不构造
	a3 := translateEntry("x", mustJSON(t, map[string]any{"version": "1.0"}), "S", "https://example.com", "")
	if a3.DownloadURL != "" {
		t.Errorf("非 GitHub 源不应构造约定直链: %q", a3.DownloadURL)
	}
}

func TestTranslateEntryTopLevelWinsOverReleases(t *testing.T) {
	// 顶层已有 version+download_url 时，releases 只补按版本说明，不覆盖链接
	m := mustJSON(t, map[string]any{
		"version":      "9.9.9",
		"download_url": "https://example.com/top.fpk",
		"releases": map[string]any{
			"1.0.0": map[string]any{
				"changelog": "c1",
				"packages":  map[string]any{"all": map[string]any{"download_url": "https://example.com/r.fpk"}},
			},
		},
	})
	a := translateEntry("top", m, "S", "https://github.com/o/r", "")
	if a.Version != "9.9.9" || a.DownloadURL != "https://example.com/top.fpk" {
		t.Errorf("顶层字段应优先: %q %q", a.Version, a.DownloadURL)
	}
	if a.ReleaseChangelogs["1.0.0"] != "c1" {
		t.Errorf("releases 说明仍应收集")
	}
}

// TestIndexBaseDir 0.6.271：相对资源 base = 索引文件所在目录（旧实现只剥
// /fnpack.json，moo.json 源的 base 残留文件名 → 相对资源 404）。
func TestIndexBaseDir(t *testing.T) {
	cases := map[string]string{
		"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json":    "https://raw.githubusercontent.com/Blue-Mink/FnDepot/main",
		"https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json": "https://raw.githubusercontent.com/Blue-Mink/FnDepot/main",
		"https://github.com/Blue-Mink/FnDepot/fnpack.json":                    "https://github.com/Blue-Mink/FnDepot",
		"http://192.0.2.15:3033/bluemink/moo/moo.json":                      "http://192.0.2.15:3033/bluemink/moo",
		// 地址本身是目录（末段非 .json）→ 原样
		"https://example.com/store/":   "https://example.com/store/",
		"https://example.com/store":    "https://example.com/store",
		"http://127.0.0.1:18899":       "http://127.0.0.1:18899",
		// 根目录索引（无路径目录）→ 主机根（相对资源直接拼主机根下）
		"https://example.com/fnpack.json": "https://example.com",
	}
	for in, want := range cases {
		if got := indexBaseDir(in); got != want {
			t.Errorf("indexBaseDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestFetchJSONRealErrorTransparent 0.6.271：直链不可达时透传真实错误
//（旧实现在 fallback 为空时报「无候选地址」，把 404/连接失败/登录墙吞掉）。
func TestFetchJSONRealErrorTransparent(t *testing.T) {
	f := NewFnDepot("t", "http://127.0.0.1:1/x.json") // 1 端口必不可达
	if _, err := f.Fetch(); err == nil {
		t.Fatal("应当失败")
	} else if err.Error() == "无候选地址" {
		t.Fatal("真实错误被吞掉，仍报无候选地址")
	}
}
