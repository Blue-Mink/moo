package api

// autoupdate_negcache.go —— 自动更新的「需人工处理」负缓存（0.6.543 引入，0.6.545 收口）。
//
// 两类判定会被缓存，命中时该应用本轮直接不进任务表：不下载、不试探、不写
// 日志、不进轮次通知。
//
//	reason=wizard  ：升级需要先确认向导（必填项无默认值）。TTL 12h。
//	reason=blocked ：平台自己标记 autoUpdateFailed（依赖未停/向导未确认等）。TTL 2h。
//	reason=channel ：平台不允许走 FPK 升级通道（10371）。TTL 2h，且**要连续两次**
//	                 命中才生效（0.6.578 批次 3a）。
//
// 两个防误伤设计：
//   - key 里带**目标包 sha256**：源把同一个版本号重发（例如补了默认值）时 sha 变了，
//     旧结论立刻失效、马上重探，不会盲等 12 小时。
//   - key 里带**reason**：平台标志一旦消失，blocked 条目就再也命中不了（变成 wizard
//     通道重新判定），不会挡住「其实已经能装」的窗口。
//
// 用户手动确认向导完成升级后该应用不再「有更新」，条目自然失效；成功更新时也会
// 主动清掉该应用的全部条目。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// autoUpdateSkipTTLWizard 「升级需先确认向导」判定的静默时长。
	autoUpdateSkipTTLWizard = 12 * time.Hour
	// autoUpdateSkipTTLBlocked 平台标记「自动更新失败」的静默时长（短，避免挡住刚修好的窗口）。
	autoUpdateSkipTTLBlocked = 2 * time.Hour
	// autoUpdateSkipTTLUnknown 「任务结果未知」（超预算/状态查不到但平台可能仍在跑）
	// 的静默时长。openclaw 事故（2026-11-20）：大应用升级要 rsync 16+ 分钟，
	// 12 分钟预算先超时；旧逻辑每小时重来一次 → 反复 12 分钟轮询 + 重复拷贝，
	// 后台 CPU/IO 持续偏高。6 小时内不再自动重试，交给手动处理。
	autoUpdateSkipTTLUnknown = 6 * time.Hour
	// autoUpdateSkipTTLChannel 「平台不允许 FPK 升级通道」（10371）的静默时长。
	// 0.6.578 批次 3a：12h → 2h。
	// 边界（复核补充）：默认 `check_interval_hours=0` → 24h 一轮，2h 的条目会在
	// 下一轮之前过期，因此**默认节奏下缓存等于不生效**（fail-open，不误拦）；
	// 只有轮次间隔 < 2h（用户显式调小，或同轮多次触发）时才会真正静默，
	// 此时「连续两次命中」才起作用。误判放大倍数的上界因此比 12h 小得多：
	// 30 分钟节奏下最多静默 4 个周期（原 24 个）。
	autoUpdateSkipTTLChannel = 2 * time.Hour
	// autoUpdateSkipMaxEntries 缓存条目上限（防御异常增长；正常远小于此值）。
	autoUpdateSkipMaxEntries = 200

	autoUpdateSkipReasonWizard  = "wizard"
	autoUpdateSkipReasonBlocked = "blocked"
	autoUpdateSkipReasonUnknown = "unknown"
	// autoUpdateSkipReasonChannel 平台不允许走 FPK 升级通道（10371：通常由官方
	// 应用中心安装的应用）。0.6.575：自动更新分支也先预检，命中就不下载整包
	// （PATCH_0575 B）。0.6.578 批次 3a 起 TTL 收窄到 2h 且要连击两次才生效
	// （见 autoUpdateSkipTTLChannel / markAutoUpdateSkip）；卸载重装成 Moo 版本
	// 后自然失效，同版本换包（sha 变）也会立即重探。
	autoUpdateSkipReasonChannel = "channel"

	// 文件从 0.6.543 的 auto_update_wizard_skip.json 改名而来：现在也存
	// blocked 判定，旧名字会误导。读取时自动迁移（写新→删旧）。
	autoUpdateSkipFileName       = "auto_update_skips.json"
	autoUpdateSkipLegacyFileName = "auto_update_wizard_skip.json"
	autoUpdateSkipSchemaVersion  = 2
)

