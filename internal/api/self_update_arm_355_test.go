package api

import "testing"

// 0.6.355：Moo 正式发布双架构 FPK 后，arm 设备也要能应用内自更新。
// 此前（0.6.329~0.6.354）探测一律只挑 _x86 资产、非 amd64 直接关闭自更新，
// arm 机器即使装的是 arm 包也会被判定「无新版本」。这里锁死两件事：
//   1) GOARCH → 资产后缀映射；
//   2) 按后缀选包 + 无同架构资产时回落任意 fpk 的兼容行为。
func TestSelfUpdateArchSuffixFor_P355(t *testing.T) {
	cases := map[string]string{
		"amd64": "_x86",
		"arm64": "_arm",
		"arm":   "_arm",
		"386":   "_x86", // 无发布资产，保持旧映射（真正拦截在 selfUpdateArchSupported）
	}
	for goarch, want := range cases {
		if got := selfUpdateArchSuffixFor(goarch); got != want {
			t.Fatalf("GOARCH=%s: 后缀 %q != %q", goarch, got, want)
		}
	}
}

func releaseAssets355() []selfUpdateAsset {
	return []selfUpdateAsset{
		{Name: "moo_0.6.355_x86.fpk.sha256", URL: "u/sha-x86"},
		{Name: "moo_0.6.355_x86.fpk.minisig", URL: "u/sig-x86"},
		{Name: "moo_0.6.355_arm.fpk", URL: "u/arm"},
		{Name: "moo_0.6.355_arm.fpk.sha256", URL: "u/sha-arm"},
		{Name: "moo_0.6.355_arm.fpk.minisig", URL: "u/sig-arm"},
		{Name: "moo_0.6.355_x86.fpk", URL: "u/x86"},
	}
}

func TestPickSelfUpdateAsset_ByArch_P355(t *testing.T) {
	cases := []struct{ suffix, wantURL, wantName string }{
		{"_x86", "u/x86", "moo_0.6.355_x86.fpk"},
		{"_arm", "u/arm", "moo_0.6.355_arm.fpk"},
	}
	for _, c := range cases {
		url, name := pickSelfUpdateAsset(releaseAssets355(), c.suffix)
		if url != c.wantURL || name != c.wantName {
			t.Fatalf("suffix=%s 选中 %q/%q，期望 %q/%q", c.suffix, url, name, c.wantURL, c.wantName)
		}
	}
}

// 只有 x86 资产的旧 release（0.6.352 之前）在 arm 机器上仍要能拿到包：
// 宁可回落到 x86 也不返回空（返回空会让自更新报「release 缺少 FPK 资产」）。
func TestPickSelfUpdateAsset_FallbackWhenArchMissing_P355(t *testing.T) {
	only := []selfUpdateAsset{{Name: "moo_0.6.351_x86.fpk", URL: "u/x86"}}
	url, name := pickSelfUpdateAsset(only, "_arm")
	if url != "u/x86" || name != "moo_0.6.351_x86.fpk" {
		t.Fatalf("缺同架构资产时未回落：%q/%q", url, name)
	}
	// 非 .fpk 资产（sha256/minisig）不得被当成安装包
	if url, _ := pickSelfUpdateAsset(releaseAssets355()[:2], "_arm"); url != "" {
		t.Fatalf("非 fpk 资产被误选: %q", url)
	}
}
