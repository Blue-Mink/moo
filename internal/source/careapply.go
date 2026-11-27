package source

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"moo/internal/config"
)

// AutoCareCooldown 空源通道的冷却窗：刚被自愈恢复过的源 72h 内不再因「仍然
// 没应用」被停用。没有它就会出现「恢复 → 下一轮还是空 → 立刻再停用」的来回
// 抖（每次都要给用户推一条通知）。冷却期内只累积计数、不动手；到期后若仍达
// 阈值立即停用，不重新数 5 轮。失败通道不受此窗影响（那是「仓库没了」，与
// 内容空不空无关）。
const AutoCareCooldown = 72 * time.Hour

// AutoCareEmptyThresholdHealed 曾自愈过的源的空源阈值（2026-11-27 F11/N12）。
//
// 这类源已经用事实证明「上游会临时空一阵、会改索引格式」：沿用 5 轮门槛等于
// 让它在同一个坑里被反复停用/恢复（每次还推一条通知）。用户口径是「宁可漏停」，
// 故放宽到 10 轮（预算轮 ≈61/h → 约 33h），再用 72h 冷却窗兜住抖动。
const AutoCareEmptyThresholdHealed = 10

// autoCareEmptyThresholdLocked 该源本轮生效的空源阈值。调用方持 careMu。
func (m *Manager) autoCareEmptyThresholdLocked(rec careRec) int {
	if rec.HealCount > 0 || !rec.LastRecoveredAt.IsZero() {
		return AutoCareEmptyThresholdHealed
	}
	return AutoCareThreshold
}

