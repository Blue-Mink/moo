package api

import "testing"

// 0.6.174 规则（用户定稿）：已装应用的更新目标只认「同一源作者的最新
// 版本」。其他源作者发布的同名更高版本不构成更新——此前为对齐 fndepot
// 采用组内最高版本作更新目标，会造成「A 作者装的应用提示更新到 B 作者
// 的版本」（渠道通知同样受污染），已废弃。
//
// 回归：wb2api 场景——wabisabi926 装 1.11.1（规范卡，版本一致），
// shuangji66 源有 1.11.6（另一作者）。规范卡必须保持 wabisabi926
// 身份、不产生跨作者更新；1.11.6 卡降级为未装（「全部」列表仍可单独安装）。
func TestMarkInstalledCanonical_CrossSourceNewerNotUpdate(t *testing.T) {
	out := []AppInfo{
		{
			Key:              "wb2api@shuangji66的应用源",
			AppName:          "wb2api",
			Source:           "shuangji66的应用源",
			Installed:        true,
			InstalledVersion: "1.11.1",
			LatestVersion:    "1.11.6",
			HasUpdate:        true, // fillInstalled 预置（自身 1.11.6 > 已装 1.11.1）
			AvailableVersion: "1.11.6",
		},
		{
			Key:              "wb2api@wabisabi926",
			AppName:          "wb2api",
			Source:           "wabisabi926",
			Installed:        true,
			InstalledVersion: "1.11.1",
			LatestVersion:    "1.11.1",
			ReleaseURL:       "https://github.com/wabisabi926/x/releases/download/wb2api-1.11.1/wb2api.fpk",
			SizeBytes:        111,
			Sha256:           "aaa",
			DownloadCount:    intPtr(10),
		},
	}
	markInstalledCanonical(out, nil)

	canonIdx := -1
	for i := range out {
		if out[i].Installed {
			canonIdx = i
		}
	}
	if canonIdx < 0 {
		t.Fatal("应保留一个已装规范卡")
	}
	c := &out[canonIdx]
	// 规范卡 = 版本一致的 wabisabi926（安装来源），身份不被跨源卡夺走
	if c.Source != "wabisabi926" {
		t.Errorf("规范卡源应为 wabisabi926（安装来源），实际 %q", c.Source)
	}
	if c.Key != "wb2api@wabisabi926" {
		t.Errorf("规范卡 Key 应保持 wabisabi926，实际 %q", c.Key)
	}
	if c.LatestVersion != "1.11.1" {
		t.Errorf("规范卡 LatestVersion 应保持自身来源版本 1.11.1，实际 %q", c.LatestVersion)
	}
	// 同作者无新版 → 无更新（跨源 1.11.6 不构成更新）
	if c.HasUpdate {
		t.Error("其他作者的同名更高版本不应产生更新")
	}
	if c.AvailableVersion != "" {
		t.Errorf("AvailableVersion 应为空，实际 %q", c.AvailableVersion)
	}
	// 跨源 1.11.6 卡降级为未装且清掉更新标记
	s := &out[1-canonIdx]
	if s.Installed {
		t.Error("非规范卡应降级为未装")
	}
	if s.HasUpdate || s.AvailableVersion != "" {
		t.Errorf("非规范卡应清更新标记: HasUpdate=%v avail=%q", s.HasUpdate, s.AvailableVersion)
	}
}

// 同一源作者（规范卡自身来源）有新版 → 正常产生更新，目标版本/源保持自身。
func TestMarkInstalledCanonical_SameAuthorUpdate(t *testing.T) {
	out := []AppInfo{
		{
			Key:              "app@alpha",
			AppName:          "app",
			Source:           "alpha",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "2.0.0",
			HasUpdate:        true, // fillInstalled 预置（自身 2.0.0 > 已装 1.0.0）
			AvailableVersion: "2.0.0",
		},
		{
			Key:              "app@beta",
			AppName:          "app",
			Source:           "beta",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "1.5.0",
			HasUpdate:        true,
			AvailableVersion: "1.5.0",
		},
	}
	markInstalledCanonical(out, nil)

	canonIdx := -1
	for i := range out {
		if out[i].Installed {
			canonIdx = i
		}
	}
	c := &out[canonIdx]
	// 无版本一致条目 → 顺序首个 alpha 为规范卡；更新保留（同源新版）
	if c.Source != "alpha" || c.Key != "app@alpha" {
		t.Fatalf("规范卡应保持 alpha 身份，实际 %q", c.Key)
	}
	if !c.HasUpdate || c.AvailableVersion != "2.0.0" {
		t.Errorf("同源新版应保留更新: HasUpdate=%v avail=%q", c.HasUpdate, c.AvailableVersion)
	}
	// beta 卡降级清标记
	b := &out[1-canonIdx]
	if b.Installed || b.HasUpdate || b.AvailableVersion != "" {
		t.Error("非规范卡应降级并清更新标记")
	}
}

