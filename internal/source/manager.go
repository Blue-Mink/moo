package source

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
)

// Manager 管理已配置源与目录缓存。
type Manager struct {
	mu        sync.RWMutex
	cfg       *config.Config
	cache     map[string]map[string]*App // source name → apps
	fresh     map[string]time.Time       // source name → 上次拉取时间
	cachePath string                     // 目录落盘快照路径（空 = 禁用）
	// 自动监测：source name → 连续「刷新成功但 0 应用」轮数（成功有应用清零；
	// 刷新失败不计）。
	careEmpty map[string]int
	// careParseSkipLogged 2026-11-27（F11/N12 复核 M2）：source name → 「0 条
	// 且索引结构未识别」的那一行日志已经记过。只在首轮记一行，防上游索引
	// 格式坏掉时每轮刷屏；真看到应用（Count>0）后清除，让下次故障重新可见。
	// 与 careEmpty 同属 careMu 保护区；纯内存态，不落盘。
	careParseSkipLogged map[string]bool
	// 失败通道（2026-11-26 CARE P1-1/P1-4）：source name → 连续确定性失败
	// 轮数 / 首次失败时间 / 最近错误 / 停用来源。落盘见 carestore.go。
	careRecs map[string]careRec
	// careMu 保护 careEmpty / careRecs / carePath（独立于 m.mu：落盘快照
	// 与 cfg 读写互不阻塞，也避免「持 m.mu 时异步落盘」的死锁）。
	// 锁序固定为 m.mu → careMu，绝不反向。
	careMu   sync.Mutex
	carePath string
	// careSeq / careWritten 2026-11-26（全量测试暴露的落盘乱序）：
	// 异步落盘在「取快照」时就固定了内容，若它晚于更新的同步 SaveCare 落盘，
	// 会把新计数覆盖回旧值（实测重启后 4 → 2）。每次取快照自增 seq，
	// 落盘前比较 careWritten，只允许更大的 seq 写盘，保证文件永不回退。
	careSeq     uint64
	careWritten uint64
	// careFlush 统计在途异步落盘，供 WaitCareFlush 等干净（测试清理临时目录
	// 时必须先等它，否则异步写会在 RemoveAll 之后重建目录 → 偶发
	// 「directory not empty」）。
	careFlush sync.WaitGroup
	// CareProbe 停用前的强制全量复核钩子（nil = 真实网络探测）。
	CareProbe CareProbeFunc
	// CareHealProbe 自愈通道的低频探测钩子（nil = ProbeCareAppCount）。
	// 返回索引条目数，<0 = 不确定（P1-1b，见 careheal.go）。
	CareHealProbe CareHealProbeFunc
	// careEvents 待外发通知的自动监测事件（DrainCareEvents 取走即清空）。
	// 受 careMu 保护（与 careRecs 同锁区，避免额外锁序）。
	careEvents []CareEvent

	// 0.6.330 WP1：源索引名记忆（见 indexpref.go）。path 为空 = 不落盘。
	prefs *prefStore

	// OnFetched 0.6.312 B3/F8：源抓取成功回调（调用方设置，如 api 层把
	// changelog 全文/releases 明细写入详情磁盘层并从 App 常驻内存移除）。
	// 在 Refresh 内、缓存赋值前同步调用；可修改 apps 内容（未发布前的新
	// map，读方仍持有旧 map，无并发问题）。nil = 不启用（测试友好）。
	OnFetched func(name string, apps map[string]*App)
}

// NewManager 用给定配置创建管理器（不触发网络）。
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg:       cfg,
		cache:     map[string]map[string]*App{},
		fresh:     map[string]time.Time{},
		careEmpty: map[string]int{},
		careRecs:  map[string]careRec{},
		prefs:     newPrefStore(),
	}
}

// AutoCareThreshold 连续「成功但空」达到该轮数后源被自动停用。
const AutoCareThreshold = 5

