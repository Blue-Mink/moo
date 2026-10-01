package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/netx"
)

// FnDepot 源：解析 fnpack.json（兼容扁平 V1 与 V2 包裹两种格式）。
type FnDepot struct {
	name string
	url  string
}

// NewFnDepot 创建一个 FnDepot 源。url 可以是 fnpack.json 直链，
// 也可以是仓库地址（github.com/user/repo 或任意托管根）。
func NewFnDepot(name, url string) *FnDepot {
	return &FnDepot{name: name, url: strings.TrimRight(url, "/")}
}

// NewSource 按地址自动选择源适配器：conversun/fnos-apps 目录走 Conversun
// （apps.json 格式），其余一律 FnDepot（fnpack.json 格式）。
func NewSource(name, url string) Source {
	if IsConversunURL(url) {
		return NewConversun(name)
	}
	return NewFnDepot(name, url)
}

func (f *FnDepot) Name() string { return f.name }

// fetchClient 源同步出网（0.6.206 起带「科学加速」代理：GitHub 域名走本机
// 代理、镜像域名直连；代理配置变更即时生效）。
var fetchClient = netx.NewClient(30 * time.Second)

// candidateURLs 把用户给的地址展开成一组候选 JSON 地址，按优先级排列。
func (f *FnDepot) candidateURLs() []string {
	u := f.url
	var out []string
	switch {
	case strings.HasSuffix(u, ".json"):
		out = append(out, u)
	case strings.Contains(u, "github.com/"):
		// github.com/user/repo → raw / gh-proxy 镜像 / jsDelivr
		// 镜像与 raw 同属一级竞速：GitHub 直连在国内 NAS 上间歇性超时
		// （2026-09-25 实测源同步 12s 全挂），而 FPK 下载走镜像一直正常——
		// JSON 拉取没有镜像导致目录刷新比包下载脆弱得多。
		trimmed := strings.TrimPrefix(u, "https://")
		trimmed = strings.TrimPrefix(trimmed, "http://")
		parts := strings.SplitN(trimmed, "/", 3)
		if len(parts) == 3 {
			owner, repo := parts[1], strings.TrimSuffix(parts[2], ".git")
			out = append(out,
				fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/fnpack.json", owner, repo),
				fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/master/fnpack.json", owner, repo),
			)
			for _, opt := range config.GitHubMirrorOptions() {
				if opt.URL == "" { // auto = 非直连项
					continue
				}
				m := strings.TrimRight(opt.URL, "/") + "/https://raw.githubusercontent.com/" + owner + "/" + repo + "/"
				out = append(out, m+"main/fnpack.json", m+"master/fnpack.json")
			}
			out = append(out, fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s/fnpack.json", owner, repo))
		}
		out = append(out, u+"/fnpack.json", u+"/raw/main/fnpack.json")
	default:
		out = append(out, u+"/fnpack.json", u+"/raw/main/fnpack.json")
	}
	return out
}

// fetchJSON 分两级拉取（整体 12s 上限）：
//  1. 一级=源站候选（raw.githubusercontent / 仓库直链）——内容永远最新，并发竞速；
//  2. 二级=CDN 候选（jsDelivr）——2026-09-23 实测 jsDelivr 缓存滞后于源站
//     （源站 11 应用、CDN 还是 10），若参与竞速会因 CDN 更快而稳定胜出，
//     导致目录长期停留在旧版。故 CDN 仅在一级全部失败（GitHub 不可达）时兜底。
//
// 返回值 base = 胜出 JSON 所在目录（去掉尾部 fnpack.json）：源里的
// 相对 download_url/图标/预览按该目录补全——与 fnpack.json 同树的文件
// 一定可达，且自动适配 master/main 分支（硬编码 main 会在 master
// 仓库上 404）。
func (f *FnDepot) fetchJSON() ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	var primary, fallback []string
	for _, u := range f.candidateURLs() {
		if strings.Contains(u, "cdn.jsdelivr.net") {
			fallback = append(fallback, u)
		} else {
			primary = append(primary, u)
		}
	}
	if data, winner, err := raceFetch(primary, ctx); err == nil {
		return data, strings.TrimSuffix(winner, "/fnpack.json"), nil
	}
	data, winner, err := raceFetch(fallback, ctx)
	if err != nil {
		return nil, "", err
	}
	return data, strings.TrimSuffix(winner, "/fnpack.json"), nil
}

