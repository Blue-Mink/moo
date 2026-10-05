package source

import (
	"fmt"
	"strings"

	"moo/internal/config"
)

// ── moo.json：Moo 原生应用源协议 v1（规范见 docs/SOURCE-PROTOCOL.md）──────────
//
// 定位：moo.json 是 FnDepot `fnpack.json` 的**超集** —— 同构的 V2 包裹
// （`schema_version` + `source_info` + `apps`），并额外支持 Moo 原生字段：
//
//	app_type   : "fpk" | "docker" | "native"（与 isdocker/is_docker 等效，优先）
//	tags       : labels/categories 的别名
//	sha256     : **条目级**校验和（FnDepot 只在 releases.packages 里带）
//	size_bytes : 条目级字节数（0 = 未提供，回退 size→MB 解析）
//
// 解析仍集中在 fndepot.go 的 translateEntry（不另起一套解析器，避免漂移）；
// 本文件只负责两件事：**候选地址**（moo.json 优先、fnpack.json 回退）与
// **Moo 扩展字段**取值。

const (
	// MooIndexFile Moo 原生索引文件名。
	MooIndexFile = "moo.json"
	// MooSchema 当前协议标识（解析只要求 schema_version 键存在，不校验取值）。
	MooSchema = "moo"
)

// jsonNamesFor 返回一个仓库地址下应按优先级尝试的索引文件名序列：
// moo.json 优先（Moo 原生，字段更全），随后是 FnDepot 的 fnpack.json（兼容回退）。
func jsonNamesFor(repo bool) []string {
	return []string{MooIndexFile, "fnpack.json"}
}

