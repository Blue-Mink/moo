package reco

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestSnapshotFileWellFormed 快照 JSON 可解析、三 key 齐备且顺序=用户定稿
// （fnos-apps-store → fndepot → fn-knock，上→下不随机）。
func TestSnapshotFileWellFormed(t *testing.T) {
	var d doc
	if err := json.Unmarshal(snapshotsJSON, &d); err != nil {
		t.Fatalf("snapshots.json 解析失败: %v", err)
	}
	want := []string{"fnos-apps-store", "fndepot", "fn-knock"}
	if len(d.Apps) != len(want) {
		t.Fatalf("快照应含 3 个应用，实际 %d", len(d.Apps))
	}
	for i, w := range want {
		if d.Apps[i].AppName != w {
			t.Errorf("快照第 %d 位应为 %q，实际 %q", i+1, w, d.Apps[i].AppName)
		}
	}
	if d.GeneratedAt == "" {
		t.Error("快照缺 generated_at 抓取时间戳")
	}
}

// TestFixedOrderAndLookup 固定顺序输出 + 大小写/分隔符漂移不敏感查找。
func TestFixedOrderAndLookup(t *testing.T) {
	order := FixedOrder()
	want := []string{"fnos-apps-store", "fndepot", "fn-knock"}
	if len(order) != len(want) {
		t.Fatalf("FixedOrder 长度 %d，期望 %d", len(order), len(want))
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("FixedOrder[%d]=%q，期望 %q", i, order[i], w)
		}
	}
	for _, q := range []string{"fn-knock", "FN-KNOCK", "Fn_Knock", "fn_knock"} {
		if Lookup(q) == nil {
			t.Errorf("Lookup(%q) 应命中 fn-knock 快照", q)
		}
	}
	if Lookup("not-in-snapshots") != nil {
		t.Error("未收录应用应返回 nil")
	}
}

// TestSnapshotFieldsComplete 每个快照字段完整（验收口径）：展示字段非空
// 关键项、readme 全文非空、releases≥1 且每版本至少 1 个带直链的包、
// 图标 base64 可解码为 PNG 魔数。
func TestSnapshotFieldsComplete(t *testing.T) {
	for _, name := range []string{"fnos-apps-store", "fndepot", "fn-knock"} {
		s := Lookup(name)
		if s == nil {
			t.Fatalf("缺 %s 快照", name)
		}
		if s.DisplayName == "" {
			t.Errorf("%s display_name 为空", name)
		}
		if s.Desc == "" {
			t.Errorf("%s desc 为空", name)
		}
		if s.Source == "" || s.SourceURL == "" {
			t.Errorf("%s 源名/源地址缺失", name)
		}
		if s.Author == "" {
			t.Errorf("%s author 为空", name)
		}
		if s.Readme == "" {
			t.Errorf("%s readme 全文为空", name)
		}
		if len(s.Releases) < 1 {
			t.Fatalf("%s releases 为空", name)
		}
		for _, r := range s.Releases {
			if r.Version == "" {
				t.Errorf("%s 存在无版本号的 release", name)
			}
			if len(r.Packages) < 1 {
				t.Errorf("%s %s 无安装包", name, r.Version)
			}
			for _, p := range r.Packages {
				if p.DownloadURL == "" || p.DownloadURL[0] != 'h' {
					t.Errorf("%s %s 包下载链接异常: %q", name, r.Version, p.DownloadURL)
				}
			}
		}
		data, ctype, ok := s.Icon()
		if !ok {
			t.Fatalf("%s 图标 base64 解码失败", name)
		}
		if !bytes.HasPrefix(data, []byte("\x89PNG")) {
			t.Errorf("%s 图标非 PNG 魔数（%s）", name, ctype)
		}
	}
}
