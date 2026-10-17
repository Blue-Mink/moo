package source

// addprobe.go —— 「添加为源」可加性探测（0.6.326e）。
//
// 详情页溯源行的「添加为源」过去无条件显示：上游仓根本没发布索引时，点了必然
// 「所有候选地址均失败 404」（用户实测 710850609/easytier-eui）。本探测器用与
// Fetch **完全同源**的候选展开（ProbeIndex）做预判：
//   - 本仓有索引            → 可加（按钮照旧指向 upstream_repo）；
//   - 本仓无索引但 owner 有  → 可加（按钮改指 owner 的索引仓，如 <owner>/FnDepot）；
//   - 都没有                → 不可加（前端不出按钮，改一行原因说明）。
//
// 不阻塞详情页：命中只读缓存（Lookup），未知即入队、由后台轮次补齐；未知期间
// 前端维持现状（照旧显示按钮）——宁可多显示一个可能失败的按钮，也不误藏。

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// addProbeTTL 已知结果有效期（过期重新排队；探测走 raw，无 API 额度消耗）。
const addProbeTTL = 7 * 24 * time.Hour

// addProbeMaxQueue 待探测队列上限（防御性）。
const addProbeMaxQueue = 512

// addProbeWorkers 并发探测的**仓库**数（0.6.326z4，用户令「addprobe 首轮并发化」）。
//
// 实测问题：首轮逐仓串行，19 仓要 15~35 分钟（单仓最坏 1+12 次候选 × 12s 预算），
// 期间详情页的「添加上游源」按钮一直维持「未知＝照旧显示」的旧状态。
// 并发只作用于「仓库之间」：单个仓库内部仍按候选名单串行、命中即止（顺序语义、
// 最坏请求数上界都不变，候选黑洞时单仓吃满一个 worker）。取 3 而非更高：
// 目标是把首轮压到分钟级，而 raw.githubusercontent/jsDelivr 对 3 路并发无感，
// 再高的收益被「单仓候选串行」与源站限流吃掉。
const addProbeWorkers = 3

// addProbeOwnerRepos owner 名下**已知的索引仓名候选**（按序探测，命中即止）。
//
// 成本：探一个候选 = 1 次 ProbeIndex 调用（内部按 buildCandidates 展开候选并
// 内容校验，与「添加为源」同一套逻辑，单个最坏 12s+12s 预算）。同一 owner 命中
// 即止，故**列表顺序 = 最坏请求数**，越可能命中的越靠前；列表长度即「未知
// owner」相对原实现的最坏增量。
//
// 0.6.326k（盲点 B6）名单 5 → 12，证据来源三类：
//   - 现行 5 个（0.6.326e 引入）原样保留，未新增依赖、未改探测语义；
//   - 审计实测的仓名变体（LINEAGE_BLINDSPOT_326K.md B6 与 §1）：FnDepot-、
//     -FnDepot、FnDepot-arm、FnDepot-1、RROrg-fn-apps、fn_fpk_packages、
//     fn-knock-turborepo（这 7 个即白名单缺口，缺一个就多一个「假死按钮」）；
//   - default_sources_legacy.txt（157 条历史默认源清单）里出现的**索引仓名**
//     （名字含 fndepot/fn-apps/fnos-apps/fpk/packages 特征且不是应用名）：
//     FnDepot 140 次、FnDepot1 2、FnDepot- 2、fn-apps 2、fnos-apps 1、
//     FnDepot-1 1、-FnDepot 1、FnDepot-arm 1、RROrg-fn-apps 1、
//     fn_fpk_packages 1、fn-knock-turborepo 1。判定为应用名（非索引仓）而**未
//     收录**：fntermx / fnos-lxc / LitePan-fpk / fn（各 1 次）。
//   - 大小写：GitHub 仓库名解析不区分大小写，但 raw 路径历史上出现过大小写敏感
//     实锤（326d 的 SmartDNS 目录案例），且 0.6.326c 现网见过 shuangji66/Fndepot
//     这类混写，故 FnDepot 与 fndepot 两条都留（仅首字母大小写差的探测是冗余
//     保险，代价 ≤1 次）。
//
// 排序依据（命中概率）：FnDepot（140/157 历史源 + 现网主流）→ 大小写变体
// fndepot → FnDepot1 → 带后缀四兄弟 FnDepot- / FnDepot-1 / -FnDepot /
// FnDepot-arm（FnDepot 家族合计 145/157）→ fnos-apps / fn-apps →
// owner 前缀（RROrg-fn-apps）/ 下划线（fn_fpk_packages）/ 应用仓（fn-knock-turborepo）
// 三个尾部变体。副作用（有意为之）：fnos-apps、fn-apps 各后移 4 位（+4 次请求），
// 换来 FnDepot 后缀家族整体前移 1~3 位；两类形态都属「同一 owner 命中即止」，
// 只在候选全落空时才付全额。
var addProbeOwnerRepos = []string{
	"FnDepot",
	"fndepot",
	"FnDepot1",
	"FnDepot-",
	"FnDepot-1",
	"-FnDepot",
	"FnDepot-arm",
	"fnos-apps",
	"fn-apps",
	"RROrg-fn-apps",
	"fn_fpk_packages",
	"fn-knock-turborepo",
}