type autoUpdateSkipEntry struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Sha     string `json:"sha,omitempty"`
	Reason  string `json:"reason"`
	Until   string `json:"until"` // RFC3339
	Missing string `json:"missing,omitempty"`
	// Hits 是同 key 的**连续命中次数**（0.6.578 批次 3a 起只对 channel 生效，
	// 其它 reason 恒为 0、也不看它）。
	// channel 判定要求 Hits>=2 才算命中：首轮只计数不拦，下一轮预检再撞一次
	// 才静默——一次瞬时 10371 不该让应用静默 2 小时。
	// 向后兼容：旧版本写的条目没有该字段 → Hits=0 → 视为「尚未连击」，
	// channel 条目会再预检一轮才生效（宁可多探一次，不误拦）；因此
	// schema_version 不必升。
	Hits int `json:"hits,omitempty"`
}

type autoUpdateSkipState struct {
	SchemaVersion int                   `json:"schema_version,omitempty"`
	Entries       []autoUpdateSkipEntry `json:"entries"`
}

// autoUpdateSkipKey 应用名大小写不敏感（daemon 与源 feed 命名习惯不同）。
func autoUpdateSkipKey(app, version, sha, reason string) string {
	return strings.ToLower(app) + "\x00" + version + "\x00" + strings.ToLower(sha) + "\x00" + reason
}

func (s *Server) autoUpdateSkipPath() string {
	d := dataDirOf(s)
	if d == "" {
		return ""
	}
	return filepath.Join(d, autoUpdateSkipFileName)
}

// legacyAutoUpdateSkipPath 0.6.543~0.6.546 的文件名（仅用于一次性迁移）。
func (s *Server) legacyAutoUpdateSkipPath() string {
	d := dataDirOf(s)
	if d == "" {
		return ""
	}
	return filepath.Join(d, autoUpdateSkipLegacyFileName)
}

