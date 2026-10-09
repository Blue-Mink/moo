package source

import (
	_ "embed"
	"strconv"
	"strings"
)

// bundledDefaultSources 0.6.247 起：内置默认应用源集（0.6.316+ 起 20 条，
// 每行一个源地址，全部带协议）。0.6.316 前 = 2026-10-02 备用测试机在用
// 全量 156 源 + fn-knock 官方源（157 条）；0.6.316 起按用户指令收敛为
// Blue-Mink 精选 20 源（与仓库根 repo_list.txt 同一清单），不含官方飞牛
// 应用中心（它是面板账号派生条目，不属于 config.Sources）。
//
// 两处用途（同一基准，语义一致）：
//  1. 首装自动填充——全新安装即带全部默认源（不再只有 2 个种子源，
//     首启无需联网、无需手动"恢复默认源"）；
//  2. 「恢复默认源」按钮的基准——误删的源可一键找回（离线可用，
//     不再依赖外部 repo_list.txt）。
//
//go:embed default_sources.txt
var bundledDefaultSources string

// bundledLegacyDefaultSources 0.6.318：0.6.316 之前的旧全量默认源清单
//（157 条 = 156 社区源 + fn-knock 官方源），供「删除默认源」按钮一键清理
//历史遗留的社区源。
// 语义（用户 2026-10-09 定稿）：命中本清单的现有源一律可一键删除，
// 不做官源保护——新 20 条精选官源中 19 条包含在本清单里，删除后由
//「恢复官源」按钮（BundledDefaultSources）一键找回，构成
//「删旧 157 默认 → 恢复 20 官源」的清单重置工作流。
//
//go:embed default_sources_legacy.txt
var bundledLegacyDefaultSources string

// BundledDefaultSources 返回内置默认源地址列表（去重、补协议）。
func BundledDefaultSources() []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(bundledDefaultSources, "\n") {
		u := strings.TrimSpace(line)
		if u == "" || strings.HasPrefix(u, "#") {
			continue
		}
		u = ensureSourceScheme(u)
		if k := NormalizeSourceURL(u); !seen[k] {
			seen[k] = true
			out = append(out, u)
		}
	}
	return out
}

// BundledLegacyDefaultSources 返回旧全量默认源清单（去重、补协议）。
// 仅用于「删除默认源」：现有源地址归一后命中本清单即视为可一键删除的
//历史默认源（含与新 20 条官源重叠的 19 条；官源可由「恢复官源」找回）。
func BundledLegacyDefaultSources() []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(bundledLegacyDefaultSources, "\n") {
		u := strings.TrimSpace(line)
		if u == "" || strings.HasPrefix(u, "#") {
			continue
		}
		u = ensureSourceScheme(u)
		if k := NormalizeSourceURL(u); !seen[k] {
			seen[k] = true
			out = append(out, u)
		}
	}
	return out
}