type addProbeInfo struct {
	Index      string `json:"index,omitempty"`       // 本仓命中的索引文件名
	OwnerIndex string `json:"owner_index,omitempty"` // 本仓无索引时，owner 的索引仓 owner/repo
	FetchedAt  string `json:"fetched_at"`
}

// AddProbe 是「添加为源」可加性探测器（并发安全；NewAddProbe 构造）。
type AddProbe struct {
	mu     sync.Mutex
	path   string
	repos  map[string]*addProbeInfo
	queue  []string
	queued map[string]bool
	probe  func(repoURL string) string // 可注入（测试）；默认 ProbeIndex
	kick   chan struct{}               // 新任务入队信号（唤醒后台轮次）
}

// NewAddProbe 创建探测器并加载落盘缓存（损坏容忍：读不了当空）。
func NewAddProbe(cachePath string) *AddProbe {
	a := &AddProbe{
		path:   cachePath,
		repos:  map[string]*addProbeInfo{},
		queued: map[string]bool{},
		probe:  ProbeIndex,
		kick:   make(chan struct{}, 1),
	}
	a.load()
	return a
}

// WaitNext 阻塞直到「有新任务入队」/ d 到期 / ctx 取消。
//
// 目录（buildCatalog）只在 /api/apps 被请求时才构建 —— 若后台轮次用固定
// 定时等，装完不打开应用就会白等一整个周期（用户看到的还是死按钮）。
// 故 Enqueue 时会 Kick，轮次立刻开跑。
func (a *AddProbe) WaitNext(ctx context.Context, d time.Duration) {
	if a == nil {
		select {
		case <-ctx.Done():
		case <-time.After(d):
		}
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-a.kick:
	case <-t.C:
	}
}

// SetForTest 注入一条已知结果（测试用）。
func (a *AddProbe) SetForTest(repo, index, ownerIndex string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.repos[strings.ToLower(strings.TrimSpace(repo))] = &addProbeInfo{
		Index: index, OwnerIndex: ownerIndex, FetchedAt: time.Now().Format(time.RFC3339)}
}

// SetProbeForTest 注入探测函数（测试用；默认 ProbeIndex 走网络）。
func (a *AddProbe) SetProbeForTest(fn func(repoURL string) string) {
	if a == nil || fn == nil {
		return
	}
	a.mu.Lock()
	a.probe = fn
	a.mu.Unlock()
}

