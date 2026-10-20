package api

import (
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/lang"
	"moo/internal/netguard"
	"moo/internal/netx"
	"moo/internal/notify"
	"moo/internal/operation"
	"moo/internal/platform"
	"moo/internal/reco"
	"moo/internal/source"
	"moo/internal/task"
)

// ---- 应用目录 ----

// appsWithEtag GET /api/apps：列表载荷 + ETag 重校验。
// 对标 FnDepot「首开从本地库秒开」的 HTTP 层等价物：目录未变时客户端
// （fn connect / WebView 的 HTTP 缓存）带 If-None-Match 重校验→304 空响应，
// 二次打开从「重传 1.5MB」变成「一次小头往返」。
// 0.6.313 B4：无 source 参数的全量响应（前端唯一热路径）走 body 缓存——
// marshal 字节 + ETag 重建时各算一次；有 source 参数的罕见路径照旧现算。
func (s *Server) appsWithEtag(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("source") == "" {
		if body, etag, ok := s.cachedAppsBody(r); ok {
			s.writeAppsBody(w, r, body, etag)
			return
		}
	}
	body, err := json.Marshal(s.appsResponse(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	etag := `"` + fmt.Sprintf("%x", md5.Sum(body)) + `"`
	s.writeAppsBody(w, r, body, etag)
}

// writeAppsBody 统一的列表响应写出（ETag 重校验→304 / 否则 200 + body）。
func (s *Server) writeAppsBody(w http.ResponseWriter, r *http.Request, body []byte, etag string) {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache") // 强制重校验：新鲜不牺牲
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

// cachedAppsBody 返回 /api/apps 全量响应（无 source 参数）的缓存字节 +
// ETag；ok=false 表示未命中（刚失效 / 首次 / marshal 失败）。未命中时
// 现建：复用 appsResponse（内部走 cachedCatalog，SWR + 失效钩子保证构建
// 频率受控），marshal 与 md5 各算一次后入缓存返回。缓存字节多请求
// 只读共享（writeAppsBody 永不修改它）。
//
// 0.6.326z5 R1（体缓存必须与目录同代）：marshal 在**锁外**完成，落盘前
// 在 catalogBodyMu 下复核代际号 catalogBodyGen 未变——marshal 期间目录被
// invalidateCatalog / 后台重建换入新切片（两者都会 ++ 代际并清 body）时，
// 本次结果已属旧代，**只返回不落盘**（落盘的旧代体会长期驻留：旧实现
// 里没有任何后续事件会清掉它，症状=源启用瞬间并发请求把「不含该源卡片」
// 的响应体钉死 >150s）。持锁时间只覆盖取/存字节，不覆盖 marshal。
func (s *Server) cachedAppsBody(r *http.Request) ([]byte, string, bool) {
	s.catalogBodyMu.Lock()
	if s.catalogBody != nil {
		body, etag := s.catalogBody, s.catalogBodyETag
		s.catalogBodyMu.Unlock() // 字节切片只读共享，返回后无需持锁
		return body, etag, true
	}
	gen := s.catalogBodyGen
	s.catalogBodyMu.Unlock()

	body, err := json.Marshal(s.appsResponse(r))
	if err != nil {
		return nil, "", false
	}
	etag := `"` + fmt.Sprintf("%x", md5.Sum(body)) + `"`

	if s.appsBodyHook != nil {
		s.appsBodyHook() // 单测缝（生产恒 nil）：落盘前的交错点
	}

	s.catalogBodyMu.Lock()
	// 代际复核 + 首写者优先：代际已变 ⇒ 本次结果属旧代，不落盘（只影响
	// 这一个请求，下次请求即新代）；body 已被并发同代请求填充 ⇒ 不覆盖
	//（保持「首个未命中者写入」的旧语义）。
	if s.catalogBodyGen == gen && s.catalogBody == nil {
		s.catalogBody = body
		s.catalogBodyETag = etag
	}
	s.catalogBodyMu.Unlock()
	return body, etag, true
}

// etagMatch 匹配 If-None-Match（兼容 W/ 弱标记与多值列表）。
func etagMatch(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" || ifNoneMatch == "*" {
		return ifNoneMatch == "*"
	}
	strip := func(s string) string {
		s = strings.TrimSpace(s)
		return strings.TrimSuffix(strings.TrimPrefix(s, "W/"), "")
	}
	for _, part := range strings.Split(ifNoneMatch, ",") {
		if strip(part) == strip(etag) {
			return true
		}
	}
	return false
}

// appsResponse 构建 GET /api/apps 响应（?source= 按源过滤）。
// 列表载荷瘦身（New Store 同款）：changelog/发布页/SHA256/更新记录等
// 仅详情页用的字段不随列表下发——详情对话框打开时按 key 重拉全量
// /api/apps/{key}（LAN 内毫秒级），接口未回前以列表条目兜底渲染。
func (s *Server) appsResponse(r *http.Request) AppsResponse {
	catalog := s.cachedCatalogCopy(lang.From(r.Context())) // 后续会剥离瘦身字段，必须用副本
	src := r.URL.Query().Get("source")
	if src != "" {
		filtered := catalog[:0]
		for _, a := range catalog {
			if a.Source == src {
				filtered = append(filtered, a)
			}
		}
		catalog = filtered
	} else {
		// 去重展示层（0.6.312 B2）：无源过滤时隐藏非代表卡（按源浏览=
		// 显式上下文，不隐藏；详情按 key 永远可达=逃生门）。
		filtered := catalog[:0]
		for _, a := range catalog {
			if !a.Hidden {
				filtered = append(filtered, a)
			}
		}
		catalog = filtered
	}
	if catalog == nil {
		catalog = []AppInfo{}
	}
	// 列表瘦身：剥离仅详情页消费的字段（1.5MB→约 600KB），
	// 详情对话框打开时 fetchAppDetail(key) 拉全量补齐。
	for i := range catalog {
		a := &catalog[i]
		a.ChangelogEntries = nil
		a.Changelog = ""
		a.ReleaseNotes = ""
		a.Homepage = ""
		a.Sha256 = ""
		a.ReleaseURL = ""
		a.SameName = nil // 候选表仅详情下发（列表留 SameNameCount 角标）
	}
	return AppsResponse{Apps: catalog, LastCheck: s.lastCheckStamp(), UpgradeAllowed: true}
}

// lastCheckStamp 最近一次源同步时间（各源 UpdatedAt 的最大值）。
// 兼作图标集版本戳：源目录变化 ⇒ 应用图标可能变化。
func (s *Server) lastCheckStamp() string {
	last := ""
	for _, st := range s.Src.Sources() {
		if st.UpdatedAt.After(time.Time{}) {
			if t := st.UpdatedAt.Format(time.RFC3339); t > last {
				last = t
			}
		}
	}
	return last
}

// appDetail GET /api/apps/{key}。
func (s *Server) appDetail(w http.ResponseWriter, r *http.Request) {
	// 目录缓存命中（读-only）——详情点击不再触发全量重建（ListInstalled
	// RPC + 1700+ 条目合并），打开详情对话框的骨架屏闪烁由此消除。
	catalog := s.cachedCatalog(lang.From(r.Context()))
	foundIdx := -1
	for i := range catalog {
		if catalog[i].Key == r.PathValue("key") {
			foundIdx = i
			break
		}
	}
	if foundIdx < 0 {
		// 0.6.314 C：固定推荐快照回落——源被删/抓取失败后，三个固定推荐
		// 应用（fnos-apps-store / fndepot / fn-knock）的详情页保持完整可达：
		// 返回与实时条目字段形态一致的快照条目（前端零改动渲染）。key 匹配
		// 取 appname 段（大小写不敏感），兼容深链 hash 携带的旧 @源名 形态。
		if snap := snapshotForKey(r.PathValue("key")); snap != nil {
			a := snapshotAppInfo(snap)
			a.Key = r.PathValue("key") // 回显请求 key（图标/资源端点按同 key 取）
			writeJSON(w, a)
			return
		}
		writeErr(w, http.StatusNotFound, errors.New("应用不存在"))
		return
	}
	a := catalog[foundIdx] // 浅拷贝——组信息/冲突字段不落共享目录元素
	// 0.6.312 B3/F8（源缓存两级）：changelog 全文/releases 明细不再常驻
	// 目录内存，详情打开时按「源+应用+版本」从详情磁盘层懒载（readme_store
	// 同款：内存命中零开销、磁盘命中首载几十 ms，用户实测无感）。
	if p, ok := s.detailStore().Get(detailKey(a.Source, a.AppName, a.LatestVersion)); ok {
		if p.Cl != "" {
			a.Changelog = p.Cl
			a.ReleaseNotes = p.Cl
		}
		if len(p.Entries) > 0 {
			a.ChangelogEntries = p.Entries
		}
	}
	// 过渡期回落：升级后尚未重新同步的源，changelog 仍在内存（旧快照
	// 恢复的 App 未剥离）而磁盘层暂无条目 → 现解析一次补齐（F7② 把解析
	// 移出重建热路径后，这里是唯一的非预解析入口）。
	if len(a.ChangelogEntries) == 0 && a.Changelog != "" {
		a.ChangelogEntries = parseChangelogEntries(a.Changelog, a.LatestVersion, nil)
	}
	// 0.6.312 B2：候选表 + 安装冲突对组内任意卡现算（含被去重隐藏的卡——
	// 显式引用/候选表跳转打开的正是它们）。
	if entries := buildSameNameGroup(catalog, a.Key); len(entries) > 1 {
		a.SameName = entries
		a.SameNameCount = len(entries)
		a.InstallConflict = installConflictOf(&a, entries)
	}
	writeJSON(w, a)
}

// buildSameNameGroup 收集目标卡的同名组（目录序，含目标卡自身）。
// IsRep = 该卡当前策略下是否在列表可见（!Hidden）。
func buildSameNameGroup(catalog []AppInfo, key string) []SameNameEntry {
	self := -1
	for i := range catalog {
		if catalog[i].Key == key {
			self = i
			break
		}
	}
	if self < 0 || catalog[self].AppName == "" {
		return nil
	}
	name := catalog[self].AppName
	entries := make([]SameNameEntry, 0, 4)
	for i := range catalog {
		c := &catalog[i]
		if c.AppName != name {
			continue
		}
		entries = append(entries, SameNameEntry{
			Key:         c.Key,
			Source:      c.Source,
			DisplayName: c.DisplayName,
			Version:     c.LatestVersion,
			Arch:        c.Arch,
			Installed:   c.Installed,
			IsRep:       !c.Hidden,
			Origin:      releaseOrigin(c.ReleaseURL),
			Sha256:      c.Sha256,
		})
	}
	return entries
}

// installConflictOf 目标卡点「安装」时的冲突分支（0.6.312 B2）：
// official=已装卡为官方平台应用（平台管理，到面板操作）；
// same=同源同版本（已装相同版本）；lineage=可验证同宗（同仓库或同版本同 sha，
// 直接就地升级保留 @appdata）；different=不同源不同构建（显式裁决）。
func installConflictOf(self *AppInfo, group []SameNameEntry) string {
	if len(group) < 2 || self.Installed {
		return ""
	}
	inst := -1
	for i := range group {
		if group[i].Installed {
			inst = i
			break
		}
	}
	if inst < 0 {
		return ""
	}
	e := group[inst]
	if e.Source == OfficialSourceID {
		return "official"
	}
	if self.Source == e.Source && self.LatestVersion != "" && self.LatestVersion == e.Version {
		return "same"
	}
	so := releaseOrigin(self.ReleaseURL)
	if (so != "" && so == e.Origin) ||
		(self.Sha256 != "" && self.Sha256 == e.Sha256 && self.LatestVersion == e.Version) {
		return "lineage"
	}
	return "different"
}

// recommended GET /api/recommended：固定前三 + 其余随机（0.6.314 C）。
//
// 前三固定位（上→下，用户定稿、不随机）：fnos-apps-store / fndepot /
// fn-knock。每位每请求按当前目录判定（不粘性）：源在目录里=实时条目；
// 源被删/抓取失败=回落内嵌完整元数据快照（internal/reco）。源恢复后
// 自动切回实时。SWR 说明：目录过期有旧时（最长 TTL 默认 60s），「源刚
// 删」最多多 60s 仍显示实时卡，下一轮重建后回落快照——可接受。
//
// 总位数 >3（?count=N，3≤N≤12）时其余位置维持既有随机逻辑（优先有
// 图标、同名只取一张、Hidden/UserHidden 卡不参与），且不得重复出现前三 key。
//
// 0.6.326z5（2.1 修复）：固定位对应的**实时卡**若被去重隐藏（Hidden）或被
// 用户禁用（UserHidden）⇒ 整位跳过且**不回落快照**（回落=照样展示，等于
// 禁用/去重对这个位置无效）；空位由下方随机补足池填补。仅「目录里根本没
// 有该应用」（fixedSlotLive 未命中）才回落到内嵌快照。
func (s *Server) recommended(w http.ResponseWriter, r *http.Request) {
	catalog := s.cachedCatalog(lang.From(r.Context()))
	want := 3
	if n, err := strconv.Atoi(r.URL.Query().Get("count")); err == nil && n > 3 && n <= 12 {
		want = n
	}
	out := make([]AppInfo, 0, want)
	used := map[string]bool{} // appname 归一（前三 + 已选随机卡），防重复
	for _, appName := range reco.FixedOrder() {
		if len(out) >= want {
			break
		}
		var ai AppInfo
		var ok bool
		if live, lOK := fixedSlotLive(catalog, appName); lOK {
			// 0.6.326z5 2.1：实时卡被隐藏/禁用 ⇒ 整位跳过，不回落快照
			//（快照回落=隐藏位照样出现，等于禁用失效）。空位由随机池补。
			if live.Hidden || live.UserHidden {
				continue
			}
			ai, ok = live, true
		} else if snap := reco.Lookup(appName); snap != nil {
			ai, ok = snapshotAppInfo(snap), true
		}
		if !ok {
			continue
		}
		out = append(out, ai)
		used[reco.NormAppName(ai.AppName)] = true
	}
	if len(out) >= want {
		writeJSON(w, map[string]any{"apps": out})
		return
	}
	// 其余位置：既有随机逻辑（优先有图标），排除前三 key（归一 appname）
	withIcon := make([]AppInfo, 0)
	rest := make([]AppInfo, 0)
	// 0.6.312 B2：同名应用只取一张（去重档未开启时也能保证发现页不推
	// 两张同应用）；Hidden 卡不参与。0.6.326-ui：被用户「禁用」的卡
	//（UserHidden）同样不参与——禁用后不得出现在发现页推荐位。
	seen := map[string]bool{}
	for _, a := range catalog {
		if a.Hidden || a.UserHidden || seen[a.AppName] {
			continue
		}
		seen[a.AppName] = true
		if used[reco.NormAppName(a.AppName)] {
			continue
		}
		if a.IconURL != "" {
			withIcon = append(withIcon, a)
		} else {
			rest = append(rest, a)
		}
	}
	need := want - len(out)
	pool := withIcon
	if len(pool) < need {
		pool = append(withIcon, rest...)
	}
	if len(pool) > need {
		pool = pickWithDailySeed(pool, need)
	}
	out = append(out, pool...)
	writeJSON(w, map[string]any{"apps": out})
}

// pickWithDailySeed 稳定随机选卡（321R7）：种子 = UTC 日期 → 同一天内
// 候选池顺序恒定（刷新/重拉目录都换不了卡），跨天才换一批。
// 旧实现 rand.Shuffle（全局自动种子）= 每次请求都重洗，用户反馈
// 「随机顺序一直变」，安装完成刷新后推荐卡全换、找不到刚装的应用。
// 注：候选池本身变化（源增删应用）仍会改变结果——属数据变化，非重洗。
func pickWithDailySeed(pool []AppInfo, need int) []AppInfo {
	day := time.Now().UTC().Format("2006-01-02")
	rng := rand.New(rand.NewSource(daySeedInt("moo-reco:" + day)))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	return pool[:need]
}

// daySeedInt 字符串 → 32 位混合种子（FNV-1a；仅用于展示随机序，非安全用途）。
func daySeedInt(s string) int64 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return int64(h)
}

// dedupSummary GET /api/dedup：当前去重策略 + 三档可见数（首页胶囊预览：
// 「默认 N / 标准 M / 去重 K」，0.6.313 文案；键 all/merge/one 不变）。计数在副本上现算（不污染目录缓存；
// 组内并查集对 ~2500 卡是亚毫秒级，只在胶囊展开/策略切换时调用）。
func (s *Server) dedupSummary(w http.ResponseWriter, r *http.Request) {
	catalog := s.cachedCatalogCopy(lang.From(r.Context()))
	probe := make([]AppInfo, len(catalog))
	copy(probe, catalog)
	merged, one := applyDedupPolicy(probe, "merge")
	writeJSON(w, map[string]any{
		"policy": orAll(s.Cfg.DedupPolicy),
		"total":  len(catalog), // all 档可见数 = 全量卡数
		"merged": merged,
		"one":    one,
	})
}

// checkUpdates POST /api/check：刷新各源后统计可更新数。
func (s *Server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	// 0.6.312 B1/F4（刷新 single-flight）：后台刷新轮正在进行时不再开第二轮
	// 全量刷新（双轮 157 源并发=实测峰值 CPU 的最差形态），等后台轮结束
	// （上限 90s）后直接返回其成果；等待超时则返回当前目录统计、不开新轮。
	// 前端只消费 HTTP 200 + 随后的 loadApps，两种返回同形。
	if s.refreshInflight.Load() {
		deadline := time.Now().Add(90 * time.Second)
		for s.refreshInflight.Load() && time.Now().Before(deadline) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		updates := 0
		for _, a := range s.cachedCatalog(lang.From(r.Context())) {
			if a.Installed && a.HasUpdate {
				updates++
			}
		}
		writeJSON(w, map[string]any{
			"status":            "ok",
			"checked_sources":   0, // 0 = 本轮未新开刷新（复用/让位进行中轮次）
			"failed_sources":    0,
			"updates_available": updates,
		})
		return
	}
	s.refreshInflight.Store(true)
	defer s.refreshInflight.Store(false)
	// 并发刷新（并发 8）：120 源串行最坏 24 分钟卡死请求，并发后通常 1-2 分钟
	var updated, failed int
	names := s.enabledSourceNames()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(nm string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.Src.Refresh(nm); err != nil {
				mu.Lock()
				failed++
				mu.Unlock()
			} else {
				mu.Lock()
				updated++
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()
	s.invalidateCatalog() // 源已刷新，旧缓存作废
	updates := 0
	for _, a := range s.cachedCatalog(lang.From(r.Context())) {
		if a.Installed && a.HasUpdate {
			updates++
		}
	}
	writeJSON(w, map[string]any{
		"status":            "ok",
		"checked_sources":   updated,
		"failed_sources":    failed,
		"updates_available": updates,
	})
}

// autoRefreshBudget 0.6.312 B3/F5（源轮转预算）：每轮非优先级源最多补刷
// 的个数。157 源里巨族长尾（59 个同源镜像仓库）按 1h/40 源拉平到 ~6h/源——
// 社区源发布频率天级，单源可见延迟 ≤6h 无感知；峰值刷新负载降 60-70%。
const autoRefreshBudget = 40

// prioritySourceNames F5 优先级集：收藏源 + 已装应用所在源（保持小时级
// 全刷）。官方目录不走源刷新（面板通道自同步），这里不含 OfficialSourceID。
func (s *Server) prioritySourceNames() []string {
	set := map[string]bool{}
	for _, f := range s.Cfg.FavoriteSources {
		if f != "" {
			set[f] = true
		}
	}
	// 已装应用所在源：取目录里已装卡（含官方合并后 Source=fnos-official 的
	// 卡——其真实源在候选组里，一并计入）
	catalog := s.cachedCatalog(s.bgLang())
	for i := range catalog {
		c := &catalog[i]
		if !c.Installed {
			continue
		}
		if c.Source != "" && c.Source != OfficialSourceID {
			set[c.Source] = true
		}
		if c.Source == OfficialSourceID {
			// 官方合并卡：原社区源 = 同名组里任一社区卡
			for j := range catalog {
				if catalog[j].AppName == c.AppName && catalog[j].Source != "" &&
					catalog[j].Source != OfficialSourceID {
					set[catalog[j].Source] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// StartSourceAutoRefresh 后台自动源刷新（由 main 启动一个 goroutine 调用）：
//  1. 启动 10s 后并发刷新一次——修「重启后社区目录空着，直到用户手动
//     检查」（源目录是内存态，此前没有任何自动刷新机制）；
//  2. 之后按 check_interval_hours 周期刷新（该设置此前是纯摆设：前端有
//     输入框、后端存字段，但没有任何 ticker 执行）。
//
// 0.6.312 B3/F5（源轮转预算）：周期轮次不再全量刷 157 源——优先级集
// （收藏+已装应用所在源）保持小时级，其余源按新鲜度升序只补刷最久未刷
// 的 autoRefreshBudget 个。手动 /api/check 与 reload/sync-all 仍全量。
func (s *Server) StartSourceAutoRefresh(ctx context.Context) {
	s.autoRefreshMu.Lock()
	defer s.autoRefreshMu.Unlock()
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second): // 等服务与 kspeeder 引擎安定
	}
	// 0.6.315 M2（启动错峰）：首轮源同步并发收敛——冷启动首刷是全进程最大的
	// 堆爆发窗口（B4 pprof 实锤：首刷+目录重建把堆撑大后 Go 保留不还，RSS
	// 大头即来源于此）。首轮 4 并发（周期轮/手动仍 8），把爆发摊平；
	// MOO_STARTUP_SYNC_CONC 可覆盖（正整数；0/非法=4 默认）。
	startupN := 4
	if v := os.Getenv("MOO_STARTUP_SYNC_CONC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			startupN = n
		}
	}
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		// 0.6.312 B1/F4：手动「检查更新」正在刷新一轮时跳过本轮（single-flight），
		// 下个小时再轮。自动更新 pass 也随之跳过（手动操作进行中本就该让位）。
		if s.refreshInflight.Load() {
			log.Printf("[auto-refresh] 刷新进行中（手动检查），本轮跳过")
		} else {
			s.refreshInflight.Store(true)
			start := time.Now()
			// 0.6.312 B3/F5：轮转预算轮次（优先级集必刷 + 最久未刷补刷 40 源）
			// 0.6.315 M2：启动首轮用 startupN 并发错峰（首刷=冷启动堆爆发窗口）
			roundN := 8
			if first {
				roundN = startupN
				first = false
			}
			sts := s.Src.RefreshBudgeted(s.prioritySourceNames(), autoRefreshBudget, roundN)
			ok, fail := 0, 0
			for _, st := range sts {
				if st.Error != "" {
					fail++
				} else {
					ok++
				}
			}
			log.Printf("[auto-refresh] 源刷新完成（预算轮 %d 源）: %d 成功 / %d 失败, 耗时 %s", len(sts), ok, fail, time.Since(start).Round(time.Second))
			// 「应用源自动监测」：连续 5 次刷新无应用的源自动停用 + 空源沉底
			//（策略关闭或无动作时为 nil）。变更落在共享 cfg 上，落盘 + 失效缓存。
			if acts := s.Src.AutoCarePass(sts); len(acts) > 0 {
				for _, a := range acts {
					log.Printf("[auto-care] %s", a)
				}
				_ = s.Cfg.Save(dataDirOf(s))
			}
			// 通知轮次：源同步失败摘要/恢复/目录异常下跌 + 收藏应用有更新（0.6.121）
			s.sourceRoundNotify(sts)
			s.invalidateCatalog()
			s.refreshInflight.Store(false)
			// 预热目录缓存：首个用户请求（详情点击/列表打开）不再撞上
			// 冷启动全量重建（实测 ~750ms）。
			if ctx.Err() == nil {
				go s.cachedCatalog(s.bgLang())
			}
		}
		// 「自动更新应用」开启时，源刷新后顺势跑一轮自动更新（同一周期）。
		if ctx.Err() == nil {
			// 0.6.325 G3：源列表自动同步接线（开关关=直接跳过）——在
			// autoUpdatePass 之前，新增的源本轮目录重建后即可参与更新判定。
			s.autoSyncSourceList(ctx)
			s.autoUpdatePass(ctx)
		}
		h := s.Cfg.CheckIntervalHours
		if h <= 0 {
			h = 24
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(h) * time.Hour):
		}
	}
}

// autoUpdatePass 「自动更新应用」一轮：对目录里所有「已装且有更新」的应用
// 顺序执行标准更新管线（官方应用走平台 cloud 通道、社区应用走 FPK 升级
// 管线，均为就地升级保留 @appdata）。排除商店自身（moo，防自更新环）。
// 单个失败不阻塞其余，该应用保留在「有更新」列表由用户手动处理；结果记
// 日志（Moo 无 UI 时也能事后查 journal）。
func (s *Server) autoUpdatePass(ctx context.Context) {
	if !s.Cfg.AutoUpdate {
		return
	}
	// daemon 报 upgradeInfo 的应用 = 应用中心认定有官方可升级版本，
	// 一律走平台 cloud 通道（与应用中心 UI 同源，保留 @appdata）；
	// 其余（纯社区目录更新）走 FPK 升级管线。
	officialUpgrade := map[string]bool{}
	if ias, err := platform.ListInstalled(ctx); err == nil {
		for _, ia := range ias {
			if ia.UpgradeInfo != nil && ia.UpgradeInfo.Version != "" {
				officialUpgrade[ia.AppName] = true
			}
		}
	}
	apps := s.buildCatalog(s.bgLang())
	type job struct {
		key, appName, display, source, target string
		official                              bool
	}
	jobs := make([]job, 0)
	for _, a := range apps {
		if !a.Installed || !a.HasUpdate {
			continue
		}
		if a.AppName == "moo" {
			continue // 商店自身：防自更新环
		}
		src := a.Source
		display := a.AppName
		if a.UpdateFromSource != "" {
			src = a.UpdateFromSource // 0.6.272：跨源同宗更新，日志/通知显示真实安装源
			display = a.AppName + "（来自 " + a.UpdateFromSource + " 源）"
		}
		jobs = append(jobs, job{
			key: a.Key, appName: a.AppName, display: display, source: src,
			target:   a.AvailableVersion,
			official: a.Source == OfficialSourceID || officialUpgrade[a.AppName],
		})
	}
	if len(jobs) == 0 {
		return
	}
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, j.appName)
	}
	log.Printf("[auto-update] 开始自动更新 %d 个应用: %v", len(jobs), names)
	done, failed := 0, 0
	var doneNames, failedNames []string
	failedSet := map[string]bool{}
	for _, j := range jobs {
		if ctx.Err() != nil {
			return
		}
		// 0.6.312 B1/F1：自动更新也走操作队列——与手动操作全局互斥（此前
		// autoUpdatePass 直调管线，手动更新撞上时同一应用会双管线并发、
		// 无 UI 提示）。手动操作进行中则整轮让位（留在「有更新」列表，
		// 下轮重试），不再逐条记失败。
		if cur := s.Ops.Current(); cur != nil && cur.State == operation.StateRunning {
			log.Printf("[auto-update] 手动操作进行中（%s %s），跳过本轮自动更新", cur.Kind, cur.Target)
			return
		}
		log.Printf("[auto-update] %s → v%s（%s）开始…", j.appName, j.target, j.source)
		var err error
		if j.official {
			_, err = s.Ops.StartSync(ctx, "update", j.appName, func(c context.Context, p func(string, float64)) error {
				return s.runOfficialUpgrade(c, j.appName, nil, p)
			})
		} else {
			_, err = s.Ops.StartSync(ctx, "update", j.appName, func(c context.Context, p func(string, float64)) error {
				srcName, a, rerr := s.resolveKey(j.key)
				if rerr != nil {
					return rerr
				}
				// 0.6.272：跨源同宗更新从同宗源安装（目标包在那里，
				// 用规范源会取到旧版被版本交叉校验挡住）
				if srcName, a, rerr = s.resolveInstallEntry(j.key, srcName, a); rerr != nil {
					return rerr
				}
				return s.Pipe.InstallWithParams(c, srcName, a.Name, nil, p)
			})
		}
		if err != nil {
			failed++
			failedNames = append(failedNames, j.display) // 跨源同宗更新带「来自 XX 源」
			failedSet[j.appName] = true
			// 结果未知（任务被回收但平台可能已完成升级）也要失效缓存，
			// 避免「有更新」列表拿着旧版本数据多滞留一个 TTL。
			s.invalidateCatalog()
			log.Printf("[auto-update] %s 更新失败（保留在「有更新」列表，可手动更新）: %v", j.appName, err)
			continue
		}
		done++
		doneNames = append(doneNames, j.display) // 跨源同宗更新带「来自 XX 源」
		s.invalidateCatalog()
		log.Printf("[auto-update] %s 已更新到 v%s", j.appName, j.target)
		// 给平台任务队列/应用重启留缓冲，避免连环升级踩踏
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	log.Printf("[auto-update] 本轮完成: %d 成功 / %d 失败", done, failed)
	// 0.6.133：后台自动更新轮次摘要通知（一轮一条）。
	// 0.6.210 排版统一：成功/失败分列（旧版混列全名单、企微换行折叠成
	// 「成功 1 / 失败 5 · - 1Panel · - Gitea …」不可读），与其余通知同款
	// 简洁/完整变体 + 卡片行 + markdown 表格。
	if done+failed > 0 {
		content, table, rows, variants := buildAutoUpdateRoundMsg(doneNames, failedNames)
		s.notifyEventRichV("auto_update_round", "应用自动更新完成", content, table, rows, "", "", failed == 0, variants)
	}
}

// buildAutoUpdateRoundMsg 拼「应用自动更新完成」通知（0.6.210 排版统一）：
// 成功/失败分列（旧版混列全名单、企微换行折叠成「成功 1 / 失败 5 · - 1Panel
// · - Gitea …」不可读），与其余通知同款简洁/完整变体 + 卡片行 + 表格。
func buildAutoUpdateRoundMsg(doneNames, failedNames []string) (content, table string, rows []notify.CardRow, variants *notify.Variants) {
	done, failed := len(doneNames), len(failedNames)
	joinNames := func(ns []string, cap int) string {
		if len(ns) > cap {
			return strings.Join(ns[:cap], "、") + fmt.Sprintf(" 等 %d 个", len(ns))
		}
		return strings.Join(ns, "、")
	}
	var b, bFull strings.Builder
	if failed == 0 {
		fmt.Fprintf(&b, "全部 %d 个成功：\n%s\n", done, joinNames(doneNames, 8))
		fmt.Fprintf(&bFull, "全部 %d 个成功：\n%s\n", done, strings.Join(doneNames, "、"))
	} else {
		fmt.Fprintf(&b, "成功 %d / 失败 %d\n", done, failed)
		if len(doneNames) > 0 {
			fmt.Fprintf(&b, "成功：%s\n", joinNames(doneNames, 5))
		}
		fmt.Fprintf(&b, "失败：%s", joinNames(failedNames, 5))
		fmt.Fprintf(&bFull, "成功 %d / 失败 %d\n", done, failed)
		if len(doneNames) > 0 {
			fmt.Fprintf(&bFull, "成功：%s\n", strings.Join(doneNames, "、"))
		}
		fmt.Fprintf(&bFull, "失败：%s", strings.Join(failedNames, "、"))
	}
	failedSet := make(map[string]bool, len(failedNames))
	for _, n := range failedNames {
		failedSet[n] = true
	}
	var tb strings.Builder
	fmt.Fprintf(&tb, "成功 %d / 失败 %d\n\n| 应用 | 结果 |\n| :-- | :-- |\n", done, failed)
	rows = make([]notify.CardRow, 0, done+failed)
	i := 0
	for _, n := range append(append([]string{}, doneNames...), failedNames...) {
		res := "成功"
		if failedSet[n] {
			res = "失败"
		}
		fmt.Fprintf(&tb, "| %s | %s |\n", mdCell(n), res)
		if i < 8 {
			rows = append(rows, notify.CardRow{Key: n, Value: res})
		}
		i++
	}
	concise := fmt.Sprintf("成功 %d / 失败 %d", done, failed)
	if failed == 0 {
		concise = fmt.Sprintf("全部 %d 个成功", done)
	}
	variants = &notify.Variants{
		ContentConcise: concise,
		ContentFull:    bFull.String(),
		TableFull:      tb.String(),
		RowsFull:       rows,
	}
	return strings.TrimSpace(b.String()), tb.String(), rows, variants
}

// StartGHStatsRefresh GitHub Release 资产下载量同步循环：
// 第三方源 fnpack.json 按规范不统计下载量，但 FPK 挂在 GitHub Releases 上，
// Release 资产的 download_count 是真实全网下载次数。匿名 API 限流 60 次/小时，
// 每 30 分钟一轮最多拉 30 个仓库（最久未拉优先，59 个源仓库约 2 小时覆盖一轮），
// ETag 条件请求（304 不计数），限流余量为 0 时本轮提前停手、下轮继续。
func (s *Server) StartGHStatsRefresh(ctx context.Context) {
	if s.GHStats == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second): // 等启动自动刷目录完成，仓库清单才全
	}
	for {
		if ctx.Err() != nil {
			return
		}
		// 0.6.312 B3/F6（活性门控）：不活跃（>30min 无 UI）时 GHStats 间隔
		// 30→120min——tick 保持 30min 不变（醒来后无需等满 120min 即恢复
		// 30min 节奏），仅跳过「距上轮不足 120min」的不活跃 tick。
		if s.uiInactive() && time.Since(s.ghStatsLastRound()) < ghStatsIdleGap {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Minute):
			}
			continue
		}
		s.ghStatsRound(ctx)
		s.lastGHStatsRound.Store(time.Now().Unix())
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Minute):
		}
	}
}