// readSkipFile 读一个缓存文件并返回有效条目（过期/坏文件按空处理）。
func (s *Server) readSkipFile(p string) map[string]autoUpdateSkipEntry {
	out := map[string]autoUpdateSkipEntry{}
	if p == "" {
		return out
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	var st autoUpdateSkipState
	if json.Unmarshal(b, &st) != nil {
		return out
	}
	now := time.Now()
	for _, e := range st.Entries {
		if e.App == "" || e.Version == "" {
			continue
		}
		until, perr := time.Parse(time.RFC3339, e.Until)
		if perr != nil || !until.After(now) {
			continue
		}
		if e.Reason == "" { // 0.6.543 的旧条目 = wizard
			e.Reason = autoUpdateSkipReasonWizard
		}
		out[autoUpdateSkipKey(e.App, e.Version, e.Sha, e.Reason)] = e
	}
	return out
}

// autoUpdateSkips 读取当前有效的缓存条目（过期的直接丢弃，坏文件当空处理）。
func (s *Server) autoUpdateSkips() map[string]autoUpdateSkipEntry {
	m := s.readSkipFile(s.autoUpdateSkipPath())
	// 一次性迁移：新文件还没生成过、旧文件名还在 → 读旧、写新、确认新文件落盘后删旧。
	// 失败最坏结果是条目丢一次（多探测一轮），不会有功能损失。
	if len(m) == 0 {
		if _, err := os.Stat(s.autoUpdateSkipPath()); err == nil {
			return m // 新文件已在（可能是真空文件），不再看旧文件
		}
		legacyPath := s.legacyAutoUpdateSkipPath()
		if legacyPath == "" {
			return m
		}
		if _, err := os.Stat(legacyPath); err != nil {
			return m
		}
		m = s.readSkipFile(legacyPath)
		s.saveAutoUpdateSkips(m)
		if _, err := os.Stat(s.autoUpdateSkipPath()); err == nil {
			_ = os.Remove(legacyPath)
		}
	}
	return m
}

// autoUpdateSkipHit 判断某应用某目标版本是否处于静默期。
// autoUpdateSkipHitAny wizard / blocked 两类缓存任一命中（0.6.548）。
//
// 依赖占用写入的是 blocked 条目，但这类应用平时 j.blocked 为空（平台没标
// autoUpdateFailed），旧逻辑只按 j.blocked 取 reason → 新写缓存永不命中，
// 既刷日志又白占 200 条上限。
func autoUpdateSkipHitAny(skips map[string]autoUpdateSkipEntry, app, version, sha string) bool {
	return autoUpdateSkipHit(skips, app, version, sha, autoUpdateSkipReasonWizard) ||
		autoUpdateSkipHit(skips, app, version, sha, autoUpdateSkipReasonBlocked) ||
		autoUpdateSkipHit(skips, app, version, sha, autoUpdateSkipReasonUnknown) ||
		autoUpdateSkipHit(skips, app, version, sha, autoUpdateSkipReasonChannel)
}

func autoUpdateSkipHit(m map[string]autoUpdateSkipEntry, app, version, sha, reason string) bool {
	if len(m) == 0 {
		return false
	}
	e, ok := m[autoUpdateSkipKey(app, version, sha, reason)]
	if !ok {
		return false
	}
	// 0.6.578 批次 3a：channel 判定是推测性的（10371 在瞬时态也会出现），
	// 要求**连续两次**预检都撞上才算命中；首轮不命中意味着下一轮会再预检一次，
	// 这就是设计意图（连击判定），不额外维护内存表。
	// 其它 reason 不看 Hits：行为与之前完全一致。
	if reason == autoUpdateSkipReasonChannel && e.Hits < 2 {
		return false
	}
	return true
}

// saveAutoUpdateSkips 落盘（过期条目不写、超过上限截断、原子替换）。
func (s *Server) saveAutoUpdateSkips(m map[string]autoUpdateSkipEntry) {
	p := s.autoUpdateSkipPath()
	if p == "" {
		return
	}
	now := time.Now()
	live := make([]autoUpdateSkipEntry, 0, len(m))
	for _, e := range m {
		until, err := time.Parse(time.RFC3339, e.Until)
		if err != nil || !until.After(now) {
			continue
		}
		live = append(live, e)
	}
	// 最近写入的优先保留，超出上限丢弃最旧的。
	sort.Slice(live, func(i, j int) bool { return live[i].Until > live[j].Until })
	if len(live) > autoUpdateSkipMaxEntries {
		live = live[:autoUpdateSkipMaxEntries]
	}
	b, err := json.Marshal(autoUpdateSkipState{SchemaVersion: autoUpdateSkipSchemaVersion, Entries: live})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + "." + time.Now().Format("20060102150405.000000000")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// markAutoUpdateSkip 记一条判定；reason 取 wizard / blocked / unknown / channel。
//
// channel 是唯一会累积 Hits 的 reason：同 key 条目仍在静默期内 → Hits+1，
// 否则从 1 起算；Until 每次都按 now+TTL 重算。只有 Hits>=2 才会被
// autoUpdateSkipHit 视为命中（连击判定，见该函数注释）。
func (s *Server) markAutoUpdateSkip(app, version, sha, reason string, missing []string) {
	if app == "" || version == "" {
		return
	}
	if reason == "" {
		reason = autoUpdateSkipReasonWizard
	}
	ttl := autoUpdateSkipTTLWizard
	switch reason {
	case autoUpdateSkipReasonBlocked:
		ttl = autoUpdateSkipTTLBlocked
	case autoUpdateSkipReasonUnknown:
		ttl = autoUpdateSkipTTLUnknown
	case autoUpdateSkipReasonChannel:
		ttl = autoUpdateSkipTTLChannel
	}
	now := time.Now()
	m := s.autoUpdateSkips()
	key := autoUpdateSkipKey(app, version, sha, reason)
	hits := 0
	if reason == autoUpdateSkipReasonChannel {
		if prev, ok := m[key]; ok {
			hits = prev.Hits
		}
		hits++
	}
	m[key] = autoUpdateSkipEntry{
		App:     app,
		Version: version,
		Sha:     sha,
		Reason:  reason,
		Until:   now.Add(ttl).Format(time.RFC3339),
		Missing: strings.Join(missing, "、"),
		Hits:    hits,
	}
	s.saveAutoUpdateSkips(m)
}

// markAutoUpdateWizardSkip 兼容旧调用：升级向导缺必填项。
func (s *Server) markAutoUpdateWizardSkip(app, version string, missing []string) {
	s.markAutoUpdateSkip(app, version, "", autoUpdateSkipReasonWizard, missing)
}

// clearAutoUpdateBlockedSkips 策略/白名单变更后清掉全部 blocked 类缓存：
// 用户刚放开某个应用，不该再等 2 小时才重试。wizard 类保留（与本次变更无关）。
func (s *Server) clearAutoUpdateBlockedSkips() {
	m := s.autoUpdateSkips()
	changed := false
	for k, e := range m {
		if e.Reason == autoUpdateSkipReasonBlocked {
			delete(m, k)
			changed = true
		}
	}
	if changed {
		s.saveAutoUpdateSkips(m)
	}
}

// clearAutoUpdateWizardSkip 应用成功更新后清掉它的全部条目（任何版本/原因）。
func (s *Server) clearAutoUpdateWizardSkip(app string) {
	if app == "" {
		return
	}
	m := s.autoUpdateSkips()
	changed := false
	for k, e := range m {
		if strings.EqualFold(e.App, app) {
			delete(m, k)
			changed = true
		}
	}
	if changed {
		s.saveAutoUpdateSkips(m)
	}
}
