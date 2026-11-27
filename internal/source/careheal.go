package source

import (
	"fmt"
	"strings"
	"time"
)

// P1-1b（2026-11-26 CARE）：自愈通道——策略停用的源以低频探测检查「后来
// 又有应用了」，连续 2 次探测到有应用就自动恢复启用并推一条通知。
//
// 为什么必须有这条通道：停用源不进 SelectBudgetedRefresh（只遍历启用源），
// 策略永远看不到「源已修复」→ 结构性死锁，只能等用户手动点开关。用户
// 2026-11-24 的指令是「漏停可以，错停不行」，恢复同理：宁可晚一点恢复，
// 也不能凭一次偶发成功就复活（单次 200 可能是边缘缓存/半截索引）。
//
// 口径（与误判红线一致）：
//   · 只探「策略停用」的源（careRec.DisabledBy == auto-care），用户手动停用
//     的源永不被自愈复活；
//   · 低频：同一源 6 小时最多探一次（独立于 60 源预算轮，不影响正常刷新）；
//   · 连续 2 次「索引条目数 > 0」才恢复；探测失败/超时/条目 0 一律不恢复；
//   · 恢复后空源计数与失败计数从 0 重新计（误判红线 3 的轮数门槛重新起算）。

const (
	// AutoCareHealInterval 同一停用源的低频探测间隔。
	AutoCareHealInterval = 6 * time.Hour
	// AutoCareHealStreak 连续多少次「探测到有应用」才恢复启用。
	AutoCareHealStreak = 2
	// AutoCareHealCap 滚动窗口内最多自动恢复次数（抖动上限）。超过就保持停用、
	// 等用户手动确认——「自己恢复、自己停用」来回抖比一直停着更让人烦。
	AutoCareHealCap = 2
	// AutoCareHealWindow 抖动上限的滚动窗口（30 天）。
	AutoCareHealWindow = 30 * 24 * time.Hour
	// AutoCareHealIntervalCapped 达到上限后的低频探测间隔：仍要探（源可能真的
	// 修好了，用户手动开之前先把信息备好），但不再频繁，24h 一次即可。
	AutoCareHealIntervalCapped = 24 * time.Hour

	// careHealUnknown 探测结论「不确定」（超时/404/解析失败），不推进也不清零。
	careHealUnknown = -1
)

// CareHealProbeFunc 自愈探测钩子：返回索引条目数，<0 = 不确定。
// nil = 使用默认实现 ProbeCareAppCount（真实网络）；测试注入假实现。
type CareHealProbeFunc func(name, url string) int

// ProbeCareAppCount 默认探测实现：拉一次索引、只数条目（不写缓存、不动
// 目录、不写索引名记忆）——停用源不在刷新轮里，这里必须自成一体。
func ProbeCareAppCount(name, url string) (n int) {
	n = careHealUnknown
	defer func() {
		if r := recover(); r != nil {
			n = careHealUnknown // 探测绝不能让策略轮崩掉
		}
	}()
	u := strings.TrimSpace(url)
	if u == "" {
		return careHealUnknown
	}
	apps, err := NewSource(name, u).Fetch()
	if err != nil {
		return careHealUnknown
	}
	return len(apps)
}

// probeHealSafe 按源串行 + 全局限并发 + panic 保护（与 probeCareSafe 同口径）。
// 2026-11-27 F10：与复核共用同一套「按源锁 + 并发闸门」，两条通道不会同时
// 打同一个仓库（否则互相撞限流）。
func (m *Manager) probeHealSafe(name, url string) int {
	mu := probeSourceLock(probeKey(name, url))
	mu.Lock()
	defer mu.Unlock()
	probeSem <- struct{}{}
	defer func() { <-probeSem }()
	fn := m.CareHealProbe
	if fn == nil {
		fn = ProbeCareAppCount
	}
	defer func() {
		if r := recover(); r != nil {
			_ = r
		}
	}()
	return fn(name, url)
}