// ghStatsRound 一轮：把目录里全部 Release 链接排入队列，拉取限额内的仓库。
func (s *Server) ghStatsRound(ctx context.Context) {
	urls := make([]string, 0, 2048)
	for _, a := range s.cachedCatalog(s.bgLang()) {
		if a.ReleaseURL != "" {
			urls = append(urls, a.ReleaseURL)
		}
	}
	s.GHStats.Enqueue(urls)
	n, err := s.GHStats.RefreshRound(ctx, 30)
	if n > 0 {
		log.Printf("[gh-stats] GitHub Release 下载量同步: 本轮更新 %d 个仓库", n)
	}
	if err != nil && err != source.ErrRateLimited {
		log.Printf("[gh-stats] 同步异常: %v", err)
	}
}

// StartGHForksRefresh GitHub 仓库 fork 关系同步循环（0.6.326c 溯源补强）：
// 溯源要区分「作者自建仓」与「整仓 fork 的搬运仓」，后者只有 GitHub 的
// fork/parent 关系能可靠判定。
//
// 0.6.334（用户实测「溯源退回 45%」）：预算/节奏改为按「是否配了 token」与
// 「本轮是否还有待拉仓库」自适应 ——
//
//	· 匿名（60 次/小时）：首轮 55，之后每 20 分钟 12（12×3 = 36/h，留余量）；
//	· 带 token（5000 次/小时）：首轮 80，之后每 1 分钟 40（≈ 3 分钟补满 159 仓）；
//	· 本轮拉到 0 个 = 队列已空/结果全新鲜 → 退避 6 小时（到期前不再打扰 API）。
//
// ETag 命中 304 不耗额度，已知结果 72h 后才重排；限流/失败不阻塞下一轮。
func (s *Server) StartGHForksRefresh(ctx context.Context) {
	if s.Forks == nil {
		return
	}
	// 首轮等待：等启动自动刷目录完成、源清单才全。0.6.340：源就绪与否已由
	// 下面循环里的 ghForkRepos 探测（空则 5 秒重试、不消耗预算），首轮等待
	// 可以压短——用户实测「装完要看溯源」时，等 15~45 秒纯属浪费。
	firstWait := 10 * time.Second
	if s.Forks.HasToken() {
		firstWait = 5 * time.Second
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(firstWait):
	}
	first := true
	waiting := false
	for {
		if ctx.Err() != nil {
			return
		}
		// 0.6.340：源清单为空 = 源/目录尚未就绪（启动早期）。原实现此时
		// RefreshRound 拉到 0 个 → ghForksInterval 判「队列空」→ 退避 6 小时，
		// fork 关系整晚不拉、溯源大面积缺失且没有任何日志（2026-10-19 主 NAS
		// 实测：22:56 启动后首轮队列为空，直到人工写入缓存才恢复）。
		// 改为短等重试（不消耗预算、不算「空轮」）。
		repos := s.ghForkRepos()
		if len(repos) == 0 {
			if !waiting {
				log.Printf("[gh-forks] 源清单尚未就绪，等待后重试（不消耗预算）")
				waiting = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		waiting = false
		budget := ghForksBudget(first, s.Forks.HasToken(), len(repos))
		first = false
		n := s.ghForksRound(ctx, budget)
		select {
		case <-ctx.Done():
			return
		case <-time.After(ghForksInterval(s.Forks.HasToken(), n)):
		}
	}
}

// ghForksBudget 单轮拉取预算。抽成函数是为了让「预算不越限流」这条约束
// 可被单测钉住：匿名档首轮 55 / 其后 12×3 = 36 次每小时 < 60 次每小时；
// token 档 5000 次每小时，可以放开。
//
// 0.6.340（用户实测「溯源不够快」）：token 档一次性给足——把待拉仓库全排上
// （159 源 ≈ 半分钟串行）。原「首轮 80 / 其后 40」在冷启动、缓存丢失或大批
// 新源时至少要走 2~3 轮，叠加每轮「目录不失效」的旧缺陷，用户要等几分钟到
// 几十分钟才看到溯源。pending 只用于放大下限，真正的上限是队列长度。
func ghForksBudget(first, hasToken bool, pending int) int {
	if hasToken {
		if pending < 400 {
			pending = 400
		}
		return pending
	}
	if first {
		return 55
	}
	return 12
}

// ghForksInterval 下一轮间隔：本轮一个都没拉到（队列空/结果全新鲜）→ 退避
// 6 小时；否则 token 档 15 秒、匿名档 20 分钟（36 次每小时，守 60 次每小时）。
//
// 0.6.340：token 档 1 分钟 → 15 秒。token 额度 5000/h 下 15 秒一轮也只会在
// 队列持续非空时发生（队列空立即退避 6h），不会白白烧额度。
func ghForksInterval(hasToken bool, fetched int) time.Duration {
	if fetched == 0 {
		return 6 * time.Hour
	}
	if hasToken {
		return 15 * time.Second
	}
	return 20 * time.Minute
}

// ghForksRound 一轮：把本轮该拉的仓库排入队列（源仓 + 已知 fork parent），
// 拉限额内的；返回实际更新数（供间隔决策）。
func (s *Server) ghForksRound(ctx context.Context, budget int) int {
	for _, r := range s.ghForkRepos() {
		s.Forks.Enqueue(r)
	}
	n, err := s.Forks.RefreshRound(ctx, budget)
	if n > 0 {
		log.Printf("[gh-forks] fork 关系同步: 本轮更新 %d 个仓库", n)
		// 0.6.340：fork 关系变了，溯源字段就得重算——目录缓存不失效的话，
		// 卡片要等下一次目录重建（源同步/安装动作/重启，常常几十分钟）才
		// 显示上游，用户视角就是「溯源很慢/没生效」。invalidateCatalog 只是
		// 作废缓存，真正的重建发生在下一次 /api/apps 请求，代价与一次普通
		// 目录刷新相同（2253 卡实测百毫秒级）。
		s.invalidateCatalog()
	}
	if err != nil && err != source.ErrRateLimited {
		log.Printf("[gh-forks] 同步异常: %v", err)
	}
	return n
}

// ghForkRepos 本轮要入队的仓库列表（owner/repo 小写，已去重）。
//
// 第一遍：全部 GitHub 源的仓库 —— 没有它们就无从判「本源仓是不是 fork」。
// 第二遍（0.6.326d，H2）：已知是 fork 的仓库，把它的 parent 也排上。原实现只
// 排源列表，于是「parent 自己也是 fork」（搬运了搬运仓）的链永远只能发现一层，
// 溯源链断在半途；parent 不在源列表时多拉一个仓库，代价可忽略而收益是链路完整。
// 用 seen 去重：既是队列卫生，也兜住脏缓存里可能出现的关系环（不会死循环）。
//
// 顺带说明「为什么 parent 也要进队列」：parent 一旦被用户加为源（详情页 0.6.326d
// 的「添加为源」按钮），它的 fork 关系若已缓存，详情页立刻就能显示上游版本，
// 不必再等一轮后台拉取。
func (s *Server) ghForkRepos() []string {
	if s == nil || s.Src == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	push := func(r string) {
		r = strings.ToLower(strings.TrimSpace(r))
		if r == "" || strings.Count(r, "/") != 1 || seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	for _, st := range s.Src.Sources() {
		k := source.SourceURLKey(st.URL)
		if !strings.HasPrefix(k, "github.com/") {
			continue // 非 GitHub 主机：没有 fork API 可用
		}
		push(strings.TrimPrefix(k, "github.com/"))
	}
	for i := 0; i < len(out); i++ {
		parent, isFork, known := s.Forks.Lookup(out[i])
		if known && isFork && parent != "" {
			push(parent)
		}
	}
	return out
}

// StartStartupMemReclaim 0.6.335 B3：启动风暴（160 源刷新 + 目录重建 +
// 首轮图标/README 预热）结束后，主动把已释放的堆页归还 OS 一次。
// 为什么：启动期临时对象（目录构建、竞速缓冲、快照解析）把堆顶到高水位后，
// GC 只复用不归还，RSS 长期停在峰值（实测 172MB vs 稳态活对象 ~133MB）。
// 只做一次；稳态 GC/GOMEMLIMIT 行为不变，不会与周期性回收打架。
func (s *Server) StartStartupMemReclaim(ctx context.Context) {
	// 等源数据就绪（首轮自动刷目录至少完成一批）——最多等 10 分钟。
	ready := false
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if s.Src != nil && len(s.Src.Apps("", "")) > 0 {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
	if !ready {
		return
	}
	// 再等 90s：让首轮目录构建 + 预热的最脏阶段跑完（预热首轮预算 10min，
	// 这里只求覆盖「目录构建 + 首屏」这一段，不追预热全量）。
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	debug.FreeOSMemory()
	log.Printf("[mem] 启动风暴结束，已归还空闲堆内存（仅一次；稳态 GC 行为不变）")
}

// StartAddProbeRefresh 「添加为源」可加性探测循环（0.6.326e）。
//
// 触发方式：buildCatalog 首次入队时会 Kick（目录只在 /api/apps 被请求时构建，
// 用纯定时会白等一整个周期）；首轮另有 60s 兜底。之后每 6 小时一轮。
//
// 成本：探测走 raw.githubusercontent（无 API 额度），待探仓库=全目录
// distinct upstream_repo ≈ 数十个，每轮上限 64 个（含 owner 索引仓回退探测
// 时单个仓库最多 2~6 次请求），对网络与源站都可忽略。结果落盘 7 天有效。
func (s *Server) StartAddProbeRefresh(ctx context.Context) {
	if s.AddProbes == nil {
		return
	}
	// 首轮：等目录首次构建（入队即 kick）——最多 60s 兜底。
	s.AddProbes.WaitNext(ctx, 60*time.Second)
	for {
		if ctx.Err() != nil {
			return
		}
		n := s.AddProbes.RefreshRound(ctx, 64)
		if n > 0 {
			log.Printf("[add-probe] 上游仓可加性探测: 本轮 %d 个", n)
			// 0.6.326z4：探测结果会改写卡片的 upstream_add_url/note，而
			// /api/apps 的响应体缓存（cachedAppsBody）只在 invalidateCatalog
			// 时失效 → 旧版列表 JSON 里的提示会滞后数十分钟（详情页读目录
			// 数据不受影响，所以只在列表接口可见）。轮次有产出即失效一次。
			s.invalidateCatalog()
		}
		// 本轮没活儿（目录还没被请求过）→ 短等重试，别错过。
		wait := 6 * time.Hour
		if n == 0 {
			wait = 2 * time.Minute
		}
		s.AddProbes.WaitNext(ctx, wait)
	}
}

// reloadSSE POST /api/apps/reload：SSE 刷新全部源。
func (s *Server) reloadSSE(w http.ResponseWriter, r *http.Request) {
	f := sseStart(w)
	sseSend(w, f, map[string]any{"step": "refresh", "message": "刷新应用源…"})
	var failed int
	for _, name := range s.enabledSourceNames() {
		if err := s.Src.Refresh(name); err != nil {
			failed++
		}
	}
	if failed > 0 {
		sseSend(w, f, map[string]any{"step": "error", "error": strconv.Itoa(failed) + " 个源刷新失败"})
		return
	}
	sseSend(w, f, map[string]any{"step": "done", "message": "已刷新"})
}

// storeUpdateSSE 已移至 self_update.go（官方 GitHub Release 渠道应用内自更新）。

// ---- 应用源 ----

func (s *Server) sourceEntry(st source.SourceStatus) SourceEntry {
	ref := config.SourceRef{}
	for _, sr := range s.Cfg.Sources {
		if sr.Name == st.Name {
			ref = sr
			break
		}
	}
	e := SourceEntry{
		ID:       st.Name,
		Name:     st.Name,
		URL:      st.URL,
		AppCount: st.Count,
		Error:    st.Error,
		Enabled:  ref.IsEnabled(),
	}
	// 0.6.326z：源重合率（只读信号）——目录重建时算好的缓存，这里只读；
	// 官方源不走本函数（s.Panel.sourceEntry 另实现），故恒不带这些字段。
	if ov := s.overlapFor(st.Name); ov.SelfCount > 0 {
		e.OverlapRisk = ov.Risk
		e.OverlapWith = ov.With
		e.OverlapNameCount = ov.NameCount
		e.OverlapExactCount = ov.ExactCount
		e.OverlapNameRatio = ov.NameRatio
		e.OverlapExactRatio = ov.ExactRatio
		e.OverlapUniqueNames = ov.UniqueNames
		e.OverlapSelfCount = ov.SelfCount
	}
	if !st.UpdatedAt.IsZero() {
		e.LastFetched = st.UpdatedAt.Format(time.RFC3339)
	}
	return e
}

// favoriteSource POST /api/sources/{id}/favorite：关注/取消关注应用源（0.6.143）。
// 关注源新增应用时触发 favorite_source_apps 通知（源×应用只推一次）。
func (s *Server) favoriteSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Favorite bool `json:"favorite"`
	}
	if err := jsonDecode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("参数错误"))
		return
	}
	// 源必须存在（官方源 id=fnos-official，普通源 id=源名）
	// 0.6.255：官方源恒存在（不再以面板账号开关为条件）。
	exists := id == OfficialSourceID
	if !exists {
		for _, sr := range s.Cfg.Sources {
			if sr.Name == id {
				exists = true
				break
			}
		}
	}
	if !exists {
		writeErr(w, http.StatusNotFound, fmt.Errorf("源不存在: %s", id))
		return
	}
	notifyMu.Lock()
	defer notifyMu.Unlock()
	// 幂等语义：最终态 = body.Favorite（已关注再点 on 保持关注）
	for i, n := range s.Cfg.FavoriteSources {
		if n == id {
			s.Cfg.FavoriteSources = append(s.Cfg.FavoriteSources[:i], s.Cfg.FavoriteSources[i+1:]...)
			break
		}
	}
	if body.Favorite {
		s.Cfg.FavoriteSources = append(s.Cfg.FavoriteSources, id)
	}
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"ok": true, "id": id, "favorite": body.Favorite})
}

