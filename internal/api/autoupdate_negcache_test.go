package api

// autoupdate_negcache_test.go —— 自动更新「需人工处理」负缓存（0.6.543 引入 / 0.6.545 收口）。

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func negCacheServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	return &Server{}
}

func TestWizardSkipMarkAndHit(t *testing.T) {
	s := negCacheServer(t)
	s.markAutoUpdateSkip("moo.livetest", "1.1.0", "abc123", autoUpdateSkipReasonWizard, []string{"moo_test_note"})

	m := s.autoUpdateSkips()
	if !autoUpdateSkipHit(m, "moo.livetest", "1.1.0", "abc123", autoUpdateSkipReasonWizard) {
		t.Fatalf("期望命中缓存: %#v", m)
	}
	// 应用名大小写不敏感
	if !autoUpdateSkipHit(m, "Moo.LiveTest", "1.1.0", "ABC123", autoUpdateSkipReasonWizard) {
		t.Fatalf("大小写不敏感匹配失效")
	}
	// 同版本换包（sha 变）必须不命中 —— 0.6.545 修的盲区
	if autoUpdateSkipHit(m, "moo.livetest", "1.1.0", "def456", autoUpdateSkipReasonWizard) {
		t.Fatalf("同版本换包不应命中缓存")
	}
	// 目标版本变化不命中
	if autoUpdateSkipHit(m, "moo.livetest", "1.2.0", "abc123", autoUpdateSkipReasonWizard) {
		t.Fatalf("不同目标版本不应命中缓存")
	}
	// reason 不同不命中（平台标志消失后立刻恢复探测）
	if autoUpdateSkipHit(m, "moo.livetest", "1.1.0", "abc123", autoUpdateSkipReasonBlocked) {
		t.Fatalf("不同 reason 不应命中缓存")
	}
}

func TestSkipTTLByReason(t *testing.T) {
	s := negCacheServer(t)
	s.markAutoUpdateSkip("app.w", "1.0.0", "", autoUpdateSkipReasonWizard, nil)
	s.markAutoUpdateSkip("app.b", "1.0.0", "", autoUpdateSkipReasonBlocked, []string{"平台自动更新已失败"})

	m := s.autoUpdateSkips()
	parse := func(app string) time.Time {
		for _, e := range m {
			if e.App == app {
				ts, err := time.Parse(time.RFC3339, e.Until)
				if err != nil {
					t.Fatalf("until 不可解析: %q", e.Until)
				}
				return ts
			}
		}
		t.Fatalf("缺条目 %s", app)
		return time.Time{}
	}
	w := time.Until(parse("app.w"))
	b := time.Until(parse("app.b"))
	if w < 11*time.Hour || w > 13*time.Hour {
		t.Fatalf("wizard TTL 应为 12h，实际 %s", w)
	}
	if b < time.Hour || b > 3*time.Hour {
		t.Fatalf("blocked TTL 应为 2h，实际 %s", b)
	}
	if b >= w {
		t.Fatalf("blocked TTL 必须短于 wizard TTL")
	}
}