// buildCandidates 把用户填写的源地址展开成候选 JSON 地址（按优先级）。
//
// 规则（与 docs/SOURCE-PROTOCOL.md §1 一致）：
//   - `.json` 直链        → 该地址本身；
//   - GitHub 仓库地址     → raw(main/master) → 各 GitHub 镜像 → jsDelivr，
//     每个前缀下 **moo.json 先于 fnpack.json**；
//   - 其它仓库/目录地址   → `<url>/moo.json` → `<url>/fnpack.json` → `<url>/raw/main/<name>`。
//
// 之所以把 moo.json 排在前：同一仓库可能同时挂着两种索引，Moo 应读自己那份；
// 只有 moo.json 不存在时（404）才回退 FnDepot 格式。
func buildCandidates(u string) []string {
	var out []string
	switch {
	case strings.HasSuffix(u, ".json"):
		// 直链：moo.json / fnpack.json 都走这里，内容自适应（V1 平铺或 V2 包裹）。
		// 0.6.283：GitHub 系直链补镜像/规范化 raw/jsDelivr 候选——用户贴
		// raw 直链当源时候选只有孤零零一条，raw 间歇断流整源就挂
		// （2026-10-05 主 NAS 一天 11 次超时实锤），而仓库形态源有全套镜像
		// 竞速。非 GitHub 主机维持原行为（仅原 URL）。
		out = append(out, u)
		out = append(out, ghJSONVariants(u)...)
	case strings.Contains(u, "github.com/"):
		trimmed := strings.TrimPrefix(u, "https://")
		trimmed = strings.TrimPrefix(trimmed, "http://")
		parts := strings.SplitN(trimmed, "/", 3)
		if len(parts) == 3 {
			owner, repo := parts[1], strings.TrimSuffix(parts[2], ".git")
			raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", owner, repo)
			for _, branch := range []string{"main", "master"} {
				for _, name := range jsonNamesFor(true) {
					out = append(out, raw+branch+"/"+name)
				}
			}
			for _, opt := range config.GitHubMirrorOptions() {
				if opt.URL == "" { // auto = 非直连项
					continue
				}
				m := strings.TrimRight(opt.URL, "/") + "/" + raw
				for _, branch := range []string{"main", "master"} {
					for _, name := range jsonNamesFor(true) {
						out = append(out, m+branch+"/"+name)
					}
				}
			}
			for _, name := range jsonNamesFor(true) {
				out = append(out, fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s/%s", owner, repo, name))
			}
		}
		for _, name := range jsonNamesFor(true) {
			out = append(out, u+"/"+name, u+"/raw/main/"+name)
		}
	default:
		for _, name := range jsonNamesFor(false) {
			out = append(out, u+"/"+name, u+"/raw/main/"+name)
		}
	}
	// 去重（保序）：GitHub 分支里可能出现重复前缀
	seen := make(map[string]struct{}, len(out))
	uniq := out[:0]
	for _, c := range out {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		uniq = append(uniq, c)
	}
	return uniq
}

// ghJSONVariants 0.6.283：为 GitHub 系 .json 直链生成补充候选
// （镜像前缀 + 规范化 raw 直链 + jsDelivr 兜底）。识别四种入口形态：
// raw 域直链、镜像前缀包裹的 raw、jsDelivr（带/不带 @分支）、
// github.com 的 /raw/ 与 /blob/ 路径；其余主机返回 nil（原行为）。
func ghJSONVariants(u string) []string {
	p := "/" + u // 前缀哨兵，保证主机名从 "/" 边界开始匹配
	low := strings.ToLower(p)
	const rawHost = "/raw.githubusercontent.com/"
	const jdHost = "/cdn.jsdelivr.net/gh/"
	if i := strings.Index(low, rawHost); i >= 0 {
		rest := p[i+len(rawHost):]
		ownerRepo, branch, file, ok := splitRawPath(rest)
		if !ok {
			return nil
		}
		raw := "https://raw.githubusercontent.com/" + rest
		return ghVariants(ownerRepo, branch, file, raw)
	}
	if i := strings.Index(low, jdHost); i >= 0 {
		rest := p[i+len(jdHost):]
		segs := strings.SplitN(rest, "/", 3) // owner / repo[@branch] / 文件路径
		if len(segs) < 3 || segs[0] == "" || segs[1] == "" || segs[2] == "" {
			return nil
		}
		repoPart, branch, hasRef := strings.Cut(segs[1], "@")
		ownerRepo := segs[0] + "/" + strings.TrimSuffix(repoPart, ".git")
		file := segs[2]
		if hasRef {
			raw := "https://raw.githubusercontent.com/" + ownerRepo + "/" + branch + "/" + file
			return ghVariants(ownerRepo, branch, file, raw)
		}
		// 不带 @ref 的 jsDelivr：分支未知，main/master 双探（与仓库形态一致）
		var out []string
		for _, b := range []string{"main", "master"} {
			raw := "https://raw.githubusercontent.com/" + ownerRepo + "/" + b + "/" + file
			out = append(out, ghVariants(ownerRepo, b, file, raw)...)
		}
		return out
	}
	// github.com/owner/repo/raw|blob/branch/... （raw 域不含 "github.com/" 子串，
	// githubusercontent ≠ github.com，无需担心误匹配）
	if i := strings.Index(low, "/github.com/"); i >= 0 {
		rest := p[i+len("/github.com/"):]
		parts := strings.SplitN(rest, "/", 5)
		if len(parts) < 5 || (parts[2] != "raw" && parts[2] != "blob") || parts[4] == "" {
			return nil
		}
		ownerRepo := parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
		rawURL := "https://raw.githubusercontent.com/" + ownerRepo + "/" + parts[3] + "/" + parts[4]
		return ghVariants(ownerRepo, parts[3], parts[4], rawURL)
	}
	return nil
}

// splitRawPath 拆 raw 域路径 "owner/repo/branch/文件(可含子目录)"。
func splitRawPath(rest string) (ownerRepo, branch, file string, ok bool) {
	parts := strings.SplitN(rest, "/", 4)
	if len(parts) < 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", "", "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), parts[2], parts[3], true
}

// ghVariants 给定规范化 raw 直链，产出镜像前缀候选 + raw 本体 + jsDelivr。
func ghVariants(ownerRepo, branch, file, rawURL string) []string {
	var out []string
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL == "" { // auto/custom/direct = 无固定前缀
			continue
		}
		out = append(out, strings.TrimRight(opt.URL, "/")+"/"+rawURL)
	}
	out = append(out, rawURL)
	out = append(out, "https://cdn.jsdelivr.net/gh/"+ownerRepo+"@"+branch+"/"+file)
	return out
}

// appTypeOf 解析条目的应用类型，返回与 App.IsDocker 相同语义的字符串
// （"true" = 容器/Docker 应用，"" = 非容器）。
//
// 取值优先级：Moo 原生 `app_type`（docker/fpk/native，大小写不敏感）→
// FnDepot 的 `isdocker`（字符串）/ `is_docker`（bool）。
func appTypeOf(m map[string]any) string {
	if t, ok := m["app_type"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "docker", "container":
			return "true"
		case "fpk", "native", "tpk":
			return ""
		}
	}
	return isDockerFlag(m) // 回退：FnDepot 字段变体
}
