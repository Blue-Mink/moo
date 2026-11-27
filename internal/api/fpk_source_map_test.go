package api

import "testing"

// 2026-11-26：已下载 FPK 的「源名」反查（只读目录快照，不触发重建）。
func TestSourceByAppName(t *testing.T) {
	s := &Server{}
	s.catalogData = []AppInfo{
		{AppName: "moo", Source: "Blue-Mink/FnDepot"},
		{AppName: "QwenPaw", Source: "naspk-com/QwenPaw"},
		{AppName: "QwenPaw", Source: "duplicate/ignored"},
		{AppName: "", Source: "x"},
		{AppName: "noSource"},
	}
	m := s.sourceByAppName()
	if m["moo"] != "Blue-Mink/FnDepot" {
		t.Fatalf("moo 源名错误: %q", m["moo"])
	}
	if m["qwenpaw"] != "naspk-com/QwenPaw" {
		t.Fatalf("大小写归一失败或重复覆盖: %q", m["qwenpaw"])
	}
	if _, ok := m["nosource"]; ok {
		t.Fatal("无源名的应用不应进映射")
	}
}
