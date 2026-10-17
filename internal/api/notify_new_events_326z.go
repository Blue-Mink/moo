package api

// notify_new_events_326z.go —— 0.6.326 批次新增的三个通知事件（一律走
// notifyEventRichV 六变体，见 internal/notify/CONVENTIONS.md 硬约束：
// 尾部不带「完整明细 + 链接」、不带「打开通知详情」入口；各自带去重/冷却）：
//
//	1. upstream_corrected        溯源纠正到上游：更新策略=「强制上游」时，
//	   已装搬运来源应用被改指上游原作者版本（lineage.go
//	   applyUpstreamCorrection 收集 → buildCatalog 锁外异步推送）。
//	   去重：config.NotifyUpstreamNotified（appName → 已推目标版本，每版一次）。
//	2. notify_channel_error      渠道推送失败：同一渠道连续 ≥3 次发送失败、
//	   距上次告警 >1 小时 → 借正常链路推一次（其它正常渠道收得到 + 必落应用内
//	   记录）。防自激：本事件自身 fan-out 与 welcome/渠道测试不计入 streak。
//	3. official_session_expired  官方源连接失效：官方应用中心连接态由「正常」
//	   变「失效」时推一次；持续失效 6 小时冷却复推；恢复不推（本次不做
//	   official_session_recovered，避免范围膨胀）。Panel == nil（未装配）不推。
//
// 事件目录条目（notifyEventCatalog）在 notify.go。

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/notify"
)

// 渠道失效告警参数（事件 2）。
const (
	notifyChannelFailStreak = 3         // 连续失败次数门槛
	notifyChannelAlertCool  = time.Hour // 同一渠道两次告警的最小间隔
	notifyChannelRowMax     = 6         // 卡片两列行上限（企微卡片展示上限）
	notifyChannelRowFullMax = 8         // 完整模式卡片行上限（放宽）
)

// 官方源连接巡检参数（事件 3）：持续失效的复推冷却。
const officialSessionCool = 6 * time.Hour

// chanFailState 单个渠道的连续失败状态（Server 内存态，不落盘：重启后重新
// 计数，避免把陈旧的「连续失败」跨重启续算成一次假告警）。
type chanFailState struct {
	failStreak int
	lastAlert  time.Time
	lastErr    string
}

// chanAlert 一条渠道失效告警（本轮聚合推送用）。
type chanAlert struct {
	label string // 渠道可读标签
	n     int    // 连续失败次数
	err   string // 最后一次错误（已掩码，见 notify.Fanout）
}

// notifyChannelLabel 渠道可读标签：优先用户自定渠道名（本就面向人），
// 缺失时回落渠道类型定义的中文名（notify.ChannelDefs 的 type → label）。
func notifyChannelLabel(ch config.NotifyChannel) string {
	if n := strings.TrimSpace(ch.Name); n != "" {
		return n
	}
	if def := notify.ChannelDefByKey(ch.Type); def != nil && def.Label != "" {
		return def.Label
	}
	if ch.Type != "" {
		return ch.Type
	}
	return "通知渠道"
}

// trackNotifyChannelHealth 记录本轮外部 fan-out 的各渠道成败，并在「同一渠道
// 连续失败 ≥3 次 + 距上次告警 >1 小时」时推一次 notify_channel_error。
//
// 防自激（必做）：本事件自身的 fan-out 结果不再计入 streak（否则告警消息自己
// 发送失败会累积成新的告警）；welcome（欢迎语/用户主动测试）同理不计入。
// 渠道「测试提供商」（notifyChannelTest / Draft）走 notify.Send 直发，不经过
// fanoutIfExternalOn，天然不计入。
func (s *Server) trackNotifyChannelHealth(key string, errs map[string]string) {
	if key == "notify_channel_error" || key == "welcome" {
		return
	}
	var enabled []config.NotifyChannel
	for _, ch := range s.Cfg.NotifyChannels {
		if ch.Enabled {
			enabled = append(enabled, ch)
		}
	}
	if len(enabled) == 0 {
		return
	}
	now := time.Now()
	var alerts []chanAlert
	s.notifyHealthMu.Lock()
	if s.notifyChanFails == nil {
		s.notifyChanFails = make(map[string]*chanFailState)
	}
	for _, ch := range enabled {
		st := s.notifyChanFails[ch.Name]
		if st == nil {
			st = &chanFailState{}
			s.notifyChanFails[ch.Name] = st
		}
		e, bad := errs[ch.Name]
		if !bad {
			// 任一次成功 → 计数清零（渠道恢复后重新累计）
			st.failStreak = 0
			st.lastErr = ""
			continue
		}
		st.failStreak++
		st.lastErr = e
		if st.failStreak < notifyChannelFailStreak {
			continue
		}
		if !st.lastAlert.IsZero() && now.Sub(st.lastAlert) < notifyChannelAlertCool {
			continue // 1 小时冷却内不重复告警
		}
		st.lastAlert = now
		alerts = append(alerts, chanAlert{label: notifyChannelLabel(ch), n: st.failStreak, err: e})
	}
	s.notifyHealthMu.Unlock()
	if len(alerts) == 0 {
		return
	}
	s.pushNotifyChannelError(alerts)
}