// enabledSourceNames 返回全部启用源名。
func (s *Server) enabledSourceNames() []string {
	names := make([]string, 0, len(s.Cfg.Sources))
	for _, sr := range s.Cfg.Sources {
		if sr.IsEnabled() {
			names = append(names, sr.Name)
		}
	}
	return names
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	out := make([]SourceEntry, 0)
	if s.Panel != nil { // 0.6.255：官方源恒存在（纯 OAuth；未授权时条目带引导文案）
		_, _ = s.Panel.Apps(r.Context()) // 触发目录缓存（拿 app_count/last_fetched）
		out = append(out, s.Panel.sourceEntry())
	}
	for _, st := range s.Src.Sources() {
		out = append(out, s.sourceEntry(st))
	}
	fav := make(map[string]bool, len(s.Cfg.FavoriteSources))
	for _, n := range s.Cfg.FavoriteSources {
		fav[n] = true
	}
	for i := range out {
		if fav[out[i].ID] {
			out[i].Favorite = true
		}
	}
	writeJSON(w, map[string]any{"sources": out})
}

func (s *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if err := jsonDecode(r, &body); err != nil || body.URL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("url 不能为空"))
		return
	}
	// 0.6.326d2：详情页「添加为源」只发 {url}（不带 name），此前把空 name
	// 直传 AddSource → manager.go 判空恒 400「源名称与地址均不能为空」。
	// 与批量端点 batchSources（本文件下半部分）同口径：空名从地址推导；
	// 推导失败仍交给 AddSource 报错。显式传 name 的行为完全不变
	// （重名消歧仍在 AddSource 内的 UniqueSourceName）。
	if body.Name == "" {
		body.Name = sourceNameFromURL(body.URL)
	}
	finalName, err := s.Src.AddSource(body.Name, body.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.invalidateCatalog()
	_ = s.Cfg.Save(dataDirOf(s))
	for _, st := range s.Src.Sources() {
		if st.Name == finalName {
			writeJSON(w, map[string]any{"source": s.sourceEntry(st)})
			return
		}
	}
	writeJSON(w, map[string]any{"source": map[string]any{"id": finalName, "name": finalName, "url": body.URL, "enabled": true}})
}

