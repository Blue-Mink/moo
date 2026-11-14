package main

import (
	"path/filepath"
	"testing"
	"time"
)

// 0.2.63（B7）：重建索引（discover 重新构造 indexedApp）不得抹掉检查状态。
// 之前 put 是整体替换 → 点一次「更新索引」红点/琥珀就静默消失。
func TestPutCarriesOverCheckState(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	now := time.Now().Unix()
	old := &indexedApp{
		appEntry:      appEntry{DisplayName: "旧", Version: "2.0.0"},
		Appname:       "demo",
		Source:        "https://github.com/x/y",
		LatestVersion: "2.0.0",
		LatestSeenAt:  now - 100,
		PrevVersion:   "1.0.0",
		UpdatedAt:     now - 50,
		UpdatedSeenAt: now - 40,
		LastCheck:     now - 30,
		LastCheckErr:  "上次网络超时",
	}
	if err := ix.upsert(old); err != nil {
		t.Fatal(err)
	}
	// 模拟 discover 重新构造：只有基础字段，检查字段全空
	fresh := &indexedApp{
		appEntry: appEntry{DisplayName: "新", Version: "2.1.0"},
		Appname:  "demo",
		Source:   "https://github.com/x/y",
	}
	if err := ix.upsert(fresh); err != nil {
		t.Fatal(err)
	}
	got := ix.get("demo")
	if got.Version != "2.1.0" || got.DisplayName != "新" {
		t.Fatalf("基础字段没更新：%+v", got)
	}
	if got.LatestVersion != "2.0.0" || got.PrevVersion != "1.0.0" ||
		got.UpdatedAt != now-50 || got.UpdatedSeenAt != now-40 ||
		got.LatestSeenAt != now-100 || got.LastCheck != now-30 ||
		got.LastCheckErr != "上次网络超时" {
		t.Fatalf("检查状态被清空：%+v", got)
	}
}

// 0.2.63（A4）：ack 只标记「确实有未读」的应用；全部已读 = 空名单。
func TestAckUpdates(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	now := time.Now().Unix()
	mk := func(name string, updatedAt, seenAt int64) *indexedApp {
		return &indexedApp{
			appEntry:      appEntry{Version: "2.0.0"},
			Appname:       name,
			PrevVersion:   "1.0.0",
			UpdatedAt:     updatedAt,
			UpdatedSeenAt: seenAt,
		}
	}
	_ = ix.upsert(mk("a", now, 0))           // 有未读
	_ = ix.upsert(mk("b", now, now))         // 已读
	_ = ix.upsert(&indexedApp{Appname: "c"}) // 从未升级

	gen := ix.generation()
	n, err := ix.ackUpdates([]string{"a"}, gen)
	if err != nil || n != 1 {
		t.Fatalf("单条 ack 应标记 1 条，得 n=%d err=%v", n, err)
	}
	if got := ix.get("a"); got.UpdatedSeenAt != now {
		t.Fatalf("a 未写入已读时间：%+v", got)
	}
	// 再点一次：没有未读了，不该写
	if n2, _ := ix.ackUpdates([]string{"a"}, gen); n2 != 0 {
		t.Fatalf("重复 ack 应 0 条，得 %d", n2)
	}
	// 全部已读：只标记仍有未读的（这里已无）
	if n3, _ := ix.ackUpdates(nil, gen); n3 != 0 {
		t.Fatalf("全部已读应 0 条，得 %d", n3)
	}
	// 重新产生未读后再全部已读
	a := ix.get("a")
	a.UpdatedAt = now + 5
	a.UpdatedSeenAt = 0
	_ = ix.upsert(a)
	if n4, _ := ix.ackUpdates(nil, gen); n4 != 1 {
		t.Fatalf("全部已读应标记 1 条，得 %d", n4)
	}
}

// 0.2.63（A6）：检查失败（Latest 为空）不得清掉已知的「上游最新版本」；
// 也不得刷新「已更新」时间（否则红点被无声续期）。
func TestApplyCheckResultsKeepsLatestOnFailure(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	now := time.Now().Unix()
	_ = ix.upsert(&indexedApp{
		appEntry:      appEntry{Version: "1.0.0"},
		Appname:       "demo",
		LatestVersion: "2.0.0",
		LatestSeenAt:  now - 10,
		PrevVersion:   "0.9.0",
		UpdatedAt:     now - 20,
		LastCheck:     now - 30,
	})
	gen := ix.generation()
	// 检查失败：Latest/Prev 都空，只有 ErrMsg
	if _, err := ix.applyCheckResults([]checkOutcome{{Appname: "demo", ErrMsg: "dial tcp: timeout"}}, gen); err != nil {
		t.Fatal(err)
	}
	got := ix.get("demo")
	if got.LatestVersion != "2.0.0" {
		t.Fatalf("失败把 latest 抹掉了：%q", got.LatestVersion)
	}
	if got.UpdatedAt != now-20 {
		t.Fatalf("无更新轮次不该刷 updated_at：%d", got.UpdatedAt)
	}
	if got.LastCheckErr != "dial tcp: timeout" || got.LastCheck == now-30 {
		t.Fatalf("last_check 状态没更新：%+v", got)
	}
}

// 0.2.63：真升级 → 刷新 updated_at（新未读），latest 跟到新版本；
// 之后 ack 一次，再跑一轮「已是最新」不会把红点重新点亮。
func TestUpgradeThenAckStaysRead(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	_ = ix.upsert(&indexedApp{appEntry: appEntry{Version: "1.0.0"}, Appname: "demo"})
	gen := ix.generation()
	if _, err := ix.applyCheckResults([]checkOutcome{{Appname: "demo", Latest: "2.0.0", Prev: "1.0.0"}}, gen); err != nil {
		t.Fatal(err)
	}
	got := ix.get("demo")
	if got.UpdatedAt == 0 || got.PrevVersion != "1.0.0" || got.LatestVersion != "2.0.0" {
		t.Fatalf("升级状态没写入：%+v", got)
	}
	if n, _ := ix.ackUpdates(nil, gen); n != 1 {
		t.Fatalf("应标记 1 条未读，得 %d", n)
	}
	seen := ix.get("demo").UpdatedSeenAt
	// 下一轮：已是最新（Latest=当前版本，Prev 空）
	if _, err := ix.applyCheckResults([]checkOutcome{{Appname: "demo", Latest: "2.0.0"}}, gen); err != nil {
		t.Fatal(err)
	}
	after := ix.get("demo")
	if after.UpdatedSeenAt != seen {
		t.Fatalf("已读状态被后续同步改掉：%d → %d", seen, after.UpdatedSeenAt)
	}
	if after.UpdatedAt > after.UpdatedSeenAt {
		t.Fatalf("不该重新产生未读：updated_at=%d seen=%d", after.UpdatedAt, after.UpdatedSeenAt)
	}
}
