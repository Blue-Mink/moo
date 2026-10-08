package source

import (
	"fmt"
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
	// 刷新失败不计）。进程内计数，重启归零（连续 5 轮按默认 24h 间隔约 5 天）。
	careEmpty map[string]int

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

// Refresh 拉取单个源的目录并更新缓存。
func (m *Manager) Refresh(name string) error {
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
		return &ErrNoSuchSource{Name: name}
	}
	apps, err := NewSource(name, ref.URL).Fetch()
	if err != nil {
		return err
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
	return nil
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
		if err := m.Refresh(n); err != nil {
			st.Error = err.Error()
		} else {
			m.mu.RLock()
			st.Count = len(m.cache[n])
			st.UpdatedAt = m.fresh[n]
			m.mu.RUnlock()
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
			if err := m.Refresh(name); err != nil {
				st.Error = err.Error()
			} else {
				m.mu.RLock()
				st.Count = len(m.cache[name])
				m.mu.RUnlock()
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
			if err := m.Refresh(name); err != nil {
				st.Error = err.Error()
			} else {
				m.mu.RLock()
				st.Count = len(m.cache[name])
				m.mu.RUnlock()
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
func (m *Manager) RefreshBudgeted(priority []string, budget int) []SourceStatus {
	names := m.SelectBudgetedRefresh(priority, budget)
	return m.RefreshNames(names, 8)
}

// AutoCarePass 一轮「应用源自动监测」策略，在后台刷新轮结束后调用
// （sts 来自 RefreshAll / RefreshAllConcurrent / RefreshBudgeted 等；
// 单源手动同步不计数）。0.6.312 B3/F5 轮转预算后 sts 只含本轮实际刷新
// 的源：未排到的源计数不变（不递增也不清零），语义与全量轮一致。
//
//  1. 刷新成功但 0 应用、连续达 AutoCareThreshold 轮 → 自动停用该源
//  2. 本轮刷新成功的空源在源列表沉底（稳定分区：两组内部保序，
//     手动排序/停用源不受影响）
//
// 计数与配置变更都在 m.cfg 上进行，**持久化由调用方负责**（Save + 日志 +
// 目录缓存失效）。策略关闭（cfg.SourceAutoCareOff）或无动作时返回 nil。
func (m *Manager) AutoCarePass(sts []SourceStatus) []string {
	if m.cfg.SourceAutoCareOff {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// 本轮成功刷新的源 → 应用数（失败源不计数、不沉底：网络故障≠空源）
	counts := make(map[string]int, len(sts))
	for _, st := range sts {
		if st.Error != "" {
			continue
		}
		counts[st.Name] = st.Count
		if st.Count == 0 {
			m.careEmpty[st.Name]++
		} else {
			delete(m.careEmpty, st.Name)
		}
	}

	var actions []string
	// 1) 连续空 N 轮 → 停用
	for name, n := range m.careEmpty {
		if n < AutoCareThreshold {
			continue
		}
		for i := range m.cfg.Sources {
			if m.cfg.Sources[i].Name == name && m.cfg.Sources[i].IsEnabled() {
				off := false
				m.cfg.Sources[i].Enabled = &off
				actions = append(actions, fmt.Sprintf("源「%s」连续 %d 次刷新无应用，已自动停用", name, n))
			}
		}
		delete(m.careEmpty, name)
	}
	// 2) 空源沉底（仅重排；停用源保留原位由用户管理）
	if len(counts) > 0 {
		kept := make([]config.SourceRef, 0, len(m.cfg.Sources))
		sunk := make([]config.SourceRef, 0)
		for _, s := range m.cfg.Sources {
			if c, ok := counts[s.Name]; ok && c == 0 {
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
				actions = append(actions, fmt.Sprintf("空源沉底: %s", strings.Join(names, "、")))
			}
		}
	}
	if len(actions) == 0 {
		return nil
	}
	return actions
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
	if n, ok := m.careEmpty[oldName]; ok {
		m.careEmpty[newName] = n
		delete(m.careEmpty, oldName)
	}
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
func (m *Manager) ToggleSource(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.cfg.Sources {
		if m.cfg.Sources[i].Name == name {
			v := enabled
			m.cfg.Sources[i].Enabled = &v
			if !enabled {
				delete(m.cache, name)
				delete(m.fresh, name)
			}
			return nil
		}
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
