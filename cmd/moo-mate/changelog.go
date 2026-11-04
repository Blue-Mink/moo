package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 0.2.37：mate-changelog.md 远程清单（与 Moo 关于页/更新弹窗同机制）。
// 只取第一段（最新版本）给版本药丸弹窗；清单可在 Gitea/GitHub 上更新，
// 不用重建 FPK 就能改日志。
const (
	mateChangelogTTL    = 10 * time.Minute
	mateChangelogErrTTL = 2 * time.Minute
)

type mateChangelogInfo struct {
	Version   string `json:"version,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Source    string `json:"source,omitempty"` // remote / cache
	URL       string `json:"url,omitempty"`
	FetchedAt int64  `json:"fetched_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

var mateChangelogCache struct {
	mu   sync.Mutex
	at   time.Time
	info mateChangelogInfo
	good mateChangelogInfo
}

var mateChangelogHeading = regexp.MustCompile(`(?m)^##\s+(\S+)\s*$`)

// parseLatestChangelog 取清单第一段：返回版本号与正文（保持 - 行）。
func parseLatestChangelog(text string) (version, body string) {
	idx := mateChangelogHeading.FindAllStringSubmatchIndex(text, -1)
	if len(idx) == 0 {
		return "", ""
	}
	start := idx[0][1]
	end := len(text)
	if len(idx) > 1 {
		end = idx[1][0]
	}
	body = strings.TrimSpace(text[start:end])
	version = text[idx[0][2]:idx[0][3]]
	return version, body
}

// rawURLVariants 为 raw.githubusercontent.com 增加镜像候选（国内直连常失败）。
func rawURLVariants(raw string) []string {
	out := []string{raw}
	const prefix = "https://raw.githubusercontent.com/"
	if !strings.HasPrefix(raw, prefix) {
		return out
	}
	rest := strings.TrimPrefix(raw, prefix) // owner/repo/branch/path...
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) == 4 {
		owner, repo, branch, path := parts[0], parts[1], parts[2], parts[3]
		out = append(out, fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s@%s/%s", owner, repo, branch, path))
	}
	out = append(out, "https://gh-proxy.com/"+raw)
	return out
}

func fetchChangelogText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "moo-mate/"+buildVersion)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

// mateChangelogCached 拉取并缓存最新一段（10 分钟 TTL；失败 2 分钟后重试，
// 期间沿用上一次成功内容，避免弹窗因临时网络抖动变空）。
func (c *companion) mateChangelogCached(force bool) mateChangelogInfo {
	c.mu.RLock()
	url := c.cfg.changelogURL()
	c.mu.RUnlock()

	mateChangelogCache.mu.Lock()
	defer mateChangelogCache.mu.Unlock()
	ttl := mateChangelogTTL
	if mateChangelogCache.info.Error != "" {
		ttl = mateChangelogErrTTL
	}
	if !force && !mateChangelogCache.at.IsZero() && time.Since(mateChangelogCache.at) < ttl {
		return mateChangelogCache.info
	}

	info := mateChangelogInfo{URL: url, FetchedAt: time.Now().Unix()}
	client := &http.Client{Timeout: 8 * time.Second}
	var lastErr string
	for _, u := range rawURLVariants(url) {
		body, err := fetchChangelogText(context.Background(), client, u)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		ver, latest := parseLatestChangelog(body)
		if latest == "" {
			lastErr = "清单无版本段"
			continue
		}
		info.Version, info.Latest, info.Source = ver, latest, "remote"
		mateChangelogCache.good = info
		mateChangelogCache.at = time.Now()
		mateChangelogCache.info = info
		return info
	}
	info.Error = lastErr
	if mateChangelogCache.good.Latest != "" {
		fallback := mateChangelogCache.good
		fallback.Source = "cache"
		fallback.Error = lastErr
		mateChangelogCache.at = time.Now()
		mateChangelogCache.info = fallback
		return fallback
	}
	mateChangelogCache.at = time.Now()
	mateChangelogCache.info = info
	return info
}

// handleChangelog GET /api/changelog[?force=1]：版本药丸弹窗的数据源。
func (c *companion) handleChangelog(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	writeJSON(w, http.StatusOK, c.mateChangelogCached(force))
}
