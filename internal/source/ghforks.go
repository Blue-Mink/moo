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
	repos   map[string]*ghForkInfo
	queue   []string
	queued  map[string]bool
	client  *http.Client
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
		if ts, err := time.Parse(time.RFC3339, e.FetchedAt); err == nil && time.Since(ts) < ttl {
			return // 结果仍新鲜（或在负缓存有效期内）：不重复入队，省额度
		}
		// 时间戳缺失/损坏 → 当作过期，重排一次以自愈。
	}
	if !g.queued[repo] {
		g.queued[repo] = true
		g.queue = append(g.queue, repo)
	}
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
	for _, k := range batch {
		if ctx.Err() != nil {
			break
		}
		if updated >= maxRepos {
			g.requeue(batch[updated:])
			break
		}
		owner, repo, _ := strings.Cut(k, "/")
		err := g.fetchRepo(ctx, owner, repo)
		if err == ErrRateLimited {
			g.requeue(batch[updated:])
			return updated, ErrRateLimited
		}
		if err != nil {
			g.requeue([]string{k}) // 单仓库失败不拖垮整轮：放回队尾下轮重试
			continue
		}
		updated++
	}
	g.save()
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
	g.mu.Unlock()

	u := fmt.Sprintf("%s/repos/%s/%s", strings.TrimRight(g.apiBase, "/"), owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
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
		return
	}
	var doc struct {
		Repos map[string]*ghForkInfo `json:"repos"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	g.mu.Lock()
	if doc.Repos != nil {
		g.repos = doc.Repos
	}
	g.mu.Unlock()
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


