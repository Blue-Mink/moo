package api

// autoupdate_negcache.go —— 自动更新的「需人工处理」负缓存（0.6.543 引入，0.6.545 收口）。
//
// 两类判定会被缓存，命中时该应用本轮直接不进任务表：不下载、不试探、不写
// 日志、不进轮次通知。
//
//	reason=wizard  ：升级需要先确认向导（必填项无默认值）。TTL 12h。
//	reason=blocked ：平台自己标记 autoUpdateFailed（依赖未停/向导未确认等）。TTL 2h。
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
	// autoUpdateSkipMaxEntries 缓存条目上限（防御异常增长；正常远小于此值）。
	autoUpdateSkipMaxEntries = 200

	autoUpdateSkipReasonWizard  = "wizard"
	autoUpdateSkipReasonBlocked = "blocked"

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
		autoUpdateSkipHit(skips, app, version, sha, autoUpdateSkipReasonBlocked)
}

func autoUpdateSkipHit(m map[string]autoUpdateSkipEntry, app, version, sha, reason string) bool {
	if len(m) == 0 {
		return false
	}
	_, ok := m[autoUpdateSkipKey(app, version, sha, reason)]
	return ok
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

// markAutoUpdateSkip 记一条判定；reason 取 wizard / blocked。
func (s *Server) markAutoUpdateSkip(app, version, sha, reason string, missing []string) {
	if app == "" || version == "" {
		return
	}
	if reason == "" {
		reason = autoUpdateSkipReasonWizard
	}
	ttl := autoUpdateSkipTTLWizard
	if reason == autoUpdateSkipReasonBlocked {
		ttl = autoUpdateSkipTTLBlocked
	}
	m := s.autoUpdateSkips()
	m[autoUpdateSkipKey(app, version, sha, reason)] = autoUpdateSkipEntry{
		App:     app,
		Version: version,
		Sha:     sha,
		Reason:  reason,
		Until:   time.Now().Add(ttl).Format(time.RFC3339),
		Missing: strings.Join(missing, "、"),
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
