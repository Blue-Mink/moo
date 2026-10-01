package source

import (
	"strings"
	"testing"
)

// moo.json 必须排在 fnpack.json 之前（同仓库两种索引共存时，Moo 读自己那份）。
func TestBuildCandidatesPrefersMooJson(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"github 仓库", "https://github.com/Blue-Mink/FnDepot"},
		{"普通目录", "https://example.com/apps"},
		{"带协议无 .json", "http://192.168.1.10:8080/store"},
	}
	for _, c := range cases {
		got := buildCandidates(c.url)
		if len(got) == 0 {
			t.Fatalf("%s: 候选为空", c.name)
		}
		iMoo, iFn := -1, -1
		for i, u := range got {
			if iMoo < 0 && strings.HasSuffix(u, "/moo.json") {
				iMoo = i
			}
			if iFn < 0 && strings.HasSuffix(u, "/fnpack.json") {
				iFn = i
			}
		}
		if iMoo < 0 {
			t.Fatalf("%s: 未生成 moo.json 候选：%v", c.name, got)
		}
		if iFn < 0 {
			t.Fatalf("%s: 未保留 fnpack.json 回退（FnDepot 兼容）：%v", c.name, got)
		}
		if iMoo > iFn {
			t.Fatalf("%s: moo.json 应早于 fnpack.json，实际 %d > %d", c.name, iMoo, iFn)
		}
	}
}

// .json 直链必须原样使用（内容自适应 V1/V2），不追加后缀。
func TestBuildCandidatesJSONDirect(t *testing.T) {
	got := buildCandidates("https://example.com/store/moo.json")
	if len(got) != 1 || got[0] != "https://example.com/store/moo.json" {
		t.Fatalf("直链应原样返回，实际 %v", got)
	}
	got2 := buildCandidates("https://example.com/store/fndepot.json")
	if len(got2) != 1 || got2[0] != "https://example.com/store/fndepot.json" {
		t.Fatalf("FnDepot 直链应原样返回，实际 %v", got2)
	}
}

// GitHub 仓库要带 main/master 两个分支、镜像与 jsDelivr 兜底，且无重复项。
func TestBuildCandidatesGitHubCoverage(t *testing.T) {
	got := buildCandidates("https://github.com/Blue-Mink/FnDepot")
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		"raw.githubusercontent.com/Blue-Mink/FnDepot/main/moo.json",
		"raw.githubusercontent.com/Blue-Mink/FnDepot/master/moo.json",
		"cdn.jsdelivr.net/gh/Blue-Mink/FnDepot/moo.json",
		"raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺少候选 %s\n实际：%v", want, got)
		}
	}
	seen := map[string]bool{}
	for _, u := range got {
		if seen[u] {
			t.Fatalf("候选重复：%s", u)
		}
		seen[u] = true
	}
}

func TestAppTypeOf(t *testing.T) {
	cases := []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"app_type": "docker"}, "true"},
		{map[string]any{"app_type": "Docker"}, "true"},
		{map[string]any{"app_type": "fpk"}, ""},
		{map[string]any{"app_type": "native"}, ""},
		{map[string]any{"app_type": "tpk"}, ""},
		{map[string]any{"isdocker": "true"}, "true"},   // FnDepot 字符串写法
		{map[string]any{"is_docker": true}, "true"},    // FnDepot bool 写法
		{map[string]any{"isdocker": "false"}, ""},      //
		{map[string]any{"app_type": "fpk", "isdocker": "true"}, ""}, // Moo 原生优先
		{map[string]any{"app_type": "weird", "isdocker": "true"}, "true"}, // 未知值回退
		{map[string]any{}, ""},
	}
	for i, c := range cases {
		if got := appTypeOf(c.in); got != c.want {
			t.Fatalf("case %d: appTypeOf(%v) = %q，期望 %q", i, c.in, got, c.want)
		}
	}
}

// moo.json（V2 包裹 + Moo 扩展字段）应被现有解析器完整吃下。
func TestParseMooJsonSample(t *testing.T) {
	doc := []byte(`{
	  "schema_version": "moo/v1",
	  "source_info": {"name": "示例仓", "distributor": "Blue-Mink"},
	  "apps": {
	    "demo-app": {
	      "display_name": "示例应用",
	      "version": "1.2.3",
	      "app_type": "docker",
	      "platform": ["x86", "arm"],
	      "size": 12.3,
	      "sha256": "abc123",
	      "download_url": "demo-app-1.2.3.fpk",
	      "icon_url": "icon.png",
	      "preview_urls": ["1.png", "2.png"],
	      "desc": "一句话简介",
	      "tags": ["网络工具"],
	      "author": "someone",
	      "distributor": "Blue-Mink",
	      "changelog": "首次发布"
	    }
	  }
	}`)
	entries, err := ParseFnpack(doc)
	if err != nil {
		t.Fatalf("解析 moo.json 失败: %v", err)
	}
	m, ok := entries["demo-app"]
	if !ok {
		t.Fatalf("未解析到 demo-app，得到 %v", entries)
	}
	app := translateEntry("demo-app", m, "示例仓", "https://example.com", "https://example.com")
	if app.DisplayName != "示例应用" || app.Version != "1.2.3" {
		t.Fatalf("基础字段错: %+v", app)
	}
	if app.IsDocker != "true" {
		t.Fatalf("app_type=docker 应判为容器应用，实际 %q", app.IsDocker)
	}
	if app.Labels != "网络工具" {
		t.Fatalf("tags 别名未生效：%q", app.Labels)
	}
	if app.Sha256 != "abc123" {
		t.Fatalf("条目级 sha256 未读取：%q", app.Sha256)
	}
	if len(app.PreviewURLs) != 2 {
		t.Fatalf("preview_urls 未读取：%v", app.PreviewURLs)
	}
	if !strings.HasPrefix(app.DownloadURL, "https://example.com/") {
		t.Fatalf("相对 download_url 未按 base 补全：%q", app.DownloadURL)
	}
}

// FnDepot 老格式（V1 平铺 + isdocker 字符串）必须继续可用。
func TestParseFnDepotStillWorks(t *testing.T) {
	doc := []byte(`{"legacy": {"display_name": "老应用", "version": "0.1", "isdocker": "true",
	  "download_url": "https://example.com/legacy.fpk", "size": "3.2"}}`)
	entries, err := ParseFnpack(doc)
	if err != nil {
		t.Fatalf("解析 FnDepot 平铺格式失败: %v", err)
	}
	app := translateEntry("legacy", entries["legacy"], "老源", "https://example.com", "https://example.com")
	if app.DisplayName != "老应用" || app.IsDocker != "true" || app.SizeMB != "3.2" {
		t.Fatalf("FnDepot 兼容性受损: %+v", app)
	}
}
