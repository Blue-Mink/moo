package source

import (
	"testing"

	"moo/internal/config"
)

// TestAddSourceSchemeNormalization 0.6.141：无协议源地址入库时补 https://
//（修复 Blue-Mink 源以 github.com/… 形式入库、复制出的链接缺协议不可用）。
func TestAddSourceSchemeNormalization(t *testing.T) {
	cfg := &config.Config{}
	m := NewManager(cfg)

	if err := m.AddSourcePassive("bm", "github.com/Blue-Mink/FnDepot", nil); err != nil {
		t.Fatalf("AddSourcePassive(无协议): %v", err)
	}
	if len(cfg.Sources) != 1 || cfg.Sources[0].URL != "https://github.com/Blue-Mink/FnDepot" {
		t.Fatalf("无协议 URL 应归一化补 https://: %+v", cfg.Sources)
	}

	if err := m.AddSourcePassive("ok", "https://github.com/x/y", nil); err != nil {
		t.Fatalf("AddSourcePassive(有协议): %v", err)
	}
	if cfg.Sources[1].URL != "https://github.com/x/y" {
		t.Fatalf("有协议 URL 不应被改动: %+v", cfg.Sources[1])
	}

	// 归一化去重不受影响：https 版与无协议版视为同源
	if err := m.AddSourcePassive("dup", "https://github.com/x/y", nil); err == nil {
		t.Fatal("同 URL 不同名应报重复")
	}
}

// TestHealLegacyURLs 0.6.246：存量无协议源地址一次性自愈（补 https://），
// 已带协议/空地址不动，重复调用幂等（第二次返回 0）。
func TestHealLegacyURLs(t *testing.T) {
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "bm", URL: "github.com/Blue-Mink/FnDepot"}, // 存量无协议
		{Name: "ok", URL: "https://github.com/x/y"},       // 已带协议
		{Name: "emp", URL: ""},                             // 空
	}}
	if n := HealLegacyURLs(cfg); n != 1 {
		t.Fatalf("应自愈 1 个，实际 %d: %+v", n, cfg.Sources)
	}
	if cfg.Sources[0].URL != "https://github.com/Blue-Mink/FnDepot" {
		t.Fatalf("无协议源应补 https://: %q", cfg.Sources[0].URL)
	}
	if cfg.Sources[1].URL != "https://github.com/x/y" || cfg.Sources[2].URL != "" {
		t.Fatalf("已带协议/空地址不应被改动: %+v", cfg.Sources)
	}
	if n := HealLegacyURLs(cfg); n != 0 {
		t.Fatalf("二次调用应幂等返回 0，实际 %d", n)
	}
}
