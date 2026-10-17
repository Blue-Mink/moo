package api

// notify_new_events_326z_test.go —— 0.6.326 通知批次三个新事件的单测。
//
//	T1 upstream_corrected：纠正明细收集正确（判定不变）/ 三档不误报 /
//	   首次推一次 / 同一 应用×版本 不重推 / 目标版本变化再推 / 事件关不推；
//	   另有载荷级捕获验证六变体形状（卡片·mdv2 × 简洁·友好·完整，
//	   照 CONVENTIONS.md §6.3 的本地自测方式：演示渠道指向本机 HTTP 端点）。
//	T2 notify_channel_error：连续 3 次失败触发 / 未达门槛不推 / 1h 冷却内不重推 /
//	   冷却过后复推 / 任一成功清零 / 多渠聚合 / 本事件自身与 welcome 不计入
//	   （防自激）/ 事件关不推。
//	T3 official_session_expired：正常→失效推一次 / 持续失效不重推 / 6h 冷却复推 /
//	   恢复不推（本次无 recovered）/ 0 应用也算失效 / Panel==nil 不推 / 事件关不推。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/panel"
	"moo/internal/platform"
	"moo/internal/source"
)

// countNotifyEvent 通知记录里某事件的条数。
func countNotifyEvent(cfg *config.Config, event string) int {
	n := 0
	for _, e := range cfg.NotifyLog {
		if e.Event == event {
			n++
		}
	}
	return n
}

// lastNotifyEvent 通知记录里某事件的最新一条（无 = 零值）。
func lastNotifyEvent(cfg *config.Config, event string) config.NotifyLogEntry {
	var out config.NotifyLogEntry
	for _, e := range cfg.NotifyLog {
		if e.Event == event {
			out = e
		}
	}
	return out
}

// ───────────────────────── T1：upstream_corrected ─────────────────────────

func TestUpstreamCorrectedCollect_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := newMatrixServer(t)

	// ① upstream 档：真被纠正 → 恰好 1 条明细，且纠正行为本身逐字不变
	cards := matrixCards()
	s.applyUpstreamLineage(cards)
	hits := resolveUpdateTargetSelection(cards, "upstream", false, map[string]platform.InstalledApp{})
	if len(hits) != 1 {
		t.Fatalf("应收集 1 条纠正记录, got %d %+v", len(hits), hits)
	}
	h := hits[0]
	if h.AppName != "app-a" || h.DisplayName != "app-a" || h.From != "1.9.0" ||
		h.To != "2.5.0" || h.UpstreamSource != "上游源" || h.Source != "搬运源" {
		t.Fatalf("纠正明细不符: %+v", h)
	}
	if !cards[0].HasUpdate || cards[0].AvailableVersion != "2.5.0" || cards[0].UpdateFromSource != "上游源" {
		t.Fatalf("纠正判定本身不得变化: has=%v avail=%q from=%q",
			cards[0].HasUpdate, cards[0].AvailableVersion, cards[0].UpdateFromSource)
	}

	// ② 其它三档不产生纠正记录（不该误报「已纠正到上游」）
	for _, pol := range []string{"strict", "origin", "lineage"} {
		c := matrixCards()
		s.applyUpstreamLineage(c)
		if got := resolveUpdateTargetSelection(c, pol, false, map[string]platform.InstalledApp{}); got != nil {
			t.Fatalf("%s 档不应产生纠正记录: %+v", pol, got)
		}
	}

	// ③ 首次推送：1 条记录 + 去重表落目标版本 + 成功态
	s.notifyUpstreamCorrected(hits)
	if n := countNotifyEvent(s.Cfg, "upstream_corrected"); n != 1 {
		t.Fatalf("应推 1 条, got %d", n)
	}
	if got := s.Cfg.NotifyUpstreamNotified["app-a"]; got != "2.5.0" {
		t.Fatalf("去重表应记目标版本: %q", got)
	}
	e := lastNotifyEvent(s.Cfg, "upstream_corrected")
	if !strings.Contains(e.Content, "已纠正到上游原作者版本") ||
		!strings.Contains(e.Content, "1.9.0 → 2.5.0") ||
		!strings.Contains(e.Content, "（上游源）") {
		t.Fatalf("友好正文不符: %q", e.Content)
	}
	if !e.OK {
		t.Fatalf("纠正事件应为成功态: %+v", e)
	}

	// ④ 同一 应用×版本 重复条件不重推
	s.notifyUpstreamCorrected(hits)
	if n := countNotifyEvent(s.Cfg, "upstream_corrected"); n != 1 {
		t.Fatalf("重复条件不得重推: %d", n)
	}

	// ⑤ 上游再发新版（目标版本变化）→ 再推一次
	s.notifyUpstreamCorrected([]upstreamCorrection{{
		AppName: "app-a", DisplayName: "app-a", Source: "搬运源",
		From: "2.5.0", To: "2.6.0", UpstreamSource: "上游源",
	}})
	if n := countNotifyEvent(s.Cfg, "upstream_corrected"); n != 2 {
		t.Fatalf("目标版本变化应再推: %d", n)
	}

	// ⑥ 事件开关关 → 不推、不记新基线
	s.Cfg.NotifyEvents = map[string]bool{"upstream_corrected": false}
	s.notifyUpstreamCorrected([]upstreamCorrection{{
		AppName: "app-b", DisplayName: "app-b", From: "1.0.0", To: "3.0.0", UpstreamSource: "上游源",
	}})
	if n := countNotifyEvent(s.Cfg, "upstream_corrected"); n != 2 {
		t.Fatalf("事件关不得推: %d", n)
	}
	if _, ok := s.Cfg.NotifyUpstreamNotified["app-b"]; ok {
		t.Fatal("事件关不得记去重基线（开了以后应能补推当前状态）")
	}
	// 该事件不冒充「应用更新摘要」
	if n := countNotifyEvent(s.Cfg, "updates_available"); n != 0 {
		t.Fatalf("不得与 updates_available 重复推送: %d", n)
	}
}

