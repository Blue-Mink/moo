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

// jsonIndexNames 是仓库形态源应按优先级尝试的索引文件名**全集**，必须与
// source_key.go 的 searchKeyIndexFiles（链接归一化识别集合）逐一对应。
//
// 顺序：moo.json（Moo 原生，字段最全）→ fnpack.json（FnDepot 现行）→
// fndepot.json / fndepot_v2.json（FnDepot 早期与 V2 协议名）→ apps.json
// （conversun 形态，内容须为应用集合，故垫底且由 fetchIndex 校验内容）。
//
// 0.6.326e：修前这里只有 {moo.json, fnpack.json}，而识别集合有 5 个 →
// 「只发布 fndepot.json / fndepot_v2.json / apps.json 的仓库」识别得出却拉不到
// （用户报「有些使用 fndepot 协议的添加不了」，详情页「添加为源」点了必 404）。
var jsonIndexNames = []string{MooIndexFile, "fnpack.json", "fndepot.json", "fndepot_v2.json", "apps.json"}

// jsonNamesFor 返回一个仓库地址下应按优先级尝试的索引文件名序列（全集）。
func jsonNamesFor(repo bool) []string {
	return jsonIndexNames
}

// indexNameOf 取候选地址的索引文件名（末段，忽略 query/fragment），小写。
// 用于把 buildCandidates 产出的扁平候选按「常规名 / 扩展名 / apps.json」
// 分阶段抓取，以及在「添加为源」探测里回报命中文件名。
func indexNameOf(u string) string {
	p := u
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return strings.ToLower(p)
}

// buildCandidates 把用户填写的源地址展开成候选 JSON 地址（按优先级）。
//
// 规则（与 docs/SOURCE-PROTOCOL.md §1 一致）：
//   - `.json` 直链        → 该地址本身（+ GitHub 系镜像/raw/jsDelivr 变体）；
//   - GitHub 仓库地址     → raw(main/master) → 各 GitHub 镜像 → jsDelivr，
//     每个前缀下按 jsonIndexNames 顺序（moo.json 先于 fnpack.json 等）；
//   - 其它仓库/目录地址   → `<url>/<name>` → `<url>/raw/main/<name>`。
//
// 之所以把 moo.json 排在前：同一仓库可能同时挂着两种索引，Moo 应读自己那份；
// 只有 moo.json 不存在时（404）才回退其它格式。
//
// 0.6.326e：GitHub 仓库形态先把 owner/repo 之后的**子路径段剥掉**——用户常从
// 浏览器地址栏直接复制 `github.com/owner/repo/tree/main`，旧实现把
// "repo/tree/main" 整个当仓库名，候选全落在 `.../FnDepot/tree/main/main/moo.json`
// 上，一律 404（「有些添加不了」的第二个成因）。
// fastMirrorCount 首波（tier0）参与的固定镜像前缀数量。
//
// 0.6.330 WP2（方案见 MOO_LOADING_REFACTOR_PLAN_20261018.md）：普通刷新只打
// 「raw + jsDelivr + 前 2 个健康镜像」（config 顺序 = 实测延迟序），命中即
// return 并取消其余；只有首波全败时才由后台全量重探（mirrorLimit=-1）。
// 目的：把单源每轮的固定扫描从 72~195 个请求降到个位数，同时减少把公益镜像
// 打成 429 的概率（429 会连带拖慢图标/README/下载）。
const fastMirrorCount = 2

// buildCandidates 全量候选（所有镜像）。保留给后台全量重探、添加为源探测与单测。
func buildCandidates(u string) []string { return buildCandidatesLimited(u, -1) }

