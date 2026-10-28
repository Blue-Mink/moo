package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"moo/internal/source"
)

// ghReleaseAsset GitHub release 里的单个资产（只取需要的字段）。
type ghReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// ghRelease GitHub release（只取需要的字段）。
type ghRelease struct {
	TagName string           `json:"tag_name"`
	Assets  []ghReleaseAsset `json:"assets"`
}

// discoveredApp 发现结果：一个应用的下载直链（后续下载解析 manifest 补全）。
type discoveredApp struct {
	Name        string // asset 文件名（可能不是 appname，appname 待解包确认）
	Version     string // 从 tag_name 推导
	DownloadURL string
	Size        int64
}

var ghRepoRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([^/]+)/([^/]+)/?`)

// parseGHRepo 从 URL 提取 owner/repo（GitHub 仓库形态）。
func parseGHRepo(raw string) (owner, repo string, ok bool) {
	m := ghRepoRe.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 3 {
		return "", "", false
	}
	repo = strings.TrimSuffix(m[2], ".git")
	return m[1], repo, true
}

var giteeRepoRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?gitee\.com/([^/]+)/([^/]+)/?`)

// parseGiteeRepo 从 URL 提取 owner/repo（Gitee 仓库形态）。
func parseGiteeRepo(raw string) (owner, repo string, ok bool) {
	m := giteeRepoRe.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 3 {
		return "", "", false
	}
	repo = strings.TrimSuffix(m[2], ".git")
	return m[1], repo, true
}

// repoOwner 取仓库 owner（GitHub / Gitee 通用）。
func repoOwner(raw string) string {
	if o, _, ok := parseGHRepo(raw); ok {
		return o
	}
	if o, _, ok := parseGiteeRepo(raw); ok {
		return o
	}
	return ""
}

// ghReleases 拉取 GitHub releases（分页，最多 max 页）。
func ghReleases(ctx context.Context, owner, repo string, httpc *http.Client, token string) ([]ghRelease, error) {
	var all []ghRelease
	page := 1
	for {
		u := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100&page=%d", owner, repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		var batch []ghRelease
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 || page >= 3 {
			break
		}
		page++
	}
	return all, nil
}

// giteeReleaseAsset Gitee release 里的单个资产（Gitee 资产列表不含 size 字段）。
type giteeReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// giteeRelease Gitee release（只取需要的字段）。
type giteeRelease struct {
	TagName string              `json:"tag_name"`
	Assets  []giteeReleaseAsset `json:"assets"`
}

// giteeAPIBase Gitee OpenAPI v5 base（测试可覆盖指向 httptest server）。
var giteeAPIBase = "https://gitee.com/api/v5"