// TestUpstreamCorrectedVariants_326Z 载荷级捕获：六变体（卡片/mdv2 × 简洁/友好/完整）
// 经真实 fan-out 落地的形状（CONVENTIONS §6.3 本地自测方式）。
//
// 注意企微既有设计（send.go sendWeCom）：verbosity=full 先短路到 sendWeComFull
// （classic markdown 全量列表，与 Format 无关）→ 两个 *-full 渠道收到的是
// ContentFull 的 markdown 列表；concise/friendly 才按 Format 分发（card / mdv2 表）。
func TestUpstreamCorrectedVariants_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	var mu sync.Mutex
	bodies := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Query().Get("name")] = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0}`)) // 企微成功响应（否则会计入渠道失败）
	}))
	defer ts.Close()

	cfg := &config.Config{NotifyViewBase: "http://notify.example.test"}
	s := &Server{Cfg: cfg}
	add := func(name, format, verbosity string) {
		cfg.NotifyChannels = append(cfg.NotifyChannels, config.NotifyChannel{
			ID: "ch_" + name, Type: "wecom", Name: name, Enabled: true,
			Format: format, Verbosity: verbosity, Timeout: 5,
			Params: map[string]string{"webhook_url": ts.URL + "/hook?name=" + name},
		})
	}
	add("card-concise", "card", "concise")
	add("card-friendly", "card", "friendly")
	add("card-full", "card", "full")
	add("mdv2-concise", "markdown_v2", "concise")
	add("mdv2-friendly", "markdown_v2", "friendly")
	add("mdv2-full", "markdown_v2", "full")

	// 10 条纠正：友好/表格版按前 8 截断，完整模式全量（可区分两者）
	var hits []upstreamCorrection
	for i := 1; i <= 10; i++ {
		app := fmt.Sprintf("app-%d", i)
		hits = append(hits, upstreamCorrection{
			AppName: app, DisplayName: app, Source: "搬运源",
			From: "1.0.0", To: "2.9.0", UpstreamSource: "上游源",
		})
	}
	s.notifyUpstreamCorrected(hits)

	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n >= 6 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	body := func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		return bodies[name]
	}
	for _, n := range []string{"card-concise", "card-friendly", "card-full",
		"mdv2-concise", "mdv2-friendly", "mdv2-full"} {
		if body(n) == "" {
			t.Fatalf("渠道 %s 未收到载荷（六变体必须真发出去）", n)
		}
	}

	// 卡片·简洁：副题 = ConciseSub（上游源名 + 数量），两列行只留 3 行
	if c := body("card-concise"); !strings.Contains(c, `"sub_title_text":"上游源 · 10 个应用"`) ||
		strings.Count(c, "keyname") != 3 {
		t.Fatalf("卡片·简洁形状不符: %s", c)
	}
	// 卡片·友好：副题 = 正文首行，两列行 6 行（企微上限）
	if c := body("card-friendly"); !strings.Contains(c, "已纠正到上游原作者版本") ||
		strings.Count(c, "keyname") != 6 {
		t.Fatalf("卡片·友好形状不符: %s", c)
	}
	// 卡片·完整：企微「完整」模式走 classic markdown 全量列表（既有设计：
	// sendWeCom 对 verbosity=full 先短路到 sendWeComFull），故此处应收到
	// markdown + 全量 10 行（ContentFull 生效），而不是卡片。
	if c := body("card-full"); strings.Contains(c, "keyname") ||
		!strings.Contains(c, `"msgtype":"markdown"`) || strings.Count(c, "- app-") != 10 {
		t.Fatalf("卡片·完整形状不符: %s", c)
	}
	// mdv2·简洁：只发简洁正文（表格被弃用）
	if c := body("mdv2-concise"); !strings.Contains(c, "10 个应用已纠正到上游版本") ||
		strings.Contains(c, "| 应用 |") {
		t.Fatalf("mdv2·简洁形状不符: %s", c)
	}
	// mdv2·友好：两列表格「应用 | 版本变化」，前 8 行 + 截断标注
	if c := body("mdv2-friendly"); !strings.Contains(c, "| 应用 | 版本变化 |") ||
		strings.Count(c, "| app-") != 8 || !strings.Contains(c, "等 10 个") {
		t.Fatalf("mdv2·友好形状不符: %s", c)
	}
	// mdv2·完整：同样走企微「完整」短路的 classic markdown 全量列表（既有设计）
	if c := body("mdv2-full"); strings.Contains(c, "| 应用 |") ||
		!strings.Contains(c, `"msgtype":"markdown"`) || strings.Count(c, "- app-") != 10 {
		t.Fatalf("mdv2·完整形状不符: %s", c)
	}
	// 发送成功 → 不产生渠道失败告警（载荷级验证的副产物断言）
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 0 {
		t.Fatalf("渠道全部成功不得出渠道失败告警: %d", n)
	}
}

// ───────────────────── T2：notify_channel_error ─────────────────────

func TestNotifyChannelError_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}
	cfg.NotifyEnabled = boolPtr(false) // 只验逻辑：不真发外部渠道（应用内记录照落）
	cfg.NotifyChannels = []config.NotifyChannel{
		{ID: "ch_1", Type: "wecom", Name: "群机器人", Enabled: true},
	}
	fail := map[string]string{"群机器人": "企业微信 errcode=93000：webhook 地址无效"}
	streak := func() int {
		s.notifyHealthMu.Lock()
		defer s.notifyHealthMu.Unlock()
		if st := s.notifyChanFails["群机器人"]; st != nil {
			return st.failStreak
		}
		return 0
	}
	backdate := func(d time.Duration) {
		s.notifyHealthMu.Lock()
		s.notifyChanFails["群机器人"].lastAlert = time.Now().Add(-d)
		s.notifyHealthMu.Unlock()
	}

	// ① 连续 2 次失败：未达门槛 → 不推
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 0 {
		t.Fatalf("未达 3 次不得告警: %d", n)
	}

	// ② 第 3 次 → 恰好 1 条，且文案按定稿（渠道名 + 次数 + 原因）
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 1 {
		t.Fatalf("第 3 次连续失败应推 1 条: %d", n)
	}
	e := lastNotifyEvent(cfg, "notify_channel_error")
	if !strings.Contains(e.Content, "渠道「群机器人」连续 3 次推送失败") ||
		!strings.Contains(e.Content, "原因：企业微信 errcode=93000") {
		t.Fatalf("渠道失败文案不符: %q", e.Content)
	}
	if e.OK {
		t.Fatalf("渠道失败应为失败态: %+v", e)
	}

	// ③ 1 小时冷却内继续失败 → 不重推（streak 仍累计）
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 1 {
		t.Fatalf("冷却内不得重推: %d", n)
	}
	if streak() != 4 {
		t.Fatalf("冷却内失败仍应累计次数: %d", streak())
	}

	// ④ 冷却过后仍失败 → 复推（次数带当前累计值）
	backdate(2 * time.Hour)
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 2 {
		t.Fatalf("冷却过后应复推: %d", n)
	}
	if c := lastNotifyEvent(cfg, "notify_channel_error").Content; !strings.Contains(c, "连续 5 次") {
		t.Fatalf("复推应带当前累计次数: %q", c)
	}

	// ⑤ 任一次成功 → 计数清零（再连续失败要重新数到 3）
	s.trackNotifyChannelHealth("source_sync_failed", nil)
	if streak() != 0 {
		t.Fatalf("成功应清零: %d", streak())
	}
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 2 {
		t.Fatalf("清零后未达 3 次不得推: %d", n)
	}

	// ⑥ 防自激：本事件自身的 fan-out 与 welcome 不计入 streak / 不再告警
	for i := 0; i < 5; i++ {
		s.trackNotifyChannelHealth("notify_channel_error", fail)
	}
	s.trackNotifyChannelHealth("welcome", fail)
	if streak() != 2 {
		t.Fatalf("本事件与 welcome 不得计入 streak: %d", streak())
	}
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 2 {
		t.Fatalf("防自激：不得新增告警: %d", n)
	}

	// ⑦ 事件开关关 → 不再推（阈值与冷却都满足也不推）
	cfg.NotifyEvents = map[string]bool{"notify_channel_error": false}
	s.notifyHealthMu.Lock()
	s.notifyChanFails["群机器人"].lastAlert = time.Time{}
	s.notifyHealthMu.Unlock()
	s.trackNotifyChannelHealth("source_sync_failed", fail)
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 2 {
		t.Fatalf("事件关不得推: %d", n)
	}
}

// TestNotifyChannelErrorMulti_326Z 多渠同时连续失败 → 一条聚合告警（不炸多条）。
func TestNotifyChannelErrorMulti_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{NotifyEnabled: boolPtr(false)}
	s := &Server{Cfg: cfg}
	cfg.NotifyChannels = []config.NotifyChannel{
		{ID: "ch_1", Type: "wecom", Name: "群机器人", Enabled: true},
		{ID: "ch_2", Type: "bark", Name: "手机推送", Enabled: true},
	}
	fail := map[string]string{"群机器人": "boom-a", "手机推送": "boom-b"}
	for i := 0; i < 3; i++ {
		s.trackNotifyChannelHealth("source_sync_failed", fail)
	}
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 1 {
		t.Fatalf("多渠应聚合成 1 条: %d", n)
	}
	c := lastNotifyEvent(cfg, "notify_channel_error").Content
	if !strings.Contains(c, "本轮 2 个渠道连续推送失败") ||
		!strings.Contains(c, "群机器人") || !strings.Contains(c, "手机推送") {
		t.Fatalf("聚合文案不符: %q", c)
	}
}

// ────────────────── T3：official_session_expired ──────────────────

func TestOfficialSessionExpired_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{NotifyEnabled: boolPtr(false)}
	// 0.6.326（审计 M-1）：告警只对「已授权过官方源」的用户生效 —— 本用例其余
	// 断言都是「授权用户连接失效」场景，故必须放一份会话在场（见 ⑩ 反向用例）。
	s := &Server{Cfg: cfg, Official: newAuthorizedOfficialSession(t)}

	// ① Panel 未装配 = 非「失效」→ 不推
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 0 {
		t.Fatalf("未装配面板不得推: %d", n)
	}

	// ② 装配且连接正常（有数据、无错误）→ 不推
	p := &Panel{}
	p.apps = make([]AppInfo, 388)
	s.Panel = p
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 0 {
		t.Fatalf("连接正常不得推: %d", n)
	}

	// ③ 正常 → 失效（错误串）：推一次（含原因与重新授权指引）
	p.err = "OAuth access token 已过期（401）"
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 1 {
		t.Fatalf("由正常转失效应推 1 条: %d", n)
	}
	e := lastNotifyEvent(cfg, "official_session_expired")
	if !strings.Contains(e.Content, "官方应用中心连接已失效") ||
		!strings.Contains(e.Content, "原因：OAuth access token 已过期") ||
		!strings.Contains(e.Content, "点 🔑 重新授权") {
		t.Fatalf("官方源失效文案不符: %q", e.Content)
	}
	if e.OK {
		t.Fatalf("连接失效应为失败态: %+v", e)
	}

	// ④ 持续失效 → 不重推
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 1 {
		t.Fatalf("持续失效不得重复推: %d", n)
	}

	// ⑤ 6 小时冷却过后仍失效 → 复推一次
	s.notifyHealthMu.Lock()
	s.officialBadAt = time.Now().Add(-7 * time.Hour)
	s.notifyHealthMu.Unlock()
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 2 {
		t.Fatalf("冷却过后应复推: %d", n)
	}

	// ⑥ 恢复（有数据且无错误）→ 不推（本次不做 official_session_recovered）
	p.err = ""
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 2 {
		t.Fatalf("恢复不得推: %d", n)
	}
	if n := countNotifyEvent(cfg, "official_session_recovered"); n != 0 {
		t.Fatal("本次不实现 recovered 事件")
	}

	// ⑦ 再次失效（状态迁移）→ 再推
	p.err = "未连接"
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 3 {
		t.Fatalf("再次失效应推: %d", n)
	}

	// ⑧ 0 应用且无错误（未连接）同样算失效，并给兜底原因文案
	p.err = ""
	p.apps = make([]AppInfo, 5)
	s.officialSessionPass() // 恢复
	p.apps = nil
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 4 {
		t.Fatalf("0 应用未连接应推: %d", n)
	}
	if c := lastNotifyEvent(cfg, "official_session_expired").Content; !strings.Contains(c, "未连接：点右侧 🔑 授权官方应用中心") {
		t.Fatalf("0 应用应有兜底原因文案: %q", c)
	}

	// ⑨ 事件开关关 → 不再推
	cfg.NotifyEvents = map[string]bool{"official_session_expired": false}
	p.apps = make([]AppInfo, 5)
	s.officialSessionPass() // 恢复（不计）
	p.apps = nil
	s.officialSessionPass()
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 4 {
		t.Fatalf("事件关不得推: %d", n)
	}

	// ⑩ 0.6.326（M-1）：从未授权官方源的纯社区源用户 → 即使面板恒报错且
	// 目录 0 条，也不得推「连接已失效」。
	cfg2 := &config.Config{NotifyEnabled: boolPtr(false)}
	s2 := &Server{Cfg: cfg2}
	p2 := &Panel{}
	p2.err = "未连接：点右侧 🔑 授权官方应用中心"
	s2.Panel = p2
	s2.officialSessionPass()
	s2.officialSessionPass()
	if n := countNotifyEvent(cfg2, "official_session_expired"); n != 0 {
		t.Fatalf("从未授权官方源不得推失效告警: %d", n)
	}
}

// TestChannelHealthWiring_326Z 接线验证：渠道失败必须经既有 fan-out 路径被计数
// （fanoutIfExternalOn → trackNotifyChannelHealth）——3 次真实发送失败后自动告警；
// 且告警自身再发给同一坏渠道不会继续推高计数（防自激）。
func TestChannelHealthWiring_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"errcode":40001,"errmsg":"invalid webhook url"}`)) // 业务错误：不重试
	}))
	defer ts.Close()
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}
	cfg.NotifyChannels = []config.NotifyChannel{{
		ID: "ch_1", Type: "wecom", Name: "群机器人", Enabled: true, Timeout: 5,
		Params: map[string]string{"webhook_url": ts.URL + "/hook"},
	}}

	for i := 0; i < 3; i++ {
		s.notifyEvent("install_success", "某个应用安装成功", "", true)
	}
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 1 {
		t.Fatalf("3 次渠道失败应自动告警 1 条: %d", n)
	}
	e := lastNotifyEvent(cfg, "notify_channel_error")
	if !strings.Contains(e.Content, "渠道「群机器人」连续 3 次推送失败") ||
		!strings.Contains(e.Content, "原因：企微 errcode=40001") {
		t.Fatalf("告警文案不符: %q", e.Content)
	}
	if ec := e.Channels["群机器人"]; ec == "" {
		t.Fatalf("记录里应带该渠道的失败结果: %+v", e.Channels)
	}
	if n := countNotifyEvent(cfg, "notify_channel_error"); n != 1 {
		t.Fatalf("告警自身失败不得再触发新告警: %d", n)
	}
}

