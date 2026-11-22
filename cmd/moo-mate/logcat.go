package main

// 批次⑦：日志归类与降噪（只影响日志输出，不改任何业务逻辑）。
//
// 目的：mate 自身日志统一带分类前缀，便于日志页按关键字过滤；
// 无信息量的重复行按 key 折叠；连续失败只打第 1/10/20… 次并在恢复时补一行。
//
// ⚠️ 分类出口叫 catLogf 而非 logf：包里已存在 selfupdate_api.go 的
// func logf(format string, args ...any)，同名会编译失败，而该文件不在本批次范围。

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 折叠表边界（0.2.76 / 审计 P2-4）：throttle 与 failStreak 的 key 可由外部请求头
// 构造（如 Origin），必须「限长 + 限条目」，否则换着 Origin 打就能让 map 单调增长，
// 既放大内存，也稀释折叠（每个新 key 都会真打一条日志）。
const (
	logKeyMaxRunes  = 128 // 单个 key 最多保留的 rune 数（超出截断加省略号）
	logTableMaxKeys = 512 // 每张折叠表最多保留的 key 数（超出淘汰最久未更新的）
)

// boundLogKey 归一化日志折叠 key：去首尾空白 → 控制字符（含换行/制表）压成空格
// （防伪造日志行）→ 超长按 rune 截断。空 key 统一为 "-"，保证仍能折叠。
func boundLogKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, key)
	key = strings.Join(strings.Fields(key), " ")
	if key == "" {
		return "-"
	}
	if utf8.RuneCountInString(key) > logKeyMaxRunes {
		rs := []rune(key)
		key = string(rs[:logKeyMaxRunes-1]) + "…"
	}
	return key
}

// 日志分类前缀（日志页可按这些关键字过滤）。
const (
	catBoot  = "[boot]"
	catSync  = "[sync]"
	catIndex = "[index]"
	catIcon  = "[icon]"
	catDL    = "[dl]"
	catHTTP  = "[http]"
	catCfg   = "[cfg]"
)

// catLogf 分类日志出口：cat + 文案主体。
func catLogf(cat, format string, args ...any) {
	log.Printf(cat+" "+format, args...)
}

// syncHeartbeatWindow 同步心跳窗口：无更新时每 6h 打一条，
// 避免每小时一条「同步完成: … 更新 0」把日志淹没。
const syncHeartbeatWindow = 6 * time.Hour

// syncLogMode 纯函数：决定本轮同步结果行是否打印、打成什么形态。
//
//	changed   —— 有更新，立即打
//	heartbeat —— 无更新但距上次结果行 ≥6h，打一条心跳
//	silent    —— 无更新且刚打过，不打
func syncLogMode(updated int, sinceLastLog time.Duration) string {
	if updated > 0 {
		return "changed"
	}
	if sinceLastLog >= syncHeartbeatWindow {
		return "heartbeat"
	}
	return "silent"
}

// 全包共用的日志状态实例（接线点直接使用，便于单测/诊断）。
var (
	syncThrottle = newThrottle(time.Hour)        // 「同步进行中」1h 折叠
	syncFail     = newFailStreak()               // 同步失败连续计数
	cfgFail      = newFailStreak()               // 源地址校验失败连续计数
	csrfThrottle = newThrottle(10 * time.Minute) // CSRF 拒绝按来源 10min 折叠

	syncLoggedMu sync.Mutex
	syncLoggedAt time.Time
)

// markSyncLogged 记录「上一次真正打出同步结果行」的时刻。
func markSyncLogged() {
	syncLoggedMu.Lock()
	syncLoggedAt = time.Now()
	syncLoggedMu.Unlock()
}

// syncLogSince 距上次打出同步结果行的时间；从未打过时返回 2 个窗口
// （保证首次无更新的同步也留一条心跳，便于确认进程活着）。
func syncLogSince() time.Duration {
	syncLoggedMu.Lock()
	defer syncLoggedMu.Unlock()
	if syncLoggedAt.IsZero() {
		return syncHeartbeatWindow * 2
	}
	return time.Since(syncLoggedAt)
}

// throttle 同 key 折叠：窗口内只打首条，窗口到期后的第一条追加折叠计数。
// now 可注入，便于单测；every <= 0 视为不折叠（反证用）。
type throttle struct {
	mu      sync.Mutex
	last    map[string]time.Time
	dropped map[string]int
	every   time.Duration
	now     func() time.Time
}

