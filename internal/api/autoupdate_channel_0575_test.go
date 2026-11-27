package api

// PATCH_0575 B（2026-11-27）：自动更新分支先探 10371（渠道不允许），
// 命中不下载整包、只计「跳过（渠道不允许）」并写负缓存。
//
// 0.6.578 批次 3a（D1）收窄：10371 之后平台侧还要**二次确认**——已装记录没有
// sourceID 就判为瞬时态放行（Moo 自己用 appcenter-cli 装的应正是这类）。
// 因此本文件只保留「无 sourceID → 继续尝试升级、不写 channel 负缓存」这条集成
// 用例；另外两条在各自更合适的位置覆盖：
//   - 「有 sourceID → 报可读错误」：platform.TestProbeFPKUpgradeNotAllowed
//   - 「连击两次才静默 + TTL 2h」：autoupdate_negcache_test.go
// 有 sourceID 的应用在目录层就被判为「无平台升级目标」（applyPlatformUpdateAuthority
// 清掉社区更新）或走 cloud 通道，自动更新轮次里走不到 FPK 预检，所以造不出
// 「channel 跳过」的集成场景——这也是批次 3a 的既有设计，不是用例缺口。

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/platform"
	"moo/internal/source"
)

// startChannelBlockedDaemon 假 daemon：已装 demo 1.0.0、平台 update/info 恒回
// 10371。sourceID 决定平台侧二次确认的结论（空 = appcenter 不跟踪 = 放行）。
func startChannelBlockedDaemon(t *testing.T, sourceID string) *auTally {
	t.Helper()
	c := &auTally{}
	// 关键：不给 upgradeInfo —— 平台没报官方可升级版本，这个「有更新」是社区
	// 目录给的（j.official=false），才会走 FPK 升级通道并撞上 10371。
	// 平台报了 upgradeInfo 就走 cloud 通道（与应用中心 UI 同源），不在本段范围。
	installed := fmt.Sprintf(`{"code":0,"data":{"total":1,"list":[{"appName":"demo",`+
		`"name":"演示","version":"1.0.0","sourceID":%q,"status":"running"}]}}`, sourceID)

	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, installed)
	})
	mux.HandleFunc("/rpc/v1/update/info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":10371,"msg":"该应用不支持通过当前渠道升级"}`)
	})
	mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&c.upgrade, 1)
		fmt.Fprint(w, `{"code":16000,"msg":"测试注入：不应走到下载"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{}}`)
	})

	lis, err := net.Listen("unix", t.TempDir()+"/daemon.sock")
	if err != nil {
		t.Fatalf("起假 daemon 失败：%v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.rec(r)
		mux.ServeHTTP(w, r)
	}))
	srv.Listener = lis
	srv.Start()
	t.Cleanup(srv.Close)
	platform.SetDaemonSocketForTest(lis.Addr().String())
	return c
}

// TestAutoUpdateProceedsOnChannel10371WithoutSourceID 批次 3a：无 sourceID 的
// 10371 判为瞬时态 → 自动更新继续走社区源升级（此前会被误拦、等 12h），
// 且不写 channel 负缓存。
//
// 假 daemon 的 download/task 不会被调用（自动更新的社区分支先进 FPK 管线），
// 所以用「操作队列历史里有 demo 的 update 记录」作为「真的进了管线」的标记：
// 被预检拦下时根本不会起操作。
// 反向验证：去掉二次确认（10371 直接 return error）→ 本用例 FAIL
// （没有操作记录，且有 Hits=1 的 channel 条目）。
func TestAutoUpdateProceedsOnChannel10371WithoutSourceID(t *testing.T) {
	c := startChannelBlockedDaemon(t, "")
	s := autoUpdateTestServer(t, true)
	requirePending(t, s)
	// 给一个「源管理器是空的」管线：InstallWithParams 会在 p.Src.Get 处立刻
	// 返回「应用不存在」，不下载、不碰 daemon 下载通道（本用例只关心有没有被
	// 预检挡在门外）。
	s.Pipe = pipeline.New(source.NewManager(config.Default()), t.TempDir()+"/staging", t.TempDir()+"/downloads")

	runAutoUpdatePass(t, s)

	entered := false
	for _, v := range s.Ops.History() {
		if v.Kind == "update" && v.Target == "demo" {
			entered = true
		}
	}
	if !entered {
		t.Fatalf("无 sourceID 的 10371 应放行并进入升级管线，实际没有 demo 的 update 操作；请求 %v", c.dump())
	}
	for _, e := range s.autoUpdateSkips() {
		if e.Reason == autoUpdateSkipReasonChannel {
			t.Fatalf("放行路径不该写 channel 负缓存，实际 %+v", e)
		}
	}
}
