package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 0.2.73：README 自托管。
// 扫描时按「仓库 + 应用」取一次 README（同仓多应用时优先取 <appname>/README.md 这类
// 子目录文件，取不到才回落仓库根 README），落盘到 <bootDir>/readme/<app>.md；
// moo.json 的 readme_url 指向 mate 自己的 /moo/<app>/README.md，由本机直接出文件，
// 不依赖用户侧访问 GitHub。缓存 7 天，过期时做一次有界刷新，失败回落旧副本。

const (
	readmeTTL      = 7 * 24 * time.Hour
	maxReadmeBytes = 512 << 10
	readmeFetchTO  = 8 * time.Second
)

// readmeFile 把 appname 映射成安全文件名（appname 可能含路径分隔符等字符）。
func readmeFile(app string) string {
	var b strings.Builder
	for _, r := range app {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || s == "." || s == ".." {
		s = "app"
	}
	return s + ".md"
}

func (c *companion) readmePath(app string) string {
	return filepath.Join(c.bootDir, "readme", readmeFile(app))
}

func (c *companion) readmeMetaPath(app string) string {
	return filepath.Join(c.bootDir, "readme", readmeFile(app)+".meta.json")
}

// saveReadme 原子落盘 README 与来源路径（先写临时文件再 rename，避免半截文件被 Moo 读到）。
func (c *companion) saveReadme(app string, body []byte, path string) error {
	p := c.readmePath(app)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	mb, _ := json.Marshal(map[string]string{"path": path})
	mt := c.readmeMetaPath(app)
	if os.WriteFile(mt+".tmp", mb, 0o644) == nil {
		_ = os.Rename(mt+".tmp", mt)
	}
	return nil
}

func (c *companion) readmeMeta(app string) string {
	b, err := os.ReadFile(c.readmeMetaPath(app))
	if err != nil {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m["path"]
}

func (c *companion) readReadme(app string) ([]byte, time.Time, bool) {
	p := c.readmePath(app)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return nil, time.Time{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil, time.Time{}, false
	}
	return b, st.ModTime(), true
}

// repoReadmePaths 返回「应用名 -> README 路径」映射与仓库根 README 路径。
// 同仓多应用（moo / moo-mate）靠这条规则各取各的 README，避免互相串内容。
func (c *companion) repoReadmePaths(ctx context.Context, src string) (map[string]string, string) {
	owner, repo, ok := parseGHRepo(src)
	if !ok {
		return nil, ""
	}
	meta, err := ghAPIGet(ctx, fmt.Sprintf("%s/repos/%s/%s", ghAPIBase, owner, repo), c.githubToken(), c.httpc)
	if err != nil {
		return nil, ""
	}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	if json.Unmarshal(meta, &info) != nil || info.DefaultBranch == "" {
		return nil, ""
	}
	tree, _, err := ghTreeFetch(ctx, owner, repo, info.DefaultBranch, c.githubToken(), c.httpc)
	if err != nil {
		return nil, ""
	}
	byApp, root := map[string]string{}, ""
	for _, e := range tree {
		p := strings.TrimSpace(e.Path)
		if e.Type != "blob" || p == "" {
			continue
		}
		low := strings.ToLower(p)
		if !strings.HasSuffix(low, "/readme.md") && low != "readme.md" {
			continue
		}
		if low == "readme.md" {
			root = p
			continue
		}
		dir := p[:strings.LastIndex(p, "/")]
		seg := dir
		if i := strings.LastIndex(dir, "/"); i >= 0 {
			seg = dir[i+1:]
		}
		k := strings.ToLower(seg)
		if cur, ok := byApp[k]; !ok || len(p) < len(cur) {
			byApp[k] = p
		}
	}
	return byApp, root
}

// fetchReadme 取原始 Markdown。path 为空时走 readme 端点（自动识别大小写/扩展名），
// 否则按仓库内路径走 contents 端点。
func (c *companion) fetchReadme(ctx context.Context, rawURL, path string) ([]byte, error) {
	owner, repo, ok := parseGHRepo(rawURL)
	if !ok {
		return nil, fmt.Errorf("非 GitHub 仓库，暂不支持 README 托管")
	}
	u := fmt.Sprintf("%s/repos/%s/%s/readme", ghAPIBase, owner, repo)
	if path != "" {
		u = fmt.Sprintf("%s/repos/%s/%s/contents/%s", ghAPIBase, owner, repo, escapePathSegments(path))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
	req.Header.Set("User-Agent", "moo-mate")
	if tok := c.githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("GitHub README HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReadmeBytes))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, fmt.Errorf("上游 README 为空")
	}
	return b, nil
}

// ensureReadme 保证本地有 README：新鲜则跳过；否则尝试刷新，失败时旧副本仍算可用。
func (c *companion) ensureReadme(ctx context.Context, app, rawURL, path string) bool {
	old, mt, has := c.readReadme(app)
	if has && time.Since(mt) < readmeTTL {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, readmeFetchTO)
	defer cancel()
	b, err := c.fetchReadme(ctx, rawURL, path)
	if err == nil && len(b) > 0 {
		if serr := c.saveReadme(app, b, path); serr == nil {
			return true
		}
	}
	return has && len(old) > 0
}

// serveReadme 输出 /moo/<app>/README.md。缺失/过期时做一次有界刷新，再回落旧副本。
func (c *companion) serveReadme(w http.ResponseWriter, r *http.Request, app string) {
	b, mt, ok := c.readReadme(app)
	if !ok || time.Since(mt) > readmeTTL {
		if ia := c.idx.get(app); ia != nil && ia.Source != "" {
			ctx, cancel := context.WithTimeout(r.Context(), readmeFetchTO)
			if nb, err := c.fetchReadme(ctx, ia.Source, c.readmeMeta(app)); err == nil && len(nb) > 0 {
				if c.saveReadme(app, nb, c.readmeMeta(app)) == nil {
					b, ok = nb, true
				}
			}
			cancel()
		}
	}
	if !ok || len(b) == 0 {
		http.Error(w, "README 暂不可用（上游未提供或拉取失败）", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(b)
}

var _ = url.PathEscape