// SetConfig 在配置变更后换绑。
func (m *Manager) SetConfig(cfg *config.Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

// SetPrefPath 设置「索引名记忆」落盘路径并加载（main 在构造后调用）。
// 返回恢复的条数。
func (m *Manager) SetPrefPath(path string) int {
	if m.prefs == nil {
		m.prefs = newPrefStore()
	}
	return m.prefs.Load(path)
}

// exploreSem 限制后台全量重探的并发（记忆失效的源可能同时出现，避免风暴）。
var exploreSem = make(chan struct{}, 2)

// Refresh 拉取单个源的目录并更新缓存。
func (m *Manager) Refresh(name string) error {
	_, err := m.refresh(name, false)
	return err
}

// refresh forceFull=true 时忽略记忆、放开全部镜像（后台重探路径）。
//
// 返回值 structOK（2026-11-27 F11/N12）：本轮拿到的索引是否是「结构可识别
// 且至少 1 个应用」。false 而 err==nil = 0 条来自「没认出索引结构」，不能当作
// 「源里确实没有应用」——空源计数据此拒绝推进（宁可漏停，不可错停）。
func (m *Manager) refresh(name string, forceFull bool) (structOK bool, err error) {
	m.mu.RLock()
	var ref *config.SourceRef
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name == name {
			ref = &m.cfg.Sources[i]
			break
		}
	}
	m.mu.RUnlock()
	if ref == nil {
		return false, &ErrNoSuchSource{Name: name}
	}
	// 0.6.330 WP1：有记忆时只试记忆中的索引名（4s 预算、fast 镜像）；
	// 失败 → 当轮快速失败 + 清记忆 + 后台全量重探（不阻塞本轮调用方）。
	prefName := ""
	if !forceFull && indexPrefEnabled() {
		prefName, _ = m.prefs.Get(name, ref.URL)
	}
	src := NewSource(name, ref.URL)
	if fd, ok := src.(*FnDepot); ok {
		if forceFull {
			fd.withFullMirrors()
		}
		if prefName != "" {
			fd.withPref(prefName)
		}
	}
	apps, err := src.Fetch()
	if err != nil {
		if prefName != "" {
			m.prefs.Clear(name, ref.URL)
			m.scheduleExplore(name, ref.URL)
		}
		return false, err
	}
	// 0.6.331 P0：陈旧索引拦截。
	//
	// 现象（2026-10-18 深度测试实锤）：重启后健康数据冷，fast 镜像/记忆 4s
	// 预算下「快但陈旧」的副本先回（jsDelivr 无 @ref 形态、个别镜像旧副本），
	// 竞速首个 200 胜出 → 卡片数骤降（2156→2081 / 1836→1814，RROrg 31→9 等）。
	// 判定：条目数较缓存下降超过 20%（原基数 ≥5 才判，小源噪声大）。
	// 处置：强制 raw 直连（无 jsDelivr、全镜像）复核一次——
	//   · 复核更多 → 采信真源；
	//   · 复核不通 → 保留旧缓存（宁可用旧数据，也不用疑似陈旧副本缩表）；
	//   · 复核确认变少 → 采信（允许真实下架）。
	verifiedIdx := ""
	if prevCount := m.cacheCount(name); !forceFull && staleDrop(prevCount, len(apps)) {
		fresh, idx := m.verifyFreshOnly(name, ref.URL)
		switch decideStale(prevCount, len(apps), fresh != nil, len(fresh)) {
		case staleKeepOld:
			log.Printf("[sync] %s 条目 %d→%d（降幅>20%%）且 raw 复核失败：保留旧缓存并安排重探", name, prevCount, len(apps))
			m.prefs.Clear(name, ref.URL)
			m.scheduleExplore(name, ref.URL)
			// 保留旧缓存：本轮条目数不是本源的结论，按「结构可用」处理。
			return true, nil
		case staleUseVerify:
			log.Printf("[sync] %s 条目 %d→%d（降幅>20%%）疑似陈旧索引，已用 raw 直连复核结果 %d 覆盖", name, prevCount, len(apps), len(fresh))
			apps = fresh
			verifiedIdx = idx
		default:
			// 复核确认确实变少（真实下架）或未触发复核：采信本轮结果。
			if fresh != nil {
				log.Printf("[sync] %s 条目 %d→%d 经 raw 直连复核确认为真（允许下架）", name, prevCount, len(apps))
				verifiedIdx = idx
			}
		}
	}
	// 写记忆的三条约束：阶段 1（常规名）命中、非 jsDelivr 兜底、确有文件名。
	if fd, ok := src.(*FnDepot); ok && indexPrefEnabled() {
		if verifiedIdx != "" {
			m.prefs.Put(name, ref.URL, verifiedIdx)
		} else if fd.lastIndex != "" && fd.lastStage == 1 && !strings.Contains(fd.lastWinner, "cdn.jsdelivr.net") {
			m.prefs.Put(name, ref.URL, fd.lastIndex)
		}
	}
	// 0.6.312 B3/F8：缓存赋值前的后处理钩子（详情磁盘层写入 + 重字段剥离）
	if m.OnFetched != nil {
		m.OnFetched(name, apps)
	}
	m.mu.Lock()
	m.cache[name] = apps
	m.fresh[name] = time.Now()
	m.mu.Unlock()
	m.saveIfConfigured() // 成功后落盘（首开/重启秒开的底气）
	// F11/N12：只有「解析出至少 1 条」才算拿到可识别的索引结构。
	// （Parser 现在对空索引直接报错，所以这条在当下是**不变式**而非新行为；
	// 它防的是将来解析器放宽/上游换格式后「0 条且无错」被当成真空源。）
	structOK = true
	if fd, ok := src.(*FnDepot); ok && fd.parsedEntries == 0 {
		structOK = false
	}
	return structOK, nil
}

