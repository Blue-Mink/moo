package source

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// careSaveMu 2026-11-26（批次 B）：care 计数落盘专用锁。
// 此前复用 cache.go 的全局 saveMu（串行化 120 源目录快照写盘）——care 写完
// 要排在整轮目录快照之后，退出/测试会白等；两类文件互不相干，拆锁即可。
var careSaveMu sync.Mutex

// P1-4（2026-11-26 CARE）：源自动监测计数的持久化。
//
// 背景：0.6.566 之前计数只有内存态 `careEmpty`，Moo 一重启就清零——「连续 5
// 轮无应用自动停用」在频繁换包的机器上永远走不到阈值；失败通道若同样只在内存
// 里，重启即绕过「连续 N 轮」的门槛（误判红线 3 要求双门槛，重启清空等于把
// 门槛抹掉）。本文件把空源计数、失败计数、首次失败时间、最近错误与「停用来源」
// 落到 `<dataDir>/cache/source_care.json`。
//
// 安全降级：文件缺失 / 损坏 / 无 path 一律当作「无计数」，不报错不阻塞启动
// （与 cache.go 目录快照、indexpref.go 索引名记忆同口径）。

// careRec 单个源的失败通道状态（空源计数在 Manager.careEmpty，见 careFile）。
type careRec struct {
	// URL 写入时的源地址：地址变了说明是「换了仓库」，旧计数不再适用。
	URL string `json:"url,omitempty"`
	// FailStreak 连续「确定性永久失败」轮数（暂态错误不计，见 care.go）。
	FailStreak int `json:"fail_streak,omitempty"`
	// FirstFailAt 本轮连续失败的第一轮时间（双门槛：≥5 轮且 ≥24h）。
	FirstFailAt time.Time `json:"first_fail_at,omitempty"`
	// LastError 最近一次刷新失败的简短描述（已脱敏，供 /api/sources 展示）。
	LastError string `json:"last_error,omitempty"`
	// LastErrorAt 最近一次失败时间。
	LastErrorAt time.Time `json:"last_error_at,omitempty"`
	// DisabledBy 停用来源标记（auto-care = 策略停用；空 = 用户手动停用）。
	// 1b 的自愈通道只允许复活 auto-care 停用的源，绝不碰用户手动停用的源。
	DisabledBy string `json:"disabled_by,omitempty"`
	// ── P1-1b 自愈通道（2026-11-26）──
	// HealStreak 低频探测「连续看到有应用」的次数（≥2 才恢复启用）。
	HealStreak int `json:"heal_streak,omitempty"`
	// LastHealAt 最近一次低频探测时间（6h 节流；不确定的探测也推进，防打爆）。
	LastHealAt time.Time `json:"last_heal_at,omitempty"`
	// ── P1-1c 通知节流（误判红线 6：同一源同类通知 24h 一条）──
	// DisableNoticeAt 最近一次「自动停用」通知时间。
	DisableNoticeAt time.Time `json:"disable_notice_at,omitempty"`
	// RecoverNoticeAt 最近一次「自动恢复」通知时间。
	RecoverNoticeAt time.Time `json:"recover_notice_at,omitempty"`
	// ── 抖动上限 / 冷却（2026-11-26 CARE 冷却批次）──
	// LastRecoveredAt 最近一次自动恢复启用的时间。空源通道的 72h 冷却窗以它
	// 为起点：刚恢复的源不该因为下一轮仍然没应用就被立刻再停用（否则来回抖）。
	LastRecoveredAt time.Time `json:"last_recovered_at,omitempty"`
	// HealCount 当前滚动窗口内已自动恢复的次数（上限 AutoCareHealCap）。
	HealCount int `json:"heal_count,omitempty"`
	// HealWindowStart 滚动窗口起点（零值 = 尚未开窗，下次恢复时置为当轮时间）。
	HealWindowStart time.Time `json:"heal_window_start,omitempty"`
	// HealCapped 本窗口内自动恢复次数已用尽：保持停用，只低频复查不自动复活。
	HealCapped bool `json:"heal_capped,omitempty"`
	// CappedNoticeAt 最近一次「已达恢复上限」通知时间（24h 节流）。
	CappedNoticeAt time.Time `json:"capped_notice_at,omitempty"`
	// ── 判定可观测（2026-11-27 FIXPLAN 盲点 #3 · C2，只读信号）──
	// LastVerdict 最近**一轮通道复核**的结论字符串：alive / dead / uncertain
	//（空 = 从未复核过）。它按轮覆盖，不是历史记录；与 FailStreak 配对看：
	// 一轮探测里「探到了什么」由它表达，「累计了几轮」由 FailStreak 表达。
	LastVerdict string `json:"last_verdict,omitempty"`
	// VerdictAt 该结论产生的时间（unix 秒，0 = 无）。
	VerdictAt int64 `json:"verdict_at,omitempty"`
}

