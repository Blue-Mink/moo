package api

// 0.6.384（用户令）：关于页更新日志改为读远程清单 changelog.md，
// 并在其下新增「文档和技能」「开发者文档」两张可折叠卡片，
// 内容同样来自远程清单（docs.md / devdocs.md）。
//
// 三个文件同属一个清单目录（设置项 about_lists_url，空=内置公网默认）：
//
//	<base>/changelog.md / docs.md / devdocs.md
//
// 缓存策略与发现页推荐清单一致：10 分钟 TTL + 有旧值先回旧值后台刷新
// （SWR）+ 冷启动限时同步抓取 + 失败退避；远程不可用时更新日志回落
// 构建内嵌文本，文档卡隐藏。内网地址与推荐清单同一道安全闸
// （source_list_allow_private）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
)

const (
	aboutListsTTL         = 10 * time.Minute
	aboutFileMaxBytes     = 128 << 10
	aboutDocsMaxItems     = 40
	aboutFallbackTTL      = 2 * time.Minute
	aboutCandidateTimeout = 8 * time.Second
	aboutRefreshBudget    = 40 * time.Second
)

// var：单测把冷启动预算压到毫秒级。
var aboutColdFetchBudget = 1500 * time.Millisecond

// aboutDocItem 文档卡里的一条：标题 + 说明 + 链接。
// 前端只展示 Title/Desc，URL 只用于「打开」「复制」两个按钮。
type aboutDocItem struct {
	Title string `json:"title"`
	Desc  string `json:"desc,omitempty"`
	URL   string `json:"url"`
}

// aboutSnapshot 一次清单快照。Remote=true 表示更新日志来自远程清单。
type aboutSnapshot struct {
	Changelog        string
	ChangelogVersion string
	// ChangelogLatest 更新日志里最新（第一个）版本段；药丸弹窗只显示这一段。
	ChangelogLatest string
	Docs            []aboutDocItem
	DevDocs         []aboutDocItem
	Remote          bool
}

func (s *Server) aboutListsBaseURL() string {
	if u := strings.TrimSpace(s.Cfg.AboutListsURL); u != "" {
		return strings.TrimRight(u, "/")
	}
	return strings.TrimRight(config.DefaultAboutListsURL, "/")
}

var (
	// - [标题](URL) — 说明
	aboutDocLinkRe = regexp.MustCompile(`^[-*+]\s+\[([^\]]+)\]\(\s*([^)\s]+)\s*\)\s*(.*)$`)
	// - 标题 | URL | 说明
	aboutDocPipeRe = regexp.MustCompile(`^[-*+]\s+(.+?)\s*\|\s*(https?://\S+)\s*(?:\|\s*(.*))?$`)
	// - 标题 URL
	aboutDocBareRe = regexp.MustCompile(`^[-*+]\s+(.+?)\s+(https?://\S+)\s*$`)
	// 更新日志里的版本文（## 0.6.384 / ### v0.6.384）
	aboutChangelogVersionRe = regexp.MustCompile(`(?m)^#{1,4}\s*[vV]?(\d+\.\d+(?:\.\d+)?(?:[-+._A-Za-z0-9]*)?)`)
)

func isHTTPURL(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return false
	}
	return !strings.ContainsAny(v, " \t\r\n\"'<>")
}

func truncRunes(v string, n int) string {
	r := []rune(v)
	if len(r) <= n {
		return v
	}
	return string(r[:n]) + "…"
}

func cleanAboutTitle(v string) string {
	v = strings.TrimSpace(v)
	v = strings.NewReplacer("**", "", "__", "", "`", "", "*", "").Replace(v)
	return truncRunes(strings.Join(strings.Fields(v), " "), 80)
}

func cleanAboutDesc(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimLeft(v, "—–-:：·| ")
	v = strings.NewReplacer("**", "", "__", "", "`", "").Replace(v)
	return truncRunes(strings.Join(strings.Fields(v), " "), 160)
}

