package api

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"aead.dev/minisign"

	"moo/internal/config"
	"moo/internal/netguard"
)

// 2026-11-22（用户令）：远端「下架黑名单」——开发者维护的一份 JSON 清单，
// 改文件即生效、不用发版。四道闸：目录打标（保留卡 + 写 blacklist_* 字段，
// P1-4 起不再剔除）/ 详情 404 带原因兜底（目录保留后仅在 key 已不在目录时
// 命中）/ 动作拦截（安装·暂存·更新·后台下载·向导）/ 自动更新跳过。
//
// 匹配粒度 = 应用名（大小写不敏感）+ 可选源名（`*` 或空 = 全源下架）；
// 同名应用在**其它源**的条目不受影响（只下架被点名的那张卡）。
//
// 拉取复用推荐清单那一套（config.ListFileURLs + 多镜像候选 + 10min TTL +
// last-known-good）：
//   - 拉取失败 → 沿用上一次成功的清单（last-known-good）；
//   - 从未成功拉取过 → **fail-open**（空清单，绝不误杀）；
//   - 成功且内容变化 → invalidateCatalog()，列表立刻反映。
//
//go:embed default_blacklist.json
var embeddedBlacklistJSON string

const (
	blacklistTTL              = 10 * time.Minute
	blacklistFallbackTTL      = 2 * time.Minute
	blacklistRefreshBudget    = 40 * time.Second
	blacklistCandidateTimeout = 8 * time.Second
	blacklistMaxBytes         = 256 << 10
	blacklistMaxEntries       = 2000
	// blacklistNoticeMaxRunes `notice` 的展示上限（DESIGN 2026-11-22 §0/§2：60 字）。
	// 按 rune 计（中文一字算一），超长只截断展示值并记一条告警——**条目不丢**。
	blacklistNoticeMaxRunes = 60
)