// pushNotifyChannelError 推送「渠道推送失败」通知（六变体；ok=false）。
// 走 notifyEventRichV 正常链路 → 其它正常渠道能收到，同时必落应用内记录。
func (s *Server) pushNotifyChannelError(alerts []chanAlert) {
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].label < alerts[j].label })
	// 友好正文：单渠道 = 用户定稿两行；多渠渠道 = 汇总行 + 逐条明细。
	var b strings.Builder
	if len(alerts) == 1 {
		fmt.Fprintf(&b, "渠道「%s」连续 %d 次推送失败\n\n原因：%s\n", alerts[0].label, alerts[0].n, alerts[0].err)
	} else {
		fmt.Fprintf(&b, "本轮 %d 个渠道连续推送失败\n\n", len(alerts))
		for _, a := range alerts {
			fmt.Fprintf(&b, "- 渠道「%s」连续 %d 次推送失败：%s\n", a.label, a.n, a.err)
		}
	}
	// mdv2 表格（渠道 | 失败次数）+ 卡片两列行（渠道 | N 次失败）。
	var tb strings.Builder
	tb.WriteString("| 渠道 | 失败次数 |\n| :-- | :-- |\n")
	rows := make([]notify.CardRow, 0, len(alerts))
	for _, a := range alerts {
		fmt.Fprintf(&tb, "| %s | %d |\n", mdCell(a.label), a.n)
		rows = append(rows, notify.CardRow{Key: a.label, Value: fmt.Sprintf("%d 次失败", a.n)})
	}
	if len(rows) == 1 {
		rows[0].Key = "渠道" // 单渠道定稿行：渠道 | N 次失败
	}
	// 简洁/完整变体：简洁只报结论，完整不截断（含全部渠道与原因）。
	concise := fmt.Sprintf("渠道「%s」连续失败", alerts[0].label)
	if len(alerts) > 1 {
		concise = fmt.Sprintf("%d 个渠道连续推送失败", len(alerts))
	}
	var bFull strings.Builder
	fmt.Fprintf(&bFull, "%d 个渠道连续推送失败\n\n", len(alerts))
	var tbFull strings.Builder
	fmt.Fprintf(&tbFull, "| 渠道 | 连续失败 | 最后错误 |\n| :-- | :-- | :-- |\n")
	var rowsFull []notify.CardRow
	for _, a := range alerts {
		fmt.Fprintf(&bFull, "- 渠道「%s」连续 %d 次推送失败：%s\n", a.label, a.n, a.err)
		fmt.Fprintf(&tbFull, "| %s | %d | %s |\n", mdCell(a.label), a.n, mdCell(a.err))
		if len(rowsFull) < notifyChannelRowFullMax {
			rowsFull = append(rowsFull, notify.CardRow{Key: a.label, Value: fmt.Sprintf("%d 次失败", a.n)})
		}
	}
	sub := alerts[0].label
	if len(alerts) > 1 {
		sub = "多个渠道"
	}
	rowsConcise := []notify.CardRow{{Key: "失败次数", Value: fmt.Sprintf("%d 次", alerts[0].n)}}
	s.notifyEventRichV("notify_channel_error", "渠道推送失败", b.String(), tb.String(),
		rowsCap(rows, notifyChannelRowMax), "", "", false, &notify.Variants{
			ContentConcise: concise,
			ContentFull:    bFull.String(),
			TableFull:      tbFull.String(),
			RowsConcise:    rowsConcise,
			ConciseSub:     sub,
			RowsFull:       rowsFull,
		})
}

