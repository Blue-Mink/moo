package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// GHStats 同步 GitHub Release 资产的下载量（assets[].download_count）。
// 背景：FnDepot 规范第三方源不在 fnpack.json 提供 download_count（外部源
// 不统计下载量），但 FPK 基本都挂在 GitHub Releases 上——Release 资产有
// 真实的全网下载次数。按仓库批量拉（59 个源仓库覆盖 895 个应用），
// 匿名 API 限流 60 次/小时：ETag 条件请求（304 不计数）、最旧优先轮换、
// 限流余量为 0 时立即停手，缓存落盘重启不丢。
// 展示层零改动：目录合并时填入 AppInfo.DownloadCount，前端按
// 「N 次下载」渲染（与官方下载量同款回退链的最优先位）。

var ghReleaseURLRe = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/releases/download/[^/]+/(.+)$`)

// ParseGhReleaseURL 从 Release 下载链接解析 owner/repo/资产文件名。
// 非 GitHub Release 链接（raw/jsDelivr/其它托管）返回 ok=false。
func ParseGhReleaseURL(raw string) (owner, repo, asset string, ok bool) {
	m := ghReleaseURLRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", "", false
	}
	asset = m[3]
	if u, err := url.PathUnescape(asset); err == nil && u != "" {
		asset = u
	}
	return m[1], m[2], asset, true
}

type ghRepoStats struct {
	ETag      string         `json:"etag"`
	FetchedAt string         `json:"fetched_at"`
	Assets    map[string]int `json:"assets"` // 资产文件名 → download_count
}

type GHStats struct {
	mu      sync.Mutex
	path    string
	apiBase string // 默认 https://api.github.com（测试可指向 httptest）
	stats   map[string]*ghRepoStats // repoKey(owner/repo) → 统计
	queue   []string                // 待拉仓库（最旧优先轮换）
	queued  map[string]bool
	client  *http.Client
}

var ghStatsClient = &http.Client{Timeout: 15 * time.Second}

// NewGHStats 创建同步器并加载落盘缓存（损坏容忍：读不了当空）。
func NewGHStats(cachePath string) *GHStats {
	g := &GHStats{
		path:    cachePath,
		apiBase: "https://api.github.com",
		stats:   map[string]*ghRepoStats{},
		queued:  map[string]bool{},
		client:  ghStatsClient,
	}
	g.load()
	return g
}

func (g *GHStats) repoKey(owner, repo string) string { return owner + "/" + repo }

// Enqueue 把一批 Release 链接涉及的仓库排入待拉队列（幂等、非阻塞，
// buildCatalog 热路径调用无负担）。
func (g *GHStats) Enqueue(releaseURLs []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, u := range releaseURLs {
		owner, repo, _, ok := ParseGhReleaseURL(u)
		if !ok {
			continue
		}
		k := g.repoKey(owner, repo)
		if !g.queued[k] {
			g.queued[k] = true
			g.queue = append(g.queue, k)
		}
	}
}

// Lookup 查仓库里某资产的下载量。
func (g *GHStats) Lookup(owner, repo, asset string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rs, ok := g.stats[g.repoKey(owner, repo)]
	if !ok || rs == nil || len(rs.Assets) == 0 {
		return 0, false
	}
	n, ok := rs.Assets[asset]
	return n, ok
}

// ErrRateLimited 限流余量耗尽（本轮停手，下一轮继续）。
var ErrRateLimited = fmt.Errorf("GitHub API 限流余量为 0，本轮暂停")

// RefreshRound 从队列拉最多 maxRepos 个仓库（最久未拉优先），
// 限流余量耗尽或队列为空时提前返回。返回实际拉取的仓库数。
func (g *GHStats) RefreshRound(ctx context.Context, maxRepos int) (int, error) {
	g.mu.Lock()
	// 最久未拉优先：按 FetchedAt 升序排（没拉过的最前）
	byAge := func(i, j int) bool {
		fi, fj := "", ""
		if s, ok := g.stats[g.queue[i]]; ok && s != nil {
			fi = s.FetchedAt
		}
		if s, ok := g.stats[g.queue[j]]; ok && s != nil {
			fj = s.FetchedAt
		}
		return fi < fj
	}
	sort.SliceStable(g.queue, byAge)
	batch := g.queue
	g.queue = nil
	for _, k := range batch {
		delete(g.queued, k) // 出队即清标记，失败/限流回队时才能重新入队
	}
	g.mu.Unlock()

	updated := 0
	for _, k := range batch {
		if ctx.Err() != nil {
			break
		}
		if updated >= maxRepos {
			g.requeueRest(batch[updated:])
			break
		}
		owner, repo, _ := strings.Cut(k, "/")
		err := g.fetchRepo(ctx, owner, repo)
		if err == ErrRateLimited {
			g.requeueRest(batch[updated:])
			return updated, ErrRateLimited
		}
		if err != nil {
			// 单仓库失败不拖垮整轮：放回队尾下一轮重试
			g.requeueOne(k)
			continue
		}
		updated++
	}
	g.save()
	return updated, nil
}

func (g *GHStats) requeueRest(keys []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range keys {
		if !g.queued[k] {
			g.queued[k] = true
			g.queue = append(g.queue, k)
		}
	}
}

func (g *GHStats) requeueOne(k string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.queued[k] {
		g.queued[k] = true
		g.queue = append(g.queue, k)
	}
}

type ghReleaseAsset struct {
	Name          string `json:"name"`
	DownloadCount int    `json:"download_count"`
}

type ghRelease struct {
	Assets []ghReleaseAsset `json:"assets"`
}

// fetchRepo 拉一个仓库的全部 Release 资产下载量（最多 3 页 × 100 条）。
func (g *GHStats) fetchRepo(ctx context.Context, owner, repo string) error {
	g.mu.Lock()
	etag := ""
	if s, ok := g.stats[g.repoKey(owner, repo)]; ok && s != nil {
		etag = s.ETag
	}
	g.mu.Unlock()

	for page := 1; page <= 3; page++ {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100", strings.TrimRight(g.apiBase, "/"), owner, repo)
		if page > 1 {
			u += fmt.Sprintf("&page=%d", page)
		}
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
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			return nil // 304：数据未变，不消耗限流
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			remain := resp.Header.Get("X-RateLimit-Remaining")
			if remain == "0" {
				return ErrRateLimited
			}
			return fmt.Errorf("GitHub API %d（限流剩余 %s）", resp.StatusCode, remain)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("GitHub API HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}

		var releases []ghRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return fmt.Errorf("GitHub releases 解析失败: %w", err)
		}

		// 合并本页资产（跨 Release 同名资产以最新 Release 为准——
		// API 已按发布时间倒序，先写者保留）
		g.mu.Lock()
		k := g.repoKey(owner, repo)
		rs, ok := g.stats[k]
		if !ok {
			rs = &ghRepoStats{Assets: map[string]int{}}
			g.stats[k] = rs
		}
		for _, r := range releases {
			for _, a := range r.Assets {
				if a.Name == "" {
					continue
				}
				if _, exists := rs.Assets[a.Name]; !exists {
					rs.Assets[a.Name] = a.DownloadCount
				}
			}
		}
		rs.ETag = resp.Header.Get("Etag")
		rs.FetchedAt = time.Now().Format(time.RFC3339)
		g.mu.Unlock()

		if len(releases) < 100 {
			return nil // 没有更多页
		}
	}
	return nil
}

// readWholeFile 读小文件（缓存用途）。
func readWholeFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeAtomic 临时文件 + rename 原子写。
func writeAtomic(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// load 读落盘缓存。
func (g *GHStats) load() {
	if g.path == "" {
		return
	}
	raw, err := readWholeFile(g.path)
	if err != nil {
		return
	}
	var doc struct {
		Stats map[string]*ghRepoStats `json:"stats"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	g.stats = doc.Stats
	if g.stats == nil {
		g.stats = map[string]*ghRepoStats{}
	}
}

// save 原子落盘。
func (g *GHStats) save() {
	if g.path == "" {
		return
	}
	g.mu.Lock()
	doc := struct {
		Stats map[string]*ghRepoStats `json:"stats"`
	}{Stats: g.stats}
	g.mu.Unlock()
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	writeAtomic(g.path, raw)
}