// ghOwner 识别 GitHub 系地址（github.com 仓库页 / raw 直链 / jsDelivr /
// 镜像前缀）并返回**原始大小写**的作者名。非 GitHub 系返回 ("", false)。
// 0.6.271：与 SourceURLKey 同一套前缀剥离规则，但保留大小写（显示名用）。
func ghOwner(u string) (string, bool) {
	s := strings.TrimSpace(u)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// 镜像前缀（gh-proxy 等）：剥掉首个已知 GitHub 主机之前的部分
	//（与 SourceURLKey 同一规则——按 "/主机/" 子串定位，不会误剥开头的
	// GitHub 主机本身；旧版 HasPrefix 写法会把 "github.com/owner/…" 剥成
	// "owner/…" 导致识别失败）
	for _, h := range searchKeyGHHosts {
		if i := strings.Index(s, "/"+h); i >= 0 {
			s = s[i+1:]
			break
		}
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	for _, form := range []string{"raw.githubusercontent.com/", "cdn.jsdelivr.net/gh/", "www.github.com/", "github.com/"} {
		rest, ok := strings.CutPrefix(s, form)
		if !ok {
			continue
		}
		owner, _, _ := strings.Cut(rest, "/")
		owner = strings.TrimSpace(owner)
		if owner == "" {
			return "", false
		}
		return owner, true
	}
	return "", false
}

// SourceNameFromURL 从源地址推导默认显示名。
// 规则：
//  1. conversun/fnos-apps 任意形态 → "fnos-store"（商店项目名，见 IsConversunURL）
//  2. GitHub 系（github.com 仓库页 / raw 直链 / jsDelivr / 镜像前缀）
//     → 作者名（owner，原始大小写）——0.6.271：raw 直链以前落入"取最后
//     一段"产生 "moo.json" 这类名字，现与仓库根地址一致取作者
//  3. 其它 → 剥 Gitea /raw/branch/<分支>/ 段、/raw/<分支>/ 段与索引文件
//     名后的路径最后一段（"…/owner/repo/raw/branch/main/moo.json" → "repo"）
func SourceNameFromURL(u string) string {
	if IsConversunURL(u) {
		return "fnos-store"
	}
	if owner, ok := ghOwner(u); ok {
		return owner
	}
	p := strings.TrimSpace(u)
	p = strings.TrimSuffix(p, "/")
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
	p = strings.TrimSuffix(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	p = strings.TrimSuffix(p, ".git")
	return strings.TrimSpace(p)
}

// UniqueSourceName 为源地址推导一个不与 taken 中任何名字冲突的源名：
// 首选 SourceNameFromURL；被占用时（典型：同 owner 的第二个仓库，
// 命名规则取 owner 导致重名）追加仓库名小写归一（owner-repo）；
// 仍冲突则依次追加 -2/-3… 直到唯一。
// 0.6.248：首装种子与「恢复默认源/同步源列表」共用——内置基准集含
// 7 组同 owner 双仓库（156 条 → 149 个 owner 名），旧逻辑直接重名
// 折叠/被 ErrExists 静默跳过，导致 7 条源永远补不回来。
func UniqueSourceName(u string, taken map[string]bool) string {
	name := SourceNameFromURL(u)
	if name == "" {
		return ""
	}
	if taken == nil || !taken[name] {
		return name
	}
	// 0.6.271：GitHub 系（含 raw/jsDelivr 直链）消歧名 = owner-repo
	// （与命名规则同源；旧逻辑取"倒数第二段-最后一段"，raw 直链会算出
	// "main-moo.json" 这类垃圾名）
	if owner, ok := ghOwner(u); ok {
		if rest := strings.TrimPrefix(SourceURLKey(u), "github.com/"); strings.Contains(rest, "/") {
			if _, repo, found := strings.Cut(rest, "/"); found && repo != "" {
				cand := normalizeSourceNamePart(owner + "-" + repo)
				if cand != name {
					base := cand
					n := 2
					for taken[cand] {
						n++
						cand = base + "-" + strconv.Itoa(n)
					}
					return cand
				}
			}
		}
	}
	// 非 GitHub：剥索引文件名与 raw 段后取倒数第二段-最后一段
	p := strings.TrimSpace(u)
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	p = searchKeyGiteaRawSegRe.ReplaceAllString(p, "/")
	p = searchKeyRawSegRe.ReplaceAllString(p, "/")
	for _, f := range searchKeyIndexFiles {
		p = strings.TrimSuffix(p, "/"+f)
	}
	p = strings.TrimSuffix(p, "/")
	seg := strings.Split(p, "/")
	var cand string
	if len(seg) >= 2 {
		cand = seg[len(seg)-2] + "-" + seg[len(seg)-1]
	} else {
		cand = name + "-2"
	}
	cand = normalizeSourceNamePart(cand)
	if cand == name {
		cand = name + "-2"
	}
	base := cand
	n := 2
	for taken[cand] {
		n++
		cand = base + "-" + strconv.Itoa(n)
	}
	return cand
}

// normalizeSourceNamePart 名称片段归一：小写、_ 与空格转 -、去尾部 -。
func normalizeSourceNamePart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return strings.TrimRight(s, "-")
}