// careFile 落盘结构（空源计数与失败记录同文件，避免两处读写不同步）。
type careFile struct {
	SavedAt string             `json:"saved_at"`
	Empty   map[string]int     `json:"empty_streak,omitempty"`
	Records map[string]careRec `json:"records,omitempty"`
}

// SetCarePath 设置「自动监测计数」落盘路径并加载（main 在构造后调用）。
// 返回恢复的条数；路径为空或文件不可用时返回 0（安全降级为无计数）。
func (m *Manager) SetCarePath(path string) int {
	m.careMu.Lock()
	m.carePath = path
	m.careMu.Unlock()
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var f careFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0 // 损坏快照不致命：退化为「无计数」，下一轮重建
	}
	m.careMu.Lock()
	defer m.careMu.Unlock()
	n := 0
	for k, v := range f.Empty {
		if v > 0 {
			m.careEmpty[k] = v
			n++
		}
	}
	for k, v := range f.Records {
		// 一次性迁移（批次 1 F2）：旧版本落盘的是原始地址（可能含 userinfo/
		// query），读入即脱敏——否则下次比较/展示仍会把凭据带出来。
		v.URL = redactSourceURL(v.URL)
		// 恢复条件必须覆盖「只剩自愈进度/通知节流时间戳」的记录：否则重启后
		// HealStreak 归零（永远凑不到连续 2 次）、24h 节流失效（重复骚扰）。
		// 抖动上限 / 冷却字段同样属于「必须持久化」的面：漏了它们重启后
		// HealCount 归零（上限形同不存在）、LastRecoveredAt 归零（冷却窗失效）。
		if v.FailStreak > 0 || v.LastError != "" || v.DisabledBy != "" ||
			v.HealStreak > 0 || !v.LastHealAt.IsZero() ||
			!v.DisableNoticeAt.IsZero() || !v.RecoverNoticeAt.IsZero() ||
			!v.LastRecoveredAt.IsZero() || v.HealCount > 0 ||
			!v.HealWindowStart.IsZero() || v.HealCapped || !v.CappedNoticeAt.IsZero() ||
			v.LastVerdict != "" || v.VerdictAt != 0 {
			m.careRecs[k] = v
			n++
		}
	}
	return n
}

// careSnapshot 取落盘快照（内部持 careMu）。
func (m *Manager) careSnapshot() careFile {
	m.careMu.Lock()
	defer m.careMu.Unlock()
	return m.careSnapshotLocked()
}

// careSnapshotLocked 调用方必须持有 careMu。
func (m *Manager) careSnapshotLocked() careFile {
	f := careFile{SavedAt: time.Now().Format(time.RFC3339)}
	if len(m.careEmpty) > 0 {
		f.Empty = make(map[string]int, len(m.careEmpty))
		for k, v := range m.careEmpty {
			f.Empty[k] = v
		}
	}
	if len(m.careRecs) > 0 {
		f.Records = make(map[string]careRec, len(m.careRecs))
		for k, v := range m.careRecs {
			f.Records[k] = v
		}
	}
	return f
}

// careSnapshotWithSeq 取快照并分配单调序号（内部持 careMu）。
// 序号用于「乱序落盘不得回退文件内容」的判据，见 persistCare。
func (m *Manager) careSnapshotWithSeq() (string, careFile, uint64) {
	m.careMu.Lock()
	defer m.careMu.Unlock()
	m.careSeq++
	return m.carePath, m.careSnapshotLocked(), m.careSeq
}

