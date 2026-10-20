package source

// ghforks.go —— GitHub 仓库 fork 关系解析（0.6.326c 溯源补强）。
//
// 用途：识别「整仓 fork 式搬运」——搬运者把某个搬运仓（如 EWEDLCM/FnDepot）
// 整个 fork 走，并把索引里的 download_url 改写成自己的仓（包路径不变）。
// 此时 releaseOrigin(包地址) 的 owner 与「本源仓库」owner 相同，旧判定会当成
// 「作者自建仓」放过 → 溯源永不出现（用户实测 picoclaw 33 张卡全漏）。
// fork=true 时 GitHub 直接给出 parent = 真正的上游仓。
//
// 成本/限流：匿名 API 60 次/小时。策略与 ghstats.go 同款——每轮最多 maxRepos
// 个（未知/最久未拉优先）、ETag 条件请求（304 不计数额度）、限流余量为 0
// 立即停手、缓存落盘重启不丢、404 负缓存 7 天后重查（见 ghForkNotFoundTTL）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ghForkTTL 已知结果的有效期：过期后重新排队（ETag 命中则 304 不耗额度）。
const ghForkTTL = 72 * time.Hour

// ghForkNotFoundTTL 负缓存（404/私有）有效期：过期后允许重查一次。
//
// 原实现把 NotFound 当永久结论（Enqueue 直接 return）→ 源仓库一旦改名、
// 转公开或短暂 404（GitHub 抖动、迁移），fork 关系就**永远**不会再被拉取，
// 该源的整仓 fork 溯源与自动纠正静默失效且无人可察。7 天 = 自愈窗口足够长
// 且重查成本极低（159 源仓里 404 通常是 0 条，全量重查也只是每 7 天一次）。
const ghForkNotFoundTTL = 7 * 24 * time.Hour

// ghForkMaxQueue 待拉队列上限（防御性）：ghForksRound 会顺带把 fork parent
// 也入队（二层链发现），加上源列表仓库与失败回队，队列理论上有增长来源。
// 超出上限直接丢弃新入队请求——丢的只是「本轮没排上」，下轮源列表仍会重排，
// 不会永久漏掉仓库。
const ghForkMaxQueue = 512

// ghForkConcurrency fork 关系并发拉取路数（0.6.340）。串行时单仓约 0.6s、
// 坏网络下单仓可用满 10s 超时；4 路并发把 159 个源仓从「约 100s（正常）/
// 40 分钟（抖动）」压到约半分钟，且对 GitHub 仍是温和负载。
const ghForkConcurrency = 4

type ghForkInfo struct {
	Fork      bool   `json:"fork"`
	Parent    string `json:"parent,omitempty"`    // owner/repo（小写）
	NotFound  bool   `json:"not_found,omitempty"` // 404/私有：负缓存
	ETag      string `json:"etag,omitempty"`
	FetchedAt string `json:"fetched_at"`
}

// GHForks 是 fork 关系查询器（并发安全；用 NewGHForks 构造）。
type GHForks struct {
	mu      sync.Mutex
	path    string
	apiBase string // 默认 https://api.github.com（测试可指向 httptest）
	token   string // 可选 GitHub token（0.6.334）：有则 5000/h，匿名 60/h
	repos   map[string]*ghForkInfo
	queue   []string
	queued  map[string]bool
	client  *http.Client
}

// SetToken 设置 GitHub API token（空串 = 回到匿名）。
//
// 为什么需要（0.6.334）：匿名 60 次/小时，159 个源仓冷启动要 4 小时以上
// 才补满 fork 关系（用户实测：覆盖率从 53% 掉到 45%，picoclaw 33 张卡
// 全无溯源）。带 token 5000 次/小时，冷装 2 分钟内可补满。
// token 只在本进程内用于 Authorization 头，不写日志、不下发到前端。
func (g *GHForks) SetToken(tok string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.token = strings.TrimSpace(tok)
	g.mu.Unlock()
}

// HasToken 是否配置了 token（决定拉取预算/轮询间隔）。
func (g *GHForks) HasToken() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.token != ""
}

var ghForksClient = &http.Client{Timeout: 15 * time.Second}

// NewGHForks 创建查询器并加载落盘缓存（损坏容忍：读不了当空）。
func NewGHForks(cachePath string) *GHForks {
	g := &GHForks{
		path:    cachePath,
		apiBase: "https://api.github.com",
		repos:   map[string]*ghForkInfo{},
		queued:  map[string]bool{},
		client:  ghForksClient,
	}
	g.load()
	return g
}