// raceFetch 并发竞速一组 URL，返回第一个 200 的 JSON（包级：FnDepot 与
// Conversun 等适配器共用）。
func raceFetch(cands []string, parent context.Context) ([]byte, string, error) {
	if len(cands) == 0 {
		return nil, "", fmt.Errorf("无候选地址")
	}
	type result struct {
		data   []byte
		winner string
		err    error
	}
	ch := make(chan result, len(cands))
	for _, cand := range cands {
		go func(u string) {
			req, err := http.NewRequestWithContext(parent, http.MethodGet, u, nil)
			if err != nil {
				ch <- result{nil, "", err}
				return
			}
			resp, err := fetchClient.Do(req)
			if err != nil {
				ch <- result{nil, "", err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			if resp.StatusCode != 200 || err != nil {
				ch <- result{nil, "", fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)}
				return
			}
			ch <- result{body, u, nil}
		}(cand)
	}
	var lastErr error
	for i := 0; i < len(cands); i++ {
		r := <-ch
		if r.err == nil {
			return r.data, r.winner, nil
		}
		lastErr = r.err
	}
	return nil, "", fmt.Errorf("所有候选地址均失败: %w", lastErr)
}

// v2Doc 是 V2 包裹格式（schema_version + source_info + apps）。
type v2Doc struct {
	SchemaVersion string                    `json:"schema_version"`
	SourceInfo    map[string]any            `json:"source_info"`
	Apps          map[string]map[string]any `json:"apps"`
}

// ParseFnpack 解析 fnpack.json 的两种已知格式：
// 有顶层 schema_version → V2 包裹（取 apps 字段）；否则 → 扁平（顶层 key 即 appname）。
func ParseFnpack(data []byte) (map[string]map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("fnpack.json 不是 JSON 对象: %w", err)
	}
	if _, hasSchema := raw["schema_version"]; hasSchema {
		var doc v2Doc
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("V2 fnpack.json 解析失败: %w", err)
		}
		if len(doc.Apps) == 0 {
			return nil, fmt.Errorf("V2 fnpack.json 的 apps 为空")
		}
		return doc.Apps, nil
	}
	var flat map[string]map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("fnpack.json 格式无法识别: %w", err)
	}
	if len(flat) == 0 {
		return nil, fmt.Errorf("fnpack.json 为空")
	}
	return flat, nil
}

// str 安全取字符串字段。
func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// pickStr 依次取多个候选键，返回第一个非空字符串（兼容源端字段名变体，
// 如 author/maintainer）。
func pickStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m, k); s != "" {
			return s
		}
	}
	return ""
}

// pickStrList 取字段为字符串或字符串数组的字段，统一拼成逗号分隔字符串
// （labels 字符串 / categories 数组等变体）。
func pickStrList(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case []any:
			parts := make([]string, 0, len(t))
			for _, it := range t {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ", ")
			}
		}
	}
	return ""
}

// anyScalarStr 取标量字段（字符串或数字）转字符串。
func anyScalarStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	return ""
}

// anyInt 取整数字段（数字或数字字符串）。
func anyInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

// isDockerFlag 兼容 isdocker（"true"/"false" 字符串）与 is_docker（bool）两种写法。
func isDockerFlag(m map[string]any) string {
	for _, k := range []string{"isdocker", "is_docker"} {
		switch t := m[k].(type) {
		case bool:
			if t {
				return "true"
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(t), "true") || t == "1" {
				return "true"
			}
		}
	}
	return ""
}

// currentArch 把 Go 运行时架构映射到 fnpack 架构名（amd64→x86，arm64→arm）。
func currentArch() string {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return "arm"
	}
	return "x86"
}

// bestRelease 从 releases 里按版本降序挑当前架构（回退 all）第一个带
// download_url 的包，返回版本号与该包（download_url/sha256/size 等）。
func bestRelease(rel map[string]any, arch string) (string, map[string]any) {
	versions := make([]string, 0, len(rel))
	for v := range rel {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versionLess(versions[j], versions[i]) })
	for _, v := range versions {
		rv, _ := rel[v].(map[string]any)
		pkgs, _ := rv["packages"].(map[string]any)
		for _, k := range []string{arch, "all"} {
			if p, ok := pkgs[k].(map[string]any); ok && strings.TrimSpace(str(p, "download_url")) != "" {
				return v, p
			}
		}
	}
	return "", nil
}