// persistCare 落盘并按 seq 单调过滤：只有比已写内容更新的快照才允许覆盖。
//
// 2026-11-26（全量测试暴露）：旧实现里异步写拿到的是「调度时」的快照，
// 若它晚于之后的同步 SaveCare 完成，会把新计数覆盖成旧值（实测 4 → 2）。
// 现在两条路径共用 saveMu + seq 判据：先占位再写，旧 seq 直接丢弃。
func (m *Manager) persistCare(path string, f careFile, seq uint64) error {
	if path == "" {
		return nil
	}
	careSaveMu.Lock()
	defer careSaveMu.Unlock()
	m.careMu.Lock()
	if seq <= m.careWritten {
		m.careMu.Unlock()
		return nil
	}
	m.careWritten = seq
	m.careMu.Unlock()
	return writeCareFile(path, f)
}

// SaveCare 同步落盘（测试与退出路径用）。path 为空 = 不落盘。
func (m *Manager) SaveCare() error {
	path, f, seq := m.careSnapshotWithSeq()
	return m.persistCare(path, f, seq)
}

// saveCareAsync 异步落盘（AutoCarePass 内部每次计数变化后调用）。
// 复用 cache.go 的 saveMu，避免与目录快照/索引记忆并发交错写同一目录。
func (m *Manager) saveCareAsync() {
	path, f, seq := m.careSnapshotWithSeq()
	if path == "" {
		return
	}
	m.careFlush.Add(1)
	go func() {
		defer m.careFlush.Done()
		_ = m.persistCare(path, f, seq)
	}()
}

// WaitCareFlush 等待所有在途异步落盘结束（测试清理临时目录、平滑退出用）。
// 落盘是「尽力而为」：本函数只等已发起的写，不保证内容已 fsync。
func (m *Manager) WaitCareFlush() { m.careFlush.Wait() }

// redactSourceURL 把源地址剥成「scheme://host/path」：去掉 userinfo 里的
// 账号/令牌与 ?query / #fragment（批次 1 F2 / N4）。care 状态文件会被备份、
// 拷走、贴进排障记录，凭据不能落进去；与 sanitizeSourceError 同口径。
// 空串原样返回（调用方用空串表达「未知/无地址」）。
func redactSourceURL(raw string) string {
	if raw == "" {
		return ""
	}
	s := careURLUserinfoRe.ReplaceAllString(raw, "$1")
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// careRefURLLocked 取某源当前地址（不存在返回空串）。调用方持 m.mu。
func (m *Manager) careRefURLLocked(name string) string {
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name == name {
			return m.cfg.Sources[i].URL
		}
	}
	return ""
}

// careRecordLocked 取失败记录。调用方持 m.mu + careMu。
// 地址变化时视为「换了仓库」：返回空记录（旧计数不再适用）。
func (m *Manager) careRecordLocked(name string) careRec {
	rec := m.careRecs[name]
	// 比较必须与落盘同口径（都过 redactSourceURL）：落盘存的是脱敏值，
	// 拿它跟原始地址比永远不等 → 每条记录每轮被重置、fail_streak 永不累积
	// （批次 1 F2 的关键点，漏了就是严重回归）。存量记录可能还是原文，也过一遍。
	if url := m.careRefURLLocked(name); url != "" && rec.URL != "" &&
		redactSourceURL(rec.URL) != redactSourceURL(url) {
		return careRec{}
	}
	return rec
}

// setCareRecordLocked 写回失败记录（带当前地址；空记录直接删除）。
// 调用方持 m.mu + careMu。
func (m *Manager) setCareRecordLocked(name string, rec careRec) {
	if rec.FailStreak <= 0 && rec.LastError == "" && rec.DisabledBy == "" && rec.FirstFailAt.IsZero() &&
		rec.HealStreak == 0 && rec.LastHealAt.IsZero() &&
		rec.DisableNoticeAt.IsZero() && rec.RecoverNoticeAt.IsZero() &&
		rec.LastRecoveredAt.IsZero() && rec.HealCount == 0 && rec.HealWindowStart.IsZero() &&
		!rec.HealCapped && rec.CappedNoticeAt.IsZero() &&
		rec.LastVerdict == "" && rec.VerdictAt == 0 {
		delete(m.careRecs, name)
		return
	}
	rec.URL = redactSourceURL(m.careRefURLLocked(name))
	if rec.URL == "" {
		rec.URL = m.careRecs[name].URL // 已是脱敏值（存量由 SetCarePath 迁移）
	}
	m.careRecs[name] = rec
}