// 平台升级目标版本命中组内某条目 → 规范卡选中真实来源源（0.6.174 ②），
// 且更新目标即该来源的版本。
func TestMarkInstalledCanonical_PlatformUpgVerPick(t *testing.T) {
	out := []AppInfo{
		{
			Key:              "app@srcA",
			AppName:          "app",
			Source:           "srcA",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "1.0.0",
		},
		{
			Key:              "app@srcC",
			AppName:          "app",
			Source:           "srcC",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "3.0.0",
			HasUpdate:        true,
			AvailableVersion: "3.0.0",
		},
	}
	markInstalledCanonical(out, map[string]string{"app": "3.0.0"})

	canonIdx := -1
	for i := range out {
		if out[i].Installed {
			canonIdx = i
		}
	}
	c := &out[canonIdx]
	if c.Source != "srcC" {
		t.Fatalf("平台升级目标 3.0.0 命中 srcC，规范卡应为 srcC，实际 %q", c.Source)
	}
	if !c.HasUpdate || c.AvailableVersion != "3.0.0" {
		t.Errorf("更新目标应为平台来源版本 3.0.0: HasUpdate=%v avail=%q", c.HasUpdate, c.AvailableVersion)
	}

	// 平台目标无人命中 → 退回版本一致/顺序规则（srcA 版本一致）
	out2 := []AppInfo{
		{
			Key:              "app@srcA",
			AppName:          "app",
			Source:           "srcA",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "1.0.0",
		},
		{
			Key:              "app@srcC",
			AppName:          "app",
			Source:           "srcC",
			Installed:        true,
			InstalledVersion: "1.0.0",
			LatestVersion:    "3.0.0",
		},
	}
	markInstalledCanonical(out2, map[string]string{"app": "9.9.9"})
	for i := range out2 {
		if out2[i].Installed && out2[i].Source != "srcA" {
			t.Errorf("平台目标未命中时规范卡应为版本一致的 srcA，实际 %q", out2[i].Source)
		}
	}
}

// 无跨源更高版本时：规范卡保持安装来源身份，不产生更新。
func TestMarkInstalledCanonical_NoNewerStaysSource(t *testing.T) {
	a := 5
	out := []AppInfo{
		{
			Key:              "app@srcA",
			AppName:          "app",
			Source:           "srcA",
			Installed:        true,
			InstalledVersion: "2.0.0",
			LatestVersion:    "2.0.0",
			DownloadCount:    &a,
		},
		{
			Key:              "app@srcB",
			AppName:          "app",
			Source:           "srcB",
			Installed:        true,
			InstalledVersion: "2.0.0",
			LatestVersion:    "2.0.0",
		},
	}
	markInstalledCanonical(out, nil)
	canon := -1
	for i := range out {
		if out[i].Installed {
			canon = i
		}
	}
	if canon < 0 {
		t.Fatal("应保留一个已装规范卡")
	}
	c := &out[canon]
	if c.HasUpdate {
		t.Error("无更高版本不应 HasUpdate")
	}
	if c.AvailableVersion != "" {
		t.Errorf("无更高版本 AvailableVersion 应为空，实际 %q", c.AvailableVersion)
	}
	if c.Source != "srcA" && c.Source != "srcB" {
		t.Errorf("规范卡源异常 %q", c.Source)
	}
}

// 官方卡不受跨源采用影响：已装官方应用即使社区同名卡版本更高，
// 规范卡也保持官方 key/版本（0.6.56 规则）。
func TestMarkInstalledCanonical_OfficialUnaffected(t *testing.T) {
	out := []AppInfo{
		{
			Key:              "nodejs_v22@shuangji66的应用源",
			AppName:          "nodejs_v22",
			Source:           "shuangji66的应用源",
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.23.2",
			HasUpdate:        true,
			AvailableVersion: "22.23.2",
		},
		{
			Key:              "nodejs_v22@fnos-official",
			AppName:          "nodejs_v22",
			Source:           OfficialSourceID,
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.18.0-1",
		},
	}
	markInstalledCanonical(out, nil)
	canon := -1
	for i := range out {
		if out[i].Installed {
			canon = i
		}
	}
	if canon < 0 {
		t.Fatal("应保留一个已装规范卡")
	}
	c := &out[canon]
	if c.Source != OfficialSourceID {
		t.Fatalf("规范卡应为官方卡，实际 %q", c.Source)
	}
	if c.Key != "nodejs_v22@fnos-official" {
		t.Errorf("官方卡 Key 不应被社区卡覆盖，实际 %q", c.Key)
	}
	if c.LatestVersion != "22.18.0-1" {
		t.Errorf("官方卡 LatestVersion 不应被社区版本覆盖，实际 %q", c.LatestVersion)
	}
	if c.HasUpdate || c.AvailableVersion != "" {
		t.Errorf("社区更高版本不构成官方卡更新: HasUpdate=%v avail=%q", c.HasUpdate, c.AvailableVersion)
	}
}

func intPtr(n int) *int { return &n }
