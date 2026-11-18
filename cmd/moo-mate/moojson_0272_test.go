package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mkApp0272 构造一条带 moo.json 协议字段的索引条目（模拟默认清单跟版后的形态）。
func mkApp0272() *indexedApp {
	pkg := func(u, sha string, n int64) *appPackage {
		return &appPackage{DownloadURL: u, Sha256: sha, SizeBytes: n}
	}
	return &indexedApp{
		appEntry: appEntry{
			DisplayName: "Moo 升级验证",
			Version:     "0.2.72",
			Platform:    []string{"x86", "arm"},
			Releases: map[string]*appRelease{
				"0.2.72": {Changelog: "本次新增跟版脚本", UpdatedAt: "2026-11-17",
					Packages: map[string]*appPackage{"x86": pkg("https://example.com/a-x86.fpk", "aa", 10)}},
				"0.2.60": {Changelog: "上一版说明", UpdatedAt: "2026-11-09",
					Packages: map[string]*appPackage{"x86": pkg("https://example.com/b-x86.fpk", "bb", 9)}},
			},
			ReadmeURL: "https://example.com/README.md",
			Changelog: "本次新增跟版脚本",
		},
		Appname:   "moo_test_0272",
		Source:    "https://example.com/repo",
		Publisher: "someone",
		Packages: map[string]*appPackage{
			"x86": pkg("https://example.com/a-x86.fpk", "aa", 10),
			"arm": pkg("https://example.com/a-arm.fpk", "cc", 11),
		},
	}
}

// 1) 三字段必须真的出现在 JSON 里（删掉字段/标签即 FAIL）。
func TestAppEntryProtocolFieldsJSON(t *testing.T) {
	b, err := json.Marshal(mkApp0272().appEntry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"releases", "readme_url", "changelog"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("appEntry JSON 缺少协议字段 %q", k)
		}
	}
	rels, _ := m["releases"].(map[string]any)
	if len(rels) != 2 {
		t.Fatalf("releases 应有 2 个版本，实际 %d", len(rels))
	}
	old, _ := rels["0.2.60"].(map[string]any)
	if old["changelog"] != "上一版说明" || old["updated_at"] != "2026-11-09" {
		t.Fatalf("旧版本节点内容丢失: %#v", old)
	}
}

// 2) buildMooJSON 必须保留历史 releases（0.2.71 之前会整块覆盖）。
func TestBuildMooJSONKeepsHistoricalReleases(t *testing.T) {
	apps := map[string]*indexedApp{"moo_test_0272": mkApp0272()}
	raw, err := buildMooJSON("Moo 升级验证", "", "", "", apps)
	if err != nil {
		t.Fatalf("buildMooJSON: %v", err)
	}
	var doc struct {
		Apps map[string]struct {
			Releases map[string]struct {
				Changelog string `json:"changelog"`
				Packages  map[string]struct {
					DownloadURL string `json:"download_url"`
				} `json:"packages"`
			} `json:"releases"`
			ReadmeURL string `json:"readme_url"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	a, ok := doc.Apps["moo_test_0272"]
	if !ok {
		t.Fatalf("mate.json 缺少应用节点")
	}
	if len(a.Releases) != 2 {
		t.Fatalf("releases 应保留 2 个版本，实际 %d", len(a.Releases))
	}
	if a.Releases["0.2.60"].Changelog != "上一版说明" {
		t.Fatalf("旧版本 changelog 被抹掉")
	}
	if got := a.Releases["0.2.72"].Packages["arm"].DownloadURL; got != "https://example.com/a-arm.fpk" {
		t.Fatalf("当前版本 packages 未取自 Packages: %q", got)
	}
	if a.ReadmeURL != "https://example.com/README.md" {
		t.Fatalf("readme_url 未透传: %q", a.ReadmeURL)
	}
}

// 3) 同步链路整体覆盖时必须保真（模拟 discover 构造的不带三字段的新条目）。
func TestIndexPutPreservesProtocolFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	ix := newIndex(path)
	if err := ix.put(mkApp0272()); err != nil {
		t.Fatalf("put#1: %v", err)
	}
	// 同步构造的新条目：只有运行时字段，三字段为空。
	fresh := &indexedApp{
		appEntry: appEntry{DisplayName: "Moo 升级验证", Version: "0.2.72"},
		Appname:  "moo_test_0272",
		Source:   "https://example.com/repo",
	}
	if err := ix.put(fresh); err != nil {
		t.Fatalf("put#2: %v", err)
	}
	got := newIndex(path).get("moo_test_0272")
	if got == nil {
		t.Fatalf("重新加载后条目消失")
	}
	if len(got.Releases) != 2 || got.ReadmeURL == "" || got.Changelog == "" {
		t.Fatalf("三字段未保真: releases=%d readme=%q changelog=%q",
			len(got.Releases), got.ReadmeURL, got.Changelog)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("索引未落盘: %v", err)
	}
}