func (s *Server) batchSources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"items"`
	}
	if err := jsonDecode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	type result struct {
		URL     string `json:"url"`
		Name    string `json:"name,omitempty"`
		OK      bool   `json:"ok"`
		Deduped bool   `json:"deduped,omitempty"` // 0.6.173：与现有/本批已加源同地址，跳过（只保留一个）
		Error   string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(body.Items))
	added := 0
	deduped := 0
	for _, it := range body.Items {
		url := strings.TrimSpace(it.URL)
		if url == "" {
			continue
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = sourceNameFromURL(url)
		}
		if name == "" {
			results = append(results, result{URL: url, Error: "无法从地址推导源名称"})
			continue
		}
		if finalName, err := s.Src.AddSource(name, url); err == nil {
			added++
			results = append(results, result{URL: url, Name: finalName, OK: true})
		} else {
			var ee *source.ErrExists
			// 0.6.173：重复（与列表已有源同地址/同名，或本批内重复）不算错误，
			// 只保留一个，标记 deduped 让前端友好提示
			if errors.As(err, &ee) {
				deduped++
				results = append(results, result{URL: url, Name: name, Deduped: true, Error: err.Error()})
			} else {
				results = append(results, result{URL: url, Name: name, Error: err.Error()})
			}
		}
	}
	if added > 0 {
		s.invalidateCatalog()
	}
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"added": added, "deduped": deduped, "results": results})
}

// sourceNameFromURL 从源地址推导源名（0.6.247 起规则统一在
// source.SourceNameFromURL：首装填充与添加源共用同一命名）。
func sourceNameFromURL(u string) string { return source.SourceNameFromURL(u) }

func (s *Server) reorderSources(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Order []string `json:"order"`
	}
	if err := jsonDecode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.Src.ReorderSources(body.Order)
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"ok": true})
}

// syncList POST /api/sources/sync-list：拉取源列表（M3 官方源清单接入前为空实现）。
// sourceListSyncResult 源列表同步结果（前端 SourceListSyncResult 契约）。
type sourceListSyncResult struct {
	Fetched    int      `json:"fetched"`
	Already    int      `json:"already"`
	Added      int      `json:"added"`
	Failed     int      `json:"failed"`
	AddedNames []string `json:"added_names,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// parseSourceList 解析源列表文本：每行一个 URL，支持 # 注释与行内注释，按 URL 去重。
func parseSourceList(body string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.IndexAny(line, " \t"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		line = strings.TrimSuffix(line, "/")
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			continue
		}
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
	}
	return out
}

// fetchSourceList 抓取源列表文本：直连 raw，失败依次走 GitHub 镜像链前缀。
func (s *Server) fetchSourceList(ctx context.Context) (string, error) {
	listURL := strings.TrimSpace(s.Cfg.SourceListURL)
	if listURL == "" {
		listURL = config.DefaultSourceListURL
	}
	candidates := []string{listURL}
	if strings.Contains(listURL, "raw.githubusercontent.com") {
		// 有健康数据按延迟升序，无则按声明顺序；最多取前 5 个镜像
		stats := s.Mirrors.gh()
		type mk struct {
			key string
			lat int64
		}
		var order []mk
		for _, st := range stats {
			if st.Status == "ok" {
				order = append(order, mk{st.Key, st.LatencyMS})
			}
		}
		if len(order) == 0 {
			for _, opt := range config.GitHubMirrorOptions() {
				if opt.URL != "" && opt.Key != "custom" {
					order = append(order, mk{opt.Key, 1 << 30})
				}
			}
		}
		sort.SliceStable(order, func(i, j int) bool { return order[i].lat < order[j].lat })
		urlByKey := map[string]string{}
		for _, opt := range config.GitHubMirrorOptions() {
			urlByKey[opt.Key] = opt.URL
		}
		for i, o := range order {
			if i >= 5 {
				break
			}
			if u := urlByKey[o.key]; u != "" {
				candidates = append(candidates, u+listURL)
			}
		}
	}
	client := &http.Client{}
	var lastErr error
	for _, c := range candidates {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, c, nil)
		if err == nil {
			req.Header.Set("User-Agent", "moo/0.4")
			resp, doErr := client.Do(req)
			if doErr == nil {
				body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				if rerr == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
					cancel()
					return string(body), nil
				}
				lastErr = fmt.Errorf("HTTP %s", resp.Status)
				if rerr != nil {
					lastErr = rerr
				}
			} else {
				lastErr = doErr
			}
		}
		cancel()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("源列表获取失败: %w", lastErr)
}