// versionLess 报告版本 a 是否小于 b：去掉 v 前缀后按点分段数值比较，
// 非数字后缀（如 -1、rc1）按整段字典序兜底。
func versionLess(a, b string) bool {
	as, aRest := splitVersionSuffix(strings.TrimPrefix(strings.TrimSpace(a), "v"))
	bs, bRest := splitVersionSuffix(strings.TrimPrefix(strings.TrimSpace(b), "v"))
	ai, bi := splitVersionNums(as), splitVersionNums(bs)
	for i := 0; i < len(ai) || i < len(bi); i++ {
		x, y := 0, 0
		if i < len(ai) {
			x = ai[i]
		}
		if i < len(bi) {
			y = bi[i]
		}
		if x != y {
			return x < y
		}
	}
	return aRest < bRest
}

// splitVersionSuffix 把 "4.8.8-1" 拆成 ("4.8.8", "-1")。
func splitVersionSuffix(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || c == '.') {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// splitVersionNums 把 "4.8.8" 拆成 [4 8 8]（非数字段按 0）。
func splitVersionNums(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out[i] = n
	}
	return out
}

func (f *FnDepot) Fetch() (map[string]*App, error) {
	data, base, err := f.fetchJSON()
	if err != nil {
		return nil, err
	}
	entries, err := ParseFnpack(data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*App, len(entries))
	for name, m := range entries {
		out[name] = translateEntry(name, m, f.name, f.url, base)
	}
	return out, nil
}

