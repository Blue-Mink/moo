package api

import "testing"

func inst(name, src, ver, latest string) AppInfo {
	return AppInfo{
		Key:              name + "@" + src,
		AppName:          name,
		DisplayName:      name,
		Source:           src,
		Installed:        true,
		InstalledVersion: ver,
		InstalledFPKVer:  ver,
		Status:           "running",
		LatestVersion:    latest,
	}
}

// 社区同名多源：版本匹配者胜出为规范条目，其余降级未装
func TestMarkInstalledCanonical_VersionMatch(t *testing.T) {
	ss := true
	out := []AppInfo{
		inst("golang", "srcA", "1.27.1", "1.26.5"),
		inst("golang", "srcB", "1.27.1", "1.27.1"), // 版本匹配
		inst("golang", "srcC", "1.27.1", "1.26.3"),
	}
	for i := range out {
		out[i].StartStop = &ss
		out[i].HasUpdate = true
		out[i].AvailableVersion = "9.9.9"
	}
	markInstalledCanonical(out, nil)

	if !out[1].Installed {
		t.Fatal("版本匹配的条目应保留为规范条目")
	}
	for i, want := range []bool{false, true, false} {
		if out[i].Installed != want {
			t.Fatalf("out[%d].Installed = %v, want %v", i, out[i].Installed, want)
		}
	}
	// 降级条目的已装状态须全部清空
	for _, a := range []AppInfo{out[0], out[2]} {
		if a.InstalledVersion != "" || a.Status != "" || a.StartStop != nil || a.HasUpdate || a.AvailableVersion != "" {
			t.Fatalf("降级条目残留已装状态: %+v", a)
		}
	}
	// 规范条目状态不动
	if out[1].InstalledVersion != "1.27.1" || out[1].Status != "running" || out[1].StartStop == nil {
		t.Fatalf("规范条目状态被误改: %+v", out[1])
	}
}

// 官方条目存在：官方恒为规范条目（即使版本不匹配）
func TestMarkInstalledCanonical_OfficialWins(t *testing.T) {
	out := []AppInfo{
		inst("gitea", "srcX", "1.20.0", "1.27.3"),
		inst("gitea", OfficialSourceID, "1.20.0", "1.21.0"),
	}
	markInstalledCanonical(out, nil)
	if !out[1].Installed || out[0].Installed {
		t.Fatalf("官方条目应为唯一规范条目: %+v / %+v", out[0].Installed, out[1].Installed)
	}
}

// 单条已装应用不受影响
func TestMarkInstalledCanonical_Singleton(t *testing.T) {
	out := []AppInfo{inst("only", "srcA", "1.0", "1.0")}
	markInstalledCanonical(out, nil)
	if !out[0].Installed || out[0].InstalledVersion != "1.0" {
		t.Fatalf("单条目被误改: %+v", out[0])
	}
}

// 无版本匹配时取顺序首个
func TestMarkInstalledCanonical_FirstFallback(t *testing.T) {
	out := []AppInfo{
		inst("app", "srcA", "2.0", "1.0"),
		inst("app", "srcB", "2.0", "1.0"),
	}
	markInstalledCanonical(out, nil)
	if !out[0].Installed || out[1].Installed {
		t.Fatalf("应取顺序首个为规范条目: %+v / %+v", out[0].Installed, out[1].Installed)
	}
}