// addMissingSources 并发添加 urls 中缺失的源（并发 4；单源失败不影响其他；
// 0.6.271：重名自动消歧、同地址计已存在）。0.6.172 从 syncList 抽出，restoreDefaults 复用。
func (s *Server) addMissingSources(urls []string) (added, already, failed int, addedNames, errs []string) {
	existing := make(map[string]bool, len(s.Src.Sources()))
	for _, st := range s.Src.Sources() {
		existing[source.NormalizeSourceURL(st.URL)] = true
	}
	var newURLs []string
	for _, u := range urls {
		if existing[source.NormalizeSourceURL(u)] {
			already++
		} else {
			newURLs = append(newURLs, u)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	sem := make(chan struct{}, 4)
	for _, u := range newURLs {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			// 0.6.325 安全加固 R1：列表驱动的加源先过 netguard.Public。
			// 远端清单（内置/自建列表地址）若被篡改塞入内网地址，一旦入库
			// 该主机 ≤30s 即进 netguard 豁免表 → 成为 SSRF 跳板。默认跳过
			// 非公共地址；管理员可用「允许自动同步内网源」显式放开。
			// 注意：必须在 AddSource 之前判定（先加后检无效）。
			// 管理员在应用源页手工输入的源不走此路径，不受影响。
			if !s.Cfg.SourceListAllowPrivate {
				if _, gerr := netguard.Public(u); gerr != nil {
					mu.Lock()
					failed++
					errs = append(errs, u+": 跳过（安全策略）"+gerr.Error())
					mu.Unlock()
					return
				}
			}
			name := sourceNameFromURL(u)
			if name == "" {
				mu.Lock()
				failed++
				errs = append(errs, u+": 无法推导源名称")
				mu.Unlock()
				return
			}
			// 0.6.271：重名 → AddSource 自动消歧返回最终名（0.6.248 的手动
			// 重试块已收进 source 包，与交互添加路径共用）；ErrExists 现在
			// 只剩「同地址」一种 → 计 already。
			if finalName, err := s.Src.AddSource(name, u); err == nil {
				mu.Lock()
				added++
				addedNames = append(addedNames, finalName)
				mu.Unlock()
			} else {
				var ee *source.ErrExists
				if errors.As(err, &ee) {
					mu.Lock()
					already++
					mu.Unlock()
				} else {
					mu.Lock()
					failed++
					errs = append(errs, u+": "+err.Error())
					mu.Unlock()
				}
			}
		}(u)
	}
	wg.Wait()
	return
}

// sourceListSyncOnce 抓源列表 → 解析 → 添加缺失源（0.6.325 G3 抽出，
// syncList 手动按钮与 StartSourceAutoRefresh 自动接线共用同一套逻辑）。
// fetchSourceList 失败即返回 err（调用方决定 502 还是记日志跳过）。
func (s *Server) sourceListSyncOnce(ctx context.Context) (sourceListSyncResult, error) {
	var res sourceListSyncResult
	body, err := s.fetchSourceList(ctx)
	if err != nil {
		return res, err
	}
	urls := parseSourceList(body)
	res.Fetched = len(urls)
	res.Added, res.Already, res.Failed, res.AddedNames, res.Errors = s.addMissingSources(urls)
	return res, nil
}

// autoSyncSourceList 「源列表自动同步」接线（0.6.325 G3，BUG-A1 修复）：
// UI 开关（SourceManager.tsx:714 title「开启后每次目录检查自动添加官源
// 列表中的新源」）此前后端零消费者——开关纯摆设。现接入周期刷新循环：
// 开关关（SourceListOff=true）直接跳过；开则跑与手动「同步官源」按钮同源
// 的 sourceListSyncOnce，任何失败单行日志跳过、绝不中断本轮刷新；
// 有新增才落盘（added=0 时 addMissingSources 无变更，无需 Save）。
func (s *Server) autoSyncSourceList(ctx context.Context) {
	if s.Cfg.SourceListOff {
		return
	}
	res, err := s.sourceListSyncOnce(ctx)
	if err != nil {
		log.Printf("[list-sync] 源列表自动同步跳过（获取失败）: %v", err)
		return
	}
	if res.Added > 0 {
		_ = s.Cfg.Save(dataDirOf(s))
		log.Printf("[list-sync] 源列表自动同步：本轮新增 %d 个源（列表 %d 条 / 已存在 %d / 失败 %d）",
			res.Added, res.Fetched, res.Already, res.Failed)
	}
}

// syncList POST /api/sources/sync-list：抓内置/自定义社区源列表，自动添加新源。
// 新源并发校验（并发 4、单源 25s 预算，AddSource 失败自动回滚）。
func (s *Server) syncList(w http.ResponseWriter, r *http.Request) {
	res, err := s.sourceListSyncOnce(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, res)
}

// sourceRestoreResult 「恢复默认源列表」结果（0.6.172）。
type sourceRestoreResult struct {
	Fetched       int      `json:"fetched"`
	Restored      int      `json:"restored"`
	Already       int      `json:"already"`
	Failed        int      `json:"failed"`
	Deduped       int      `json:"deduped"`
	RestoredNames []string `json:"restored_names,omitempty"`
	RemovedNames  []string `json:"removed_names,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

// restoreDefaults POST /api/sources/restore-defaults：一键恢复所有默认应用源
// （0.6.172，「源列表自动同步」卡按钮，防误删后无法恢复）：
//  1. 去重——全部源按归一化地址（协议/尾斜杠无关）分组，相同地址只保留一个
//     （组内有官方源保官方，否则保列表顺序第一个）；
//  2. 重抓内置社区源列表补齐被删/缺失的默认源（只增不删用户自加源）。
func (s *Server) restoreDefaults(w http.ResponseWriter, _ *http.Request) {
	// 0.6.247：以内置默认源集为基准（0.6.316 起 20 源 → 0.6.326 起 21 源、全带协议、= Blue-Mink
	// 精选清单，与首装默认同一集合）——误删的源可一键找回，离线可用。
	// 更早依赖外部 repo_list.txt（118 条）/0.6.314 前为 157 源全量集，均
	// 已废弃。取源方式=同一嵌入清单 BundledDefaultSources（default_sources.txt），
	// 加源只需改清单，本函数与首装填充自动跟随。
	urls := source.BundledDefaultSources()
	res := sourceRestoreResult{Fetched: len(urls)}

	// 1) 去重：相同源地址只保留一个（官方源永不被删）
	type pair struct {
		Name string
		URL  string
	}
	byURL := map[string][]pair{}
	for _, st := range s.Src.Sources() {
		if st.URL == "" {
			continue
		}
		byURL[source.NormalizeSourceURL(st.URL)] = append(byURL[source.NormalizeSourceURL(st.URL)], pair{st.Name, st.URL})
	}
	for _, group := range byURL {
		if len(group) < 2 {
			continue
		}
		keep := 0
		for i, g := range group {
			if g.Name == OfficialSourceID {
				keep = i
				break
			}
		}
		for i, g := range group {
			if i == keep {
				continue
			}
			if err := s.Src.RemoveSource(g.Name); err == nil {
				res.Deduped++
				res.RemovedNames = append(res.RemovedNames, g.Name)
			}
		}
	}
	if res.Deduped > 0 {
		s.invalidateCatalog()
	}

	// 2) 补齐缺失的默认源
	res.Restored, res.Already, res.Failed, res.RestoredNames, res.Errors = s.addMissingSources(urls)
	_ = s.Cfg.Save(dataDirOf(s))
	s.invalidateCatalog()
	writeJSON(w, res)
}

// deleteDefaults POST /api/sources/delete-defaults：一键删除历史默认源
// （0.6.318，「源列表自动同步」卡的「删除默认源」按钮）。
// 删除范围 = 现有源中地址归一后命中旧全量默认清单（157 条，0.6.316 前
// 首装即带的默认源集）的条目，**不做官源保护**（用户 2026-10-09 定稿：
// 只要在旧 157 清单里的都可一键删除）——与新 21 条官源重叠的 19 条会被
// 一并删除，随后由「恢复官源」按钮一键找回，构成清单重置工作流。
// 天然不受影响：官方源（面板 OAuth 条目，id=fnos-official，非 URL 源）、
// 不在旧清单里的用户自加源、不在旧清单里的官源（如 Blue-Mink/moo）。
//
// ?dry_run=1 只返回将被删除的清单与计数，不落任何变更（前端确认弹窗用）。
type sourceDeleteDefaultsResult struct {
	DryRun       bool     `json:"dry_run"`
	Scanned      int      `json:"scanned"`
	Deleted      int      `json:"deleted"`
	DeletedNames []string `json:"deleted_names,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

func (s *Server) deleteDefaults(w http.ResponseWriter, r *http.Request) {
	dry := r.URL.Query().Get("dry_run") == "1"
	legacy := map[string]bool{}
	for _, u := range source.BundledLegacyDefaultSources() {
		legacy[source.NormalizeSourceURL(u)] = true
	}
	res := sourceDeleteDefaultsResult{DryRun: dry}
	for _, st := range s.Src.Sources() {
		if st.URL == "" || st.Name == OfficialSourceID {
			continue
		}
		res.Scanned++
		key := source.NormalizeSourceURL(st.URL)
		if !legacy[key] {
			continue
		}
		if dry {
			res.Deleted++
			res.DeletedNames = append(res.DeletedNames, st.Name)
			continue
		}
		if err := s.Src.RemoveSource(st.Name); err != nil {
			res.Errors = append(res.Errors, st.Name+": "+err.Error())
			continue
		}
		res.Deleted++
		res.DeletedNames = append(res.DeletedNames, st.Name)
	}
	if !dry && res.Deleted > 0 {
		_ = s.Cfg.Save(dataDirOf(s)) // 落盘：RemoveSource 只改内存，漏存则重启后被删源复活（审计 ISSUE-1）
		s.invalidateCatalog()
	}
	writeJSON(w, res)
}

// 注：「从 New Store 同步」已移除——New Store 项目已弃用，其应用源已全部
// 迁移到 Moo（2026-09-26 实测 135 源 0 缺失），入口与接口一并下线。

func (s *Server) syncSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// 官方源：面板通道不走源管理器。手动同步走 force 路径（绕过失败退避
	// 窗口并重置计数）。0.6.252 起修复：此前该端点对官方源返回 404
	// 「源不存在」，UI 官方源卡片的手动刷新按钮空转。
	if id == OfficialSourceID {
		// 0.6.255：官方源恒存在；未授权时 AppsForce 返回「未连接」错误
		// 透传给 UI（此前以面板账号开关判定存在性）。
		if s.Panel == nil {
			writeErr(w, http.StatusNotFound, fmt.Errorf("源不存在: %s", id))
			return
		}
		_, perr := s.Panel.AppsForce(r.Context())
		s.invalidateCatalog()
		entry := s.Panel.sourceEntry()
		entry.Error = perr
		writeJSON(w, map[string]any{"source": entry})
		return
	}
	if err := s.Src.Refresh(id); err != nil {
		var eNoSuch *source.ErrNoSuchSource
		if errors.As(err, &eNoSuch) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.invalidateCatalog()
	for _, st := range s.Src.Sources() {
		if st.Name == id {
			writeJSON(w, map[string]any{"source": s.sourceEntry(st)})
			return
		}
	}
	writeJSON(w, map[string]any{"source": map[string]any{"id": id, "name": id}})
}

// syncAllSources POST /api/sources/sync-all：一键刷新所有应用源（0.6.171，
// 「应用源自动监测」卡的圆形刷新按钮）。复用周期轮次的并发刷新
// RefreshAllConcurrent（8 并发、只刷已启用源、单源失败不影响其他）；
// 手动同步不计入自动监测连续轮次。
func (s *Server) syncAllSources(w http.ResponseWriter, r *http.Request) {
	sts := s.Src.RefreshAllConcurrent(8)
	failed := []map[string]any{}
	synced := 0
	for _, st := range sts {
		if st.Error != "" {
			failed = append(failed, map[string]any{"name": st.Name, "error": st.Error})
		} else {
			synced++
		}
	}
	// 0.6.252：一键刷新同时刷新官方目录（手动路径，绕过失败退避窗口）。
	// 0.6.255：官方源恒参与（纯 OAuth；未授权时记入 failed 并提示未连接）。
	total := len(sts)
	if s.Panel != nil {
		total++
		if _, perr := s.Panel.AppsForce(r.Context()); perr != "" {
			failed = append(failed, map[string]any{"name": "飞牛应用中心", "error": perr})
		} else {
			synced++
		}
	}
	s.invalidateCatalog()
	writeJSON(w, map[string]any{"ok": true, "total": total, "synced": synced, "failed": failed})
}

func (s *Server) toggleSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := jsonDecode(r, &body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, errors.New("enabled 不能为空"))
		return
	}
	if err := s.Src.ToggleSource(r.PathValue("id"), *body.Enabled); err != nil {
		var eNoSuch *source.ErrNoSuchSource
		if errors.As(err, &eNoSuch) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.invalidateCatalog()
	_ = s.Cfg.Save(dataDirOf(s))
	if *body.Enabled {
		// 0.6.326z4：恢复（启用）源时顺带后台拉一次。ToggleSource 会清空该源
		// 的缓存，此前要等下一次刷新轮次（实测约 20 分钟）卡片才回来——用户点
		// 「恢复fork源」看到的是「源回到列表了，应用没回来」。失败静默：恢复
		// 动作本身已成功，网络抖动交给下一轮刷新补，不该让开关报错。
		go func(name string) {
			if err := s.Src.Refresh(name); err == nil {
				s.invalidateCatalog()
			}
		}(r.PathValue("id"))
	}
	writeJSON(w, map[string]any{"ok": true, "id": r.PathValue("id"), "enabled": *body.Enabled})
}

// renameSource POST /api/sources/{id}/rename：修改应用源显示名。
// 应用列表里的源徽章（key=appname@source）按源名实时计算，改名自动同步。
func (s *Server) renameSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == OfficialSourceID {
		writeErr(w, http.StatusBadRequest, errors.New("官方应用中心源名固定，不可修改"))
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := jsonDecode(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("源名称不能为空"))
		return
	}
	if strings.TrimSpace(body.Name) == OfficialSourceID {
		writeErr(w, http.StatusBadRequest, errors.New("不能使用官方源名称"))
		return
	}
	if len([]rune(strings.TrimSpace(body.Name))) > 32 {
		writeErr(w, http.StatusBadRequest, errors.New("源名称不能超过 32 个字符"))
		return
	}
	if err := s.Src.RenameSource(id, body.Name); err != nil {
		var eNoSuch *source.ErrNoSuchSource
		if errors.As(err, &eNoSuch) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.invalidateCatalog()
	_ = s.Cfg.Save(dataDirOf(s))
	// 快照按源名落盘：重命名后立即重写，避免旧名残留在启动快照里
	s.Src.FlushCache()
	writeJSON(w, map[string]any{"ok": true, "id": id, "name": strings.TrimSpace(body.Name)})
}

func (s *Server) removeSource(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("id") == OfficialSourceID {
		writeErr(w, http.StatusBadRequest, errors.New("官方应用中心源不可删除（在设置里关闭面板账号即可停用）"))
		return
	}
	if err := s.Src.RemoveSource(r.PathValue("id")); err != nil {
		var eNoSuch *source.ErrNoSuchSource
		if errors.As(err, &eNoSuch) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.invalidateCatalog()
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- 设置 / 加速源 ----

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	interval := s.Cfg.CheckIntervalHours
	if interval <= 0 {
		interval = 24 // 默认值（与前端默认一致）
	}
	set := Settings{
		CheckIntervalHours:  interval,
		Mirror:              s.Cfg.Mirror,
		MirrorIncludeDirect: s.Cfg.MirrorIncludeDirect,
		DockerMirror:        s.Cfg.DockerMirror,
		// 0.6.325 H1(F2)：自定义镜像必须随 GET 透出——PUT 能写但 GET
		// 不下发时，设置页水合恒空串，下次全量保存把 config 里的
		// 自定义镜像静默抹掉（数据丢失级）。
		CustomGitHubMirror: s.Cfg.CustomGitHubMirror,
		CustomDockerMirror: s.Cfg.CustomDockerMirror,
		InstallVolume:      s.Cfg.InstallVolume,
		DownloadDir:        s.Cfg.DownloadDir,
		// 0.6.255：面板账号已从设置中彻底移除（官方源 = 纯 OAuth，
		// 授权时临时输入账号不落地）。以下字段不再下发：
		// PanelEnabled / PanelUsername / PanelBaseURL / PanelHasPassword / PanelDecryptFailed
		SourceListOff: s.Cfg.SourceListOff,
		// 0.6.325 H1(F3)：PUT 可写但 GET 不下发 → UI 永远显示空、
		// SourceManager 回传恒 undefined。透出生效前的存储值
		// （空串=未自定义，前端自行回落展示 DefaultSourceListURL）。
		SourceListURL: s.Cfg.SourceListURL,
		// 0.6.325 安全加固 R1：随设置页下发（默认 false）。
		SourceListAllowPrivate: s.Cfg.SourceListAllowPrivate,
		// 0.6.334：只下发「是否已配置」，token 本身绝不回传。
		GitHubTokenSet:    s.Cfg.GithubToken != "",
		SourceAutoCareOff: s.Cfg.SourceAutoCareOff,
		AutoUpdate:        s.Cfg.AutoUpdate,
		// 0.6.326 溯源批次：自动纠正上游版本（独立开关，默认关）。
		AutoCorrect:         s.Cfg.AutoCorrect,
		CatalogLanguage:     orAuto(s.Cfg.CatalogLanguage),
		UpdatePolicy:        orStrict(s.Cfg.UpdatePolicy),
		DedupPolicy:         orAll(s.Cfg.DedupPolicy),
		BackupDir:           s.Cfg.BackupDir,
		BackupAuto:          s.Cfg.BackupAuto,
		BackupIntervalDays:  s.backupIntervalDaysOf(),
		CacheCleanDays:      s.Cfg.CacheCleanDays,
		CacheCleanEveryDays: s.cacheCleanEveryDaysOf(),
		LogLines:            s.Cfg.LogLines,
		GhProbeHours:        s.Cfg.GhProbeHours,
		GhProbeMinutes:      s.Cfg.GhProbeMinutes,
		DkProbeHours:        s.Cfg.DkProbeHours,
		DkProbeMinutes:      s.Cfg.DkProbeMinutes,
		ProxyEnabled:        s.Cfg.ProxyEnabled,
		ProxyURL:            s.Cfg.ProxyURL,
		DockOrder:           resolveOrderWithLegacy(s.Cfg.DockOrder, DockTabKeys, legacyDockTabKeys),
		SettingsTabOrder:    resolveOrder(s.Cfg.SettingsTabOrder, SettingsTabKeys),
	}
	if set.Mirror == "" {
		set.Mirror = "auto"
	}
	// 0.6.325 C12：docker_mirror 读取侧归一 —— 空值或已下线/未知 key
	// （如 registry-cyou）一律输出 auto，避免设置页下拉显示生 key/空白。
	// 仅作用于 GET 输出，s.Cfg.DockerMirror 本身不改写、不落盘。
	set.DockerMirror = normalizeDockerMirror(set.DockerMirror)
	// 0.6.325 G2：不再把 0 显示成 1——「0=系统默认」须原样往返，
	// 否则清除后重开设置页显示「存储空间 1」、下次全量保存把 1 写回去，
	// 「切回系统默认」在 UI 上永远保不住（与 PUT 指针语义配套）。
	if s.Pipe.Downloads != "" {
		set.DownloadDir = s.Pipe.Downloads
	}
	set.MirrorOptions = config.GitHubMirrorOptions()
	set.DockerMirrorOptions = config.DockerMirrorOptions()
	vols, err := volumeOptions()
	if err == nil {
		set.VolumeOptions = vols
	}
	writeJSON(w, set)
}

// proxyTestHandler 科学加速代理连通性测试（0.6.206）：单独校验给定地址
// （不保存、不影响当前生效配置），经代理 HEAD https://github.com。
func (s *Server) proxyTestHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyURL string `json:"proxy_url"`
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	u := strings.TrimSpace(in.ProxyURL)
	if u == "" {
		writeErr(w, http.StatusBadRequest, errors.New("请填写代理地址"))
		return
	}
	if err := netx.Validate(u); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	lat, err := netx.Test(u, 12*time.Second)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error(), "latency_ms": int(lat / time.Millisecond)})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "latency_ms": int(lat / time.Millisecond)})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mirror       string `json:"mirror"`
		DockerMirror string `json:"docker_mirror"`
		// 0.6.321 F7：「智能选源包含直连」开关（nil = 未提交不改动，
		// 指针区分显式关与未提交；默认 false = 零行为变化）。
		MirrorIncludeDirect *bool `json:"mirror_include_direct"`
		// 指针区分「未提交」(nil=不改动) 与「提交空串」(=显式清除)：
		// 此前字符串字段一律 `!= ""` 判断，用户清空后保存无效（旧值留存，
		// 重开设置页又看到旧值 → 观感「保存没作用」）。
		CustomGitHubMirror *string `json:"custom_github_mirror"`
		CustomDockerMirror *string `json:"custom_docker_mirror"`
		// 0.6.325 G2：安装卷指针语义 —— nil=未提交不改动；0=清除（回
		// 「系统默认」自动探测）；1-8=指定卷；越界（负数/>8）整单拒绝。
		// 此前 `int` + `>0` 守卫让「切回系统默认」永远保存无效（0 被忽略）。
		InstallVolume *int `json:"install_volume"`
		// 0.6.325 H5(F4)：int 一族指针化——nil=未提交不改动；
		// 0=清除回各自默认（检查间隔 24h、备份周期 7 天、清理周期 1 天，
		// 生效值由消费方 `<=0` 回落兜底）；越界整单拒绝。
		// 此前 `>0` 守卫让显式 0 被静默忽略（「改了没反应」）。
		CheckIntervalHours *int    `json:"check_interval_hours"`
		SourceListURL      *string `json:"source_list_url"`
		// 指针区分「未提交」与「显式开/关」：此前 `url != "" || disabled` 条件
		// 在列表地址为空时无法把自动同步切回开启（false 被忽略）。
		SourceListDisabled *bool `json:"source_list_disabled"`
		// 0.6.325 安全加固 R1：nil=未提交不改动；true=允许列表同步添加内网源。
		SourceListAllowPrivate *bool `json:"source_list_allow_private"`
		// 0.6.334：GitHub API token（可选）。nil=未提交不改动；空串=显式清除
		// （回匿名 60/h）；非空=设置并立即生效（无需重启）。只接受
		// ghp_ / github_pat_ 前缀，脏值整单拒绝。
		GitHubToken       *string `json:"github_token"`
		SourceAutoCareOff *bool   `json:"source_auto_care_disabled"`
		// 0.6.255：面板账号字段（panel_enabled/panel_username/panel_password/
		// panel_base_url/panel_clear_password）已从设置中彻底移除。
		// 指针区分「未提交」与「显式关闭」，开关允许切回 false。
		AutoUpdate *bool `json:"auto_update"`
		// 自动纠正上游版本（0.6.326）：nil = 未提交不改动（指针语义，
		// 同 auto_update，开关允许切回 false）。
		AutoCorrect *bool `json:"auto_correct"`
		// 目录语言（0.6.269）：nil = 未提交不改动；auto/zh-CN/en-US。
		CatalogLanguage *string `json:"catalog_language"`
		// 跨源更新策略（0.6.272）：nil = 未提交不改动；
		// strict（默认）/ origin / lineage，其余值整单拒绝。
		UpdatePolicy *string `json:"update_policy"`
		// 列表去重展示策略（0.6.312 B2）：nil = 未提交不改动；
		// all（默认）/ merge / one，其余值整单拒绝。
		DedupPolicy *string `json:"dedup_policy"`
		// FPK 下载目录（选择器给出的绝对路径；空 = 不改动）。
		DownloadDir string `json:"download_dir"`
		// 备份设置（备份设置 tab）：
		// 目录用指针区分「未提交」与「显式清空」（空串 = 回到本机默认数据目录）；
		// 天数同理用指针，避免局部提交（如只切下载目录）把 0 误写进去。
		BackupDir           *string `json:"backup_dir"`
		BackupAuto          *bool   `json:"backup_auto"`
		BackupIntervalDays  *int    `json:"backup_interval_days"`
		CacheCleanDays      *int    `json:"cache_clean_days"`
		CacheCleanEveryDays *int    `json:"cache_clean_every_days"`
		// 日志页显示行数（0.6.261）：nil = 未提交不改动；越界整单拒绝。
		LogLines *int `json:"log_lines"`
		// 加速源自动测速间隔（0.6.148 齿轮选择框）：指针语义 nil=未提交不改动；
		// 前端成对提交（小时+分钟），越界整单拒绝。
		GhProbeHours   *int `json:"gh_probe_hours"`
		GhProbeMinutes *int `json:"gh_probe_minutes"`
		DkProbeHours   *int `json:"dk_probe_hours"`
		DkProbeMinutes *int `json:"dk_probe_minutes"`
		// 科学加速（0.6.206）：前端成对提交当前值（开关+地址）；
		// nil = 未提交不改动，非 nil = 按值设置。
		ProxyEnabled *bool   `json:"proxy_enabled"`
		ProxyURL     *string `json:"proxy_url"`
		// Dock 主导航 / 设置 tab 排序（0.6.122）：nil = 未提交不改动；
		// 非 nil 必须是完整排列（全量提交，部分提交视为错误防脏序）。
		DockOrder        []string `json:"dock_order"`
		SettingsTabOrder []string `json:"settings_tab_order"`
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// 读全量→改单字段→写回（防止部分提交抹掉其它配置）。
	// 字符串/布尔开关用指针语义：nil=未提交不改动，非 nil=按值设置
	// （空串/ false 均合法 = 显式清除/关闭）。
	if in.Mirror != "" {
		s.Cfg.Mirror = in.Mirror
	}
	if in.DockerMirror != "" {
		s.Cfg.DockerMirror = in.DockerMirror
	}
	if in.CustomGitHubMirror != nil {
		s.Cfg.CustomGitHubMirror = *in.CustomGitHubMirror
	}
	if in.CustomDockerMirror != nil {
		s.Cfg.CustomDockerMirror = *in.CustomDockerMirror
	}
	// 0.6.325 G2：nil=未提交不改动；0=清除回「系统默认」（安装走
	// DefaultVolume 自动探测）；越界整单拒绝（与 dock_order/log_lines 同风格）。
	if in.InstallVolume != nil {
		v := *in.InstallVolume
		if v < 0 || v > 8 {
			writeErr(w, http.StatusBadRequest, errors.New("安装卷须为 0-8（0 = 系统默认）"))
			return
		}
		s.Cfg.InstallVolume = v
	}
	// 0.6.325 H5：指针语义（nil=不改）；0=清除回默认 24h（消费循环
	// store_routes.go 与 GET 均对 <=0 回落 24）；上界 168（7 天，
	// 原无上限校验，本轮补齐——越界整单拒绝）；负数拒绝。
	if in.CheckIntervalHours != nil {
		v := *in.CheckIntervalHours
		if v < 0 || v > 168 {
			writeErr(w, http.StatusBadRequest, errors.New("检查间隔须为 0-168 小时（0 = 默认 24）"))
			return
		}
		s.Cfg.CheckIntervalHours = v
	}
	if in.SourceListURL != nil {
		s.Cfg.SourceListURL = *in.SourceListURL
	}
	if in.SourceListDisabled != nil {
		s.Cfg.SourceListOff = *in.SourceListDisabled
	}
	if in.SourceListAllowPrivate != nil {
		s.Cfg.SourceListAllowPrivate = *in.SourceListAllowPrivate
	}
	// 0.6.334：GitHub token（可选）。设置后立即生效（改内存里的查询器），
	// 落盘走 config 的 enc:v1 密文；空串=清除回匿名。格式校验防止把
	// 别的凭据粘进来（脏值整单拒绝，避免"看起来保存了其实无效"）。
	if in.GitHubToken != nil {
		tok := strings.TrimSpace(*in.GitHubToken)
		if tok != "" && !strings.HasPrefix(tok, "ghp_") && !strings.HasPrefix(tok, "github_pat_") {
			writeErr(w, http.StatusBadRequest, errors.New("GitHub token 格式不正确（应以 ghp_ 或 github_pat_ 开头）"))
			return
		}
		s.Cfg.GithubToken = tok
		if s.Forks != nil {
			s.Forks.SetToken(tok)
		}
	}
	if in.SourceAutoCareOff != nil {
		s.Cfg.SourceAutoCareOff = *in.SourceAutoCareOff
	}
	// 0.6.255：面板账号已彻底移除——不再接受/落盘 panel_* 字段。
	if in.AutoUpdate != nil {
		s.Cfg.AutoUpdate = *in.AutoUpdate
	}
	// 0.6.326x：「自动纠正」已并入更新策略（upstream = 强制上游），不再是独立
	// 开关。旧客户端若仍提交 auto_correct=true，在下面 update_policy 赋值之后
	// 按 upstream 处理（放后面以免被同一请求里的策略值覆盖）。
	// 0.6.321 F7：「智能选源包含直连」（nil = 未提交不改动）。
	// 只影响测速候选池，下一探测周期经 applyMirrorProbeFlags 自动生效。
	if in.MirrorIncludeDirect != nil {
		s.Cfg.MirrorIncludeDirect = *in.MirrorIncludeDirect
	}
	// 目录语言（0.6.269）：只接受白名单值，越界整单拒绝。
	if in.CatalogLanguage != nil {
		v := strings.TrimSpace(*in.CatalogLanguage)
		if !lang.Supported[v] {
			writeErr(w, http.StatusBadRequest, errors.New("目录语言仅支持 auto / zh-CN / en-US"))
			return
		}
		s.Cfg.CatalogLanguage = v
	}
	// 跨源更新策略（0.6.272）：nil = 未提交不改动；白名单外值整单拒绝。
	// 策略变化影响目录更新判定 → 保存成功后失效目录缓存。
	if in.UpdatePolicy != nil {
		v := strings.TrimSpace(*in.UpdatePolicy)
		if v != "strict" && v != "origin" && v != "lineage" && v != "upstream" {
			writeErr(w, http.StatusBadRequest, errors.New("更新策略仅支持 strict / origin / lineage / upstream"))
			return
		}
		if s.Cfg.UpdatePolicy != v {
			s.Cfg.UpdatePolicy = v
			s.invalidateCatalog() // 策略变化 → 目录更新判定重建（下次 /api/apps）
		}
		// 0.6.326x：策略已是更新目标的唯一事实源 → 归一旧的独立开关。
		s.Cfg.AutoCorrect = false
	}
	// 兼容旧客户端：仍提交 auto_correct（0.6.326 独立开关）。
	// true ⇒ 等价「强制上游」，必须在 update_policy 赋值之后处理。
	if in.AutoCorrect != nil {
		s.Cfg.AutoCorrect = false // 归一：独立开关不再保留
		if *in.AutoCorrect && s.Cfg.UpdatePolicy != "upstream" {
			s.Cfg.UpdatePolicy = "upstream"
			s.invalidateCatalog()
		}
	}
	// 列表去重展示策略（0.6.312 B2）：nil = 未提交不改动；白名单外值整单拒绝。
	// 策略变化只影响展示层 → 失效目录缓存（Hidden 标记随下次构建重算）。
	if in.DedupPolicy != nil {
		v := strings.TrimSpace(*in.DedupPolicy)
		if v != "all" && v != "merge" && v != "one" {
			writeErr(w, http.StatusBadRequest, errors.New("去重策略仅支持 all / merge / one"))
			return
		}
		if orAll(s.Cfg.DedupPolicy) != v {
			s.Cfg.DedupPolicy = v
			s.invalidateCatalog()
			// 0.6.312 B3/F9：档位切换改变可见集 → 触发一轮增量预热
			//（切「默认」回补此前隐藏卡；切「去重」（one，原「一卡」）缩小后续轮范围）
			go s.warmVisibleNow()
		}
	}
	// 加速源自动测速间隔（0.6.148）：越界整单拒绝（齿轮只产出 0-23/0-59，
	// 拒绝脏值入 config；0h0m 合法 = 未设置 → 后端按默认 5 分钟执行）。
	for _, f := range []struct {
		name string
		h    *int
		m    *int
	}{{"gh", in.GhProbeHours, in.GhProbeMinutes}, {"dk", in.DkProbeHours, in.DkProbeMinutes}} {
		if f.h != nil && (*f.h < 0 || *f.h > 23) {
			writeErr(w, http.StatusBadRequest, errors.New("测速间隔小时须为 0-23"))
			return
		}
		if f.m != nil && (*f.m < 0 || *f.m > 59) {
			writeErr(w, http.StatusBadRequest, errors.New("测速间隔分钟须为 0-59"))
			return
		}
	}
	if in.GhProbeHours != nil {
		s.Cfg.GhProbeHours = *in.GhProbeHours
	}
	if in.GhProbeMinutes != nil {
		s.Cfg.GhProbeMinutes = *in.GhProbeMinutes
	}
	if in.DkProbeHours != nil {
		s.Cfg.DkProbeHours = *in.DkProbeHours
	}
	if in.DkProbeMinutes != nil {
		s.Cfg.DkProbeMinutes = *in.DkProbeMinutes
	}
	// 科学加速（0.6.206）：开关+地址成对校验；地址非空即须合法，
	// 通过后即时生效（netx 全局代理），失败整单拒绝。
	if in.ProxyEnabled != nil || in.ProxyURL != nil {
		enabled := s.Cfg.ProxyEnabled
		if in.ProxyEnabled != nil {
			enabled = *in.ProxyEnabled
		}
		u := s.Cfg.ProxyURL
		if in.ProxyURL != nil {
			u = strings.TrimSpace(*in.ProxyURL)
		}
		if enabled && u == "" {
			writeErr(w, http.StatusBadRequest, errors.New("开启科学加速须先填写代理地址"))
			return
		}
		if u == "" {
			enabled = false // 空地址 = 视为关闭，防脏状态
		} else if err := netx.Validate(u); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.Cfg.ProxyEnabled = enabled
		s.Cfg.ProxyURL = u
		if enabled {
			netx.SetProxy(u)
		} else {
			netx.SetProxy("")
		}
		log.Printf("[accel] 科学加速: %s", map[bool]string{true: "开启 " + u, false: "关闭"}[enabled])
	}
	// FPK 下载目录：只接受选择器给出的可访问目录（/volN 卷根或卷下顶层
	// 共享目录）；切换会迁移旧目录缓存并即时生效，失败要整单报错。
	if in.DownloadDir != "" {
		if err := s.applyDownloadDir(in.DownloadDir); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	// 备份设置：目录允许 /volN 下任意目录或空串（回默认）；保存时验证可写。
	if in.BackupDir != nil {
		d := strings.TrimSpace(*in.BackupDir)
		if d != "" && !strings.HasPrefix(d, "/vol") {
			writeErr(w, http.StatusBadRequest, errors.New("备份目录须为 /volN 存储卷下的路径（空 = 默认应用数据目录）"))
			return
		}
		if d != "" {
			if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("备份目录不存在: %s", d))
				return
			}
		}
		s.Cfg.BackupDir = d
	}
	if in.BackupAuto != nil {
		s.Cfg.BackupAuto = *in.BackupAuto
	}
	// 0.6.325 H5：显式 0 = 清除回默认（备份 7 天 / 清理每 1 天，见
	// backupIntervalDaysOf / cacheCleanEveryDaysOf 的 `<=0` 回落）；
	// 上界沿用既有 ≤30 钳制逻辑（前端 parseDays 同界）；负数整单拒绝。
	if in.BackupIntervalDays != nil {
		d := *in.BackupIntervalDays
		if d < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("备份周期天数不能为负（0 = 默认 7 天）"))
			return
		}
		if d > 30 {
			d = 30
		}
		s.Cfg.BackupIntervalDays = d
	}
	if in.CacheCleanDays != nil {
		d := *in.CacheCleanDays
		if d > 30 {
			d = 30
		}
		s.Cfg.CacheCleanDays = d // 0 = 显式关闭自动清理
	}
	if in.CacheCleanEveryDays != nil {
		d := *in.CacheCleanEveryDays
		if d < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("清理周期天数不能为负（0 = 默认每天）"))
			return
		}
		if d > 30 {
			d = 30
		}
		s.Cfg.CacheCleanEveryDays = d
	}
	// 日志页显示行数（0.6.261）：越界整单拒绝（下拉只产出 50/100/200/500/1000，
	// 拒绝脏值入 config）。
	if in.LogLines != nil {
		if *in.LogLines < 1 || *in.LogLines > 10000 {
			writeErr(w, http.StatusBadRequest, errors.New("日志行数须为 1-10000"))
			return
		}
		s.Cfg.LogLines = *in.LogLines
	}
	// Dock / 设置 tab 排序（0.6.122；0.6.325 C11 dock 纳入 settings + 老 4 键迁移）：
	// 全量排列校验，非法整体拒绝；dock 兼容 legacy 4 键（归一化后存储）。
	dockOrder, err := normalizeOrderField("dock_order", in.DockOrder, DockTabKeys, legacyDockTabKeys)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if dockOrder != nil {
		s.Cfg.DockOrder = dockOrder
	}
	settingsTabOrder, err := normalizeOrderField("settings_tab_order", in.SettingsTabOrder, SettingsTabKeys, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if settingsTabOrder != nil {
		s.Cfg.SettingsTabOrder = settingsTabOrder
	}
	_ = s.Cfg.Save(dataDirOf(s))
	writeJSON(w, map[string]any{"ok": true})
}

