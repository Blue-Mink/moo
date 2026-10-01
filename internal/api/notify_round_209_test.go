package api

import (
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// favSeed 直接种目录缓存（绕过 buildCatalog 的源/daemon 依赖）。
func favSeed(s *Server, cats ...AppInfo) {
	s.catalogMu.Lock()
	s.catalogData = cats
	s.catalogAt = time.Now()
	s.catalogMu.Unlock()
}

func favPushCount(s *Server) int {
	n := 0
	for _, e := range s.Cfg.NotifyLog {
		if e.Event == "favorite_update" {
			n++
		}
	}
	return n
}

// 0.6.209：收藏键回退 + 已装收藏同权威信号判定。
// 背景：官方源空窗期规范卡回落社区源、版本号体系跳变（Gitea
// 1.27.3→28.0.0）被误判为「更新」推渠道；且官方条目出现后规范卡 key
// 变带 @源后缀（Gitea@fnos-official），收藏存的旧裸 key 精确未命中、
// 收藏静默失效。
func TestFavoriteUpdatePass209(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	cfg := config.Default()
	cfg.Favorites = []string{"Gitea", "uptime-kuma"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	official := func(hasUpdate bool, latest, avail string) AppInfo {
		return AppInfo{
			Key: "Gitea@fnos-official", AppName: "Gitea", DisplayName: "Gitea",
			Source: OfficialSourceID, Installed: true, InstalledVersion: "1.27.3",
			LatestVersion: latest, HasUpdate: hasUpdate, AvailableVersion: avail,
		}
	}

	// ① 裸键收藏 + 权威信号无更新（社区源有 28.0.0 噪音）→ 不推、不记
	favSeed(s,
		official(false, "1.27.3", ""),
		AppInfo{Key: "gitea@fnos-store", AppName: "gitea", DisplayName: "Gitea",
			Source: "fnos-store", LatestVersion: "28.0.0"},
	)
	s.favoriteUpdatePass()
	if got := s.Cfg.NotifyFavNotified["Gitea"]; got != "" {
		t.Fatalf("权威信号无更新不得推/不得记版本: %q", got)
	}
	if n := favPushCount(s); n != 0 {
		t.Fatalf("权威信号无更新不得产生通知: %d", n)
	}

	// ② 未装收藏（uptime-kuma）：首次只记基线、不推
	favSeed(s,
		official(false, "1.27.3", ""),
		AppInfo{Key: "uptime-kuma@fnos-store", AppName: "uptime-kuma",
			DisplayName: "Uptime Kuma", Source: "fnos-store", LatestVersion: "2.4.1"},
	)
	s.favoriteUpdatePass()
	if got := s.Cfg.NotifyFavNotified["uptime-kuma"]; got != "2.4.1" {
		t.Fatalf("未装收藏应记基线 2.4.1: %q", got)
	}
	if n := favPushCount(s); n != 0 {
		t.Fatalf("首次收藏基线不得推: %d", n)
	}

	// ③ 裸键收藏 + 权威信号有更新 → 推一次（按裸键记账）
	favSeed(s, official(true, "1.28.0", "1.28.0"))
	s.favoriteUpdatePass()
	if got := s.Cfg.NotifyFavNotified["Gitea"]; got != "1.28.0" {
		t.Fatalf("应推一次并记 1.28.0: %q", got)
	}
	if n := favPushCount(s); n != 1 {
		t.Fatalf("应恰好推 1 次: %d", n)
	}

	// ④ 同版本再跑 → 去重不重复推
	s.favoriteUpdatePass()
	if n := favPushCount(s); n != 1 {
		t.Fatalf("同版本不得重复推: %d", n)
	}

	// ⑤ 更高权威版本 → 再推一次
	favSeed(s, official(true, "1.29.0", "1.29.0"))
	s.favoriteUpdatePass()
	if got := s.Cfg.NotifyFavNotified["Gitea"]; got != "1.29.0" {
		t.Fatalf("应记 1.29.0: %q", got)
	}
	if n := favPushCount(s); n != 2 {
		t.Fatalf("新权威版本应再推 1 次（共 2）: %d", n)
	}
}

// ⑥ 回退匹配的择优：已装卡优先于未装同名卡；都未装时官方源优先。
func TestFavoriteUpdatePass209KeyFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	cfg := config.Default()
	cfg.Favorites = []string{"NodeJS"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}

	// 收藏存裸键 "NodeJS"；目录里有官方已装卡 + 社区未装卡（更高版本噪音）
	favSeed(s,
		AppInfo{Key: "NodeJS@fnos-official", AppName: "NodeJS", DisplayName: "Node.js",
			Source: OfficialSourceID, Installed: true, InstalledVersion: "22.20.0",
			LatestVersion: "22.20.0", HasUpdate: false},
		AppInfo{Key: "nodejs@wabisabi", AppName: "nodejs", DisplayName: "Node.js",
			Source: "wabisabi", LatestVersion: "22.23.2"},
	)
	s.favoriteUpdatePass()
	// 已装卡优先命中、权威无更新 → 不推；若错误命中社区卡则会推（旧行为）
	if n := favPushCount(s); n != 0 {
		t.Fatalf("已装卡应优先命中且无更新不推: %d", n)
	}
	if got := s.Cfg.NotifyFavNotified["NodeJS"]; got != "" {
		t.Fatalf("不得记社区噪音版本: %q", got)
	}
}
