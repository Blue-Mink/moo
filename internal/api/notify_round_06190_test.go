package api

import (
	"strings"
	"testing"

	"moo/internal/config"
)

// countReportEvent 统计 favorite_source_report 推送条数。
func countReportEvent(s *Server) int {
	n := 0
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "favorite_source_report" {
			n++
		}
	}
	return n
}

func lastReportEvent(s *Server) *config.NotifyLogEntry {
	last := -1
	for i := range s.Cfg.NotifyLog {
		if s.Cfg.NotifyLog[i].Event == "favorite_source_report" {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	return &s.Cfg.NotifyLog[last]
}

// TestFavoriteSourceReport（0.6.190）：首轮基线不推；新增 → 推（总数+新增+清单+源链接）；
// 移除 → 推；无变化不推。
func TestFavoriteSourceReport(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{
		FavoriteSources: []string{"s1"},
		Sources:         []config.SourceRef{{Name: "s1", URL: "https://repo.example/s1"}},
	}}

	// 第 1 轮：记基线，不推
	injectCatalog(t, s, []AppInfo{
		{Key: "a@s1", AppName: "a", DisplayName: "应用甲", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "b@s1", AppName: "b", DisplayName: "应用乙", Source: "s1", LatestVersion: "2.0.0"},
	})
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 0 {
		t.Fatalf("基线轮不应推, got %d", n)
	}

	// 第 2 轮：新增应用丙 → 推（总数 3 + 新增 + 全清单 + 源链接）
	injectCatalog(t, s, []AppInfo{
		{Key: "a@s1", AppName: "a", DisplayName: "应用甲", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "b@s1", AppName: "b", DisplayName: "应用乙", Source: "s1", LatestVersion: "2.0.0"},
		{Key: "c@s1", AppName: "c", DisplayName: "应用丙", Source: "s1", LatestVersion: "3.0.0"},
	})
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 1 {
		t.Fatalf("新增应推, got %d", n)
	}
	e := lastReportEvent(s)
	for _, want := range []string{
		"当前 3 个应用", "+1 新增", "**新增**", "应用丙（3.0.0）",
		"**全部应用**", "应用甲（1.0.0）", "https://repo.example/s1",
	} {
		if !strings.Contains(e.Content, want) {
			t.Errorf("报表缺 %q：\n%s", want, e.Content)
		}
	}
	if strings.Contains(e.Content, "**移除**") {
		t.Errorf("无移除不应有移除节：\n%s", e.Content)
	}

	// 第 3 轮：移除应用甲 → 推（-1 移除 + 移除节）
	injectCatalog(t, s, []AppInfo{
		{Key: "b@s1", AppName: "b", DisplayName: "应用乙", Source: "s1", LatestVersion: "2.0.0"},
		{Key: "c@s1", AppName: "c", DisplayName: "应用丙", Source: "s1", LatestVersion: "3.0.0"},
	})
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 2 {
		t.Fatalf("移除应推, got %d", n)
	}
	e = lastReportEvent(s)
	for _, want := range []string{"当前 2 个应用", "-1 移除", "**移除**", "应用甲（1.0.0）"} {
		if !strings.Contains(e.Content, want) {
			t.Errorf("移除报表缺 %q：\n%s", want, e.Content)
		}
	}

	// 第 4 轮：无变化 → 不推
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 2 {
		t.Fatalf("无变化不应推, got %d", n)
	}
}

// TestFavoriteSourceReportMulti（0.6.190；0.6.194 改每源一卡）：多源变化
// → 每个源单独一条通知，内容只含本源。
func TestFavoriteSourceReportMulti(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{
		FavoriteSources: []string{"s1", "s2"},
		Sources: []config.SourceRef{
			{Name: "s1", URL: "https://r1.example"},
			{Name: "s2", URL: "https://r2.example"},
		},
	}}

	// 基线轮
	injectCatalog(t, s, []AppInfo{
		{Key: "a@s1", AppName: "a", DisplayName: "应用A", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "x@s2", AppName: "x", DisplayName: "应用X", Source: "s2", LatestVersion: "1.0.0"},
	})
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 0 {
		t.Fatalf("基线轮不应推, got %d", n)
	}

	// 两源各新增 1 → 每源一条（0.6.194：不挤一张卡）
	injectCatalog(t, s, []AppInfo{
		{Key: "a@s1", AppName: "a", DisplayName: "应用A", Source: "s1", LatestVersion: "1.0.0"},
		{Key: "b@s1", AppName: "b", DisplayName: "应用B", Source: "s1", LatestVersion: "2.0.0"},
		{Key: "x@s2", AppName: "x", DisplayName: "应用X", Source: "s2", LatestVersion: "1.0.0"},
		{Key: "y@s2", AppName: "y", DisplayName: "应用Y", Source: "s2", LatestVersion: "2.0.0"},
	})
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 2 {
		t.Fatalf("多源变化应每源 1 条, got %d", n)
	}
	// 各源一条、互不串内容
	var sawS1, sawS2 bool
	for _, e := range s.Cfg.NotifyLog {
		if e.Event != "favorite_source_report" {
			continue
		}
		switch {
		case strings.Contains(e.Content, "「s1」"):
			sawS1 = true
			for _, want := range []string{"当前 2 个应用", "+1 新增", "应用B（2.0.0）", "https://r1.example"} {
				if !strings.Contains(e.Content, want) {
					t.Errorf("s1 报表缺 %q：\n%s", want, e.Content)
				}
			}
			if strings.Contains(e.Content, "s2") || strings.Contains(e.Content, "应用Y") {
				t.Errorf("s1 报表不应含 s2 内容：\n%s", e.Content)
			}
		case strings.Contains(e.Content, "「s2」"):
			sawS2 = true
			for _, want := range []string{"当前 2 个应用", "+1 新增", "应用Y（2.0.0）", "https://r2.example"} {
				if !strings.Contains(e.Content, want) {
					t.Errorf("s2 报表缺 %q：\n%s", want, e.Content)
				}
			}
			if strings.Contains(e.Content, "s1") || strings.Contains(e.Content, "应用B") {
				t.Errorf("s2 报表不应含 s1 内容：\n%s", e.Content)
			}
		}
	}
	if !sawS1 || !sawS2 {
		t.Fatalf("两条报表应各覆盖一个源: s1=%v s2=%v", sawS1, sawS2)
	}
}

// TestFavoriteSourceReportZeroApps（0.6.190）：关注源全部应用消失（源坏了）→ 推「当前 0 个应用」。
func TestFavoriteSourceReportZeroApps(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{
		FavoriteSources: []string{"s1"},
		Sources:         []config.SourceRef{{Name: "s1", URL: "https://r1.example"}},
	}}
	injectCatalog(t, s, []AppInfo{
		{Key: "a@s1", AppName: "a", DisplayName: "应用A", Source: "s1", LatestVersion: "1.0.0"},
	})
	s.favoriteSourceReportPass() // 基线
	injectCatalog(t, s, []AppInfo{}) // 源空了
	s.favoriteSourceReportPass()
	if n := countReportEvent(s); n != 1 {
		t.Fatalf("源清空应推, got %d", n)
	}
	e := lastReportEvent(s)
	for _, want := range []string{"当前 0 个应用", "-1 移除", "**移除**"} {
		if !strings.Contains(e.Content, want) {
			t.Errorf("清空报表缺 %q：\n%s", want, e.Content)
		}
	}
}