// translateEntry 把一条 fnpack.json 条目转成 App（纯函数，便于单测）。
// 兼容：V1 平铺单版本、V2 多版本 releases、字段名变体（maintainer/categories/
// is_docker 等）、arch_diff 按架构分列、GitHub 仓库内 FPK 约定直链。
// base = fnpack.json 所在目录（Fetch 实测胜出的 URL 前缀）；相对路径
// （download_url/图标/预览）一律按它补全，base 为空时回退仓库 URL 猜测。
func translateEntry(name string, m map[string]any, srcName, repoURL, base string) *App {
	a := &App{
		Name:          name,
		Source:        srcName,
		DisplayName:   str(m, "display_name"),
		Desc:          str(m, "desc"),
		Version:       str(m, "version"),
		Platform:      pickStrList(m, "platform"),
		Labels:        pickStrList(m, "labels", "categories"),
		IconURL:       str(m, "icon_url"),
		HomePage:      str(m, "homepage"),
		ReadmeURL:     str(m, "readme_url"),
		BugReportURL:  str(m, "bug_report_url"),
		Author:        pickStr(m, "author", "maintainer"),
		AuthorURL:     pickStr(m, "author_url", "maintainer_url"),
		Distributor:   str(m, "distributor"),
		InstallType:   str(m, "install_type"),
		SizeMB:        anyScalarStr(m, "size"),
		IsDocker:      isDockerFlag(m),
		ServicePort:   anyScalarStr(m, "service_port"),
		DownloadURL:   str(m, "download_url"),
		Changelog:     str(m, "changelog"),
		UpdatedAt:     str(m, "updated_at"),
		DownloadCount: anyInt(m, "download_count"),
		PreviewURLs:   strListField(m, "preview_urls"),
	}
		if a.DisplayName == "" {
			a.DisplayName = name
		}
		// releases: 版本 → {changelog, updated_at, packages: {arch: {download_url, sha256, size}}}
		// V2 多版本源（如 RROrg）顶层没有 version/download_url：按版本降序取
		// 当前架构（x86→all 回退）第一个可安装包，补齐版本/下载链接/校验和/体积。
		if rel, ok := m["releases"].(map[string]any); ok && len(rel) > 0 {
			a.ReleaseChangelogs = make(map[string]string, len(rel))
			for ver, rv := range rel {
				if rm, ok := rv.(map[string]any); ok {
					if t := str(rm, "changelog"); t != "" {
						a.ReleaseChangelogs[ver] = t
					}
				}
			}
			if a.Version == "" || a.DownloadURL == "" {
				if ver, pkg := bestRelease(rel, currentArch()); ver != "" {
					if a.Version == "" {
						a.Version = ver
					}
					if a.DownloadURL == "" {
						a.DownloadURL = str(pkg, "download_url")
					}
					if s := str(pkg, "sha256"); s != "" {
						a.Sha256 = s
					}
					if n := anyInt(pkg, "size"); n > 0 {
						a.SizeBytes = int64(n)
					}
					// 注意：不把 release 说明回填到顶层 Changelog——
					// parseChangelogEntries 已优先用 ReleaseChangelogs 生成
					// 版本化条目，回填会产生一条版本为空的重复条目。
					if a.UpdatedAt == "" {
						if rm, ok := rel[ver].(map[string]any); ok {
							a.UpdatedAt = str(rm, "updated_at")
						}
					}
				}
			}
		}
		// arch_diff: 旧格式变体源把 download_url 按 x86/arm/all 分列（无顶层链接）。
		if a.DownloadURL == "" {
			if ad, ok := m["arch_diff"].(map[string]any); ok {
				for _, k := range []string{currentArch(), "all"} {
					if p, ok2 := ad[k].(map[string]any); ok2 {
						if u := str(p, "download_url"); u != "" {
							a.DownloadURL = u
							if s := str(p, "sha256"); s != "" {
								a.Sha256 = s
							}
							if n := anyInt(p, "size"); n > 0 {
								a.SizeBytes = int64(n)
							}
							break
						}
					}
				}
			}
		}
	// 无任何下载字段（releases/平铺/arch_diff 全缺）但有版本：按 FnDepot 社区约定
	// 从仓库内 <appname>/<appname>.fpk 构造直链（Docker 应用不走此约定）。
	// 有实测 base 时按它拼（自动适配 master/main），否则回退 raw main 猜测。
	if a.DownloadURL == "" && a.Version != "" && a.IsDocker != "true" {
		if base != "" {
			a.DownloadURL = strings.TrimRight(base, "/") + "/" + name + "/" + name + ".fpk"
		} else if strings.Contains(repoURL, "github.com/") {
			trimmed := strings.TrimPrefix(repoURL, "https://")
			trimmed = strings.TrimPrefix(trimmed, "http://")
			if parts := strings.SplitN(trimmed, "/", 3); len(parts) == 3 {
				a.DownloadURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s/%s.fpk",
					parts[1], strings.TrimSuffix(parts[2], ".git"), name, name)
			}
		}
	}
	// 相对资源路径（下载链接/图标/文档/预览）补全为可访问的直链：
	// 优先按 fnpack.json 实测所在目录（base）拼接——与 JSON 同树的文件一定
	// 可达，且分支正确；base 未知时回退：GitHub 仓库 → raw main 猜测，
	// 其他托管 → 源根 URL 直接拼接。
	// 相对路径判定=不含 "://"（兼容 "./x"、"app/ICON.PNG"、"app/README.md"
	// 等写法——社区源普遍不带 ./ 前缀，只认 ./ 会让 187/231 个 README
	// 和大量图标拿不到直链，靠布局猜测兜底，命中不了就整块不显示）。
	resolveRel := func(relPath string) string {
		relPath = strings.TrimSpace(relPath)
		if relPath == "" || strings.Contains(relPath, "://") {
			return relPath
		}
		rel := strings.TrimPrefix(relPath, "./")
		if base != "" {
			return strings.TrimRight(base, "/") + "/" + rel
		}
		if strings.Contains(repoURL, "github.com/") {
			trimmed := strings.TrimPrefix(repoURL, "https://")
			trimmed = strings.TrimPrefix(trimmed, "http://")
			if parts := strings.SplitN(trimmed, "/", 3); len(parts) == 3 {
				return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s",
					parts[1], strings.TrimSuffix(parts[2], ".git"), rel)
			}
		}
		return repoURL + "/" + rel
	}
	a.DownloadURL = resolveRel(a.DownloadURL)
	a.IconURL = resolveRel(a.IconURL)
	a.ReadmeURL = resolveRel(a.ReadmeURL)
	// 预览图相对路径（"./<app>/Preview/01.png" 等）同样补全为直链
	previewURLs := make([]string, 0, len(a.PreviewURLs))
	for _, p := range a.PreviewURLs {
		if abs := resolveRel(p); abs != "" {
			previewURLs = append(previewURLs, abs)
		}
	}
	a.PreviewURLs = previewURLs
	return a
}

// strListField 取字符串数组字段（兼容 JSON 数组与单字符串两种写法）。
func strListField(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	}
	return nil
}