// autoCareApplyLocked 阶段 C：计数推进 + 停用 + 沉底。调用方持写锁。
// 返回 (动作列表, 是否有计数变化需落盘)。
func (m *Manager) autoCareApplyLocked(sts []SourceStatus, verdicts map[string]CareVerdict) ([]string, bool) {
	// 计数在 careMu 下读写（锁序 m.mu → careMu；调用方持 m.mu）。
	m.careMu.Lock()
	defer m.careMu.Unlock()
	now := time.Now()
	dirty := false
	sink := map[string]bool{}

	for _, st := range sts {
		if st.Error != "" {
			rec := m.careRecordLocked(st.Name)
			rec.LastError = describeFetchError(errors.New(st.Error))
			rec.LastErrorAt = now
			switch verdicts[st.Name] {
			case CarePermanentlyDead, CareDirectGoneMirrorsUnreachable:
				// 直连确定性 404/410 且镜像全部不可达：同样算「一轮确认失败」，
				// 但仍要过下面的 5 轮 + 24h 双门槛才动手（误判红线 3）。
				if rec.FailStreak == 0 || rec.FirstFailAt.IsZero() {
					rec.FirstFailAt = now
				}
				rec.FailStreak++
			case CareAlive:
				rec.FailStreak = 0
				rec.FirstFailAt = time.Time{}
			}
			// C2（2026-11-27 盲点 #3）：记录「这一轮到底探出了什么」。只有本轮
			// 真的复核过（verdicts 有键）才覆盖，没探过的源保留上一轮结论与时间。
			if v, ok := verdicts[st.Name]; ok {
				rec.LastVerdict = careVerdictLabel(v)
				rec.VerdictAt = now.Unix()
			}
			m.setCareRecordLocked(st.Name, rec)
			dirty = true
			continue
		}
		// 刷新成功：空源计数与失败计数各自推进。
		switch {
		case st.Count > 0:
			if _, ok := m.careEmpty[st.Name]; ok {
				delete(m.careEmpty, st.Name)
				dirty = true
			}
			// 真看到应用：清掉「解析失败只记一行」的标记，下次故障重新可见。
			delete(m.careParseSkipLogged, st.Name)
		case st.ParseFailed:
			// 2026-11-27（F11/N12）：「0 条」不是解析成功的结果（索引没被识别/
			// 无结构）→ 本轮既不加计数、也不清零、也不沉底。与失败通道的
			// 「不确定」同口径：宁可漏停，不可错停。
			// 只在首轮记一行日志，避免上游格式坏掉时每轮刷屏。M2（2026-11-27
			// 复核）：原实现用 careEmpty[st.Name]==0 当「首轮」判据，但本分支
			// 从不自增计数 → 条件恒真、每轮都打一行。改用独立标记。
			if !m.careParseSkipLogged[st.Name] {
				if m.careParseSkipLogged == nil {
					m.careParseSkipLogged = map[string]bool{}
				}
				m.careParseSkipLogged[st.Name] = true
				log.Printf("[auto-care] %s 本轮 0 应用但索引结构未识别 → 不计入空源轮次（不推进停用计数）", st.Name)
			}
		default:
			m.careEmpty[st.Name]++
			sink[st.Name] = true
			dirty = true
		}
		if rec := m.careRecordLocked(st.Name); rec.FailStreak > 0 || !rec.FirstFailAt.IsZero() {
			rec.FailStreak = 0
			rec.FirstFailAt = time.Time{}
			m.setCareRecordLocked(st.Name, rec)
			dirty = true
		}
	}

	var acts []string

	// 1) 空源通道：连续空 N 轮 → 停用（策略语义不变，但必须与失败通道一样
	// 记「停用来源」、发通知、进自愈通道——否则空源停用的源成了死胡同：
	// 界面显示成「用户手动关闭」、没人通知用户、也永远等不到自动恢复）。
	for name, n := range m.careEmpty {
		rec := m.careRecordLocked(name)
		// 2026-11-27（F11/N12）：曾自愈过的源（证明它「会临时空一阵/会改格式」）
		// 阈值放宽到 AutoCareEmptyThresholdHealed，避免二次误停。
		if n < m.autoCareEmptyThresholdLocked(rec) {
			continue
		}
		// 冷却窗（仅空源通道）：刚被自愈恢复过的源先别急着再停——计数照常
		// 累积，冷却期内一律不动手。
		if !rec.LastRecoveredAt.IsZero() && now.Sub(rec.LastRecoveredAt) < AutoCareCooldown {
			continue
		}
		if m.disableSourceLocked(name) {
			rec.DisabledBy = careDisabledByAuto
			rec.HealStreak = 0     // 新一轮停用：自愈进度从 0 起算
			rec.HealCapped = false // 新一轮停用：上一窗口的上限标记不沿用
			reason := fmt.Sprintf("连续 %d 次刷新无应用", n)
			acts = append(acts, fmt.Sprintf("源「%s」%s，已自动停用", name, reason))
			// P1-1c：停用必须通知。空源与失败分因（ev.Empty）：文案不能
			// 把「源还在、只是没内容」说成「刷新失败」。
			ev := CareEvent{Kind: CareEventDisabled, Name: name, URL: m.careRefURLLocked(name),
				Reason: reason, Streak: n, Empty: true}
			ev.Notify = rec.allowNotice(CareEventDisabled, now)
			m.pushCareEventLocked(ev)
			m.setCareRecordLocked(name, rec)
			sink[name] = true
		}
		delete(m.careEmpty, name)
		dirty = true
	}

	// 2) 失败通道：双门槛 + 本轮复核仍确定性失效 → 停用（误判红线 3/4/5）
	names := make([]string, 0, len(m.careRecs))
	for name := range m.careRecs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rec := m.careRecordLocked(name)
		if rec.FailStreak < AutoCareFailThreshold || rec.FirstFailAt.IsZero() {
			continue
		}
		if now.Sub(rec.FirstFailAt) < AutoCareFailMinWindow {
			continue
		}
		if v := verdicts[name]; v != CarePermanentlyDead && v != CareDirectGoneMirrorsUnreachable {
			continue // 复核不确定/可达：本轮放弃，计数不推进
		}
		if !m.disableSourceLocked(name) {
			// 已经停用（用户手动 / 上轮已策略停用）：不重复动作、不告警
			continue
		}
		rec.DisabledBy = careDisabledByAuto
		rec.HealStreak = 0 // 新一轮停用：自愈进度从 0 起算
		reason := rec.LastError
		if reason == "" {
			reason = "源地址已失效"
		}
		acts = append(acts, fmt.Sprintf("源「%s」连续 %d 次刷新失败（%s），已自动停用", name, rec.FailStreak, reason))
		// P1-1c：停用必须通知（用户无从知晓某个源被静默停用）。24h 内同一
		// 源只推一条（误判红线 6）；被节流的事件照样入队，只是 Notify=false。
		ev := CareEvent{Kind: CareEventDisabled, Name: name, URL: m.careRefURLLocked(name),
			Reason: reason, Streak: rec.FailStreak}
		ev.Notify = rec.allowNotice(CareEventDisabled, now)
		m.pushCareEventLocked(ev)
		m.setCareRecordLocked(name, rec)
		sink[name] = true
		dirty = true
	}

	// 3) 沉底：仅本轮「空源」与「刚被停用的死源」，稳定分区保序
	if len(sink) > 0 {
		kept := make([]config.SourceRef, 0, len(m.cfg.Sources))
		sunk := make([]config.SourceRef, 0)
		for _, s := range m.cfg.Sources {
			if sink[s.Name] {
				sunk = append(sunk, s)
			} else {
				kept = append(kept, s)
			}
		}
		if len(sunk) > 0 {
			newOrder := append(kept, sunk...)
			if !sameSourceOrder(m.cfg.Sources, newOrder) {
				m.cfg.Sources = newOrder
				names := make([]string, 0, len(sunk))
				for _, s := range sunk {
					names = append(names, s.Name)
				}
				acts = append(acts, "空源沉底: "+strings.Join(names, "、"))
				dirty = true
			}
		}
	}
	return acts, dirty
}

// disableSourceLocked 只把「当前仍启用」的源置为停用（可逆标记，保留配置与
// 计数、保留原始 URL；用户手动停用的源不会被覆盖）。返回是否真的改了。
func (m *Manager) disableSourceLocked(name string) bool {
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name != name {
			continue
		}
		if !m.cfg.Sources[i].IsEnabled() {
			return false
		}
		off := false
		m.cfg.Sources[i].Enabled = &off
		return true
	}
	return false
}