// cacheCount 当前缓存里该源的条目数（陈旧判定的对照基线）。
func (m *Manager) cacheCount(name string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.cache[name])
}

// staleDrop 判定「疑似陈旧索引」：条目数较基线下降超过 20%，且基线 ≥5
// （小于 5 条的源单条变化即 >20%，噪声太大不拦）。
func staleDrop(prev, cur int) bool {
	return prev >= 5 && cur*5 < prev*4
}

// staleDecision 陈旧索引复核后的处置。
type staleDecision int

const (
	staleAccept    staleDecision = iota // 采信本轮结果（未触发复核 / 复核确认变少）
	staleUseVerify                      // 采信复核结果（真源条目更多）
	staleKeepOld                        // 复核不可用：保留旧缓存，不用疑似陈旧副本缩表
)

func decideStale(prev, cur int, verifyOK bool, verifyCount int) staleDecision {
	if !staleDrop(prev, cur) {
		return staleAccept
	}
	if !verifyOK {
		return staleKeepOld
	}
	if verifyCount > cur {
		return staleUseVerify
	}
	return staleAccept
}

// verifyFreshOnly 用作者仓库的 raw/镜像直连（剔除 jsDelivr、忽略记忆）复核
// 一次索引。返回 (应用集合, 命中的索引文件名)；复核不可用返回 (nil, "")。
// conversun 形态（apps.json 单一来源）不走这条路径。
func (m *Manager) verifyFreshOnly(name, url string) (map[string]*App, string) {
	if IsConversunURL(url) {
		return nil, ""
	}
	fd := NewFnDepot(name, url)
	fd.withFullMirrors()
	fd.withRawOnly()
	apps, err := fd.Fetch()
	if err != nil || len(apps) == 0 {
		return nil, ""
	}
	return apps, fd.lastIndex
}

// scheduleExplore 记忆失效后的后台全量重探：同一源 10min 内最多一次，
// 全局最多 2 个在跑。成功路径与普通刷新一致（写回记忆 + 更新目录缓存）。
func (m *Manager) scheduleExplore(name, url string) {
	if m.prefs == nil || !m.prefs.AllowedExplore(name, url) {
		return
	}
	go func() {
		select {
		case exploreSem <- struct{}{}:
		case <-time.After(time.Second):
			return // 本次让位，下轮刷新再探
		}
		defer func() { <-exploreSem }()
		_, _ = m.refresh(name, true)
	}()
}

// RefreshAll 拉取全部已配置源；单个源失败不影响其他源。
func (m *Manager) RefreshAll() []SourceStatus {
	m.mu.RLock()
	names := make([]string, 0, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		names = append(names, s.Name)
	}
	m.mu.RUnlock()
	var out []SourceStatus
	for _, n := range names {
		st := SourceStatus{Name: n}
		if structOK, err := m.refresh(n, false); err != nil {
			st.Error = err.Error()
		} else {
			m.mu.RLock()
			st.Count = len(m.cache[n])
			st.UpdatedAt = m.fresh[n]
			m.mu.RUnlock()
			st.ParseFailed = !structOK && st.Count == 0
		}
		out = append(out, st)
	}
	return out
}

// RefreshAllConcurrent 并发拉取全部**已启用**源（并发数 n；单源失败不影响其他）。
// 供启动自动同步与周期检查使用——串行版 RefreshAll 在 100+ 源时最坏要 20 分钟。
func (m *Manager) RefreshAllConcurrent(n int) []SourceStatus {
	if n <= 0 {
		n = 8
	}
	m.mu.RLock()
	names := make([]string, 0, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		if s.IsEnabled() {
			names = append(names, s.Name)
		}
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	var mu sync.Mutex
	out := make([]SourceStatus, 0, len(names))
	sem := make(chan struct{}, n)
	for _, nm := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			st := SourceStatus{Name: name}
			if structOK, err := m.refresh(name, false); err != nil {
				st.Error = err.Error()
			} else {
				m.mu.RLock()
				st.Count = len(m.cache[name])
				m.mu.RUnlock()
				st.ParseFailed = !structOK && st.Count == 0
			}
			mu.Lock()
			out = append(out, st)
			mu.Unlock()
		}(nm)
	}
	wg.Wait()
	return out
}