// parseAboutDocs 解析文档清单。容忍 BOM/CRLF/# 注释/围栏代码块，
// 只接受 http(s) 链接并按 URL 去重，最多 aboutDocsMaxItems 条。
func parseAboutDocs(body string) []aboutDocItem {
	out := make([]aboutDocItem, 0, 8)
	seen := map[string]bool{}
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var title, url, desc string
		switch {
		case aboutDocLinkRe.MatchString(line):
			m := aboutDocLinkRe.FindStringSubmatch(line)
			title, url, desc = m[1], m[2], m[3]
		case aboutDocPipeRe.MatchString(line):
			m := aboutDocPipeRe.FindStringSubmatch(line)
			title, url, desc = m[1], m[2], m[3]
		case aboutDocBareRe.MatchString(line):
			m := aboutDocBareRe.FindStringSubmatch(line)
			title, url = m[1], m[2]
		default:
			continue
		}
		title = cleanAboutTitle(title)
		desc = cleanAboutDesc(desc)
		url = strings.TrimRight(strings.TrimSpace(url), ".,;)")
		if title == "" || !isHTTPURL(url) || seen[url] {
			continue
		}
		seen[url] = true
		out = append(out, aboutDocItem{Title: title, Desc: desc, URL: url})
		if len(out) >= aboutDocsMaxItems {
			break
		}
	}
	return out
}

func parseAboutChangelogVersion(body, fallback string) string {
	if m := aboutChangelogVersionRe.FindStringSubmatch(body); m != nil && m[1] != "" {
		return m[1]
	}
	return fallback
}

// latestChangelogSection 取更新日志里最新（第一个）版本段：
// 从第一个版本标题行起，到下一个版本标题行之前（不含）。
// 没有版本标题时整段返回。0.6.387（用户令）：药丸弹窗只显示最新版日志。
func latestChangelogSection(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	idx := aboutChangelogVersionRe.FindAllStringIndex(body, -1)
	if len(idx) == 0 {
		return body
	}
	end := len(body)
	if len(idx) > 1 {
		end = idx[1][0]
	}
	return strings.TrimSpace(body[idx[0][0]:end])
}

// fetchAboutFile 拉取单个清单文件：GitHub 地址走镜像优先（与推荐清单同一
// 候选链），其它地址直连；单候选 aboutCandidateTimeout 超时。
func (s *Server) fetchAboutFile(ctx context.Context, name string) (string, error) {
	u := s.aboutListsBaseURL() + "/" + name
	// 清单地址是管理员配置的信任面；仍与推荐清单同口径过 netguard，
	// 指向内网需显式开 source_list_allow_private（本地 Gitea 测试用）。
	if !s.Cfg.SourceListAllowPrivate {
		if host, err := netguard.Public(u); err != nil {
			return "", fmt.Errorf("%s 被安全策略拒绝（%s）: %w", name, host, err)
		}
	}
	client := &http.Client{}
	var lastErr error
	for _, cand := range recoListCandidates(u, s.Mirrors) {
		cctx, cancel := context.WithTimeout(ctx, aboutCandidateTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, cand, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "moo/0.6")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, aboutFileMaxBytes))
		status := resp.StatusCode
		resp.Body.Close()
		cancel()
		if rerr == nil && status == http.StatusOK && len(strings.TrimSpace(string(body))) > 0 {
			return string(body), nil
		}
		if rerr != nil {
			lastErr = rerr
		} else {
			lastErr = fmt.Errorf("HTTP %d", status)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("没有可用候选")
	}
	return "", fmt.Errorf("%s 获取失败: %w", name, lastErr)
}

// refreshAboutLists 并发拉取三个文件并落缓存。任一文件失败不拖累其它：
// 更新日志失败=回落内嵌文本（Remote=false），文档失败=该卡片为空。
func (s *Server) refreshAboutLists(ctx context.Context) (aboutSnapshot, error) {
	names := []string{"changelog.md", "docs.md", "devdocs.md"}
	type result struct {
		name string
		body string
		err  error
	}
	ch := make(chan result, len(names))
	for _, n := range names {
		go func(n string) {
			body, err := s.fetchAboutFile(ctx, n)
			ch <- result{n, body, err}
		}(n)
	}
	got := make(map[string]string, len(names))
	var errs []string
	for range names {
		r := <-ch
		if r.err != nil {
			errs = append(errs, r.err.Error())
			continue
		}
		got[r.name] = r.body
	}
	snap := aboutSnapshot{
		Changelog:        changelogCurrentFor(s.Version),
		ChangelogVersion: s.Version,
		Docs:             []aboutDocItem{},
		DevDocs:          []aboutDocItem{},
	}
	if body, ok := got["changelog.md"]; ok {
		snap.Changelog = strings.TrimSpace(body)
		snap.ChangelogVersion = parseAboutChangelogVersion(body, s.Version)
		snap.Remote = true
	}
	snap.ChangelogLatest = latestChangelogSection(snap.Changelog)
	if body, ok := got["docs.md"]; ok {
		snap.Docs = parseAboutDocs(body)
	}
	if body, ok := got["devdocs.md"]; ok {
		snap.DevDocs = parseAboutDocs(body)
	}

	s.aboutMu.Lock()
	s.aboutSnap, s.aboutAt, s.aboutLoaded, s.aboutURL = snap, time.Now(), snap.Remote, s.aboutListsBaseURL()
	s.aboutMu.Unlock()

	if len(errs) > 0 {
		return snap, errors.New(strings.Join(errs, "; "))
	}
	return snap, nil
}