// EnqueueForTest 无视有效期强制入队并唤醒轮次（测试用；Enqueue 会跳过新鲜结果）。
func (a *AddProbe) EnqueueForTest(repo string) {
	if a == nil {
		return
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	if repo == "" || strings.Count(repo, "/") != 1 {
		return
	}
	a.mu.Lock()
	if !a.queued[repo] {
		a.queued[repo] = true
		a.queue = append(a.queue, repo)
	}
	a.mu.Unlock()
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

// Lookup 查可加性：known=false = 还没探过（调用方应维持现状）。
func (a *AddProbe) Lookup(repo string) (index, ownerIndex string, known bool) {
	if a == nil {
		return "", "", false
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.repos[repo]
	if !ok || e == nil {
		return "", "", false
	}
	return e.Index, e.OwnerIndex, true
}

// Enqueue 排入待探测队列（未知或已过期才排；幂等、不阻塞，可在目录热路径调用）。
func (a *AddProbe) Enqueue(repo string) {
	if a == nil {
		return
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	if repo == "" || strings.Count(repo, "/") != 1 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) >= addProbeMaxQueue {
		return
	}
	if e, ok := a.repos[repo]; ok && e != nil {
		if ts, err := time.Parse(time.RFC3339, e.FetchedAt); err == nil && time.Since(ts) < addProbeTTL {
			return
		}
	}
	if !a.queued[repo] {
		a.queued[repo] = true
		a.queue = append(a.queue, repo)
		// 非阻塞唤醒后台轮次（缓冲 1）：目录首次构建入队那一刻就能开跑，
		// 不必等 6 小时周期。
		select {
		case a.kick <- struct{}{}:
		default:
		}
	}
}

// RefreshRound 从队列探测最多 maxRepos 个仓库（最久未探优先）。返回实际探测数。
func (a *AddProbe) RefreshRound(ctx context.Context, maxRepos int) int {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	sort.SliceStable(a.queue, func(i, j int) bool {
		fi, fj := "", ""
		if e, ok := a.repos[a.queue[i]]; ok && e != nil {
			fi = e.FetchedAt
		}
		if e, ok := a.repos[a.queue[j]]; ok && e != nil {
			fj = e.FetchedAt
		}
		return fi < fj
	})
	batch := a.queue
	a.queue = nil
	for _, k := range batch {
		delete(a.queued, k)
	}
	a.mu.Unlock()

	// 本轮上限：排序后取最久未探的前 maxRepos 个。
	if maxRepos <= 0 || maxRepos > len(batch) {
		maxRepos = len(batch)
	}
	batch = batch[:maxRepos]

	type probeResult struct {
		repo       string
		index      string
		ownerIndex string
	}
	results := make(chan probeResult, len(batch))
	sem := make(chan struct{}, addProbeWorkers)
	var wg sync.WaitGroup
	for _, k := range batch {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			index := a.probe("https://github.com/" + k)
			ownerIndex := ""
			if index == "" {
				owner, _, _ := strings.Cut(k, "/")
				for _, nm := range addProbeOwnerRepos {
					cand := owner + "/" + nm
					if cand == k {
						continue
					}
					if a.probe("https://github.com/"+cand) != "" {
						ownerIndex = cand
						break
					}
				}
			}
			select {
			case results <- probeResult{repo: k, index: index, ownerIndex: ownerIndex}:
			case <-ctx.Done():
			}
		}(k)
	}
	go func() { wg.Wait(); close(results) }()

	// 结果写回与落盘统一在本 goroutine 串行处理（a.repos/a.save 不并发）。
	done := 0
	for r := range results {
		a.mu.Lock()
		a.repos[r.repo] = &addProbeInfo{
			Index: r.index, OwnerIndex: r.ownerIndex, FetchedAt: time.Now().Format(time.RFC3339)}
		a.mu.Unlock()
		done++
		// 逐条落盘：一轮可能几十个仓库、每个最多 24s（候选黑洞），整轮结束才存
		// 会让 UI 几十分钟看不到提示；逐条存让结果边探边生效（文件仅几 KB）。
		a.save()
	}
	a.save()
	return done
}

// load 读落盘缓存（损坏容忍）。
func (a *AddProbe) load() {
	if a.path == "" {
		return
	}
	raw, err := readWholeFile(a.path)
	if err != nil {
		return
	}
	var doc struct {
		Repos map[string]*addProbeInfo `json:"repos"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	a.mu.Lock()
	if doc.Repos != nil {
		a.repos = doc.Repos
	}
	a.mu.Unlock()
}

// save 原子落盘。
func (a *AddProbe) save() {
	if a.path == "" {
		return
	}
	a.mu.Lock()
	doc := struct {
		Repos map[string]*addProbeInfo `json:"repos"`
	}{Repos: a.repos}
	a.mu.Unlock()
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	writeAtomic(a.path, raw)
}