// buildCandidatesLimited 展开候选；mirrorLimit < 0 表示全部镜像，>=0 只用前 N 个。
func buildCandidatesLimited(u string, mirrorLimit int) []string {
	var out []string
	switch {
	case strings.HasSuffix(u, ".json"):
		// 直链：moo.json / fnpack.json 都走这里，内容自适应（V1 平铺或 V2 包裹）。
		// 0.6.283：GitHub 系直链补镜像/规范化 raw/jsDelivr 候选——用户贴
		// raw 直链当源时候选只有孤零零一条，raw 间歇断流整源就挂
		// （2026-10-05 主 NAS 一天 11 次超时实锤），而仓库形态源有全套镜像
		// 竞速。非 GitHub 主机维持原行为（仅原 URL）。
		out = append(out, u)
		out = append(out, ghJSONVariantsLimited(u, mirrorLimit)...)
	case strings.Contains(u, "github.com/"):
		trimmed := strings.TrimPrefix(u, "https://")
		trimmed = strings.TrimPrefix(trimmed, "http://")
		parts := strings.SplitN(trimmed, "/", 3)
		if len(parts) == 3 {
			// 0.6.326e：/tree/<分支>[/子目录]、/blob/... 等子路径段一律剥掉，
			// 只保留 owner/repo（GitHub 仓库根的规范形态）。
			owner, repo := parts[1], strings.TrimSuffix(parts[2], ".git")
			if i := strings.Index(repo, "/"); i >= 0 {
				repo = strings.TrimSuffix(repo[:i], ".git")
			}
			if owner != "" && repo != "" {
				raw := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", owner, repo)
				for _, branch := range []string{"main", "master"} {
					for _, name := range jsonNamesFor(true) {
						out = append(out, raw+branch+"/"+name)
					}
				}
				used := 0
				for _, opt := range config.GitHubMirrorOptions() {
					if opt.URL == "" { // auto = 非直连项
						continue
					}
					if mirrorLimit >= 0 && used >= mirrorLimit {
						break
					}
					used++
					m := strings.TrimRight(opt.URL, "/") + "/" + raw
					for _, branch := range []string{"main", "master"} {
						for _, name := range jsonNamesFor(true) {
							out = append(out, m+branch+"/"+name)
						}
					}
				}
				// 0.6.331 P0：jsDelivr 候选必须带 @<分支>。
				//
				// 无 @ref 形态（`cdn.jsdelivr.net/gh/<o>/<r>/<索引>`）走 jsDelivr
				// 的「默认分支」解析 + 长期边缘缓存，实测会返回**陈旧副本**：
				// RROrg/fn-apps 9 vs 正确 31、wabisabi926/FnDepot 58 vs 109、
				// Blue-Mink/FnDepot 10 vs 14、xianyu110 86 vs 87（2026-10-18 实锤）。
				// 而竞速是「首个 200 胜出」——快而陈旧的副本会压过 raw 直连，
				// 重启后健康数据冷时尤其明显（实测卡片 2156→2081 / 1836→1814）。
				// 带 @main/@master 的形态缓存键含 ref，内容与 raw 一致。
				for _, branch := range []string{"main", "master"} {
					for _, name := range jsonNamesFor(true) {
						out = append(out, fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s@%s/%s", owner, repo, branch, name))
					}
				}
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
// ghJSONVariants 全量变体（兼容旧调用/单测）。
func ghJSONVariants(u string) []string { return ghJSONVariantsLimited(u, -1) }

// ghJSONVariantsLimited 生成直链的补充候选；mirrorLimit 控制镜像数量。
func ghJSONVariantsLimited(u string, mirrorLimit int) []string {
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
		return ghVariants(ownerRepo, branch, file, raw, mirrorLimit)
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
			return ghVariants(ownerRepo, branch, file, raw, mirrorLimit)
		}
		// 不带 @ref 的 jsDelivr：分支未知，main/master 双探（与仓库形态一致）
		var out []string
		for _, b := range []string{"main", "master"} {
			raw := "https://raw.githubusercontent.com/" + ownerRepo + "/" + b + "/" + file
			out = append(out, ghVariants(ownerRepo, b, file, raw, mirrorLimit)...)
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
		return ghVariants(ownerRepo, parts[3], parts[4], rawURL, mirrorLimit)
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
func ghVariants(ownerRepo, branch, file, rawURL string, mirrorLimit int) []string {
	var out []string
	used := 0
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL == "" { // auto/custom/direct = 无固定前缀
			continue
		}
		if mirrorLimit >= 0 && used >= mirrorLimit {
			break
		}
		used++
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