// TestOfficialSessionWiring_326Z 接线验证：官方源连接巡检挂在既有源刷新轮次
// （sourceRoundNotify 末尾）——连接失效时轮次结束即告警。
func TestOfficialSessionWiring_326Z(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{NotifyEnabled: boolPtr(false)}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Official: newAuthorizedOfficialSession(t)}
	// 官方目录抓取桩：恒失败（等价 OAuth 会话过期）→ 面板记错误 → 巡检告警
	s.Panel = &Panel{listApps: func(context.Context) ([]panel.PanelApp, error) {
		return nil, errors.New("OAuth 会话已过期")
	}}
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 0 {
		t.Fatalf("轮次前不应有告警: %d", n)
	}
	s.sourceRoundNotify(nil)
	if n := countNotifyEvent(cfg, "official_session_expired"); n != 1 {
		t.Fatalf("源刷新轮次应巡检官方源连接: %d", n)
	}
}

// ────────────────── 事件目录（33 → 36） ──────────────────
func TestNewEventsCatalog_326Z(t *testing.T) {
	if len(notifyEventCatalog) != 36 {
		t.Fatalf("事件目录应为 36 条（33 + 3）, got %d", len(notifyEventCatalog))
	}
	want := map[string]struct{ label, group string }{
		"upstream_corrected":       {"溯源纠正到上游", "应用更新"},
		"notify_channel_error":     {"渠道推送失败", "Moo 自身"},
		"official_session_expired": {"官方源连接失效", "源与网络"},
	}
	for _, e := range notifyEventCatalog {
		w, ok := want[e.Key]
		if !ok {
			continue
		}
		if e.Label != w.label || e.Group != w.group || !e.Default || e.Desc == "" {
			t.Fatalf("新事件目录条目不符: %+v", e)
		}
		delete(want, e.Key)
	}
	if len(want) != 0 {
		t.Fatalf("事件目录缺新条目: %v", want)
	}
	// 分组展示（设置页「通知规则」按 Group 折叠）：三个分组都存在
	groups := map[string]bool{}
	for _, e := range notifyEventCatalog {
		groups[e.Group] = true
	}
	for _, g := range []string{"应用更新", "Moo 自身", "源与网络"} {
		if !groups[g] {
			t.Fatalf("缺分组 %s", g)
		}
	}
	// upstream_corrected 紧跟 auto_update_round（用户定稿行序）
	for i, e := range notifyEventCatalog {
		if e.Key == "upstream_corrected" {
			if i == 0 || notifyEventCatalog[i-1].Key != "auto_update_round" {
				t.Fatalf("upstream_corrected 应紧跟 auto_update_round（行序）: idx=%d", i)
			}
		}
	}
}