// DownloadDirOption 是设置页「FPK 下载目录」选择器的一项。
type DownloadDirOption struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// downloadDirOptions GET /api/settings/download-dirs：列出可访问目录
// （/volN 卷根 + 卷下顶层共享目录；跳过 @appdata/@appcenter/@eaDir 等
// 隐藏目录）与当前生效目录。供前端选择器渲染——不再手输路径。
func (s *Server) downloadDirOptions(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	out := []DownloadDirOption{}
	add := func(path, label string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, DownloadDirOption{Path: path, Label: label})
	}
	// 默认目录（应用数据目录）常列：切走之后仍可随时切回
	add(defaultDownloadDir(), "默认目录（应用数据）")
	for i := 1; i <= 8; i++ {
		p := "/vol" + strconv.Itoa(i)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		add(p, fmt.Sprintf("存储 %d（%s）", i, p))
		entries, _ := os.ReadDir(p)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "@") {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			add(filepath.Join(p, n), n)
		}
	}
	cur := s.Pipe.Downloads
	add(cur, "当前目录（"+cur+"）")
	writeJSON(w, map[string]any{"options": out, "current": cur})
}

// defaultDownloadDir 应用数据目录下的默认 FPK 下载目录（历史默认值）。
func defaultDownloadDir() string {
	return filepath.Join(config.DataDir(), "downloads")
}

