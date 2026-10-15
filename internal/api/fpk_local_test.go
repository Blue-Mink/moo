package api

import "testing"

// TestCleanManifestValue 覆盖 0.6.325 C9 的 manifest 脏值清洗：
// 成对引号（双/单/多层）、未展开模板变量、正常值不得被破坏。
func TestCleanManifestValue(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"正常值不动", "com.trek.app", "com.trek.app"},
		{"正常值含点线不动", "trim.snapshots", "trim.snapshots"},
		{"双引号包裹", `"docker-iyuuplus"`, "docker-iyuuplus"},
		{"单引号包裹", `'docker-iyuuplus'`, "docker-iyuuplus"},
		{"双引号包裹带空格", `  "docker-iyuuplus"  `, "docker-iyuuplus"},
		{"多层双引号", `""docker-iyuuplus""`, "docker-iyuuplus"},
		{"多层混合引号", `"'docker-iyuuplus'"`, "docker-iyuuplus"},
		{"未展开变量", "${common.display_name}", ""},
		{"引号包裹的未展开变量", `"${common.display_name}"`, ""},
		{"变量出现在中段", "prefix-${x}", ""},
		{"单边引号不剥", `"docker-iyuuplus`, `"docker-iyuuplus`},
		{"空串", "", ""},
		{"仅空格", "   ", ""},
	}
	for _, c := range cases {
		if got := cleanManifestValue(c.in); got != c.want {
			t.Errorf("%s: cleanManifestValue(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestParseManifestTextDirtyValues 验证 parseManifestText 已对全部字段套用清洗，
// 且正常字段（com.trek.app）保持原样。
func TestParseManifestTextDirtyValues(t *testing.T) {
	t.Run("引号包裹的 appname 清洗后等于目录 key", func(t *testing.T) {
		// 模拟 docker-iyuuplus 场景：包内 appname/display_name 都带字面引号
		data := []byte("appname=\"docker-iyuuplus\"\n" +
			"display_name=\"IYUUPlus\"\n" +
			"version=\"1.1.6\"\n")
		info := parseManifestText(data)
		if info.AppName != "docker-iyuuplus" {
			t.Fatalf("AppName = %q, want docker-iyuuplus", info.AppName)
		}
		if info.DisplayName != "IYUUPlus" {
			t.Fatalf("DisplayName = %q, want IYUUPlus", info.DisplayName)
		}
		if got := normAppName(info.AppName); got != normAppName("docker-iyuuplus") {
			t.Fatalf("normAppName(AppName) = %q, want %q", got, normAppName("docker-iyuuplus"))
		}
	})

	t.Run("未展开变量的 display_name 置空但 appname 可用", func(t *testing.T) {
		// 模拟 trim.snapshots 场景
		data := []byte("appname=\"trim.snapshots\"\n" +
			"display_name=\"${common.display_name}\"\n" +
			"version=\"0.1.71\"\n")
		info := parseManifestText(data)
		if info.AppName != "trim.snapshots" {
			t.Fatalf("AppName = %q, want trim.snapshots", info.AppName)
		}
		if info.DisplayName != "" {
			t.Fatalf("DisplayName = %q, want empty (未展开变量)", info.DisplayName)
		}
	})

	t.Run("正常值不被改写", func(t *testing.T) {
		data := []byte("appname = com.trek.app\n" +
			"display_name = TREK\n" +
			"desc = 自托管旅行规划工具\n" +
			"maintainer = liketrek\n")
		info := parseManifestText(data)
		if info.AppName != "com.trek.app" {
			t.Fatalf("AppName = %q, want com.trek.app", info.AppName)
		}
		if info.DisplayName != "TREK" {
			t.Fatalf("DisplayName = %q, want TREK", info.DisplayName)
		}
		if info.Desc != "自托管旅行规划工具" {
			t.Fatalf("Desc = %q", info.Desc)
		}
		if info.Maintainer != "liketrek" {
			t.Fatalf("Maintainer = %q, want liketrek", info.Maintainer)
		}
	})

	t.Run("desc 中的未展开变量也被清空", func(t *testing.T) {
		data := []byte("appname=foo\ndesc=\"${something}\"\n")
		info := parseManifestText(data)
		if info.AppName != "foo" {
			t.Fatalf("AppName = %q", info.AppName)
		}
		if info.Desc != "" {
			t.Fatalf("Desc = %q, want empty", info.Desc)
		}
	})
}