// blacklistEntry 清单里的一条。Source 空 或 `*` = 全源；Until 空 = 长期有效。
//
// App 允许为空，但仅当 Sha256 合法（= 按包指纹拦，不看名字）；App 与 Sha256
// 同时为空视为无效条目丢弃。Sha256 合法时**额外**约束指纹：调用方拿不到
// 包指纹（sha 未知）时不命中——宁可漏拦旧包，也绝不误伤刚修好的新版本。
type blacklistEntry struct {
	App    string `json:"app"`
	Source string `json:"source,omitempty"`
	Sha256 string `json:"sha256,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Notice 展示用说明（≤ blacklistNoticeMaxRunes 字）：**只做展示，不参与判定**
	// （判定仍只看 app/source/sha256/until）。前端卡面浮层与详情页副行优先用它，
	// 为空时回落 blacklistReasonShort(reason)。
	Notice string `json:"notice,omitempty"`
	Since  string `json:"since,omitempty"`
	Until  string `json:"until,omitempty"`
	Ref    string `json:"ref,omitempty"`

	untilT time.Time // Until 解析结果（零值 = 不过期）
}

// blacklistFile lists/blacklist.json 的顶层结构。
type blacklistFile struct {
	SchemaVersion int              `json:"schema_version"`
	UpdatedAt     string           `json:"updated_at"`
	Entries       []blacklistEntry `json:"entries"`
}

// parseBlacklist 解析清单文本。兼容顶层对象（{"entries":[...]}）与裸数组；
// 条目里的空 app / 重复条目直接丢弃，超量截断（防异常清单撑爆内存）。
func parseBlacklist(body string) ([]blacklistEntry, error) {
	body = strings.TrimSpace(strings.TrimPrefix(body, "\ufeff"))
	if body == "" {
		return nil, nil
	}
	if strings.HasPrefix(body, "[") {
		var entries []blacklistEntry
		if err := json.Unmarshal([]byte(body), &entries); err != nil {
			return nil, err
		}
		return normalizeBlacklist(entries), nil
	}
	var f blacklistFile
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		return nil, err
	}
	return normalizeBlacklist(f.Entries), nil
}

// normalizeSha 归一化包指纹：去掉全部空白、转小写、校验 64 位 hex。
// 非法（长度不对 / 含非 hex 字符）返回 ""，由调用方决定是丢弃条目还是当作
// 「不按指纹」——两者语义不同，不能在这里混淆。
func normalizeSha(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	if len(s) != 64 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return s
}

// normalizeBlacklistNotice 归一化展示说明：去首尾空白；超过上限按 **rune**
// 截断到上限并记一条告警——`notice` 只是文案，写超长不丢条目不废清单。
// 告警只回显开头 20 字，避免把整段正文灌进日志。
func normalizeBlacklistNotice(s, app, source string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= blacklistNoticeMaxRunes {
		return s
	}
	log.Printf("[blacklist] notice 超长（%d 字 > %d 字），已截断展示（条目保留 app=%q source=%q 开头=%q）",
		len(r), blacklistNoticeMaxRunes, app, source, string(r[:20]))
	return string(r[:blacklistNoticeMaxRunes])
}

// normalizeBlacklist 归一化：去空白、丢无效条目、去重（app+source+sha256 键）、
// 预算 Until 到期时刻（日期型 = 当天 24:00 前有效）。
func normalizeBlacklist(in []blacklistEntry) []blacklistEntry {
	out := make([]blacklistEntry, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, e := range in {
		e.App = strings.TrimSpace(e.App)
		e.Source = strings.TrimSpace(e.Source)
		e.Reason = strings.TrimSpace(e.Reason)
		e.Until = strings.TrimSpace(e.Until)
		e.Notice = normalizeBlacklistNotice(e.Notice, e.App, e.Source)
		// 指纹：写错一个字符只丢这一条（记告警），不能让整份清单失效——
		// 清单是「下架」控制面，一条坏数据把整份清单作废 = 全部放行。
		if raw := e.Sha256; strings.TrimSpace(raw) != "" {
			if n := normalizeSha(raw); n != "" {
				e.Sha256 = n
			} else {
				log.Printf("[blacklist] 丢弃指纹非法的条目（app=%q source=%q sha256=%q 不是 64 位 hex）", e.App, e.Source, raw)
				continue
			}
		} else {
			e.Sha256 = ""
		}
		// app 与 sha 至少有一个：都空既不能按名字也不能按指纹拦，纯噪音。
		if e.App == "" && e.Sha256 == "" {
			continue
		}
		// 通配应用名一律拒绝：`app:"*"` 会匹配所有应用，一行写错就会把整个
		// 商店都隐藏掉（源名 `*` 仍然合法 = 全源下架，别改）。要「全名按指纹」
		// 请用 sha-only 条目（app 留空）。
		if e.App == "*" {
			log.Printf("[blacklist] 拒绝通配条目 app=\"*\"（会隐藏整个商店）")
			continue
		}
		k := strings.ToLower(e.App) + "@" + strings.ToLower(e.Source) + "#" + e.Sha256
		if seen[k] {
			continue
		}
		seen[k] = true
		e.untilT = parseBlacklistUntil(e.Until)
		out = append(out, e)
		if len(out) >= blacklistMaxEntries {
			break
		}
	}
	return out
}

// parseBlacklistUntil 解析到期时间：`2006-01-02`（当天有效，到次日 00:00）
// 或 RFC3339；空/解析失败 → 零值（不过期——宁可多下架一会儿，也不因写错
// 一个日期把恶意应用提前放出来）。
func parseBlacklistUntil(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Add(24 * time.Hour)
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

// embeddedBlacklist 内嵌空清单（解析一次）。清单从未拉成功的 fail-open 值。
var (
	embeddedBlacklistOnce    sync.Once
	embeddedBlacklistEntries []blacklistEntry
)

func embeddedBlacklist() []blacklistEntry {
	embeddedBlacklistOnce.Do(func() {
		e, err := parseBlacklist(embeddedBlacklistJSON)
		if err != nil {
			log.Printf("[blacklist] 内嵌清单解析失败（按空清单处理，fail-open）: %v", err)
			return
		}
		embeddedBlacklistEntries = e
	})
	return embeddedBlacklistEntries
}

// blacklistStates 拉取结果按「候选地址串」隔离（生产只有一份；单测里每个
// httptest 源天然独立，不会互相串味）。不放进 Server 结构体是为了让状态与
// 配置地址同生命周期：改了 list_base_url 就是另一份缓存。
type blacklistState struct {
	mu         sync.Mutex
	entries    []blacklistEntry
	loaded     bool // 是否成功拉到过（决定 last-known-good 是否有效）
	at         time.Time
	refreshing bool
	triedLKG   bool // 本次进程内是否已尝试读磁盘快照（只试一次）
}

var (
	blacklistStatesMu sync.Mutex
	blacklistStates   = map[string]*blacklistState{}
)

// blacklistLegacyURL 根目录兼容副本地址。做成变量只为单测能把它指到本地
// httptest（否则用例会真的去打 raw.githubusercontent.com）；生产恒等于
// config.DefaultBlacklistURL，行为与 mirrors / 推荐清单完全一致。
//
// 读写一律走 blacklistLegacy()/setBlacklistLegacyURL()：后台刷新 goroutine 与
// 测试 Cleanup 曾并发读写裸变量，`go test -race` 会报 DATA RACE。
var (
	blacklistLegacyMu  sync.RWMutex
	blacklistLegacyURL = config.DefaultBlacklistURL
)

func blacklistLegacy() string {
	blacklistLegacyMu.RLock()
	defer blacklistLegacyMu.RUnlock()
	return blacklistLegacyURL
}

// setBlacklistLegacyURL 仅供单测指向本地 httptest；生产不调用。
func setBlacklistLegacyURL(u string) {
	blacklistLegacyMu.Lock()
	blacklistLegacyURL = u
	blacklistLegacyMu.Unlock()
}

// blacklistURLs 生效的清单地址候选：lists/ 新路径优先，根目录兼容副本兜底
// （与 mirrors / 推荐清单同款，0.6.512 清单目录化口径）。
func (s *Server) blacklistURLs() []string {
	base := ""
	if s.cfg() != nil {
		base = s.cfg().ListBaseURL
	}
	return config.ListFileURLs(base, "blacklist.json", blacklistLegacy())
}

// blacklistState 取（或建）本 Server 生效地址对应的缓存状态。
func (s *Server) blacklistState() *blacklistState {
	return s.blacklistStateFor(s.blacklistURLs())
}

// ── last-known-good 落盘（P1，2026-11-22）──────────────────────────────
//
// 背景：清单只在内存里，进程重启（应用升级/崩溃/面板重载）后必须重新拉取；
// 若此刻断网/镜像不可用，从未成功过 → fail-open 空清单，下架控制面整体失效。
// 这里把「最近一次验签通过」的清单写到 <data>/blacklist_last.json（0600，
// tmp+rename），启动后第一次用到黑名单时读回，作为 last-known-good 使用；
// 读回值一律视为**过期**（会立刻触发后台刷新），只用于兜底不用于长期生效。
const blacklistLKGFilename = "blacklist_last.json"

type blacklistLKGSnapshot struct {
	SavedAt time.Time        `json:"saved_at"`
	Entries []blacklistEntry `json:"entries"`
}

// blacklistLKGFile 生效的落盘路径（Server.BlacklistLKGPath 仅供单测注入）。
func (s *Server) blacklistLKGFile() string {
	if s != nil && strings.TrimSpace(s.BlacklistLKGPath) != "" {
		return s.BlacklistLKGPath
	}
	return filepath.Join(config.DataDir(), blacklistLKGFilename)
}

// saveBlacklistLKG 原子写 0600；失败只记日志（持久化是增强，不影响主流程）。
func (s *Server) saveBlacklistLKG(entries []blacklistEntry) {
	path := s.blacklistLKGFile()
	if path == "" {
		return
	}
	if entries == nil {
		entries = []blacklistEntry{}
	}
	b, err := json.Marshal(blacklistLKGSnapshot{SavedAt: time.Now(), Entries: entries})
	if err != nil {
		log.Printf("[blacklist] 快照序列化失败: %v", err)
		return
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("[blacklist] 快照目录创建失败: %v", err)
			return
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[blacklist] 快照写入失败: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[blacklist] 快照替换失败: %v", err)
	}
}

// loadBlacklistLKG 读磁盘快照；损坏/缺失/空条目一律视为无快照（fail-open）。
func (s *Server) loadBlacklistLKG() ([]blacklistEntry, time.Time, bool) {
	path := s.blacklistLKGFile()
	if path == "" {
		return nil, time.Time{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[blacklist] 快照读取失败（按无快照处理）: %v", err)
		}
		return nil, time.Time{}, false
	}
	var snap blacklistLKGSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		log.Printf("[blacklist] 快照损坏（按无快照处理）: %v", err)
		return nil, time.Time{}, false
	}
	entries := normalizeBlacklist(snap.Entries)
	if len(entries) == 0 {
		return nil, time.Time{}, false
	}
	return entries, snap.SavedAt, true
}

// blacklistStateFor 用调用方已算好的地址串取状态：后台 goroutine 必须用启动
// 那一刻的地址快照，避免运行中再次读全局配置（与 Cleanup 竞态）。
func (s *Server) blacklistStateFor(urls []string) *blacklistState {
	k := strings.Join(urls, "|")
	blacklistStatesMu.Lock()
	defer blacklistStatesMu.Unlock()
	st, ok := blacklistStates[k]
	if !ok {
		st = &blacklistState{}
		blacklistStates[k] = st
	}
	return st
}

// fetchBlacklistText 拉清单：候选地址依次试（lists/ → 根目录副本）。
func (s *Server) fetchBlacklistText(ctx context.Context) (string, error) {
	return s.fetchBlacklistTextFor(ctx, s.blacklistURLs())
}

// fetchBlacklistTextFor 用给定候选地址拉清单（后台 goroutine 用启动快照）。
func (s *Server) fetchBlacklistTextFor(ctx context.Context, urls []string) (string, error) {
	var lastErr error
	for _, listURL := range urls {
		body, err := s.fetchBlacklistOne(ctx, listURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

// blacklistVerifyKey 黑名单清单的验签公钥（与自更新共用同一把 minisign 公钥）。
// 做成变量只为单测能换成临时生成的密钥对；生产恒为内嵌公钥。
// 后台刷新 goroutine 会跨用例存活，读写必须走锁（-race 实测竞态）。
var (
	blacklistVerifyKeyMu sync.RWMutex
	blacklistVerifyKey   = selfUpdatePubkey
)

// blacklistPubkey 读当前验签公钥（锁内拷贝，避免与单测替换并发）。
func blacklistPubkey() minisign.PublicKey {
	blacklistVerifyKeyMu.RLock()
	defer blacklistVerifyKeyMu.RUnlock()
	return blacklistVerifyKey
}

// verifyBlacklistSignature 校验清单的 minisign 分离签名。
// 黑名单是「下架」控制面：清单经第三方镜像拉取，不验签时镜像可删条目
// （绕过下架）或加条目（隐藏合法应用）。签名缺失/无效 = 该候选失败，
// 换下一个候选；全部失败沿用 last-known-good（从未成功过仍 fail-open）。
func verifyBlacklistSignature(msg, sig []byte) error {
	if len(bytes.TrimSpace(sig)) == 0 {
		return errors.New("缺少 minisign 签名（.minisig）")
	}
	if !minisign.Verify(blacklistPubkey(), msg, sig) {
		return errors.New("minisign 验签失败（清单可能被篡改）")
	}
	return nil
}

// blacklistCandidateAllowed 校验候选地址是否是公网可达目标。
// SourceListAllowPrivate=true（单测/内网自建）时放行；生产一律过 netguard。
func blacklistCandidateAllowed(cfg *config.Config, candidate string) error {
	if cfg != nil && cfg.SourceListAllowPrivate {
		return nil
	}
	host, err := netguard.Public(candidate)
	if err != nil {
		return fmt.Errorf("黑名单地址被安全策略拒绝（%s）: %w", host, err)
	}
	return nil
}

// fetchBlacklistOne 拉单个地址：镜像优先（GitHub 地址时），直连兜底；
// 与推荐清单同口径过 netguard（地址是管理员可配的，防被指向内网端点）。
func (s *Server) fetchBlacklistOne(ctx context.Context, listURL string) (string, error) {
	if err := blacklistCandidateAllowed(s.cfg(), listURL); err != nil {
		return "", err
	}
	client := &http.Client{}
	var lastErr error
	for _, c := range recoListCandidates(listURL, s.Mirrors) {
		// 镜像前缀候选同样要过安全策略：镜像清单本身无签名，被篡改时不能
		// 借它把请求打到内网（P2，2026-11-22）。
		if err := blacklistCandidateAllowed(s.cfg(), c); err != nil {
			lastErr = err
			continue
		}
		// 正文与签名必须来自**同一个候选地址**：不同镜像可能代理到不同副本，
		// 交叉取会把 A 的正文配 B 的签名，验签必然失败（也会掩盖真问题）。
		body, err := fetchBlacklistURL(ctx, client, c)
		if err != nil {
			lastErr = err
			continue
		}
		sig, err := fetchBlacklistURL(ctx, client, c+".minisig")
		if err != nil {
			lastErr = fmt.Errorf("签名获取失败（%s）: %w", c, err)
			continue
		}
		if err := verifyBlacklistSignature(body, sig); err != nil {
			lastErr = fmt.Errorf("%s: %w", c, err)
			continue
		}
		return string(body), nil
	}
	return "", fmt.Errorf("黑名单获取失败: %w", lastErr)
}

// fetchBlacklistURL 拉单个候选地址（正文或 .minisig），带超时与大小上限。
func fetchBlacklistURL(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, blacklistCandidateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moo/0.6")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, blacklistMaxBytes))
	if rerr != nil {
		return nil, rerr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	if len(body) == 0 {
		return nil, errors.New("空响应")
	}
	return body, nil
}

// blacklistEntries 当前生效清单（**请求路径绝不长时间阻塞**）：
//   - TTL 内 → 直接回缓存（含空清单）；
//   - 有成功过的旧值且已过期 → 立刻回旧值，后台单飞刷新（last-known-good）；
//   - 从未成功过 → 回内嵌空清单（fail-open），退避窗口内不反复打网络。
func (s *Server) blacklistEntries() []blacklistEntry {
	st := s.blacklistState()
	st.mu.Lock()
	fresh := st.loaded && time.Since(st.at) < blacklistTTL
	entries, loaded, at, tried := st.entries, st.loaded, st.at, st.triedLKG
	st.mu.Unlock()
	if fresh {
		return entries
	}
	if !loaded && !tried {
		// 进程重启/断网兜底：先试磁盘上的 last-known-good（验签过的旧清单）。
		// 读回值一律当过期 → 下面仍会触发后台刷新，只用于「不空窗」。
		lkg, savedAt, ok := s.loadBlacklistLKG()
		st.mu.Lock()
		if ok && !st.loaded {
			st.entries, st.loaded, st.at = lkg, true, savedAt
			loaded, entries, at = true, lkg, savedAt
		}
		st.triedLKG = true
		st.mu.Unlock()
		if !ok {
			log.Printf("[blacklist] 无本地快照（fail-open 空清单，后台重试）")
		}
	}
	if !loaded {
		// 失败退避：刚拉失败过就别每个请求都再打一轮网络。
		if at.IsZero() || time.Since(at) >= blacklistFallbackTTL {
			s.startBlacklistRefresh()
		}
		return embeddedBlacklist()
	}
	s.startBlacklistRefresh()
	return entries
}

// refreshBlacklist 同步拉取并落缓存（单测与显式刷新用）。
// 返回 (生效清单, 内容是否变化, 错误)。失败不动 last-known-good；
// 从未成功过则保持空清单（fail-open）。
func (s *Server) refreshBlacklist(ctx context.Context) ([]blacklistEntry, bool, error) {
	urls := s.blacklistURLs()
	return s.refreshBlacklistInto(ctx, s.blacklistStateFor(urls), urls)
}

// refreshBlacklistInto 用调用方给的状态与地址快照完成一次刷新。
// 后台 goroutine 必须走这里：期内在不读全局配置，避免与测试 Cleanup /
// 设置变更形成数据竞争。
func (s *Server) refreshBlacklistInto(ctx context.Context, st *blacklistState, urls []string) ([]blacklistEntry, bool, error) {
	entries, err := s.fetchAndParseBlacklistFor(ctx, urls)
	now := time.Now()
	if err != nil {
		st.mu.Lock()
		// 记一次失败时刻：loaded 时让 TTL 提前 blacklistFallbackTTL 到期（稍后重试）；
		// 未 loaded 时从「现在」起算退避窗口——P1-3（2026-11-22）：此前无条件写成
		// now-(TTL-fallback)，而 !loaded 分支判据是 since>=fallback（恒真），
		// 退避窗口实际不存在，目录热路径每个请求都会再起一条拉取链。
		if st.loaded {
			st.at = now.Add(-(blacklistTTL - blacklistFallbackTTL))
		} else {
			st.at = now
		}
		if !st.loaded {
			st.entries = embeddedBlacklist()
		}
		cur := st.entries
		st.mu.Unlock()
		log.Printf("[blacklist] 拉取失败（沿用 last-known-good / 空清单 fail-open）: %v", err)
		return cur, false, err
	}
	st.mu.Lock()
	old, wasLoaded := st.entries, st.loaded
	st.entries, st.loaded, st.at = entries, true, now
	st.mu.Unlock()
	changed := !wasLoaded || !reflect.DeepEqual(old, entries)
	if changed {
		// 清单变了（含首次加载）：目录/响应体缓存立即作废，不当场返回旧卡。
		s.invalidateCatalog()
		// 只把「验签通过且已生效」的清单落盘：重启/断网后仍有 last-known-good。
		s.saveBlacklistLKG(entries)
	}
	return entries, changed, nil
}

// fetchAndParseBlacklistFor 拉取 + 解析（解析失败按拉取失败处理，保留旧值）。
func (s *Server) fetchAndParseBlacklistFor(ctx context.Context, urls []string) ([]blacklistEntry, error) {
	body, err := s.fetchBlacklistTextFor(ctx, urls)
	if err != nil {
		return nil, err
	}
	entries, perr := parseBlacklist(body)
	if perr != nil {
		return nil, fmt.Errorf("黑名单解析失败: %w", perr)
	}
	return entries, nil
}

// startBlacklistRefresh 单飞后台刷新（同一时刻最多一条拉取链）。
func (s *Server) startBlacklistRefresh() {
	// 地址 + 状态在启动瞬间定死：goroutine 运行期间不再读全局配置，
	// 避免与「设置变更 / 单测 Cleanup 还原 blacklistLegacyURL」竞态。
	urls := s.blacklistURLs()
	st := s.blacklistStateFor(urls)
	st.mu.Lock()
	if st.refreshing {
		st.mu.Unlock()
		return
	}
	st.refreshing = true
	st.mu.Unlock()
	go func() {
		defer func() {
			st.mu.Lock()
			st.refreshing = false
			st.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), blacklistRefreshBudget)
		defer cancel()
		_, _, _ = s.refreshBlacklistInto(ctx, st, urls)
	}()
}

// 文案（2026-11-22 用户定稿，不得改写）：
//   - 动作拦截：恶意类用恶意版，其余一律社区规范版；
//   - 详情 404：`该应用已被停止分发（原因：<原因文案>）`。
const (
	blacklistMsgCommunity = "该应用违反社区规范，已被停止分发，无法下载"
	blacklistMsgMalware   = "该应用存在恶意行为，已被停止分发，无法下载"
)

// blacklistReasonText 原因码 → 可读文案。
var blacklistReasonText = map[string]string{
	"malware":   "检测到恶意行为（窃取数据/挖矿/后门）",
	"fraud":     "涉嫌欺诈或误导",
	"copyright": "侵权投诉",
	"security":  "存在高危安全漏洞",
	"policy":    "违反社区规范",
	"author":    "应作者要求下架",
}

// blacklistReasonLabel 原因码 → 文案；未知码原样回显（不吞信息，也不瞎编）。
func blacklistReasonLabel(code string) string {
	c := strings.ToLower(strings.TrimSpace(code))
	if t, ok := blacklistReasonText[c]; ok {
		return t
	}
	if c == "" {
		return blacklistReasonText["policy"]
	}
	return c
}

// blacklistReasonShort 原因码 → 卡面短句（DESIGN 2026-11-22 §2 定稿文案，
// ≤10 字，**勿改**）。空码 / 未知码一律回落「已被停止分发」——不瞎编、
// 也不回显原因码。仅供展示（前端浮层标题、日志）；JSON 不下发短句，
// 前端按 `blacklist_reason` 自行映射。
var blacklistReasonShortText = map[string]string{
	"malware":   "检测到恶意行为",
	"fraud":     "涉嫌欺诈或误导",
	"copyright": "侵权投诉",
	"security":  "存在高危漏洞",
	"policy":    "违反社区规范",
	"author":    "应作者要求下架",
}

// blacklistReasonShort 见上表；未知/空 → 「已被停止分发」。
func blacklistReasonShort(code string) string {
	if t, ok := blacklistReasonShortText[strings.ToLower(strings.TrimSpace(code))]; ok {
		return t
	}
	return "已被停止分发"
}

// active 条目当前是否生效（Until 到期即失效）。
func (e blacklistEntry) active(now time.Time) bool {
	return e.untilT.IsZero() || now.Before(e.untilT)
}

// matches 条目是否命中某张卡（**AND** 语义）：
//   - 名称：条目 app 空 = 不约束（sha-only 条目）；否则大小写不敏感相等；
//   - 源：空 或 `*` = 全源；否则大小写不敏感相等；
//   - 指纹：条目 sha256 空 = 不约束；否则**要求调用方 sha 已知且相等**。
//
// 指纹「未知不命中」是刻意的：sha 是版本级标识，拿不到就无从判断这批包
// 是不是被下架的那批；按旧指纹硬拦会误伤刚发的新版本（用户装不上修复版）。
func (e blacklistEntry) matches(appName, source, sha string) bool {
	if !e.matchNameSha(appName, sha) {
		return false
	}
	if e.Source != "" && e.Source != "*" && !strings.EqualFold(e.Source, strings.TrimSpace(source)) {
		return false
	}
	return true
}

// matchNameSha 名称 + 指纹判定（**忽略源名**）：目录/详情/动作闸用 matches
// （再叠源名），本地包闸与手动更新队列拿不到源名，直接用它。抽出来是为了让
// 「指纹未知不命中」只有一处实现——两处各写一遍迟早走偏。
func (e blacklistEntry) matchNameSha(appName, sha string) bool {
	if e.App != "" && !strings.EqualFold(e.App, strings.TrimSpace(appName)) {
		return false
	}
	if e.Sha256 != "" {
		caller := normalizeSha(sha)
		if caller == "" || caller != e.Sha256 {
			return false
		}
	}
	return true
}

// blacklistHitIn 纯函数匹配（单测直接用）。sha 未知传 ""。
func blacklistHitIn(entries []blacklistEntry, appName, source, sha string, now time.Time) (blacklistEntry, bool) {
	for _, e := range entries {
		if e.active(now) && e.matches(appName, source, sha) {
			return e, true
		}
	}
	return blacklistEntry{}, false
}

// blacklistEntrySpecificity 条目「具体度」（DESIGN §2 的最具体优先顺序）：
//
//	4 = 指纹 + 源     3 = 指纹
//	2 = 应用 + 源     1 = 应用
//
// 同一张卡命中多条时用它挑一条展示：防止泛化的「违反社区规范」盖掉
// 「检测到恶意行为」。**只影响展示**——命中判定仍是「命中任一条即命中」。
func blacklistEntrySpecificity(e blacklistEntry) int {
	hasSHA := e.Sha256 != ""
	hasApp := strings.TrimSpace(e.App) != ""
	hasSrc := e.Source != "" && e.Source != "*"
	switch {
	case hasSHA && hasSrc:
		return 4
	case hasSHA:
		return 3
	case hasApp && hasSrc:
		return 2
	default:
		return 1
	}
}

// blacklistHitBest 在所有生效命中里取**最具体**的一条（同级取清单靠前的一条）。
// 判定语义与 blacklistHitIn 完全一致，只多一条「挑哪条展示」的规则。
func blacklistHitBest(entries []blacklistEntry, appName, source, sha string, now time.Time) (blacklistEntry, bool) {
	var best blacklistEntry
	bestScore, found := 0, false
	for _, e := range entries {
		if !e.active(now) || !e.matches(appName, source, sha) {
			continue
		}
		if sc := blacklistEntrySpecificity(e); !found || sc > bestScore {
			best, bestScore, found = e, sc, true
		}
	}
	return best, found
}

// blacklistHit 命中探测（当前生效清单）。sha 未知传 ""（指纹条目不命中）。
func (s *Server) blacklistHit(appName, source, sha string) (blacklistEntry, bool) {
	entries := s.blacklistEntries()
	if len(entries) == 0 {
		return blacklistEntry{}, false
	}
	return blacklistHitIn(entries, appName, source, sha, time.Now())
}

// isBlacklisted 唯一的「是否下架」判定入口（四道闸全部经此）。测试会把它
// 临时改成恒 false 做反向验证——所以**不要**在闸里绕开它。
func (s *Server) isBlacklisted(appName, source, sha string) bool {
	_, ok := s.blacklistHit(appName, source, sha)
	return ok
}

// blacklistHitByKey 按目录 key（`appname` 或 `appname@源名`）探测：先按源
// 元数据还原真实源名（key 可能不带源名），还原不到才退回按 key 拆。
// 解析成功时**顺手**取同一次 resolve 的 a.Sha256：详情闸与目录闸必须用
// 同一个包指纹判定口径，否则「列表里没了、详情还 200」或反之。
func (s *Server) blacklistHitByKey(key string) (blacklistEntry, bool) {
	app, src, sha := splitCatalogKey(key)
	if s.Src != nil {
		if srcName, a, err := s.resolveKey(key); err == nil && a != nil {
			app, src, sha = a.Name, srcName, a.Sha256
		}
	}
	return s.blacklistHit(app, src, sha)
}

// splitCatalogKey 拆 `appname@源名`（key 只有一份时无 @）。第三个返回值恒为
// ""：key 本身不带包指纹，sha 只能靠 resolveKey 拿。
func splitCatalogKey(key string) (string, string, string) {
	if i := strings.LastIndex(key, "@"); i > 0 {
		return key[:i], key[i+1:], ""
	}
	return key, "", ""
}

// blacklistBlockMessage 动作拦截文案（malware 用恶意版，其余社区规范版）。
func (s *Server) blacklistBlockMessage(appName, source, sha string) string {
	if e, ok := s.blacklistHit(appName, source, sha); ok && strings.EqualFold(e.Reason, "malware") {
		return blacklistMsgMalware
	}
	return blacklistMsgCommunity
}

// blacklistHitByAppName 按应用名（+ 可选包指纹）的命中探测，**忽略源名**。
// 本地 FPK 包（下载中心 / 本地上传 / KSpeeder）与手动更新队列拿不到源名，
// 宁可多拦：只要该应用在任一源被下架，就不允许从本地包装进来。
//
// 指纹条目同样遵守「sha 未知不命中」：本地包能实算指纹就一定传（见
// installFpkFileSSE），算不出来时只让 app-only 条目生效，绝不按旧指纹误伤。
func (s *Server) blacklistHitByAppName(appName, sha string) (blacklistEntry, bool) {
	name := strings.TrimSpace(appName)
	want := normalizeSha(sha)
	if name == "" && want == "" {
		return blacklistEntry{}, false
	}
	entries := s.blacklistEntries()
	if len(entries) == 0 {
		return blacklistEntry{}, false
	}
	now := time.Now()
	for _, e := range entries {
		if !e.active(now) {
			continue
		}
		// 与 matches 共用同一套名称/指纹规则（源名忽略）：sha-only 条目天然
		// 「任意名字命中」，app 空且无指纹的条目在归一化阶段已被丢弃。
		if e.matchNameSha(name, want) {
			return e, true
		}
	}
	return blacklistEntry{}, false
}

// blacklistLocalInstallBlock 本地包安装闸的判定：命中返回 (文案, true)。
// 文案映射与动作拦截一致（malware 用恶意版，其余社区规范版）。
func (s *Server) blacklistLocalInstallBlock(appName, sha string) (string, bool) {
	e, ok := s.blacklistHitByAppName(appName, sha)
	if !ok {
		return "", false
	}
	if strings.EqualFold(e.Reason, "malware") {
		return blacklistMsgMalware, true
	}
	return blacklistMsgCommunity, true
}

// blacklistDetailMessageByKey 详情 404 文案（按目录 key 还原真实源名后带原因）。
func (s *Server) blacklistDetailMessageByKey(key string) string {
	e, _ := s.blacklistHitByKey(key)
	return "该应用已被停止分发（原因：" + blacklistReasonLabel(e.Reason) + "）"
}

// blacklistGuardSSE 动作拦截闸（安装/暂存/更新/后台下载共用）：命中即写
// 一条 SSE `step=error` 事件并返回 true（调用方直接 return）。用 SSE 而不是
// 裸 HTTP 500——前端各动作面板都按 step 事件渲染错误，裸状态码只会显示
// 「请求失败」，看不到下架原因。
func (s *Server) blacklistGuardSSE(w http.ResponseWriter, appName, source, sha string) bool {
	if !s.isBlacklisted(appName, source, sha) {
		return false
	}
	f := sseStart(w)
	sseSend(w, f, map[string]any{"step": "error", "error": s.blacklistBlockMessage(appName, source, sha)})
	return true
}

// annotateBlacklisted 目录打标（DESIGN 2026-11-22 定稿，取代旧 filterBlacklisted）：
// 命中清单的卡**全部保留**（已装 + 未装都不再从列表删除——「已下架」只表示
// 停止分发，不是已卸载；Moo 不做远程卸载，已装副本仍可打开/自行卸载，
// 未装卡保留以便用户点开看清原因），就地写入：
//
//	Blacklisted / BlacklistReason / BlacklistNotice / BlacklistSince
//
// 同时清掉命中卡的「有更新」类标记（HasUpdate / UpdateIgnored /
// UpdateIgnoredPending）：下架卡不能更新，留着角标只会把用户引到必被动作闸
// 拦截的按钮上。同源其它应用原样返回；空清单（含 fail-open）直接返回入参。
func (s *Server) annotateBlacklisted(in []AppInfo) []AppInfo {
	entries := s.blacklistEntries()
	if len(entries) == 0 {
		return in // 空清单（含 fail-open）：原样返回，连副本都不做
	}
	now := time.Now()
	out := in
	copied := false
	for i := range in {
		// 判定入口只有 matches（经 blacklistHitBest）：加规则/反向验证都只改一处。
		// 名称**只认 AppName**：DisplayName 是展示名（可被源随意填，甚至与
		// 别的应用同名），拿它判定会把无辜应用连坐下架。
		e, hit := blacklistHitBest(entries, in[i].AppName, in[i].Source, in[i].Sha256, now)
		if !hit {
			continue
		}
		if !copied {
			// 延迟拷贝：只有真命中才付这份拷贝，无命中时零分配。
			out = make([]AppInfo, len(in))
			copy(out, in)
			copied = true
		}
		out[i].Blacklisted = true
		out[i].BlacklistReason = strings.ToLower(strings.TrimSpace(e.Reason))
		out[i].BlacklistNotice = e.Notice
		out[i].BlacklistSince = strings.TrimSpace(e.Since)
		out[i].HasUpdate = false
		out[i].UpdateIgnored = false
		out[i].UpdateIgnoredPending = false
	}
	return out
}
