package source

// ghforks_340_test.go —— 0.6.340 溯源提速批次的两个回归点：
//   ① parseForkTime 必须容忍 `+0800`（Go 默认布局）——预取脚本/手工写的
//      缓存常用该格式，严格 RFC3339 解析失败会被当成「过期」反复重拉；
//   ② 缓存文件缺失/为空时 load 不 panic，且 Lookup 维持 unknown。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseForkTimeAcceptsBothLayouts(t *testing.T) {
	for _, s := range []string{
		"2026-10-19T00:14:57+08:00", // RFC3339（预取脚本 1019 版）
		"2026-10-19T00:14:57+0800",  // Go 默认布局（0.6.334 版预取脚本）
	} {
		got, err := parseForkTime(s)
		if err != nil {
			t.Fatalf("parseForkTime(%q) 失败: %v", s, err)
		}
		want := time.Date(2026, 10, 19, 0, 14, 57, 0, time.FixedZone("", 8*3600))
		if !got.Equal(want) {
			t.Fatalf("parseForkTime(%q) = %v, want %v", s, got, want)
		}
	}
	if _, err := parseForkTime("garbage"); err == nil {
		t.Fatalf("非法时间戳应报错")
	}
}

// 0.6.340：`+0800` 格式的记录应被判为「新鲜」，不得重新入队（省额度）。
func TestEnqueueRespectsPlus0800Freshness(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ghforks.json")
	now := time.Now().Format("2006-01-02T15:04:05-0700")
	raw := `{"repos":{"a/fndepot":{"fork":true,"parent":"b/fndepot","fetched_at":"` + now + `"}}}`
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	g := NewGHForks(p)
	g.Enqueue("a/fndepot")
	if n, err := g.RefreshRound(context.Background(), 10); err != nil || n != 0 {
		t.Fatalf("新鲜记录不应重拉: n=%d err=%v", n, err)
	}
}

// 缓存缺失：load 容忍（不 panic），Lookup 返回 unknown。
func TestLoadMissingCacheIsTolerated(t *testing.T) {
	g := NewGHForks(filepath.Join(t.TempDir(), "nope.json"))
	if _, _, known := g.Lookup("a/b"); known {
		t.Fatalf("缺失缓存时应为 unknown")
	}
}
