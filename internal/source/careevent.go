package source

import "time"

// P1-1c（2026-11-26 CARE）：自动监测产生的「用户可见事件」。
//
// 为什么用事件队列而不是让策略直接发通知：停用/恢复发生在 source 包（持
// m.mu + careMu，且要落盘做跨重启幂等），通知在 api 层（notify 渠道、事件
// 开关、应用内记录）。source 只回答「该不该说」（含 24h 节流），api 只回答
// 「怎么说」（排版见 internal/notify/CONVENTIONS.md，禁自拼渠道）。
type CareEventKind string

const (
	// CareEventDisabled 策略停用（连续刷新失败达到双门槛）。
	CareEventDisabled CareEventKind = "disabled"
	// CareEventRecovered 自愈恢复（低频探测连续 2 次看到有应用）。
	CareEventRecovered CareEventKind = "recovered"
	// CareEventHealCapped 自动恢复次数已用尽（30 天 2 次）：保持停用，只低频
	// 复查、不再自动复活——这条只推一次，告诉用户「得你手动开一下」。
	CareEventHealCapped CareEventKind = "heal_capped"
)

const (
	// careDisabledByAuto 策略停用标记：自愈通道只认这个标记，绝不碰用户
	// 手动停用的源（误判红线 5 / 误判侧 ⑧）。
	careDisabledByAuto = "auto-care"
	// careNoticeCool 同一源「同类」通知的冷却：24h 内只推一条（误判红线 6）。
	// 落在 source_care.json，跨重启幂等（内存态节流一重启就失效）。
	careNoticeCool = 24 * time.Hour
)

// CareEvent 一条待外发通知的自动监测事件（脱敏：只带源名与已清洗的原因）。
type CareEvent struct {
	Kind   CareEventKind
	Name   string
	URL    string
	Reason string
	// Streak 触发时的连续失败轮数（停用事件）。
	Streak int
	// Count 恢复时探测到的应用数（恢复事件）。
	Count int
	// Empty 停用起因是否为「空源通道」（刷新成功但连续 N 轮没有应用）。
	// false = 失败通道（仓库已失效）。通知文案必须分因：空源不等于刷新失败。
	Empty bool
	// Notify 是否放行外发通知。false = 已被 24h 节流吞掉：仍记日志、仍落
	// 应用内动作，只是不再打扰用户。
	Notify bool
}

// allowNotice 判断某类通知是否放行（24h/源），放行则推进时间戳。
// 调用方必须持 careMu。
func (r *careRec) allowNotice(kind CareEventKind, now time.Time) bool {
	at := &r.DisableNoticeAt
	switch kind {
	case CareEventRecovered:
		at = &r.RecoverNoticeAt
	case CareEventHealCapped:
		at = &r.CappedNoticeAt
	}
	if !at.IsZero() && now.Sub(*at) < careNoticeCool {
		return false
	}
	*at = now
	return true
}

// DrainCareEvents 取走并清空待通知事件（api 每轮策略后调用一次）。
// 只取一次 = 天然幂等：重启后不会有半截事件重复推送。
func (m *Manager) DrainCareEvents() []CareEvent {
	m.careMu.Lock()
	defer m.careMu.Unlock()
	if len(m.careEvents) == 0 {
		return nil
	}
	out := m.careEvents
	m.careEvents = nil
	return out
}

// pushCareEventLocked 入队一条事件。调用方持 careMu。
func (m *Manager) pushCareEventLocked(ev CareEvent) {
	m.careEvents = append(m.careEvents, ev)
}