// aboutFallback 远程不可用时的兜底快照：更新日志=构建内嵌文本。
func (s *Server) aboutFallback() aboutSnapshot {
	cl := changelogCurrentFor(s.Version)
	return aboutSnapshot{
		Changelog:        cl,
		ChangelogVersion: s.Version,
		ChangelogLatest:  latestChangelogSection(cl),
		Docs:             []aboutDocItem{},
		DevDocs:          []aboutDocItem{},
	}
}

// aboutSnapshotFor 请求路径取快照（永不长时间阻塞）：
//   - TTL 内 → 缓存；
//   - 有旧值但过期 → 立刻回旧值 + 后台单飞刷新；
//   - 冷启动/换地址 → 限时同步试一次，超时回兜底并转后台。
func (s *Server) aboutSnapshotFor(ctx context.Context) aboutSnapshot {
	url := s.aboutListsBaseURL()
	s.aboutMu.Lock()
	if s.aboutLoaded && s.aboutURL == url && time.Since(s.aboutAt) < aboutListsTTL {
		snap := s.aboutSnap
		s.aboutMu.Unlock()
		return snap
	}
	stale, staleURL, loaded, staleAt := s.aboutSnap, s.aboutURL, s.aboutLoaded, s.aboutAt
	s.aboutMu.Unlock()

	// 上次失败已回退：退避窗口内直接回它，别每个请求都重试。
	if !loaded && staleURL == url && strings.TrimSpace(stale.Changelog) != "" && time.Since(staleAt) < aboutFallbackTTL {
		return stale
	}
	if staleURL == url && strings.TrimSpace(stale.Changelog) != "" {
		s.startAboutRefresh()
		return stale
	}

	cctx, cancel := context.WithTimeout(ctx, aboutColdFetchBudget)
	defer cancel()
	done := make(chan aboutSnapshot, 1)
	go func() {
		snap, _ := s.refreshAboutLists(cctx)
		done <- snap
	}()
	select {
	case snap := <-done:
		return snap
	case <-cctx.Done():
	}
	fb := s.aboutFallback()
	s.aboutMu.Lock()
	s.aboutSnap, s.aboutAt, s.aboutLoaded, s.aboutURL = fb, time.Now(), false, url
	s.aboutMu.Unlock()
	s.startAboutRefresh()
	return fb
}

// startAboutRefresh 后台单飞刷新（TTL 过期/冷启动超时后补齐）。
func (s *Server) startAboutRefresh() {
	s.aboutMu.Lock()
	if s.aboutRefreshing {
		s.aboutMu.Unlock()
		return
	}
	s.aboutRefreshing = true
	s.aboutMu.Unlock()
	go func() {
		defer func() {
			s.aboutMu.Lock()
			s.aboutRefreshing = false
			s.aboutMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), aboutRefreshBudget)
		defer cancel()
		_, _ = s.refreshAboutLists(ctx)
	}()
}

// aboutListsRefresh POST /api/about/lists/refresh（admin）：
// 立即重拉三个清单，返回结果计数（测试/运维用）。
func (s *Server) aboutListsRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), aboutRefreshBudget)
	defer cancel()
	snap, err := s.refreshAboutLists(ctx)
	source := "embedded"
	if snap.Remote {
		source = "remote"
	}
	resp := map[string]any{
		"ok":                snap.Remote,
		"changelog_source":  source,
		"changelog_version": snap.ChangelogVersion,
		"docs":              len(snap.Docs),
		"devdocs":           len(snap.DevDocs),
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	writeJSON(w, resp)
}

// invalidateAboutLists 清空缓存（换清单地址/切换内网放行后立即重拉）。
func (s *Server) invalidateAboutLists() {
	s.aboutMu.Lock()
	s.aboutSnap, s.aboutAt, s.aboutLoaded, s.aboutURL = aboutSnapshot{}, time.Time{}, false, ""
	s.aboutMu.Unlock()
}