// FreshTimes 返回各源上次成功拉取时间的快照（F5 轮转预算/测试用）。
// 从未拉取（含重启后快照无时间戳）的源不在 map 里 = 视为最陈旧。
func (m *Manager) FreshTimes() map[string]time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]time.Time, len(m.fresh))
	for k, v := range m.fresh {
		out[k] = v
	}
	return out
}

// SelectBudgetedRefresh 0.6.312 B3/F5（源轮转预算）本轮刷新选源：
//  1. 优先级集（官方/收藏/已装应用所在源，由调用方传入）保持小时级——
//     每轮必刷；
//  2. 其余已启用源按新鲜度升序（从未拉取=最旧）只补刷最久未刷的前 budget 个——
//     59 源巨族长尾按 1h/40 源拉平到 ~6h/源；
//  3. 手动 /api/check、reload、sync-all 仍走全量（RefreshAllConcurrent），
//     用户显式动作不受预算限制。
//
// 返回（本轮刷新的源名列表，确定性排序：优先级集在前保持配置序，
// 补刷集按新鲜度升序+名称决胜）。budget<=0 时只刷优先级集。
func (m *Manager) SelectBudgetedRefresh(priority []string, budget int) []string {
	if budget < 0 {
		budget = 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	pr := make(map[string]bool, len(priority))
	for _, n := range priority {
		if n != "" {
			pr[n] = true
		}
	}
	var prio, rest []string
	for _, s := range m.cfg.Sources {
		if !s.IsEnabled() {
			continue
		}
		if pr[s.Name] {
			prio = append(prio, s.Name)
		} else {
			rest = append(rest, s.Name)
		}
	}
	// 补刷集：新鲜度升序（零值=从未拉取，排最前）；同新鲜度按名称决胜（确定性）
	sort.SliceStable(rest, func(i, j int) bool {
		ti, tiOk := m.fresh[rest[i]]
		tj, tjOk := m.fresh[rest[j]]
		if tiOk != tjOk {
			return !tiOk // 未拉取在前
		}
		if tiOk && !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return rest[i] < rest[j]
	})
	if len(rest) > budget {
		rest = rest[:budget]
	}
	return append(append([]string{}, prio...), rest...)
}

// RefreshNames 并发刷新指定源（并发数 n；单源失败不影响其他）。
// 与 RefreshAllConcurrent 同核，供 F5 轮转预算轮次按选定的名单刷新。
func (m *Manager) RefreshNames(names []string, n int) []SourceStatus {
	if n <= 0 {
		n = 8
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	out := make([]SourceStatus, 0, len(names))
	sem := make(chan struct{}, n)
	for _, nm := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			st := SourceStatus{Name: name}
			if structOK, err := m.refresh(name, false); err != nil {
				st.Error = err.Error()
			} else {
				m.mu.RLock()
				st.Count = len(m.cache[name])
				m.mu.RUnlock()
				st.ParseFailed = !structOK && st.Count == 0
			}
			mu.Lock()
			out = append(out, st)
			mu.Unlock()
		}(nm)
	}
	wg.Wait()
	return out
}

// RefreshBudgeted 0.6.312 B3/F5：优先级集必刷 + 其余源新鲜度升序补刷
// budget 个（1 小时一轮时巨族源拉平 ~6h/源）。见 SelectBudgetedRefresh。
// 0.6.315 M2：新增 n 并发参数（n<=0 回落 8，同 RefreshNames 约定）——启动首轮
// 传小并发错峰，压低冷启动堆爆发（启动爆发是 B4 实测保留堆的根因，
// pprof：156 源首刷+目录重建把堆撑大后 Go 保留不还）。
func (m *Manager) RefreshBudgeted(priority []string, budget int, n int) []SourceStatus {
	names := m.SelectBudgetedRefresh(priority, budget)
	return m.RefreshNames(names, n)
}

