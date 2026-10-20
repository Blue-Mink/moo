package api

import "testing"

// TestApplyDetailFallbackHomepage（0.6.337）：官网三级回退——
// upstream_url → upstream_repo → maintainer_url，已有值不覆盖。
func TestApplyDetailFallbackHomepage(t *testing.T) {
	out := []AppInfo{
		{Homepage: "", UpstreamURL: "https://github.com/org/up", UpstreamRepo: "org/up", MaintainerURL: ""},
		{Homepage: "", UpstreamURL: "", UpstreamRepo: "org/up2", MaintainerURL: ""},
		{Homepage: "", UpstreamURL: "", UpstreamRepo: "", MaintainerURL: "https://example.com/author"},
		{Homepage: "", UpstreamURL: "", UpstreamRepo: "", MaintainerURL: ""},
		{Homepage: "https://keep.me", UpstreamURL: "https://github.com/org/up3"},
	}
	applyDetailFallback(out)
	if out[0].Homepage != "https://github.com/org/up" {
		t.Errorf("upstream_url 回退失败：%q", out[0].Homepage)
	}
	if out[1].Homepage != "https://github.com/org/up2" {
		t.Errorf("upstream_repo 回退失败（应补 scheme）：%q", out[1].Homepage)
	}
	if out[2].Homepage != "https://example.com/author" {
		t.Errorf("maintainer_url 回退失败：%q", out[2].Homepage)
	}
	if out[3].Homepage != "" {
		t.Errorf("全空应保持空：%q", out[3].Homepage)
	}
	if out[4].Homepage != "https://keep.me" {
		t.Errorf("已有 homepage 不应被覆盖：%q", out[4].Homepage)
	}
}

// TestApplyDetailFallbackArch（0.6.337）：架构从 Release 资产文件名推断，
// 已有 arch/archs 不覆盖、非 GitHub 链接跳过。
func TestApplyDetailFallbackArch(t *testing.T) {
	out := []AppInfo{
		{Arch: "", Archs: nil, ReleaseURL: "https://github.com/conversun/fnos-apps/releases/download/v1/1panel_1.0_x86.fpk"},
		{Arch: "", Archs: nil, ReleaseURL: "https://github.com/x/y/releases/download/v1/app_arm.fpk"},
		{Arch: "", Archs: nil, ReleaseURL: "https://github.com/x/y/releases/download/v1/app_amd64.fpk"},
		{Arch: "", Archs: nil, ReleaseURL: "https://raw.githubusercontent.com/x/y/main/app.fpk"}, // 非 release
		{Arch: "arm", Archs: []string{"x86", "arm"}, ReleaseURL: "https://github.com/x/y/releases/download/v1/a_x86.fpk"}, // 已有不覆盖
	}
	applyDetailFallback(out)
	if out[0].Arch != "x86" {
		t.Errorf("x86 推断失败：%q", out[0].Arch)
	}
	if out[1].Arch != "arm" {
		t.Errorf("arm 推断失败：%q", out[1].Arch)
	}
	if out[2].Arch != "x86" {
		t.Errorf("amd64 推断失败：%q", out[2].Arch)
	}
	if out[3].Arch != "" {
		t.Errorf("非 GitHub Release 应跳过：%q", out[3].Arch)
	}
	if out[4].Arch != "arm" || len(out[4].Archs) != 2 {
		t.Errorf("已有 arch/archs 不应被覆盖：%q %v", out[4].Arch, out[4].Archs)
	}
}