// SetForTest 注入一条已知结果（parent 为空 = 该仓库不是 fork）。
func (g *GHForks) SetForTest(repo, parent string) {
	repo = strings.ToLower(strings.TrimSpace(repo))
	parent = strings.ToLower(strings.TrimSpace(parent))
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repos[repo] = &ghForkInfo{Fork: parent != "", Parent: parent,
		FetchedAt: time.Now().Format(time.RFC3339)}
}

// SetAPIBaseForTest 注入 API 基址（测试用：指向 httptest 假 GitHub）。
// 与 SetForTest 同属测试钩子；生产路径只在 NewGHForks 里默认
// https://api.github.com，不会调用本方法。
func (g *GHForks) SetAPIBaseForTest(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		return
	}
	g.mu.Lock()
	g.apiBase = base
	g.mu.Unlock()
}

// Lookup 查已知 fork 关系；known=false = 还没拉过（调用方应保守放过）。
func (g *GHForks) Lookup(repo string) (parent string, isFork bool, known bool) {
	if g == nil {
		return "", false, false
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.repos[repo]
	if !ok || e == nil {
		return "", false, false
	}
	return e.Parent, e.Fork, true
}

// Enqueue 排入待拉队列（未知或已过期才排；幂等、不阻塞，可在目录热路径调用）。
func (g *GHForks) Enqueue(repo string) {
	if g == nil {
		return
	}
	repo = strings.ToLower(strings.TrimSpace(repo))
	if repo == "" || strings.Count(repo, "/") != 1 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.queue) >= ghForkMaxQueue {
		return // 队列已满：本轮不排（下轮源列表仍会重排，不会永久漏）
	}
	if e, ok := g.repos[repo]; ok && e != nil {
		ttl := ghForkTTL
		if e.NotFound {
			ttl = ghForkNotFoundTTL // 负缓存也过期重查（改名/转公开可自愈）
		}
		if ts, err := parseForkTime(e.FetchedAt); err == nil && time.Since(ts) < ttl {
			return // 结果仍新鲜（或在负缓存有效期内）：不重复入队，省额度
		}
		// 时间戳缺失/损坏 → 当作过期，重排一次以自愈。
	}
	if !g.queued[repo] {
		g.queued[repo] = true
		g.queue = append(g.queue, repo)
	}
}

// parseForkTime 解析缓存时间戳：兼容 RFC3339（`+08:00`）与 Go 默认布局
// （`+0800`）。0.6.340：预取脚本/手工写入的缓存常用 `2026-10-19T00:14:57+0800`，
// 严格按 RFC3339 解析会失败 → 该条被当成「时间戳缺失/过期」→ 每轮重新入队，
// token 档 15 秒一轮时等于持续白拉全部仓库。
func parseForkTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02T15:04:05-0700", s)
}

// RefreshRound 从队列拉最多 maxRepos 个仓库（未知/最久未拉优先）；
// 限流余量耗尽或队列为空时提前返回。返回实际拉取数。
func (g *GHForks) RefreshRound(ctx context.Context, maxRepos int) (int, error) {
	g.mu.Lock()
	byAge := func(i, j int) bool {
		fi, fj := "", ""
		if e, ok := g.repos[g.queue[i]]; ok && e != nil {
			fi = e.FetchedAt
		}
		if e, ok := g.repos[g.queue[j]]; ok && e != nil {
			fj = e.FetchedAt
		}
		return fi < fj
	}
	sort.SliceStable(g.queue, byAge)
	batch := g.queue
	g.queue = nil
	for _, k := range batch {
		delete(g.queued, k) // 出队即清标记，失败回队时才能重新入队
	}
	g.mu.Unlock()

	updated := 0
	// 0.6.340：并发拉取（4 路）。原实现串行 + 单仓 15s 超时：GitHub API 在
	// 本机出口经常单仓挂满超时，159 个源仓最坏要 40 分钟才补满（用户实测
	// 「溯源不够快」的直接成因之一）。4 路 + 8s 超时后，实测 159 仓 ≈ 30s。
	limit := len(batch)
	if maxRepos < limit {
		limit = maxRepos
	}
	if limit < len(batch) {
		g.requeue(batch[limit:]) // 超预算的留在队里，下轮继续
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		limited  bool
		failed   []string
		okCounts int
	)
	sem := make(chan struct{}, ghForkConcurrency)
	for _, k := range batch[:limit] {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			owner, repo, _ := strings.Cut(k, "/")
			err := g.fetchRepo(ctx, owner, repo)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == ErrRateLimited:
				// 限流 = 这个仓库没有任何结论（不得当「非 fork」落库），
				// 与其它失败一样回队等下轮（0.6.340 并发化时补齐）。
				limited = true
				failed = append(failed, k)
			case err != nil:
				failed = append(failed, k) // 单仓库失败不拖垮整轮：放回队尾下轮重试
			default:
				okCounts++
			}
		}(k)
	}
	wg.Wait()
	updated = okCounts
	if len(failed) > 0 {
		g.requeue(failed)
	}
	g.save()
	if limited {
		return updated, ErrRateLimited
	}
	return updated, nil
}