// giteeReleases 拉取 Gitee releases（分页，最多 max 页）。
// Gitee API v5 匿名可用（限流较宽），无需 token。
func giteeReleases(ctx context.Context, owner, repo string, httpc *http.Client) ([]giteeRelease, error) {
	var all []giteeRelease
	page := 1
	for {
		u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=20&page=%d", giteeAPIBase, owner, repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "moo-mate")
		resp, err := httpc.Do(req)
		if err != nil {
			return nil, err
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Gitee API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		var batch []giteeRelease
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 20 || page >= 5 {
			break
		}
		page++
	}
	return all, nil
}

// versionFromTag 从 tag 名（如 v1.2.3、1.2.3、app/v1.2.3）提取版本号。
// 失败返回空串（该 release 不能直接用作版本）。
func versionFromTag(tag string) string {
	tag = strings.TrimSpace(tag)
	// 先按 / 取最后一段（app/v1.2.3 → v1.2.3），再剥前导 v。
	if i := strings.LastIndex(tag, "/"); i >= 0 {
		tag = tag[i+1:]
	}
	tag = strings.TrimPrefix(tag, "v")
	// 只认以数字开头的版本串（含 x.y.z-N 构建号）。
	if tag == "" || (tag[0] < '0' || tag[0] > '9') {
		return ""
	}
	return tag
}

// releaseFPK release 里一条 .fpk 资产（GitHub/Gitee 统一中间形态）。
type releaseFPK struct {
	name string
	url  string
	size int64
	ver  string // 从 tag 推导的版本（可能为空）
}

// dedupFPKs 按资产文件名去重，同名只留最高版，返回 discoveredApp 列表。
// Gitee 资产无 size 字段 → size 传 0，实际体积由 build 阶段 inspectFPK 下载后补全。
func dedupFPKs(items []releaseFPK) []discoveredApp {
	best := map[string]discoveredApp{}
	bestVer := map[string]string{}
	for _, it := range items {
		if !strings.HasSuffix(strings.ToLower(it.name), ".fpk") {
			continue
		}
		if it.url == "" {
			continue
		}
		if _, exists := best[it.name]; exists {
			if it.ver == "" || (bestVer[it.name] != "" && !versionGreater(it.ver, bestVer[it.name])) {
				continue
			}
		}
		best[it.name] = discoveredApp{
			Name:        it.name,
			Version:     it.ver,
			DownloadURL: it.url,
			Size:        it.size,
		}
		bestVer[it.name] = it.ver
	}
	out := make([]discoveredApp, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	return out
}

// discoverFromRepo 从仓库发现 .fpk 应用（L2 release 扫描）。
// 按 host 分派：github.com → GitHub API；gitee.com → Gitee API。
// 返回按版本降序去重后的候选列表（同名只留最高版）。
func discoverFromRepo(ctx context.Context, rawURL, token string, httpc *http.Client) ([]discoveredApp, error) {
	// GitHub
	if owner, repo, ok := parseGHRepo(rawURL); ok {
		rels, err := ghReleases(ctx, owner, repo, httpc, token)
		if err != nil {
			return nil, err
		}
		items := make([]releaseFPK, 0, len(rels))
		for _, rel := range rels {
			ver := versionFromTag(rel.TagName)
			for _, a := range rel.Assets {
				items = append(items, releaseFPK{name: a.Name, url: a.URL, size: a.Size, ver: ver})
			}
		}
		return dedupFPKs(items), nil
	}
	// Gitee
	if owner, repo, ok := parseGiteeRepo(rawURL); ok {
		rels, err := giteeReleases(ctx, owner, repo, httpc)
		if err != nil {
			return nil, err
		}
		items := make([]releaseFPK, 0, len(rels))
		for _, rel := range rels {
			ver := versionFromTag(rel.TagName)
			for _, a := range rel.Assets {
				items = append(items, releaseFPK{name: a.Name, url: a.URL, size: 0, ver: ver})
			}
		}
		return dedupFPKs(items), nil
	}
	return nil, fmt.Errorf("仅支持 GitHub / Gitee 仓库地址: %s", rawURL)
}

// versionGreater 数值分段比较版本（与 gen-moo-json 同口径）。
func versionGreater(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := parseIntPrefix(av)
		bn, berr := parseIntPrefix(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an > bn
			}
			continue
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

func parseIntPrefix(s string) (int, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("非数字")
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// probeIndex 复用 Moo 的 ProbeIndex：探测仓库是否已发布可解析索引。
// 返回命中的索引文件名（"" = 无索引 / 网络失败）。
func probeIndex(repoURL string) string {
	return source.ProbeIndex(repoURL)
}

// hostFromURL 取 URL 的主机名（用于日志/去重展示）。
func hostFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Hostname()
}

// githubMirrorPrefixes GitHub 加速镜像前缀（与 Moo config.GitHubMirrorOptions 对齐，
// 排除 conversun（仅代理 conversun 仓）与 auto/custom/direct 特殊项）。
// 本机直连 objects.githubusercontent.com 常 0B/s（15s 只下 207KB），必须走镜像。
var githubMirrorPrefixes = []string{
	"https://hk.gh-proxy.org/",
	"https://cdn.gh-proxy.org/",
	"https://ghproxy.cxkpro.top/",
	"https://git.yylx.win/",
	"https://gh-proxy.com/",
	"https://gitproxy.mrhjx.cn/",
	"https://gh-proxy.org/",
	"https://gh.felicity.ac.cn/",
	"https://wget.la/",
	"https://github.dpik.top/",
	"https://ghproxy.net/",
	"https://cors.isteed.cc/",
	"https://gh.dpik.top/",
	"https://github-proxy.memory-echoes.cn/",
	"https://ghfast.top/",
}

// mirrorCandidates 把 GitHub release 资产 URL 展开成「镜像前缀 + 原 URL」候选。
// 镜像前缀排在前面（本机直连 objects CDN 常 0B/s），直连原 URL 兜底放最后。
// 非 GitHub 地址只保留原 URL。前缀服务会自行跟随 302 到 objects CDN。
func mirrorCandidates(raw string) []string {
	var out []string
	if strings.Contains(raw, "github.com/") {
		for _, p := range githubMirrorPrefixes {
			out = append(out, p+raw)
		}
	}
	out = append(out, raw)
	return out
}

// timeoutCtx 短超时上下文（发现/下载预算）。
func timeoutCtx(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, d)
}
