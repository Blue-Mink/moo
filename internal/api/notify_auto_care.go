package api

import (
	"fmt"
	"strings"

	"moo/internal/notify"
	"moo/internal/source"
)

// P1-1c（2026-11-26 CARE）：源「自动停用 / 自动恢复」通知。
//
// 排版硬约束见 internal/notify/CONVENTIONS.md：一律走 notifyEventRichV 并
// 填齐六变体（卡片·mdv2 × 简洁·友好·完整），不自拼渠道、不写死 webhook。
// 节流在 source 侧判定（24h/源，落 source_care.json，跨重启幂等）：这里只
// 消费 ev.Notify 为真的事件。
func (s *Server) autoCareNotifyPass() {
	for _, ev := range s.Src.DrainCareEvents() {
		if !ev.Notify {
			continue
		}
		switch ev.Kind {
		case source.CareEventDisabled:
			s.notifySourceAutoDisabled(ev)
		case source.CareEventRecovered:
			s.notifySourceAutoRecovered(ev)
		case source.CareEventHealCapped:
			s.notifySourceAutoHealCapped(ev)
		}
	}
}

// careClip 卡片行 Value 上限 26 字：按 rune 截断，不切坏多字节。
func careClip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func (s *Server) notifySourceAutoDisabled(ev source.CareEvent) {
	if ev.Empty {
		// 空源通道：源是好的、只是索引里没应用——文案必须分因，不能把
		// 「刷新成功但没有应用」说成「刷新失败」（用户会以为仓库挂了）。
		s.notifySourceAutoEmptyDisabled(ev)
		return
	}
	reason := strings.TrimSpace(ev.Reason)
	if reason == "" {
		reason = "源地址已失效"
	}
	guide := "到「应用源」页把该源开关重新打开即可恢复；Moo 每 6 小时也会低频复查一次，连续 2 次看到有应用会自动恢复并通知你"
	content := fmt.Sprintf("源「%s」已自动停用\n\n连续 %d 次刷新失败\n\n原因：%s\n\n恢复指引：%s",
		ev.Name, ev.Streak, reason, guide)
	table := fmt.Sprintf("源「%s」已自动停用\n\n| 项目 | 值 |\n| :-- | :-- |\n| 状态 | 已自动停用（可恢复） |\n| 连续失败 | %d 次 |\n| 原因 | %s |\n",
		ev.Name, ev.Streak, mdCell(reason))
	rows := []notify.CardRow{
		{Key: "状态", Value: "已自动停用"},
		{Key: "连续失败", Value: fmt.Sprintf("%d 次", ev.Streak)},
		{Key: "原因", Value: careClip(reason, 26)},
	}
	full := content + "\n\n判定口径：raw 直连与至少一个加速镜像同时返回 404/410 才停用；超时/5xx/429/单通道失败一律不计（宁可漏停，不误停）。\n- 源已改名或搬家：编辑源地址后重新启用\n- 源仍有效只是暂时没应用：属空源通道，与本事件无关"
	tableFull := table + "\n| 自动复查 | 每 6 小时一次，连续 2 次有应用即恢复 |\n"
	s.notifyEventRichV("source_auto_disabled", "源自动停用", content, table, rows, "", "", false,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("源「%s」连续 %d 次刷新失败已停用", ev.Name, ev.Streak),
			ContentFull:    full,
			TableFull:      tableFull,
			RowsConcise:    []notify.CardRow{{Key: "状态", Value: "已自动停用"}, {Key: "原因", Value: careClip(reason, 26)}},
			ConciseSub:     "可在应用源页恢复",
			RowsFull:       append(rows, notify.CardRow{Key: "恢复", Value: "应用源页开开关"}),
		})
}

func (s *Server) notifySourceAutoRecovered(ev source.CareEvent) {
	content := fmt.Sprintf("源「%s」已自动恢复启用\n\n连续 %d 次低频探测到 %d 个应用\n\n- 自动监测计数已从 0 重新开始\n- 若再连续 5 次刷新无应用，会被再次自动停用（可再次恢复）",
		ev.Name, ev.Streak, ev.Count)
	table := fmt.Sprintf("源「%s」已自动恢复启用\n\n| 项目 | 值 |\n| :-- | :-- |\n| 状态 | 已恢复启用 |\n| 探测到应用 | %d 个 |\n| 探测方式 | 连续 %d 次低频探测 |\n",
		ev.Name, ev.Count, ev.Streak)
	rows := []notify.CardRow{
		{Key: "状态", Value: "已恢复启用"},
		{Key: "应用数", Value: fmt.Sprintf("%d 个", ev.Count)},
		{Key: "探测", Value: fmt.Sprintf("连续 %d 次有应用", ev.Streak)},
	}
	full := content + "\n\n说明：恢复只代表源索引又能拉到应用了，不代表内容没问题；如发现异常应用请在应用详情页核对来源。"
	tableFull := table + "\n| 计数 | 空源/失败计数均已清零 |\n"
	s.notifyEventRichV("source_auto_recovered", "源自动恢复", content, table, rows, "", "", true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("源「%s」已自动恢复（%d 个应用）", ev.Name, ev.Count),
			ContentFull:    full,
			TableFull:      tableFull,
			RowsConcise:    []notify.CardRow{{Key: "状态", Value: "已恢复启用"}, {Key: "应用数", Value: fmt.Sprintf("%d 个", ev.Count)}},
			ConciseSub:     "连续 2 次探测有应用",
			RowsFull:       rows,
		})
}