// rowsCap 截断卡片两列行（≤max 行；空/超量时返回原切片语义不变）。
func rowsCap(rows []notify.CardRow, max int) []notify.CardRow {
	if len(rows) <= max {
		return rows
	}
	return rows[:max]
}

// ───────────────────────── 事件 1：溯源纠正到上游 ─────────────────────────

// notifyUpstreamCorrected 推送「溯源纠正到上游」通知。
//
// 入参 = buildCatalog 本轮 applyUpstreamCorrection 真正改过更新目标的应用
// （lineage.go upstreamCorrection）。去重：config.NotifyUpstreamNotified
// （appName → 已通知的目标版本），只有 map[app] != to 才推 → 同一应用同一
// 上游版本只推一次；上游再发新版（目标版本变了）才会再推。
//
// 与 updates_available 不重复：本事件只讲「因溯源纠正而改变了更新目标」，
// 措辞明确「上游/原作者」。事件开关关 = 完全静默且不记去重基线。
func (s *Server) notifyUpstreamCorrected(hits []upstreamCorrection) {
	if len(hits) == 0 {
		return
	}
	notifyMu.Lock()
	on := s.IsNotifyEventOn("upstream_corrected")
	var news []upstreamCorrection
	if on {
		if s.Cfg.NotifyUpstreamNotified == nil {
			s.Cfg.NotifyUpstreamNotified = make(map[string]string)
		}
		seen := s.Cfg.NotifyUpstreamNotified
		for _, h := range hits {
			if seen[h.AppName] == h.To {
				continue // 该应用×目标版本已推过
			}
			seen[h.AppName] = h.To
			news = append(news, h)
		}
		if len(news) > 0 {
			_ = s.Cfg.Save(dataDirOf(s))
		}
	}
	notifyMu.Unlock()
	if !on || len(news) == 0 {
		return
	}
	sort.Slice(news, func(i, j int) bool { return news[i].DisplayName < news[j].DisplayName })

	const head = "本轮 %d 个已装应用已纠正到上游原作者版本\n\n"
	var b, bFull strings.Builder
	fmt.Fprintf(&b, head, len(news))
	fmt.Fprintf(&bFull, head, len(news))
	var tb, tbFull strings.Builder
	fmt.Fprintf(&tb, "本轮 %d 个已装应用已纠正到上游原作者版本\n\n| 应用 | 版本变化 |\n| :-- | :-- |\n", len(news))
	fmt.Fprintf(&tbFull, "本轮 %d 个已装应用已纠正到上游原作者版本\n\n| 应用 | 版本变化 |\n| :-- | :-- |\n", len(news))
	line := func(h upstreamCorrection) string {
		return fmt.Sprintf("- %s（%s）：%s → %s（%s）\n",
			h.DisplayName, orDash(h.Source), orDash(h.From), h.To, h.UpstreamSource)
	}
	var rows, rowsFull []notify.CardRow
	for i, h := range news {
		fmt.Fprintf(&bFull, "%s", line(h))
		fmt.Fprintf(&tbFull, "| %s | %s |\n", mdCell(h.DisplayName+"（"+h.UpstreamSource+"）"),
			mdCell(orDash(h.From)+" → "+h.To))
		if i < 8 {
			rowsFull = append(rowsFull, notify.CardRow{Key: h.DisplayName, Value: orDash(h.From) + " → " + h.To})
		}
		if i < 6 {
			rows = append(rows, notify.CardRow{Key: h.DisplayName, Value: orDash(h.From) + " → " + h.To})
		}
		if i >= 8 {
			continue // 友好/表格版只列前 8 条（其余走完整模式与应用内记录）
		}
		fmt.Fprintf(&b, "%s", line(h))
		fmt.Fprintf(&tb, "| %s | %s |\n", mdCell(h.DisplayName+"（"+h.UpstreamSource+"）"),
			mdCell(orDash(h.From)+" → "+h.To))
	}
	if len(news) > 8 {
		fmt.Fprintf(&b, "- …等 %d 个\n", len(news))
		fmt.Fprintf(&tb, "| … | 等 %d 个 |\n", len(news))
	}
	// 简洁副题：卡片两列行只列前 3 条，数量与来源必须在副题里交代——
	// 全部同一上游源 →「源名 · N 个应用」；混合源 →「溯源纠正 · N 个应用」。
	sub := "溯源纠正"
	same := true
	for _, h := range news[1:] {
		if h.UpstreamSource != news[0].UpstreamSource {
			same = false
			break
		}
	}
	if same && strings.TrimSpace(news[0].UpstreamSource) != "" {
		sub = news[0].UpstreamSource
	}
	sub = fmt.Sprintf("%s · %d 个应用", sub, len(news))
	s.notifyEventRichV("upstream_corrected", "溯源纠正到上游", b.String(), tb.String(),
		rowsCap(rows, notifyChannelRowMax), "", "", true, &notify.Variants{
			ContentConcise: fmt.Sprintf("%d 个应用已纠正到上游版本", len(news)),
			ContentFull:    bFull.String(),
			TableFull:      tbFull.String(),
			RowsConcise:    rowsCap(rowsFull, 3),
			ConciseSub:     sub,
			RowsFull:       rowsFull,
		})
}