// isAccessibleDownloadDir 下载目录白名单：默认目录（应用数据目录），
// /volN 卷根本身，或卷下任意深度的可见子目录（共享目录及其子层；允许
// 尚不存在、由 MkdirAll 创建）。路径中任何一层以 . 或 @ 开头（隐藏目录/
// 平台数据如 @appdata/@appcenter）一律拒绝，防止把缓存写进系统深处。
func (s *Server) isAccessibleDownloadDir(path string) bool {
	if path == defaultDownloadDir() {
		return true
	}
	for i := 1; i <= 8; i++ {
		vp := "/vol" + strconv.Itoa(i)
		if vfi, err := os.Stat(vp); err != nil || !vfi.IsDir() {
			continue
		}
		if path == vp {
			return true
		}
		if strings.HasPrefix(path, vp+"/") {
			for _, comp := range strings.Split(strings.TrimPrefix(path, vp+"/"), "/") {
				if strings.HasPrefix(comp, ".") || strings.HasPrefix(comp, "@") {
					return false
				}
			}
			return true
		}
	}
	return false
}

// browseDownloadDirs GET /api/settings/download-dirs/browse?path=…
// 目录浏览器单级列表（设置页「选择下载目录」对话框用）：path 为 "/"
// 时列出现存卷根（虚拟根），否则列出该目录下的可见子目录（跳过 . / @
// 开头的隐藏与平台目录）。is_allowed 告知当前层是否可选为下载目录。
func (s *Server) browseDownloadDirs(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	if path != "/" && !strings.HasPrefix(path, "/vol") {
		writeErr(w, http.StatusBadRequest, errors.New("只能浏览 /volN 存储卷下的目录"))
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	entries := []entry{}
	parent := ""
	if path == "/" {
		for i := 1; i <= 8; i++ {
			vp := "/vol" + strconv.Itoa(i)
			if vfi, err := os.Stat(vp); err == nil && vfi.IsDir() {
				entries = append(entries, entry{Name: fmt.Sprintf("存储 %d", i), Path: vp})
			}
		}
	} else {
		if vfi, err := os.Stat(path); err != nil || !vfi.IsDir() {
			writeErr(w, http.StatusNotFound, fmt.Errorf("目录不存在: %s", path))
			return
		}
		parent = filepath.Dir(path)
		if parent == path || parent == "" {
			parent = "/"
		}
		list, _ := os.ReadDir(path)
		for _, e := range list {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "@") {
				continue
			}
			entries = append(entries, entry{Name: n, Path: filepath.Join(path, n)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
	writeJSON(w, map[string]any{
		"path":       path,
		"parent":     parent,
		"is_allowed": s.isAccessibleDownloadDir(path),
		"current":    s.Pipe.Downloads,
		"entries":    entries,
	})
}

// applyDownloadDir 切换 FPK 下载目录并即时生效（无需重启应用）：
// 有进行中/已暂停下载任务时拒绝；旧目录的 FPK 缓存文件迁移到新目录
// （跨卷 = 跨文件系统时 rename 降级为复制）；任务管理器与管线目录同步。
func (s *Server) applyDownloadDir(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return errors.New("下载目录必须是绝对路径")
	}
	if !s.isAccessibleDownloadDir(path) {
		return fmt.Errorf("目录不可用：%s（只允许 /volN 卷根或卷下顶层共享目录）", path)
	}
	cur := s.Pipe.Downloads
	if path == cur {
		return nil
	}
	if s.Tasks.HasActive() {
		return errors.New("有下载任务进行中或已暂停，请完成后再切换目录")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}
	entries, _ := os.ReadDir(cur)
	for _, e := range entries {
		if e.IsDir() || !isFpk(e.Name()) {
			continue
		}
		old := filepath.Join(cur, e.Name())
		new := filepath.Join(path, e.Name())
		if err := os.Rename(old, new); err != nil {
			if cerr := copyFileFull(old, new); cerr != nil {
				return fmt.Errorf("迁移 %s 失败: %w", e.Name(), cerr)
			}
			_ = os.Remove(old)
		}
	}
	if err := s.Tasks.SetDownloadDir(path); err != nil {
		return err
	}
	s.Pipe.SetDownloads(path)
	s.Cfg.DownloadDir = path
	fpkLocalInstance.SetExtraDirs([]string{path}) // 下载目录变更 → 索引快照失效
	log.Printf("[settings] FPK 下载目录已切换: %s → %s（缓存已迁移）", cur, path)
	return nil
}

// copyFileFull 简单文件复制（跨文件系统 rename 失败的降级路径）。
func copyFileFull(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// mirrorHealth GET /api/mirrors/health：GitHub 加速源健康（5 分钟缓存）。
// ?refresh=1 强制立即重探（面板「立即测速」按钮；阻塞至探测完成，上限 ~15s）。
func (s *Server) mirrorHealth(w http.ResponseWriter, r *http.Request) {
	s.applyMirrorProbeFlags()
	s.Mirrors.ensureFresh(r.URL.Query().Get("refresh") == "1")
	writeJSON(w, s.healthOf(orAuto(s.Cfg.Mirror), "gh", s.Mirrors.gh()))
}

// dockerMirrorHealth GET /api/mirrors/docker/health：Docker 镜像加速源健康。
// ?refresh=1 强制立即重探（面板「立即测速」按钮；阻塞至探测完成，上限 ~15s）。
func (s *Server) dockerMirrorHealth(w http.ResponseWriter, r *http.Request) {
	s.applyMirrorProbeFlags()
	s.Mirrors.ensureFresh(r.URL.Query().Get("refresh") == "1")
	// 0.6.325 C12：与 getSettings 同源归一——老配置残留的已下线 key
	// （registry-cyou）不得作为 Selected/Active 透出（面板会显示生 key）。
	writeJSON(w, s.healthOf(normalizeDockerMirror(s.Cfg.DockerMirror), "dk", s.Mirrors.dk()))
}

// mirrorsCheck POST /api/mirrors/check：强制全量测速。
func (s *Server) mirrorsCheck(w http.ResponseWriter, r *http.Request) {
	// 0.6.321 F7：与两个 health 端点一致——手动全量测速前应用开关
	//（漏调则「立即测速」恒按上一周期标志跑，切换开关后点测速看不到 direct）。
	s.applyMirrorProbeFlags()
	s.Mirrors.ensureFresh(true)
	gh := make([]MirrorCheckResult, 0)
	for _, st := range s.Mirrors.gh() {
		gh = append(gh, MirrorCheckResult{Key: st.Key, Label: st.Label, LatencyMS: st.LatencyMS, SpeedBPS: st.SpeedBPS, Status: checkStatus(st)})
	}
	dk := make([]MirrorCheckResult, 0)
	for _, st := range s.Mirrors.dk() {
		dk = append(dk, MirrorCheckResult{Key: st.Key, Label: st.Label, LatencyMS: st.LatencyMS, Status: checkStatus(st)})
	}
	writeJSON(w, map[string]any{"github_mirrors": gh, "docker_mirrors": dk})
}

func orAuto(m string) string {
	if m == "" {
		return "auto"
	}
	return m
}

// normalizeDockerMirror 0.6.325 C12：Docker 镜像加速源的读取侧归一。
// 空值、或不在 config.DockerMirrorOptions() key 集合内的值（如已下线的
// registry-cyou）→ "auto"。只影响 GET /api/settings 的输出，不写回配置，
// 用户 config 里的原值保持不变（下次若该源回归仍可生效）。
func normalizeDockerMirror(v string) string {
	if v == "" {
		return "auto"
	}
	for _, o := range config.DockerMirrorOptions() {
		if o.Key == v {
			return v
		}
	}
	return "auto"
}

// orStrict 更新策略缺省归一（0.6.272）："" = 未设置 = strict（0.6.174 行为）。
func orStrict(p string) string {
	if p == "" {
		return "strict"
	}
	return p
}

func orAll(p string) string {
	if p == "" {
		return "all"
	}
	return p
}

func checkStatus(st MirrorStat) string {
	if st.Status == "ok" || st.Status == "limited" {
		// 0.6.333 F2：429 限流单列 limited（链路可达、临时性），
		// 旧前端只认 ok/timeout/error → 会落到「未测速」分支，行为安全。
		return st.Status
	}
	return "error"
}

// ---- FPK 下载缓存 ----

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(s.Pipe.Downloads)
	files := make([]FpkDownloadFile, 0, len(entries))
	installed := listInstalledNames()
	// 把下载目录纳入本地 FPK manifest 索引（中文名 display_name 来源；
	// 后台渐进解析，未就绪时该字段为空、下次请求补齐）。
	fpkLocalInstance.SetExtraDirs([]string{s.Pipe.Downloads})
	fpkLocalInstance.EnsureIndex()
	for _, e := range entries {
		if e.IsDir() || !isFpk(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		appName := nameFromFpk(e.Name())
		f := FpkDownloadFile{
			Name:      e.Name(),
			Size:      fi.Size(),
			ModAt:     fi.ModTime().Format(time.RFC3339),
			AppName:   appName,
			Installed: installed[appName],
		}
		// 321R8：改用按路径的 DisplayFor（直接解析「正在展示的这个文件」的
		// manifest），不再按文件名派生 appname 做 Lookup——后者对版本化文件名
		// （moo_0.6.321_x86 → moo06321x86 ≠ 包内 moo）永远匹配不上，display_name 恒空。
		// 0.6.332 F4：解析在后台进行时标 Parsing，前端据此显示「解析中…」
		// 而不是首开时整列空白（实测 137 个包首次查询 0 个 display_name）。
		full := filepath.Join(s.Pipe.Downloads, e.Name())
		if info, ok := fpkLocalInstance.DisplayFor(full); ok {
			if info.DisplayName != "" {
				f.DisplayName = info.DisplayName
			}
			if info.AppName != "" {
				// 包内 appname 与文件名不一致时（如 istoreos.fpk →
				// com.istoreos.vm），以包内为准做安装态判断
				f.AppName = info.AppName
				f.Installed = installed[info.AppName] || f.Installed
			}
		} else if fpkLocalInstance.Pending(full) {
			f.Parsing = true
		}
		files = append(files, f)
	}
	// 321R8：最近修改的排最前（mtime 倒序）；mtime 相同时按文件名升序兜底。
	// （原按文件名字母序，新上传/新下载的 FPK 不会浮到顶部，用户反馈）
	sort.Slice(files, func(i, j int) bool {
		if files[i].ModAt != files[j].ModAt {
			return files[i].ModAt > files[j].ModAt
		}
		return files[i].Name < files[j].Name
	})
	writeJSON(w, map[string]any{"dir": s.Pipe.Downloads, "files": files})
}

func (s *Server) removeDownload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if !isFpk(name) {
		writeErr(w, http.StatusBadRequest, errors.New("非法文件名"))
		return
	}
	if err := os.Remove(filepath.Join(s.Pipe.Downloads, name)); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// uploadFpkDownload POST /api/fpk-downloads/upload：321R7 E——上传本地
// FPK 安装包（手机/电脑文件）到 FPK 下载目录，上传后即可在「已下载的
// FPK」列表里直接安装（向导探测/daemon 本地安装全走既有管线）。
//
// 安全边界（与其余 fpk-downloads 端点同级）：
//   - requireAdmin：面板网关管理员身份；非 GET 另强制 X-Moo-Admin 头（D2 CSRF）
//   - 文件名取 filepath.Base 防路径穿越；仅 .fpk（大小写不敏感，归一小写）
//   - 同目录临时文件 + rename 原子落盘（不会产生半成品）；同名覆盖
//     （replaced=true，语义=重传同一文件）
//   - 上限 512MB（现役最大包仅几十 MB，余量充足）
func (s *Server) uploadFpkDownload(w http.ResponseWriter, r *http.Request) {
	const maxUpload = 512 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload+4<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, errors.New("文件超过 512MB 上限"))
			return
		}
		writeErr(w, http.StatusBadRequest, errors.New("表单解析失败: "+err.Error()))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("缺少上传文件（字段名 file）"))
		return
	}
	defer file.Close()
	name := filepath.Base(header.Filename)
	if strings.ToLower(filepath.Ext(name)) != ".fpk" {
		writeErr(w, http.StatusBadRequest, errors.New("仅支持 .fpk 安装包"))
		return
	}
	// 扩展名归一小写（Windows 端常见 .FPK/.Fpk）
	name = strings.TrimSuffix(name, filepath.Ext(name)) + ".fpk"
	dst := filepath.Join(s.Pipe.Downloads, name)
	replaced := false
	if _, err := os.Stat(dst); err == nil {
		replaced = true
	}
	// 322R4：随机后缀防并发同名上传竞争同一 tmp（os.Create 截断会互踩）；
	// 后缀不以 .fpk 结尾 → isFpk 过滤，不会出现在「已下载的 FPK」列表。
	rb := make([]byte, 4)
	_, _ = crand.Read(rb)
	tmp := fmt.Sprintf("%s.upload-%s.tmp", dst, hex.EncodeToString(rb))
	f, err := os.Create(tmp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, errors.New("创建临时文件失败: "+err.Error()))
		return
	}
	n, err := io.Copy(f, file)
	closeErr := f.Close()
	if err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusInternalServerError, errors.New("写入失败: "+err.Error()))
		return
	}
	if closeErr != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusInternalServerError, errors.New("写入失败: "+closeErr.Error()))
		return
	}
	if n == 0 {
		os.Remove(tmp)
		writeErr(w, http.StatusBadRequest, errors.New("空文件"))
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		writeErr(w, http.StatusInternalServerError, errors.New("落盘失败: "+err.Error()))
		return
	}
	// 321R8：落盘后同步解析 manifest 写入本地 FPK 索引，使新上传的 FPK 在
	// 「已下载的 FPK」列表立即有应用名（此前依赖后台全量扫描 + 10 分钟目录
	// TTL，窗口内 display_name 恒空）。
	fpkLocalInstance.NotePath(dst)
	writeJSON(w, map[string]any{"name": name, "size": n, "replaced": replaced})
}

