package source

import (
	"strings"
	"testing"
)

func TestIsConversunURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://github.com/conversun/fnos-apps", true},
		{"https://github.com/conversun/fnos-apps/", true},
		{"https://github.com/conversun/fnos-apps.git", true},
		{"http://github.com/conversun/fnos-apps", true},
		{"github.com/conversun/fnos-apps", true},
		{"https://github.com/conversun/fnos-apps/tree/main", true},
		{"https://github.com/conversun/fnos-apps/blob/main/apps.json", true},
		{"https://raw.githubusercontent.com/conversun/fnos-apps/main/apps.json", true},
		{"https://cdn.jsdelivr.net/gh/conversun/fnos-apps@main/apps.json", true},
		{"https://gh-proxy.com/https://raw.githubusercontent.com/conversun/fnos-apps/main/apps.json", true},
		{"  https://github.com/conversun/fnos-apps  ", true},
		// 边界：fnos-apps-store（商店代码仓库）不得误匹配
		{"https://github.com/conversun/fnos-apps-store", false},
		{"https://github.com/conversun/fnos-apps-store/tree/main", false},
		{"https://github.com/conversun/fnos-store", false},
		{"https://github.com/conversun", false},
		{"https://github.com/conversun/fnos-app", false},
		{"https://github.com/other/fnos-apps", false},
		// releases 下的 FPK 链接不是源
		{"https://github.com/conversun/fnos-apps/releases/download/v1/1panel_1.0_x86.fpk", false},
		{"", false},
		{"   ", false},
	}
	for _, c := range cases {
		if got := IsConversunURL(c.url); got != c.ok {
			t.Errorf("IsConversunURL(%q) = %v, want %v", c.url, got, c.ok)
		}
	}
}

func TestNewSourceDispatch(t *testing.T) {
	if _, ok := NewSource("s", "https://github.com/conversun/fnos-apps").(*Conversun); !ok {
		t.Fatal("仓库地址应分发到 Conversun")
	}
	if _, ok := NewSource("s", "https://raw.githubusercontent.com/conversun/fnos-apps/main/apps.json").(*Conversun); !ok {
		t.Fatal("apps.json 直链应分发到 Conversun")
	}
	if _, ok := NewSource("s", "https://github.com/conversun/fnos-apps-store").(*Conversun); ok {
		t.Fatal("fnos-apps-store 不得分发到 Conversun")
	}
	if _, ok := NewSource("s", "https://github.com/Wyf841015/FnDepot").(*FnDepot); !ok {
		t.Fatal("普通仓库地址应分发到 FnDepot")
	}
}

func TestConversunCandidateURLs(t *testing.T) {
	c := NewConversun("conversun")
	primary, fallback := c.candidateURLs()
	if len(primary) < 2 || len(fallback) < 1 {
		t.Fatalf("候选不足: primary=%d fallback=%d", len(primary), len(fallback))
	}
	if !strings.HasSuffix(primary[0], "conversun/fnos-apps/main/apps.json") {
		t.Errorf("首选应为 raw main: %s", primary[0])
	}
	if !strings.HasSuffix(primary[1], "conversun/fnos-apps/master/apps.json") {
		t.Errorf("次选应为 raw master: %s", primary[1])
	}
	if !strings.HasSuffix(fallback[len(fallback)-1], "gh/conversun/fnos-apps/apps.json") {
		t.Errorf("兜底应为 jsDelivr: %s", fallback[len(fallback)-1])
	}
	for _, u := range append(primary, fallback...) {
		if u == "" {
			t.Error("存在空候选地址")
		}
	}
}