// sameSourceOrder 按名称序列比较两个源列表是否同序。
func sameSourceOrder(a, b []config.SourceRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// ensureSourceScheme 0.6.141：无协议的源地址统一补 https://
// （修复：Blue-Mink 源以 github.com/… 无协议形式入库，复制出的链接缺 https:// 不可用）
func ensureSourceScheme(u string) string {
	if !strings.Contains(u, "://") {
		return "https://" + u
	}
	return u
}

// HealLegacyURLs 0.6.246：0.6.141 之前的存量数据一次性自愈——无协议的
// 源地址（如 github.com/Blue-Mink/FnDepot）统一补 https:// 写回 cfg。
// 0.6.141 只修了写入路径（AddSource/AddSourcePassive）；老安装里已存在的
// 无协议源（"恢复默认源"按"已存在"去重时会原样保留旧值）一直裸奔，
// 应用源列表显示/复制出来就是不带 http 的地址。启动时在建 Manager
// 之前调用一次；返回修复的源数（0 = 无变化，调用方据此决定是否落盘）。
func HealLegacyURLs(cfg *config.Config) int {
	fixed := 0
	for i := range cfg.Sources {
		u := cfg.Sources[i].URL
		if u != "" && !strings.Contains(u, "://") {
			cfg.Sources[i].URL = ensureSourceScheme(u)
			fixed++
		}
	}
	return fixed
}

// AddSource 校验并添加源（立即拉取验证可达性），返回最终源名。
// 0.6.271：名字与现有源冲突但地址是新地址时（典型：同作者的第二个仓库，
// 自动命名取 owner 撞名）不再报 ErrExists——改用 UniqueSourceName 自动
// 消歧（owner-repo → -2/-3…）后添加；地址与现有源相同仍报 ErrExists
// （同一仓库 = 同一源，协议文件 moo/fnpack 自动共存，moo.json 优先）。
func (m *Manager) AddSource(name, url string) (string, error) {
	name = strings.TrimSpace(name)
	url = ensureSourceScheme(strings.TrimSpace(url))
	if name == "" || url == "" {
		return "", &ErrInvalid{"源名称与地址均不能为空"}
	}
	m.mu.Lock()
	taken := make(map[string]bool, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		taken[s.Name] = true
		// 同一仓库不同形态的链接（仓库根/moo.json 直链/fnpack.json 直链/
		// 镜像前缀）归一化后同地址，视为重复
		if normalizeSourceURL(s.URL) == normalizeSourceURL(url) {
			m.mu.Unlock()
			return "", &ErrExists{"源已存在（与「" + s.Name + "」同地址）"}
		}
	}
	if taken[name] {
		if uname := UniqueSourceName(url, taken); uname != "" && uname != name {
			name = uname
		} else {
			m.mu.Unlock()
			return "", &ErrExists{"源已存在: " + name}
		}
	}
	m.cfg.Sources = append(m.cfg.Sources, config.SourceRef{Name: name, URL: url})
	m.mu.Unlock()
	if err := m.Refresh(name); err != nil {
		// 拉取失败则回滚
		m.mu.Lock()
		for i, s := range m.cfg.Sources {
			if s.Name == name {
				m.cfg.Sources = append(m.cfg.Sources[:i], m.cfg.Sources[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
		return "", err
	}
	return name, nil
}

// AddSourcePassive 校验并添加源，但不立即拉取（供「从 New Store 同步」等
// 批量导入场景：坏源不应因首拉失败而被回滚；目录由后续后台刷新填充）。
// enabled 为 nil 时缺省启用（与 SourceRef 语义一致）。
func (m *Manager) AddSourcePassive(name, url string, enabled *bool) (string, error) {
	name = strings.TrimSpace(name)
	url = ensureSourceScheme(strings.TrimSpace(url))
	if name == "" || url == "" {
		return "", &ErrInvalid{"源名称与地址均不能为空"}
	}
	m.mu.Lock()
	taken := make(map[string]bool, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		taken[s.Name] = true
		if normalizeSourceURL(s.URL) == normalizeSourceURL(url) {
			m.mu.Unlock()
			return "", &ErrExists{"源已存在（与「" + s.Name + "」同地址）"}
		}
	}
	if taken[name] {
		if uname := UniqueSourceName(url, taken); uname != "" && uname != name {
			name = uname
		} else {
			m.mu.Unlock()
			return "", &ErrExists{"源已存在: " + name}
		}
	}
	m.cfg.Sources = append(m.cfg.Sources, config.SourceRef{Name: name, URL: url, Enabled: enabled})
	m.mu.Unlock()
	return name, nil
}

// FlushCache 立即把目录快照落盘（改名后调用，避免旧名残留在启动快照里）。
func (m *Manager) FlushCache() {
	m.saveIfConfigured()
}

// RenameSource 重命名源。应用 key（appname@source）按源名实时计算，
// 改名后自动跟随；目录缓存/拉取时间/空源计数一并迁移到新名。
func (m *Manager) RenameSource(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return &ErrInvalid{"源名称不能为空"}
	}
	if oldName == newName {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := -1
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name == oldName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &ErrNoSuchSource{Name: oldName}
	}
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name == newName {
			return &ErrExists{"源已存在: " + newName}
		}
	}
	m.cfg.Sources[idx].Name = newName
	if apps, ok := m.cache[oldName]; ok {
		// App.Source 在抓取时写入、key/列表筛选/详情查询都按它工作：
		// 改名必须逐条改写，否则改名后的 key（appname@新名）查不到。
		for _, a := range apps {
			if a != nil {
				a.Source = newName
			}
		}
		m.cache[newName] = apps
		delete(m.cache, oldName)
	}
	if t, ok := m.fresh[oldName]; ok {
		m.fresh[newName] = t
		delete(m.fresh, oldName)
	}
	m.careMu.Lock()
	if n, ok := m.careEmpty[oldName]; ok {
		m.careEmpty[newName] = n
		delete(m.careEmpty, oldName)
	}
	if rec, ok := m.careRecs[oldName]; ok {
		m.careRecs[newName] = rec
		delete(m.careRecs, oldName)
	}
	m.careMu.Unlock()
	return nil
}

// RemoveSource 删除源及其缓存。
func (m *Manager) RemoveSource(name string) error {
	m.mu.Lock()
	found := false
	for i, s := range m.cfg.Sources {
		if s.Name == name {
			m.cfg.Sources = append(m.cfg.Sources[:i], m.cfg.Sources[i+1:]...)
			found = true
			break
		}
	}
	delete(m.cache, name)
	delete(m.fresh, name)
	m.careMu.Lock()
	delete(m.careEmpty, name)
	delete(m.careRecs, name)
	m.careMu.Unlock()
	m.mu.Unlock()
	if !found {
		return &ErrNoSuchSource{Name: name}
	}
	return nil
}

// Sources 返回当前源列表（带缓存状态）。
func (m *Manager) Sources() []SourceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SourceStatus, 0, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		st := SourceStatus{Name: s.Name, URL: s.URL}
		if t, ok := m.fresh[s.Name]; ok {
			st.UpdatedAt = t
			st.Count = len(m.cache[s.Name])
			st.CacheAgeSeconds = int(time.Since(t).Seconds())
		}
		// P1-2/P1-3（2026-11-26 CARE）：计数与最近错误回填到源列表。
		// 之前这两个字段只有声明、无赋值 → 前端角标永不显示、死源与
		// 「刚添加未拉取」在界面上无法区分。
		m.careMu.Lock()
		st.EmptyStreak = m.careEmpty[s.Name]
		if rec, ok := m.careRecs[s.Name]; ok {
			st.FailStreak = rec.FailStreak
			st.LastError = rec.LastError
			st.DisabledBy = rec.DisabledBy
			st.HealCapped = rec.HealCapped
			st.HealCount30d = rec.HealCount
			st.LastRecoveredAt = rec.LastRecoveredAt
			st.LastVerdict = rec.LastVerdict
			st.VerdictAt = rec.VerdictAt
		}
		m.careMu.Unlock()
		out = append(out, st)
	}
	return out
}

// Apps 返回应用列表；source 为空时聚合全部已启用源——同名应用**不去重**，
// 各源各留一条（KeyOf 生成 "appname@源名" 唯一 key，列表并排展示带源徽章）。
// source 指定时只查该源。q 为不区分大小写的名称/描述过滤词。
func (m *Manager) Apps(source, q string) []*App {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var picked map[string]*App
	if source != "" {
		picked = m.cache[source]
	} else {
		picked = map[string]*App{}
		for _, s := range m.cfg.Sources {
			if !s.IsEnabled() {
				continue
			}
			for name, a := range m.cache[s.Name] {
				picked[s.Name+"\x00"+name] = a
			}
		}
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]*App, 0, len(picked))
	for _, a := range picked {
		if q != "" &&
			!strings.Contains(strings.ToLower(a.DisplayName), q) &&
			!strings.Contains(strings.ToLower(a.Name), q) &&
			!strings.Contains(strings.ToLower(a.Desc), q) {
			continue
		}
		out = append(out, a)
	}
	// 确定性全序：同名条目按 Source→Name 决胜。Go map 迭代序随机 +
	// sort.Slice 不稳定，不决胜则同名对每次请求换位→/api/apps 字节流
	// 变→ETag 永不命中 304（fn connect 每次打开都重传整包）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Get 查单个应用（按源名 + appname）。
func (m *Manager) Get(source, name string) *App {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if apps, ok := m.cache[source]; ok {
		return apps[name]
	}
	return nil
}

// GetByKey 按目录 key 查应用：key = "appname" 或 "appname@源名"。
// 无 @ 时查第一个含该 appname 的已启用源。
//
// 0.6.208：裸 key 精确未命中时做大小写不敏感回退。已装应用的规范卡 key
// 取自平台 daemon appname（如 "Gitea"），而源 feed 的 appname 可能是
// 另一种大小写（如 conversun feed "gitea"）——精确匹配会恒 404
// 「应用不存在」，导致「有更新」徽章（按规范卡判定）与更新动作（按 key
// 解析源条目）不同源、点击必失败。回退仅接受唯一匹配，歧义仍诚实报错。
func (m *Manager) GetByKey(key string) (*App, error) {
	name, source, hasSource := splitKey(key)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if hasSource {
		if a, ok := m.cache[source][name]; ok {
			return a, nil
		}
		return nil, &ErrNoSuchSource{Name: source}
	}
	for _, s := range m.cfg.Sources {
		if !s.IsEnabled() {
			continue
		}
		if a, ok := m.cache[s.Name][name]; ok {
			return a, nil
		}
	}
	var ciMatch *App
	for _, s := range m.cfg.Sources {
		if !s.IsEnabled() {
			continue
		}
		for k, a := range m.cache[s.Name] {
			if strings.EqualFold(k, name) {
				if ciMatch != nil {
					return nil, &ErrNotFound{Msg: "应用不存在: " + name}
				}
				ciMatch = a
			}
		}
	}
	if ciMatch != nil {
		return ciMatch, nil
	}
	return nil, &ErrNotFound{Msg: "应用不存在: " + name}
}

// splitKey 拆分目录 key（appname 本身不含 @）。
func splitKey(key string) (name, source string, hasSource bool) {
	if i := strings.LastIndex(key, "@"); i > 0 {
		return key[:i], key[i+1:], true
	}
	return key, "", false
}

// KeyOf 生成应用 key：仅一个已启用源含该 appname 时直接用 appname，
// 否则 "appname@源名"（同名共存时前端用它做唯一标识）。
func KeyOf(a *App, all []*App) string {
	count := 1
	for _, o := range all {
		if o != a && o.Name == a.Name {
			count++
		}
	}
	if count > 1 {
		return a.Name + "@" + a.Source
	}
	return a.Name
}

// KeyOfCounted 与 KeyOf 同语义，但用预算好的同名条目数（O(1)）。
// buildCatalog 对 1700+ 条目逐个 KeyOf(a, all) 是 O(n²)（实测每次
// /api/apps、/api/apps/{key} 白烧 50-200ms），改为一次 map 预计数。
func KeyOfCounted(a *App, sameNameCount int) string {
	if sameNameCount > 1 {
		return a.Name + "@" + a.Source
	}
	return a.Name
}

// ToggleSource 开关源。
//
// 2026-11-26 CARE：停用/启用时同步维护自动监测状态——用户手动启用视为
// 「我确认这个源要跑」：空源计数与失败计数清零、清掉策略停用标记（自愈通道
// 只允许复活 auto-care 停用的源，绝不覆盖用户意愿）；用户手动停用则清掉
// 策略标记，避免自愈通道把它当成「策略停用」而擅自复活。
func (m *Manager) ToggleSource(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name != name {
			continue
		}
		v := enabled
		m.cfg.Sources[i].Enabled = &v
		if !enabled {
			delete(m.cache, name)
			delete(m.fresh, name)
		}
		m.careMu.Lock()
		if enabled {
			delete(m.careEmpty, name) // 手动启用：计数从 0 重来
		}
		if rec, ok := m.careRecs[name]; ok {
			if enabled {
				rec.FailStreak = 0
				rec.FirstFailAt = time.Time{}
				// 用户动作优先：手动打开 = 「我确认要跑」，抖动上限、冷却窗与
				// 触顶通知节流一并作废（否则用户开了又会被旧上限立刻再停用）。
				rec.LastRecoveredAt = time.Time{}
				rec.HealCount = 0
				rec.HealWindowStart = time.Time{}
				rec.HealCapped = false
				rec.CappedNoticeAt = time.Time{}
			}
			rec.DisabledBy = "" // 用户动作优先：策略停用标记作废（自愈不再介入）
			rec.HealStreak = 0  // 自愈进度由用户动作重置
			rec.LastHealAt = time.Time{}
			m.setCareRecordLocked(name, rec)
		}
		m.careMu.Unlock()
		m.saveCareAsync()
		return nil
	}
	return &ErrNoSuchSource{Name: name}
}