// CleanupUploadTmp 322R4：启动时清理上传残留临时文件——进程在 os.Create 与
// os.Rename 之间崩溃会遗留 .upload-<hex>.tmp（旧版 .upload-tmp 一并清）。
// 由 main 启动时同步调用一次；这类文件不以 .fpk 结尾，本就不进列表，清理只
// 为回收磁盘。匹配规则：含 ".upload-" 且以 ".tmp" 结尾，或以 ".upload-tmp" 结尾。
func (s *Server) CleanupUploadTmp() {
	entries, err := os.ReadDir(s.Pipe.Downloads)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if (strings.Contains(n, ".upload-") && strings.HasSuffix(n, ".tmp")) ||
			strings.HasSuffix(n, ".upload-tmp") {
			os.Remove(filepath.Join(s.Pipe.Downloads, n))
		}
	}
}

// fpkDownloadWizard GET /api/fpk-downloads/{name}/wizard 返回已下载 FPK 的
// 安装向导定义（与商店 GET /api/apps/{key}/wizard 同一形状，前端直接复用
// WizardDialog）。安装带必填向导字段的 FPK 前用它探测，弹出向导收集参数。
//
// 0.6.264：改为直接从 FPK tar 里读 wizard/install + manifest，不再做平台
// 暂存——旧路径 StageFpk 要把整包解进暂存区（大包秒级），用户实测「获取
// 安装向导太慢」；向导定义只是包内一个几百字节的小文件，直读毫秒级。
// 安装本身仍走 StageFpk，暂存产物不受影响。
func (s *Server) fpkDownloadWizard(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if !isFpk(name) {
		writeErr(w, http.StatusBadRequest, errors.New("非法文件名"))
		return
	}
	p := filepath.Join(s.Pipe.Downloads, name)
	if _, err := os.Stat(p); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("缓存文件不存在"))
		return
	}
	members, err := readFpkMembers(p, []string{"wizard/install", "manifest"})
	if err != nil {
		writeJSON(w, map[string]any{"has_wizard": false, "error": err.Error()})
		return
	}
	var content []any
	hasWizard := false
	if wz := members["wizard/install"]; len(wz) > 0 {
		var steps []any
		if jsonUnmarshal(wz, &steps) == nil && len(steps) > 0 {
			content = steps
			hasWizard = true
		}
	}
	writeJSON(w, map[string]any{
		"appname":    nameFromFpk(name),
		"version":    fpkManifestVersion(members["manifest"]),
		"has_wizard": hasWizard,
		"content":    content,
	})
}

// installDownloadSSE 安装已下载缓存里的 FPK（daemon 本地安装）。
func (s *Server) installDownloadSSE(w http.ResponseWriter, r *http.Request) {
	f := sseStart(w)
	name := filepath.Base(r.PathValue("name"))
	if !isFpk(name) {
		sseSend(w, f, map[string]any{"step": "error", "error": "非法文件名"})
		return
	}
	p := filepath.Join(s.Pipe.Downloads, name)
	if _, err := os.Stat(p); err != nil {
		sseSend(w, f, map[string]any{"step": "error", "error": "缓存文件不存在"})
		return
	}
	appName := nameFromFpk(name)
	sseSend(w, f, map[string]any{"step": "stage", "progress": 10, "message": "暂存安装包…"})
	staged, err := platform.StageFpk(r.Context(), p, func(frac float64) {
		sseSend(w, f, map[string]any{"step": "stage", "progress": 10 + frac*15, "message": "暂存安装包…"})
	})
	if err != nil {
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	what := "安装"
	// 前端可经 ?wizard=[{key,value}] 传向导参数（fpkDownloadWizard 探测后
	// 用 WizardDialog 收集）；未传时走 AutoFillParams 自动填充。
	var params []platform.WizardParam
	if qz := r.URL.Query().Get("wizard"); qz != "" {
		if jerr := jsonUnmarshal([]byte(qz), &params); jerr != nil {
			sseSend(w, f, map[string]any{"step": "error", "error": "wizard 参数格式错误"})
			return
		}
	}
	if staged.Installed {
		what = "升级"
		// 防旧包假升级：包版本不比已装版本新时，平台会「成功」地把旧
		// 版本再装一遍，版本纹丝不动（与应用源安装管线同一守卫）。
		if inst, ierr := platform.FindInstalled(r.Context(), staged.AppName); ierr == nil && inst != nil &&
			!source.IsNewer(staged.Version, inst.Version) {
			sseSend(w, f, map[string]any{"step": "error", "error": fmt.Sprintf("该包版本为 %s，不比已安装的 %s 新，未执行更新", staged.Version, inst.Version)})
			return
		}
	} else if params == nil {
		// 向导参数自动填充（与应用源安装管线同一逻辑）：必填无默认 →
		// 明确报错，而不是 daemon 侧 19000 裸错误。
		var missing []string
		params, missing, err = platform.AutoFillParams(r.Context(), staged)
		if err != nil {
			sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
			return
		}
		if len(missing) > 0 {
			sseSend(w, f, map[string]any{"step": "error", "error": fmt.Sprintf("以下字段必填且无默认值: %s（该应用需要安装向导配置，请从应用源安装以填写向导）", strings.Join(missing, "、"))})
			return
		}
	}
	sseSend(w, f, map[string]any{"step": "install", "progress": 30, "message": what + " " + appName + "…"})
	err = platform.InstallStaged(r.Context(), staged, params, s.Cfg.InstallVolume, func(frac float64) {
		// 平台进度可能以千分比上报（2110 = 21.1%），统一归一化为 0-1 比例
		// （0.6.325 H4：规则抽至 platform.NormalizeProgress，两条安装通道共用）。
		sseSend(w, f, map[string]any{"step": "install", "progress": 30 + platform.NormalizeProgress(frac)*65, "message": what + "中…"})
	})
	if err != nil {
		// 暂存目录处置（实测平台行为）：
		// ① 平台会每 ~3 分钟自动重试失败的安装，且重试直接复用现有
		//    -tpk 暂存目录（不再从原 FPK 重新解包）；
		// ② 任务运行中删掉暂存目录 → 平台侧报 10111 manifest not exist。
		// 因此：只有安装「确定成功」才清理暂存目录（daemon 不自动回收，
		// 不清理会累积）；失败/结果未知一律保留，供平台自动重试使用。
		if errors.Is(err, platform.ErrTaskOutcomeUnknown) {
			sseSend(w, f, map[string]any{"step": "retry", "progress": 30, "message": "平台任务丢失，正在确认实际结果…"})
			done := false
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				list, lerr := platform.ListInstalled(r.Context())
				if lerr == nil {
					for _, a := range list {
						if a.AppName == staged.AppName &&
							(!staged.Installed || a.Version == staged.Version) {
							done = true
							break
						}
					}
				}
				if done {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if done {
				s.invalidateCatalog()
				sseSend(w, f, map[string]any{"step": "done", "progress": 100, "message": what + "完成"})
				return
			}
			sseSend(w, f, map[string]any{"step": "error", "error": fmt.Sprintf("安装任务被平台回收，应用中心可能在自动重试（约 3 分钟一次）；若稍后仍未%s，请再次点击「安装」", what)})
			return
		}
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	platform.RemoveStagedPackage(staged.Path)
	s.invalidateCatalog()
	sseSend(w, f, map[string]any{"step": "done", "progress": 100, "message": what + "完成"})
}

// ---- 任务列表 ----

// taskFromView 把队列里的长操作视图转成任务视图（终态：done / failed）。
func taskFromView(v operation.View) BackgroundTask {
	status := "done"
	msg := v.Msg
	if v.State == operation.StateError {
		status = "failed"
		if v.Err != "" {
			msg = v.Err
		}
	}
	return BackgroundTask{
		ID:       v.ID,
		AppName:  v.Target,
		Op:       v.Kind,
		Status:   status,
		Step:     v.Msg,
		Progress: 100,
		Message:  msg,
	}
}

// listTasks GET /api/tasks：合并长操作与后台下载任务。
// 长操作：排队中报 queued、运行中报 running、终态报 done/failed，
// 供前端顶部通知栏检测状态翻转弹出结果提示。操作结束时 Current() 被清空，
// 若只报 running，前端永远看不到「安装成功」。
// 0.6.346（并发安装）：排队中的操作与最近 8 条终态一并上报——一次排了多个
// 应用时，只报「最新一条」会让先完成的那些永远不弹「XX 安装成功」。
// 前端首轮轮询只建基线不通知，旧的终态残留不会重复弹。
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	out := make([]BackgroundTask, 0)
	seen := make(map[string]bool)
	for _, v := range s.Ops.Pending() {
		seen[v.ID] = true
		out = append(out, BackgroundTask{
			ID:      v.ID,
			AppName: v.Target,
			Op:      v.Kind,
			Status:  "queued",
			Step:    "排队中",
			Message: "排队中",
		})
	}
	// 0.6.347：多操作并行（下载可并行）时逐个上报，否则只有最早那条可见
	for _, cur := range s.Ops.Running() {
		if seen[cur.ID] {
			continue
		}
		seen[cur.ID] = true
		if cur.State == operation.StateRunning {
			out = append(out, BackgroundTask{
				ID:       cur.ID,
				AppName:  cur.Target,
				Op:       cur.Kind,
				Status:   "running",
				Step:     cur.Msg,
				Progress: cur.Progress,
				Message:  cur.Msg,
			})
		} else {
			out = append(out, taskFromView(cur))
		}
	}
	hist := s.Ops.History()
	const recentTasks = 8
	for i := 0; i < len(hist) && i < recentTasks; i++ {
		if seen[hist[i].ID] {
			continue
		}
		out = append(out, taskFromView(hist[i]))
	}
	for _, t := range s.Tasks.Views() {
		out = append(out, BackgroundTask{
			ID:         t.ID,
			AppName:    t.AppName,
			Op:         "download",
			Status:     t.State,
			Step:       t.State,
			Progress:   t.Percent,
			Message:    t.Err,
			Downloaded: t.Done,
			Total:      t.Size,
		})
	}
	writeJSON(w, out)
}

// removeTask 移除已完成/失败/暂停的下载任务（0.6.146）；运行中拒绝。
// 0.6.261：下载管理器未命中时回退长操作队列——官方 cloud 下载走 Ops
// （store_ops.go sseRunOp kind="download"）而非下载管理器，失败行此前
// 点删除恒 404。
func (s *Server) removeTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Tasks.Remove(id); err != nil {
		var notFound *task.ErrNotFound
		if errors.As(err, &notFound) {
			if removed, _ := s.Ops.Remove(id); removed {
				writeJSON(w, map[string]any{"ok": true})
				return
			}
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---- 小工具 ----

// listInstalledNames 已安装应用名集合（出错返回空集）。
func listInstalledNames() map[string]bool {
	out := map[string]bool{}
	if list, err := platform.ListInstalled(context.Background()); err == nil {
		for _, a := range list {
			out[a.AppName] = true
		}
	}
	return out
}

// maxJSONBodyBytes 是 JSON 请求体上限（0.6.207-panel D4）：防超大 body
// 耗尽内存/带宽。超限后 json 解码报错，各 handler 按既有路径回「参数错误」。
const maxJSONBodyBytes = 10 << 20

func jsonDecode(r *http.Request, v any) error {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBodyBytes)
	return json.NewDecoder(r.Body).Decode(v)
}

func isFpk(name string) bool {
	return len(name) > 4 && name[len(name)-4:] == ".fpk"
}

func nameFromFpk(name string) string {
	if isFpk(name) {
		return name[:len(name)-4]
	}
	return name
}

func progressFrac(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total) * 100
}

// volumeOptions 列出可写数据卷（/vol1..8 中真实存在的目录）。
//
// 0.6.325 C1：VolumeOption.Name（卷友好名）当前恒为空——2026-10-14 主 NAS
// 实机按序侦察（PG open_gateway / trim 库 / LVM / btrfs / mountmgr /
// sysinfo+filestor UDS / 面板 locale）未找到可靠来源，且本版本 fnOS 面板
// 本身不提供存储卷重命名能力，故不做伪造读取；来源可用时在此按 /volN
// 回填 Name 即可（前端显示优先级已就位，见 volumeOptionLabel）。
func volumeOptions() ([]VolumeOption, error) {
	var out []VolumeOption
	for i := 1; i <= 8; i++ {
		p := "/vol" + strconv.Itoa(i)
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		out = append(out, VolumeOption{Index: i, Path: p})
	}
	return out, nil
}