func TestTranslateConversun(t *testing.T) {
	it := conversunApp{
		Slug:        "1panel",
		AppName:     "1panel",
		FilePrefix:  "1panel",
		DisplayName: "1Panel",
		Description: "现代化、开源的 Linux 服务器运维管理面板",
		Version:     "1.10.34-lts",
		FpkVersion:  "1.10.34-lts-r8",
		ReleaseTag:  "1panel/v1.10.34-lts-r8",
		ServicePort: 9562,
		HomepageURL: "https://1panel.cn/",
		IconURL:     "https://raw.githubusercontent.com/conversun/fnos-apps/main/apps/1panel/icon.png",
		Platforms:   []string{"x86", "arm"},
		UpdatedAt:   "2026-09-20T00:00:00Z",
		DownloadCount: 12345,
		AppType:     "docker",
		Category:    "运维管理",
	}
	a := translateConversun(it, "conversun")
	if a == nil {
		t.Fatal("translateConversun 返回 nil")
	}
	wantArch := currentArch()
	wantURL := "https://github.com/conversun/fnos-apps/releases/download/1panel/v1.10.34-lts-r8/1panel_1.10.34-lts-r8_" + wantArch + ".fpk"
	if a.DownloadURL != wantURL {
		t.Errorf("DownloadURL = %s, want %s", a.DownloadURL, wantURL)
	}
	if a.Version != "1.10.34-lts" {
		t.Errorf("Version = %s, want 1.10.34-lts", a.Version)
	}
	if a.DisplayName != "1Panel" || a.Desc == "" || a.Labels != "运维管理" {
		t.Errorf("基础字段映射错误: %+v", a)
	}
	if a.IsDocker != "1" {
		t.Errorf("docker 应用 IsDocker = %q, want 1", a.IsDocker)
	}
	if a.ServicePort != "9562" {
		t.Errorf("ServicePort = %q, want 9562", a.ServicePort)
	}
	if a.DownloadCount != 12345 {
		t.Errorf("DownloadCount = %d, want 12345", a.DownloadCount)
	}
	if a.Author != "conversun" || a.Source != "conversun" {
		t.Errorf("作者/来源标注错误: author=%s source=%s", a.Author, a.Source)
	}

	// 原生应用（非 docker）
	native := translateConversun(conversunApp{
		AppName: "n1", FilePrefix: "n1", FpkVersion: "1.0.0", ReleaseTag: "n1/v1.0.0",
		Platforms: []string{"x86"}, AppType: "native",
	}, "conversun")
	if native == nil && currentArch() != "arm" {
		t.Fatal("x86 原生应用在 x86 架构应可用")
	} else if native != nil && native.IsDocker != "" {
		t.Errorf("原生应用 IsDocker = %q, want 空", native.IsDocker)
	}

	// 缺 FPK 三要素 → 跳过
	if a3 := translateConversun(conversunApp{AppName: "incomplete"}, "conversun"); a3 != nil {
		t.Error("缺 release_tag/file_prefix/fpk_version 应跳过")
	}
	// appname 空回退 slug
	if a4 := translateConversun(conversunApp{Slug: "slugged", FilePrefix: "f", FpkVersion: "1", ReleaseTag: "t"}, "conversun"); a4 == nil || a4.Name != "slugged" || a4.DisplayName != "slugged" {
		t.Errorf("slug 回退错误: %+v", a4)
	}
	// 完全无名 → 跳过
	if a5 := translateConversun(conversunApp{FilePrefix: "f", FpkVersion: "1", ReleaseTag: "t"}, "conversun"); a5 != nil {
		t.Error("无 appname/slug 应跳过")
	}
}

func TestSupportsArch(t *testing.T) {
	if !supportsArch(nil) || !supportsArch([]string{}) {
		t.Error("空 platforms 应视为全架构可用")
	}
	if !supportsArch([]string{"all"}) {
		t.Error("all 应可用")
	}
	if !supportsArch([]string{"x86", "arm"}) {
		t.Error("x86+arm 应覆盖当前架构")
	}
	other := "x86"
	if currentArch() == "x86" {
		other = "arm"
	}
	if supportsArch([]string{other}) {
		t.Errorf("仅 %s 的应用在当前架构应不可用", other)
	}
}

func TestArchOfPlatforms(t *testing.T) {
	if got := archOfPlatforms(nil); got != currentArch() {
		t.Errorf("空 platforms 回退当前架构: got %s", got)
	}
	if got := archOfPlatforms([]string{"all"}); got != currentArch() {
		t.Errorf("all 应用包按当前架构命名: got %s", got)
	}
	if got := archOfPlatforms([]string{"x86", "arm"}); got != currentArch() {
		t.Errorf("x86+arm 取当前架构: got %s", got)
	}
	other := "x86"
	if currentArch() == "x86" {
		other = "arm"
	}
	if got := archOfPlatforms([]string{other}); got != other {
		t.Errorf("架构不匹配回退声明值: got %s want %s", got, other)
	}
}