// AutoCareHealPass 自愈一轮（A 快照 / B 锁外探测 / C 应用）。
// 返回动作描述（供 [auto-care] 日志），待通知事件入队由 DrainCareEvents 取。
func (m *Manager) AutoCareHealPass() []string {
	if m.cfg.SourceAutoCareOff {
		return nil
	}
	now := time.Now()

	// ── A：快照需要探测的源（锁内只复制，不做网络）──
	type healTarget struct{ name, url string }
	var targets []healTarget
	m.mu.Lock()
	m.careMu.Lock()
	for i := range m.cfg.Sources {
		s := &m.cfg.Sources[i]
		if s.IsEnabled() {
			continue // 启用源走正常刷新轮，不经自愈通道
		}
		rec := m.careRecs[s.Name]
		if rec.DisabledBy != careDisabledByAuto {
			continue // 用户手动停用：永不自动恢复
		}
		// 与落盘口径一致（批次 1 F2）：记录里的地址是脱敏值，必须同样过
		// redactSourceURL 再比，否则带 query/userinfo 的源永远「不相等」→
		// 自愈通道对这些源静默失效。
		if rec.URL != "" && redactSourceURL(rec.URL) != redactSourceURL(s.URL) {
			continue // 换了仓库地址：旧停用标记不适用（等下一轮刷新重新判定）
		}
		iv := AutoCareHealInterval
		if rec.HealCapped {
			iv = AutoCareHealIntervalCapped // 已达上限：降到 24h 一次
		}
		if !rec.LastHealAt.IsZero() && now.Sub(rec.LastHealAt) < iv {
			continue // 低频：间隔内已探过
		}
		targets = append(targets, healTarget{s.Name, s.URL})
	}
	m.careMu.Unlock()
	m.mu.Unlock()

	if len(targets) == 0 {
		return nil
	}

	// ── B：锁外探测 ──
	counts := make(map[string]int, len(targets))
	for _, t := range targets {
		counts[t.name] = m.probeHealSafe(t.name, t.url)
	}

	// ── C：应用结论（锁序 m.mu → careMu）──
	m.mu.Lock()
	m.careMu.Lock()
	acts := m.autoCareHealApplyLocked(now, counts)
	m.careMu.Unlock()
	m.mu.Unlock()
	if len(acts) > 0 {
		m.saveCareAsync()
	}
	return acts
}

// autoCareHealApplyLocked 阶段 C。调用方持 m.mu + careMu。
func (m *Manager) autoCareHealApplyLocked(now time.Time, counts map[string]int) []string {
	var acts []string
	for i := range m.cfg.Sources {
		s := &m.cfg.Sources[i]
		n, ok := counts[s.Name]
		if !ok {
			continue
		}
		rec := m.careRecs[s.Name]
		if rec.DisabledBy != careDisabledByAuto {
			continue // 本轮期间被用户改了开关/删了源：放弃
		}
		rec.LastHealAt = now
		// 抖动上限：30 天滚动窗口内最多自动恢复 AutoCareHealCap 次。窗口到期
		// 先滚（计数归零、上限标记作废），再判本轮能不能恢复——否则「好了又
		// 坏、坏了又好」的源会被无限次自动复活，用户每次都得重新关一遍。
		if !rec.HealWindowStart.IsZero() && now.Sub(rec.HealWindowStart) >= AutoCareHealWindow {
			rec.HealCount = 0
			rec.HealWindowStart = time.Time{}
			rec.HealCapped = false
		}
		switch {
		case n == careHealUnknown:
			// 超时/404/解析失败 = 不确定：不推进（也不清零——偶发网络抖动
			// 不该把「已连续成功 1 次」的进度抹掉），下个间隔再探。
		case n <= 0:
			rec.HealStreak = 0 // 能拉到索引但确实没有应用：连续计数中断
		default:
			rec.HealStreak++
			if rec.HealStreak < AutoCareHealStreak {
				break
			}
			wasCapped := rec.HealCapped
			if rec.HealCount >= AutoCareHealCap {
				// 上限已达：保持停用。HealStreak 钉在门槛值（不再增长，避免
				// 每轮刷日志）；动作与事件只在「首次触顶」那轮产生一次。
				rec.HealCapped = true
				rec.HealStreak = AutoCareHealStreak
				if !wasCapped {
					acts = append(acts, fmt.Sprintf(
						"源「%s」30 天内已自动恢复 %d 次，本轮又探测到 %d 个应用，但已达上限，保持停用",
						s.Name, rec.HealCount, n))
					ev := CareEvent{Kind: CareEventHealCapped, Name: s.Name, URL: s.URL,
						Reason: "30 天内自动恢复次数已用尽", Count: n, Streak: rec.HealCount}
					ev.Notify = rec.allowNotice(CareEventHealCapped, now)
					m.pushCareEventLocked(ev)
				}
				break
			}
			on := true
			s.Enabled = &on
			delete(m.careEmpty, s.Name) // 恢复后空源计数从 0 重新计
			rec.FailStreak = 0
			rec.FirstFailAt = time.Time{}
			rec.HealStreak = 0
			rec.DisabledBy = ""
			rec.HealCapped = false
			if rec.HealWindowStart.IsZero() {
				rec.HealWindowStart = now
			}
			rec.HealCount++
			rec.LastRecoveredAt = now
			acts = append(acts, fmt.Sprintf(
				"源「%s」连续 %d 次低频探测到 %d 个应用，已自动恢复启用", s.Name, AutoCareHealStreak, n))
			ev := CareEvent{Kind: CareEventRecovered, Name: s.Name, URL: s.URL, Count: n, Streak: AutoCareHealStreak}
			ev.Notify = rec.allowNotice(CareEventRecovered, now)
			m.pushCareEventLocked(ev)
		}
		m.setCareRecordLocked(s.Name, rec)
	}
	return acts
}
