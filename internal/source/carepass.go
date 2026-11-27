package source

// P1-1（2026-11-26 CARE）：失败通道的落地实现。
//
// 分三段执行，网络动作全部在锁外：
//
//	A（持锁快照）本轮失败的源 → 需要实测通道
//	B（锁外）probeCareSafe 逐源复核（短超时、短路；按源串行 + 全局限并发 2，
//	  单源总预算 careProbeBudget，超预算按不确定处理）
//	C（持锁）按复核结论推进/清零/放弃计数，满足双门槛再停用
//
// 计数语义（误判红线 1~4 的落地）：
//
//	· CarePermanentlyDead → FailStreak++（记录首次失败时间）
//	· CareAlive          → FailStreak 清零（含「镜像 404 但直连 200」）
//	· CareUncertain      → 本轮放弃：不增不减，也不动作
//	· 停用条件：FailStreak ≥ 5 **且** 距 FirstFailAt ≥ 24h，且本轮复核仍为
//	  确定性失效（复核就是 B 段这次无视记忆/放开全部镜像的实测）
func (m *Manager) AutoCarePass(sts []SourceStatus) []string {
	if m.cfg.SourceAutoCareOff {
		return nil
	}

	// ── A：快照需要复核的源（锁内只复制，不做网络）──
	type probeTarget struct{ name, url string }
	var targets []probeTarget
	m.mu.Lock()
	for _, st := range sts {
		if st.Error == "" {
			continue
		}
		if url := m.careRefURLLocked(st.Name); url != "" {
			targets = append(targets, probeTarget{st.Name, url})
		}
	}
	m.mu.Unlock()

	// ── B：锁外复核（探测失败/超时一律 CareUncertain）──
	verdicts := make(map[string]CareVerdict, len(targets))
	for _, t := range targets {
		verdicts[t.name] = m.probeCareSafe(t.name, t.url)
	}

	// ── C：应用结论，产出动作 ──
	m.mu.Lock()
	acts, dirty := m.autoCareApplyLocked(sts, verdicts)
	m.mu.Unlock()
	if dirty {
		m.saveCareAsync()
	}
	return acts
}