// newThrottle 造一个折叠器（every = 窗口长度）。
func newThrottle(every time.Duration) *throttle {
	return &throttle{
		last:    make(map[string]time.Time),
		dropped: make(map[string]int),
		every:   every,
		now:     time.Now,
	}
}

// Logf 打一条折叠日志：同 key 在窗口内被丢弃并计数，
// 窗口到期后的第一条按原文打印，并追加「（期间同类 N 条已折叠）」。
func (t *throttle) Logf(key, cat, format string, args ...any) {
	if t == nil {
		return
	}
	key = boundLogKey(key)
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if last, ok := t.last[key]; ok && t.every > 0 && now.Sub(last) < t.every {
		t.dropped[key]++
		return
	}
	if _, ok := t.last[key]; !ok {
		t.evictLocked()
	}
	n := t.dropped[key]
	t.last[key] = now
	t.dropped[key] = 0
	msg := fmt.Sprintf(format, args...)
	if n > 0 {
		msg += fmt.Sprintf("（期间同类 %d 条已折叠）", n)
	}
	log.Print(cat + " " + msg)
}

// evictLocked 在插入新 key 前把表压回上限：淘汰「最久未打印」的 key（调用方需持锁）。
// 上限是硬保证，避免外部可控 key 把 map 撑大（P2-4）。
func (t *throttle) evictLocked() {
	for len(t.last) >= logTableMaxKeys {
		oldestKey := ""
		var oldest time.Time
		for k, ts := range t.last {
			if oldestKey == "" || ts.Before(oldest) {
				oldestKey, oldest = k, ts
			}
		}
		if oldestKey == "" {
			return
		}
		delete(t.last, oldestKey)
		delete(t.dropped, oldestKey)
	}
}

// failStreak 连续失败计数：同 key 连续失败只打第 1、10、20… 次，
// 第 10 次起追加「（连续第 N 次）」；恢复时打一行并把计数清零。
type failStreak struct {
	mu   sync.Mutex
	n    map[string]int
	last map[string]string
	seen map[string]time.Time // key 最近一次更新时间，用于上限淘汰（P2-4）
	now  func() time.Time
}

// newFailStreak 造一个连续失败计数器。
func newFailStreak() *failStreak {
	return &failStreak{
		n:    make(map[string]int),
		last: make(map[string]string),
		seen: make(map[string]time.Time),
		now:  time.Now,
	}
}

// Fail 记一次失败：n==1 打印原文；n%10==0 打印原文并追加次数；
// 其余次数静默累计（避免每轮一条同样的失败把日志刷满）。
func (f *failStreak) Fail(key, cat, format string, args ...any) {
	if f == nil {
		return
	}
	key = boundLogKey(key)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.n[key]; !ok {
		f.evictLocked()
	}
	f.n[key]++
	msg := fmt.Sprintf(format, args...)
	f.last[key] = msg
	f.seen[key] = f.now()
	switch {
	case f.n[key] == 1:
		log.Print(cat + " " + msg)
	case f.n[key]%10 == 0:
		log.Print(cat + " " + msg + fmt.Sprintf("（连续第 %d 次）", f.n[key]))
	}
}

// evictLocked 在插入新 key 前把表压回上限：淘汰最久未更新的 key（调用方需持锁）。
func (f *failStreak) evictLocked() {
	for len(f.n) >= logTableMaxKeys {
		oldestKey := ""
		var oldest time.Time
		for k, ts := range f.seen {
			if oldestKey == "" || ts.Before(oldest) {
				oldestKey, oldest = k, ts
			}
		}
		if oldestKey == "" {
			return
		}
		delete(f.n, oldestKey)
		delete(f.last, oldestKey)
		delete(f.seen, oldestKey)
	}
}

// OK 记一次成功：此前有连续失败时打一行「msg（此前连续失败 N 次）」并清零；
// 本来就没失败过则什么也不打（不制造噪音）。
func (f *failStreak) OK(key, cat, msg string) {
	if f == nil {
		return
	}
	key = boundLogKey(key)
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.n[key]
	if n <= 0 {
		return
	}
	f.n[key] = 0
	delete(f.last, key)
	delete(f.seen, key)
	log.Print(cat + " " + msg + fmt.Sprintf("（此前连续失败 %d 次）", n))
}

// Count 当前连续失败次数（诊断 / 单测用）。
func (f *failStreak) Count(key string) int {
	if f == nil {
		return 0
	}
	key = boundLogKey(key)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[key]
}