// notifySourceAutoEmptyDisabled 空源通道的停用通知（2026-11-26 CARE 冷却批次）。
//
// 与失败通道分开写：判定依据是「刷新成功但连续 N 轮没有应用」，文案里绝不能
// 出现「刷新失败」；恢复指引也要说清「源仍可访问，只是复查等它有应用」。
func (s *Server) notifySourceAutoEmptyDisabled(ev source.CareEvent) {
	n := ev.Streak
	guide := "源仍可访问，每 6 小时复查，连续 2 次有应用即恢复"
	content := fmt.Sprintf("源「%s」已自动停用\n\n连续 %d 次刷新成功但没有应用\n\n恢复指引：%s；也可以到「应用源」页直接打开开关",
		ev.Name, n, guide)
	table := fmt.Sprintf("源「%s」已自动停用\n\n| 项目 | 值 |\n| :-- | :-- |\n| 状态 | 已自动停用（可恢复） |\n| 连续空源 | %d 次 |\n| 判定 | 刷新成功但无应用 |\n",
		ev.Name, n)
	rows := []notify.CardRow{
		{Key: "状态", Value: "已自动停用"},
		{Key: "连续空源", Value: fmt.Sprintf("%d 次", n)},
		{Key: "判定", Value: "刷新成功但无应用"},
	}
	full := content + "\n\n判定口径：只有「刷新成功但索引条目为 0」连续 " + fmt.Sprintf("%d ", source.AutoCareThreshold) +
		"轮才停用；网络失败另走失败通道，不计入空源。\n- 想立刻恢复：到「应用源」页把该源开关重新打开\n- 源已改名或搬家：编辑源地址后重新启用"
	tableFull := table + "\n| 自动复查 | 每 6 小时一次，连续 2 次有应用即恢复 |\n"
	s.notifyEventRichV("source_auto_disabled", "源自动停用", content, table, rows, "", "", false,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("源「%s」连续 %d 次刷新无应用已停用", ev.Name, n),
			ContentFull:    full,
			TableFull:      tableFull,
			RowsConcise:    []notify.CardRow{{Key: "状态", Value: "已自动停用"}, {Key: "连续空源", Value: fmt.Sprintf("%d 次", n)}},
			ConciseSub:     "可在应用源页恢复",
			RowsFull:       append(rows, notify.CardRow{Key: "恢复", Value: "应用源页开开关"}),
		})
}

// notifySourceAutoHealCapped 抖动上限触顶的通知：源又好起来了，但 30 天内
// 已经自动恢复 2 次，策略不再自动开——必须让用户知道「这次要你点一下」。
func (s *Server) notifySourceAutoHealCapped(ev source.CareEvent) {
	content := fmt.Sprintf("源「%s」自动恢复已达上限\n\n已连续 2 次探测到 %d 个应用，但 30 天内已自动恢复 %d 次（上限 %d 次）\n\n- 源仍可访问：Moo 改成每 24 小时复查一次，但不会再自动开启\n- 需要恢复：到「应用源」页手动打开开关（手动动作优先，会重置上限窗口）",
		ev.Name, ev.Count, ev.Streak, source.AutoCareHealCap)
	table := fmt.Sprintf("源「%s」自动恢复已达上限\n\n| 项目 | 值 |\n| :-- | :-- |\n| 状态 | 保持停用（需手动） |\n| 30 天恢复 | %d 次 |\n| 探测到应用 | %d 个 |\n",
		ev.Name, ev.Streak, ev.Count)
	rows := []notify.CardRow{
		{Key: "状态", Value: "保持停用"},
		{Key: "30天恢复", Value: fmt.Sprintf("%d 次", ev.Streak)},
		{Key: "应用数", Value: fmt.Sprintf("%d 个", ev.Count)},
	}
	full := content + fmt.Sprintf("\n\n判定口径：30 天滚动窗口内最多自动恢复 %d 次；窗口到期后计数归零，可再次自动恢复。", source.AutoCareHealCap)
	tableFull := table + "\n| 复查频率 | 每 24 小时一次（不自动开启） |\n"
	s.notifyEventRichV("source_auto_heal_capped", "源自动恢复已达上限", content, table, rows, "", "", false,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("源「%s」已达自动恢复上限（%d 次/30 天）", ev.Name, source.AutoCareHealCap),
			ContentFull:    full,
			TableFull:      tableFull,
			RowsConcise:    []notify.CardRow{{Key: "状态", Value: "需手动开启"}, {Key: "30天恢复", Value: fmt.Sprintf("%d 次", ev.Streak)}},
			ConciseSub:     "可在应用源页开启",
			RowsFull:       append(rows, notify.CardRow{Key: "恢复", Value: "应用源页开开关"}),
		})
}