// ReorderSources 按给定名称顺序重排源（未列出的保持原相对顺序追加在后）。
func (m *Manager) ReorderSources(order []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	sorted := make([]config.SourceRef, 0, len(m.cfg.Sources))
	for _, s := range m.cfg.Sources {
		if _, ok := pos[s.Name]; ok {
			sorted = append(sorted, s)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return pos[sorted[i].Name] < pos[sorted[j].Name]
	})
	rest := make([]config.SourceRef, 0)
	for _, s := range m.cfg.Sources {
		if _, ok := pos[s.Name]; !ok {
			rest = append(rest, s)
		}
	}
	m.cfg.Sources = append(sorted, rest...)
}

// SourceStatus 是源的状态视图。
type SourceStatus struct {
	Name            string    `json:"name"`
	URL             string    `json:"url,omitempty"`
	Count           int       `json:"app_count"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	CacheAgeSeconds int       `json:"cache_age_seconds"`
	Error           string    `json:"error,omitempty"`
	// P1-2（2026-11-26 CARE）：空源 / 失败连续计数（自动监测角标用）。
	EmptyStreak int `json:"empty_streak"`
	FailStreak  int `json:"fail_streak"`
	// P1-3：最近一次刷新失败的简短原因（已脱敏，见 careerr.go）。
	LastError string `json:"last_error,omitempty"`
	// ParseFailed 2026-11-27（F11/N12）：本轮 0 条来自「索引没被识别/无结构」，
	// 而不是「源里确实没应用」。空源计数据此拒绝推进（宁可漏停，不可错停）。
	ParseFailed bool `json:"parse_failed,omitempty"`
	// P1-1c：停用来源标记（auto-care = 策略停用；空 = 未停用/用户手动停用）。
	// 前端据此分因展示「已自动停用（可自动恢复）」与「已关闭」。
	DisabledBy string `json:"disabled_by,omitempty"`
	// ── 抖动上限 / 冷却（2026-11-26 CARE，只读信号）──
	// HealCapped 本窗口内自动恢复次数已用尽：源保持停用，Moo 只低频复查，
	// 不再自动复活。前端据此把角标从「可自动恢复」改成「需手动开启」。
	HealCapped bool `json:"heal_capped,omitempty"`
	// HealCount30d 当前 30 天滚动窗口内已自动恢复的次数（上限 2）。
	HealCount30d int `json:"heal_count_30d,omitempty"`
	// LastRecoveredAt 最近一次自动恢复时间（空源通道 72h 冷却窗的起点）。
	LastRecoveredAt time.Time `json:"last_recovered_at,omitempty"`
	// ── 判定可观测（2026-11-27 FIXPLAN 盲点 #3 · C2，只读信号）──
	// LastVerdict 最近一轮通道复核的结论（alive/dead/uncertain；空 = 从未复核）。
	// VerdictAt 该结论的时间（unix 秒）。两者都不参与任何判定。
	LastVerdict string `json:"last_verdict,omitempty"`
	VerdictAt   int64  `json:"verdict_at,omitempty"`
}

// NormalizeSourceURL 归一化源地址用于去重：去协议/尾斜杠/fnpack.json，小写 host。
// NormalizeSourceURL 把源地址归一化为身份比较用的规范形（小写）：
//   - GitHub 系（github.com / raw.githubusercontent.com / cdn.jsdelivr.net/gh /
//     镜像前缀）→ "github.com/owner/repo"——同一仓库的仓库根、moo.json 直链、
//     fnpack.json 直链、tree 页、镜像前缀都算同一源（协议文件只是同一源的
//     索引文件候选，moo.json 优先，见 mooindex.go）；
//   - 其它主机 → 剥协议、Gitea /raw/branch/<分支>/ 段、/raw/<分支>/ 段、
//     已知索引文件名（moo/fnpack/fndepot/fndepot_v2/apps.json）、.git。
//
// 0.6.271：旧实现只剥 /fnpack.json——moo.json 直链与仓库根被判成两个源
// （同仓库重复入库、应用列两遍、双份同步）；raw moo.json 直链与仓库根也
// 因归一化不同而逃过同地址去重。现与 SourceURLKey 共用 GitHub 系归一。
func NormalizeSourceURL(u string) string {
	if k := SourceURLKey(u); strings.HasPrefix(k, "github.com/") {
		return k
	}
	p := strings.TrimSpace(strings.TrimSuffix(u, "/"))
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = searchKeyGiteaRawSegRe.ReplaceAllString(p, "/")
	p = searchKeyRawSegRe.ReplaceAllString(p, "/")
	for _, f := range searchKeyIndexFiles {
		p = strings.TrimSuffix(p, "/"+f)
	}
	p = strings.TrimSuffix(p, ".git")
	return strings.ToLower(strings.TrimRight(p, "/"))
}

// normalizeSourceURL 包内别名。
func normalizeSourceURL(u string) string { return NormalizeSourceURL(u) }

// ErrNoSuchSource 源不存在。
type ErrNoSuchSource struct{ Name string }

func (e *ErrNoSuchSource) Error() string { return "源不存在: " + e.Name }

// ErrExists 源已存在。
type ErrExists struct{ Msg string }

func (e *ErrExists) Error() string { return e.Msg }

// ErrInvalid 参数非法。
type ErrInvalid struct{ Msg string }

func (e *ErrInvalid) Error() string { return e.Msg }

// ErrNotFound 应用不存在。
type ErrNotFound struct{ Msg string }

func (e *ErrNotFound) Error() string { return e.Msg }