func TestSkipExpired(t *testing.T) {
	s := negCacheServer(t)
	p := s.autoUpdateSkipPath()
	st := autoUpdateSkipState{Entries: []autoUpdateSkipEntry{{
		App: "moo.livetest", Version: "1.1.0", Sha: "abc", Reason: autoUpdateSkipReasonWizard,
		Until: time.Now().Add(-time.Minute).Format(time.RFC3339),
	}}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if autoUpdateSkipHit(s.autoUpdateSkips(), "moo.livetest", "1.1.0", "abc", autoUpdateSkipReasonWizard) {
		t.Fatalf("过期条目不应命中")
	}
	s.saveAutoUpdateSkips(s.autoUpdateSkips())
	raw, _ := os.ReadFile(p)
	var again autoUpdateSkipState
	if json.Unmarshal(raw, &again) != nil || len(again.Entries) != 0 {
		t.Fatalf("过期条目应在落盘时被清理")
	}
}

func TestSkipLegacyEntryWithoutReason(t *testing.T) {
	s := negCacheServer(t)
	// 0.6.543 写下的旧格式：无 reason、无 sha
	raw := []byte(`{"entries":[{"app":"old.app","version":"2.0.0","until":"` +
		time.Now().Add(time.Hour).Format(time.RFC3339) + `"}]}`)
	if err := os.WriteFile(s.autoUpdateSkipPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if !autoUpdateSkipHit(s.autoUpdateSkips(), "old.app", "2.0.0", "", autoUpdateSkipReasonWizard) {
		t.Fatalf("旧格式条目应按 wizard 命中")
	}
}

func TestSkipCorruptAndCapAndClear(t *testing.T) {
	s := negCacheServer(t)
	if err := os.WriteFile(s.autoUpdateSkipPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := len(s.autoUpdateSkips()); n != 0 {
		t.Fatalf("坏文件应按空处理，实际 %d 条", n)
	}
	s.markAutoUpdateSkip("app.x", "2.0.0", "", autoUpdateSkipReasonWizard, nil)
	if !autoUpdateSkipHit(s.autoUpdateSkips(), "app.x", "2.0.0", "", autoUpdateSkipReasonWizard) {
		t.Fatalf("坏文件后写入失败")
	}

	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	m := map[string]autoUpdateSkipEntry{}
	for i := 0; i < autoUpdateSkipMaxEntries+50; i++ {
		app := "cap." + time.Duration(i).String()
		m[autoUpdateSkipKey(app, "1.0.0", "", autoUpdateSkipReasonWizard)] = autoUpdateSkipEntry{
			App: app, Version: "1.0.0", Reason: autoUpdateSkipReasonWizard, Until: future,
		}
	}
	s.saveAutoUpdateSkips(m)
	if n := len(s.autoUpdateSkips()); n > autoUpdateSkipMaxEntries {
		t.Fatalf("条目上限失效: %d", n)
	}

	s2 := negCacheServer(t)
	s2.markAutoUpdateSkip("app.keep", "1.0.0", "", autoUpdateSkipReasonWizard, nil)
	s2.markAutoUpdateSkip("app.drop", "1.0.0", "", autoUpdateSkipReasonBlocked, nil)
	s2.clearAutoUpdateWizardSkip("APP.DROP")
	sk := s2.autoUpdateSkips()
	if autoUpdateSkipHit(sk, "app.drop", "1.0.0", "", autoUpdateSkipReasonBlocked) {
		t.Fatalf("clear 未删除条目")
	}
	if !autoUpdateSkipHit(sk, "app.keep", "1.0.0", "", autoUpdateSkipReasonWizard) {
		t.Fatalf("clear 误删其他应用条目")
	}
}

func TestSkipLegacyFileMigration(t *testing.T) {
	s := negCacheServer(t)
	legacy := s.legacyAutoUpdateSkipPath()
	raw := []byte(`{"entries":[{"app":"old.app","version":"2.0.0","until":"` +
		time.Now().Add(time.Hour).Format(time.RFC3339) + `","missing":"x"}]}`)
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m := s.autoUpdateSkips()
	if !autoUpdateSkipHit(m, "old.app", "2.0.0", "", autoUpdateSkipReasonWizard) {
		t.Fatalf("迁移后条目丢失: %#v", m)
	}
	if _, err := os.Stat(s.autoUpdateSkipPath()); err != nil {
		t.Fatalf("新文件未生成: %v", err)
	}
	if _, err := os.Stat(legacy); err == nil {
		t.Fatalf("旧文件应在新文件落盘后删除")
	}
	// 迁移后再读仍命中（幂等）
	if !autoUpdateSkipHit(s.autoUpdateSkips(), "old.app", "2.0.0", "", autoUpdateSkipReasonWizard) {
		t.Fatalf("二次读取丢失")
	}
}
