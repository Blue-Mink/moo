package main

import (
	"os"
	"strings"
	"testing"
)

// TestInspectFPKReal 用工作区现成 FPK 验证 inspectFPK 解析 manifest 正确。
// FPK 路径经环境变量 MATE_TEST_FPK 注入（避免硬编码绝对路径）。
func TestInspectFPKReal(t *testing.T) {
	p := os.Getenv("MATE_TEST_FPK")
	if p == "" {
		t.Skip("未设 MATE_TEST_FPK")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("打开 FPK 失败: %v", err)
	}
	defer f.Close()
	meta, err := inspectFPK(f)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	appname := strings.TrimSpace(meta.Manifest["appname"])
	version := meta.Manifest["version"]
	if appname == "" {
		t.Fatal("appname 为空")
	}
	if version == "" {
		t.Fatal("version 为空")
	}
	if len(meta.Sha256) != 64 {
		t.Fatalf("sha256 长度错误: %d", len(meta.Sha256))
	}
	if meta.Size <= 0 {
		t.Fatalf("size 异常: %d", meta.Size)
	}
	t.Logf("appname=%s version=%s sha256=%s size=%d", appname, version, meta.Sha256[:12], meta.Size)
}

// TestVersionFromTag 验证 tag → 版本推导。
func TestVersionFromTag(t *testing.T) {
	cases := map[string]string{
		"v1.2.3":           "1.2.3",
		"1.2.3":            "1.2.3",
		"v0.7.17":          "0.7.17",
		"app/v1.2.3":       "1.2.3",
		"release-2026.1.1": "", // 标签前缀，不可比
		"latest":           "",
		"v1.0.0-rc.1":      "1.0.0-rc.1",
	}
	for tag, want := range cases {
		if got := versionFromTag(tag); got != want {
			t.Errorf("versionFromTag(%q)=%q, want %q", tag, got, want)
		}
	}
}

// TestParseManifest 验证 manifest 解析（key=value + # 注释）。
func TestParseManifest(t *testing.T) {
	s := "appname = dockflare\nversion = 3.1.3\n# 注释行\ndisplay_name=DockFlare\n"
	kv := parseManifest(s)
	if kv["appname"] != "dockflare" || kv["version"] != "3.1.3" || kv["display_name"] != "DockFlare" {
		t.Fatalf("manifest 解析错误: %+v", kv)
	}
	if _, ok := kv["# 注释行"]; ok {
		t.Fatal("注释行不应被解析")
	}
}
