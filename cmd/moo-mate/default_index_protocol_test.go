package main

import (
	"encoding/json"
	"testing"
)

// 0.2.72 默认清单协议字段守门：内嵌 gz 必须能被 mate 的 indexDoc 解析，
// 且每个应用都能原样导出 releases / readme_url / changelog / packages / size_bytes。
// 这几项缺失时 Moo 详情页的「更新日志 / README / 多架构下载」会静默变空。
func TestDefaultIndexProtocolFields(t *testing.T) {
	d, err := decodeDefaultIndex()
	if err != nil {
		t.Fatalf("解析内嵌默认清单失败: %v", err)
	}
	if len(d.Apps) != 5 {
		t.Fatalf("默认清单应有 5 个应用，实际 %d", len(d.Apps))
	}
	for key, a := range d.Apps {
		if a.Appname == "" || a.Version == "" {
			t.Fatalf("%s: appname/version 为空", key)
		}
		if a.SizeBytes <= 0 {
			t.Fatalf("%s: size_bytes=%d", key, a.SizeBytes)
		}
		if len(a.Packages) == 0 {
			t.Fatalf("%s: packages 为空", key)
		}
		if a.ReadmeURL == "" {
			t.Fatalf("%s: readme_url 为空", key)
		}
		if len(a.Releases) == 0 || len(a.Releases) > 2 {
			t.Fatalf("%s: releases=%d（应 1~2 个）", key, len(a.Releases))
		}
		raw, _ := json.Marshal(a)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: 导出序列化失败 %v", key, err)
		}
		for _, f := range []string{"releases", "readme_url", "changelog", "packages", "size_bytes"} {
			if _, ok := m[f]; !ok {
				t.Fatalf("%s: 导出缺字段 %s", key, f)
			}
		}
	}
}

// 反向验证：updated_at 写成字符串必须解析失败 —— mate 侧是 int64，
// 一处类型不符会让整份清单解析失败（0.2.72 开发期实测踩过）。
func TestDefaultIndexUpdatedAtMustBeInt(t *testing.T) {
	var d indexDoc
	if err := json.Unmarshal([]byte(`{"apps":{"x":{"updated_at":"2026-11-17"}}}`), &d); err == nil {
		t.Fatal("updated_at 为字符串时应当解析失败")
	}
}
