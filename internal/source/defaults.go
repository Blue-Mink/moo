package source

import (
	_ "embed"
	"strconv"
	"strings"
)

// bundledDefaultSources 0.6.247：内置默认应用源集（156 条，每行一个源地址，
// 全部带协议）。基准 = 2026-10-02 备用测试机（0.6.245-panel）在用的全部
// 156 个源（用户验收过的完整集，含 0.6.141 修复后的规范地址）；不含官方
// 飞牛应用中心（它是面板账号派生条目，不属于 config.Sources）。
//
// 两处用途（同一基准，语义一致）：
//  1. 首装自动填充——全新安装即带全部默认源（不再只有 2 个种子源，
//     首启无需联网、无需手动"恢复默认源"）；
//  2. 「恢复默认源」按钮的基准——误删的源可一键找回（离线可用，
//     不再依赖外部 repo_list.txt）。
//
//go:embed default_sources.txt
var bundledDefaultSources string

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

// SourceNameFromURL 从源地址推导源名（与添加源入口同一命名规则）：
// conversun/fnos-apps 固定取商店项目名 fnos-store；其余 GitHub 仓库取
// owner；其它地址取最后一段路径（去 .git 与 /fnpack.json）。
// 0.6.247 从 api 层迁入 source 包：首装填充与添加源共用，避免两套命名。
func SourceNameFromURL(u string) string {
	if IsConversunURL(u) {
		return "fnos-store"
	}
	p := strings.TrimSpace(u)
	p = strings.TrimSuffix(p, "/")
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	p = strings.TrimSuffix(p, "/fnpack.json")
	p = strings.TrimSuffix(p, "/")
	seg := strings.Split(p, "/")
	if len(seg) >= 3 && (seg[0] == "github.com" || seg[0] == "www.github.com") {
		if owner := strings.TrimSpace(seg[1]); owner != "" {
			return owner
		}
	}
	last := seg[len(seg)-1]
	last = strings.TrimSuffix(last, ".git")
	return strings.TrimSpace(last)
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
	p := strings.TrimSpace(u)
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
	}
	p = strings.TrimSuffix(p, "/fnpack.json")
	p = strings.TrimSuffix(p, "/")
	seg := strings.Split(p, "/")
	var cand string
	if len(seg) >= 3 && (seg[0] == "github.com" || seg[0] == "www.github.com") {
		cand = seg[1] + "-" + seg[len(seg)-1]
	} else if len(seg) >= 2 {
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