func (g *GHForks) requeue(keys []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range keys {
		if !g.queued[k] {
			g.queued[k] = true
			g.queue = append(g.queue, k)
		}
	}
}

type ghRepoMeta struct {
	Fork   bool `json:"fork"`
	Parent *struct {
		FullName string `json:"full_name"`
	} `json:"parent"`
}

// fetchRepo 拉一个仓库的 fork 关系（ETag 条件请求：304 = 未变，不耗额度）。
func (g *GHForks) fetchRepo(ctx context.Context, owner, repo string) error {
	k := strings.ToLower(owner + "/" + repo)
	g.mu.Lock()
	etag := ""
	if e, ok := g.repos[k]; ok && e != nil {
		etag = e.ETag
	}
	token := g.token
	g.mu.Unlock()

	u := fmt.Sprintf("%s/repos/%s/%s", strings.TrimRight(g.apiBase, "/"), owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		g.mu.Lock()
		if e, ok := g.repos[k]; ok && e != nil {
			e.FetchedAt = time.Now().Format(time.RFC3339)
		}
		g.mu.Unlock()
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// 私有/已删/改名：负缓存，不再重试（溯源保守放过）
		g.store(k, &ghForkInfo{NotFound: true, FetchedAt: time.Now().Format(time.RFC3339)})
		return nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrRateLimited
		}
		return fmt.Errorf("GitHub API %d（限流剩余 %s）", resp.StatusCode, resp.Header.Get("X-RateLimit-Remaining"))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub API HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var meta ghRepoMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("GitHub repo 解析失败: %w", err)
	}
	info := &ghForkInfo{Fork: meta.Fork, ETag: resp.Header.Get("Etag"),
		FetchedAt: time.Now().Format(time.RFC3339)}
	if meta.Fork && meta.Parent != nil {
		info.Parent = strings.ToLower(strings.TrimSpace(meta.Parent.FullName))
	}
	g.store(k, info)
	return nil
}

func (g *GHForks) store(k string, info *ghForkInfo) {
	g.mu.Lock()
	g.repos[k] = info
	g.mu.Unlock()
}

// load 读落盘缓存（损坏容忍：读不了当空）。
func (g *GHForks) load() {
	if g.path == "" {
		return
	}
	raw, err := readWholeFile(g.path)
	if err != nil {
		// 0.6.340：缓存缺失/不可读是最容易被忽略的「溯源回退」成因——
		// ghforks.json 被清理后，全部仓库变未知 → 整仓 fork 溯源静默失效，
		// 而旧实现连一行日志都没有（2026-10-19 主 NAS 实测：缓存文件消失
		// 后 34 张 picoclaw 卡溯源从 31 张掉到 0 张，无人可察）。
		log.Printf("[gh-forks] fork 缓存不可读（首次启动或文件缺失）: %v；将按全量重拉", err)
		return
	}
	var doc struct {
		Repos map[string]*ghForkInfo `json:"repos"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		log.Printf("[gh-forks] fork 缓存解析失败（按空处理）: %v", err)
		return
	}
	g.mu.Lock()
	if doc.Repos != nil {
		g.repos = doc.Repos
	}
	g.mu.Unlock()
	if len(g.repos) == 0 {
		log.Printf("[gh-forks] fork 缓存为空；将按全量重拉（溯源在补满前不显示）")
	}
}

// save 原子落盘。
func (g *GHForks) save() {
	if g.path == "" {
		return
	}
	g.mu.Lock()
	doc := struct {
		Repos map[string]*ghForkInfo `json:"repos"`
	}{Repos: g.repos}
	g.mu.Unlock()
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	writeAtomic(g.path, raw)
}
