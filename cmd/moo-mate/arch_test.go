package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormArch(t *testing.T) {
	cases := map[string]string{
		"x86": "x86", "x86_64": "x86", "amd64": "x86", "X86-64": "x86",
		"arm": "arm", "arm64": "arm", "aarch64": "arm", "ARM": "arm",
		"all":  "all",
		"mips": "", "64": "",
	}
	for in, want := range cases {
		if got := normArch(in); got != want {
			t.Errorf("normArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArchFromFilename(t *testing.T) {
	cases := map[string]string{
		"motrix_1.9.8_arm.fpk": "arm",
		"motrix_1.9.8_x86.fpk": "x86",
		"app_2.0_x86_64.fpk":   "x86",
		"app_2.0_aarch64.fpk":  "arm",
		"app_2.0.fpk":          "", // 无架构标识
		"harmony_1.0_x86.fpk":  "x86",
	}
	for in, want := range cases {
		if got := archFromFilename(in); got != want {
			t.Errorf("archFromFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// buildMooJSON 多架构：>1 架构时补 releases.<version>.packages.<arch>，
// 顶层 download_url 保留为便捷链接（指向 x86）。
func TestBuildMooJSONMultiArch(t *testing.T) {
	ia := &indexedApp{
		appEntry: appEntry{
			DisplayName: "Motrix",
			Version:     "1.9.8",
			DownloadURL: "https://github.com/RockerHX/motrix-fnos/releases/download/v1.9.8/motrix_1.9.8_x86.fpk",
			Sha256:      "x86sha",
			Size:        8.2,
			SizeBytes:   8593353,
		},
		Appname:   "motrix",
		Publisher: "motrix-fnos",
		Source:    "https://github.com/RockerHX/motrix-fnos",
		Packages: map[string]*appPackage{
			"x86": {DownloadURL: "https://github.com/RockerHX/motrix-fnos/releases/download/v1.9.8/motrix_1.9.8_x86.fpk", Sha256: "x86sha", SizeBytes: 8593353},
			"arm": {DownloadURL: "https://github.com/RockerHX/motrix-fnos/releases/download/v1.9.8/motrix_1.9.8_arm.fpk", Sha256: "armsha", SizeBytes: 7800000},
		},
	}
	raw, err := buildMooJSON("motrix", "motrix-fnos", "https://github.com/RockerHX/motrix-fnos", map[string]*indexedApp{"motrix": ia})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	apps := doc["apps"].(map[string]any)
	m := apps["motrix"].(map[string]any)
	rel := m["releases"].(map[string]any)
	rv := rel["1.9.8"].(map[string]any)
	pkgs := rv["packages"].(map[string]any)
	if len(pkgs) != 2 {
		t.Fatalf("packages 架构数 = %d, want 2", len(pkgs))
	}
	if _, ok := pkgs["x86"]; !ok {
		t.Error("packages 缺 x86")
	}
	if _, ok := pkgs["arm"]; !ok {
		t.Error("packages 缺 arm")
	}
	// 顶层便捷链接保留（指向 x86）
	if m["download_url"] != "https://github.com/RockerHX/motrix-fnos/releases/download/v1.9.8/motrix_1.9.8_x86.fpk" {
		t.Errorf("顶层 download_url = %v, want x86 便捷链接", m["download_url"])
	}
	// 包级 size 单位字节
	p86 := pkgs["x86"].(map[string]any)
	if p86["size"] != float64(8593353) {
		t.Errorf("包级 size = %v, want 8593353 字节", p86["size"])
	}
	if p86["sha256"] != "x86sha" {
		t.Errorf("包级 sha256 = %v, want x86sha", p86["sha256"])
	}
}

// 单架构不产生 releases 节点（保持扁平，与 gen-moo-json 一致）。
func TestBuildMooJSONSingleArchFlat(t *testing.T) {
	ia := &indexedApp{
		appEntry: appEntry{
			DisplayName: "风扇控制器",
			Version:     "1.1.1",
			DownloadURL: "https://github.com/AriesOxO/fnos-fan-control/releases/download/v1.1.1/fan-control-1.1.1.fpk",
			Sha256:      "abc",
		},
		Appname:   "fan-control",
		Publisher: "fnos-fan-control",
		Source:    "https://github.com/AriesOxO/fnos-fan-control",
	}
	raw, err := buildMooJSON("fan-control", "fnos-fan-control", "https://github.com/AriesOxO/fnos-fan-control", map[string]*indexedApp{"fan-control": ia})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"releases"`) {
		t.Errorf("单架构不应有 releases 节点: %s", raw)
	}
	if !strings.Contains(string(raw), `"fan-control"`) {
		t.Errorf("缺 appname: %s", raw)
	}
}