// ───────────────────── 事件 3：官方源连接失效 ─────────────────────

// officialSessionPass 官方应用中心连接态巡检（挂在既有通知轮次
// sourceRoundNotify 末尾，与源刷新同一周期）。
//
// 规则：连接失效 = 有错误串（OAuth access 约 1 小时过期 / 未授权）或 0 应用
// （未连接）→ 状态由「正常」变「失效」时推一次；持续失效按 6 小时冷却复推
// （用户始终能看到「需重新授权」，但不刷屏）；恢复（有数据且无错误）只重置
// 状态，不推送（official_session_recovered 本次不做）。
//
// Panel == nil（面板未装配）= 非「失效」，不推（不发假告警）。
func (s *Server) officialSessionPass() {
	if s.Panel == nil {
		return
	}
	errStr := strings.TrimSpace(s.Panel.LastError())
	apps := s.Panel.CachedAppCount()
	// 0.6.326（审计 M-1）：只对「曾经接通过官方源」的用户告警。从未授权官方源的
	// 纯社区源用户，面板通道恒写 err（未连接）且 apps=0 —— 旧判定会在首个轮次
	// 立刻推一条与事实不符的「连接已失效」，此后每 6h 冷却复推。
	// linked 用「会话仍在」（hadOAuthSession）而非「会话有效」：access 过期且
	// refresh 也失效（真需要重新授权）时仍要告警。Panel.Enabled() 覆盖 0.6.255
	// 之前遗留的面板账号通道。
	linked := s.officialStoreV().hadOAuthSession() || s.Panel.Enabled()
	bad := linked && (errStr != "" || apps == 0)
	now := time.Now()
	s.notifyHealthMu.Lock()
	was := s.officialBad
	lastAlert := s.officialBadAt
	s.officialBad = bad
	push := false
	if bad {
		if !was {
			push = true // 正常 → 失效：推一次
		} else if lastAlert.IsZero() || now.Sub(lastAlert) >= officialSessionCool {
			push = true // 持续失效：6 小时冷却复推
		}
	}
	if push {
		s.officialBadAt = now
	}
	s.notifyHealthMu.Unlock()
	if !push {
		return
	}
	if errStr == "" {
		errStr = "未连接：点右侧 🔑 授权官方应用中心"
	}
	note := "在「应用源 → 飞牛应用中心」点 🔑 重新授权"
	content := "官方应用中心连接已失效\n\n原因：" + errStr + "\n\n" + note
	table := "官方应用中心连接已失效\n\n| 项目 | 值 |\n| :-- | :-- |\n| 状态 | 未连接 |\n| 原因 | " + mdCell(errStr) + " |\n"
	rows := []notify.CardRow{{Key: "状态", Value: "未连接"}}
	s.notifyEventRichV("official_session_expired", "官方源连接失效", content, table, rows, "", "", false,
		&notify.Variants{
			ContentConcise: "官方源未连接，点 🔑 重新授权",
			ContentFull:    content,
			TableFull:      table,
			RowsConcise:    []notify.CardRow{{Key: "状态", Value: "未连接"}},
			ConciseSub:     "点 🔑 重新授权", // 卡片副题直接给动作（行只承载状态，不重复）
			RowsFull:       rows,
		})
}
