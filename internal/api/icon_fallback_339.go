package api

// icon_fallback_339.go —— 图标最后一层兜底（0.6.339）。
//
// 背景（2026-10-19 实测）：社区源「Hxido-RXM」22 张卡里 18 张没有 icon_url
// （自动构建产物只写了 releases），而源仓库根目录只有 4 个应用目录——
// 主候选 <appname>/ICON.PNG 六条全 404；目录别名（icon_alias）依赖
// jsDelivr 列目录，该仓 >50MB 被 403「Package size exceeded」，退避到 6h
// 上限，永久不可用；FPK 兜底又要求先声明 icon_url，全部跳过。
// 于是发现页「最近更新」里成片空图标。
//
// 真图其实在两个「同 owner 的确定位置」：
//  ① 源仓里目录名与 appname 只差大小写（1Panel → 1panel、CloudDrive2 → clouddrive2）
//  ② 同 owner 的自动构建仓：<owner>/fnos-apps → apps/<小写名>/fnos/ICON.PNG
//
// 本文件只做这两类**确定性**探测：小写全等 + 极短显式别名白名单，不做
// 模糊匹配（张冠李戴的图标比占位更糟）。且只在主流程全部候选失败后串行
// 触发，绝不并进主候选列表——全目录 2200+ 张卡，并行候选会重演 0.6.326
// 的镜像放大 429 风暴（当时 24 候选/卡打出 4088 条 429）。

import (
	"context"
	"strings"
	"time"

	"moo/internal/source"
)

const (
	// iconFBWindow 兜底层整体预算：主流程已吃掉大部分 15s，这里只给 6s。
	iconFBWindow = 6 * time.Second
	// iconFBMaxCands 候选上限（展开镜像后 ×3），避免失败路径打太多请求。
	iconFBMaxCands = 6
)

// iconFBAlias 显式别名白名单：只放「确认是同一个应用」的改名，
// 宁缺毋滥——类似 clouddrive→cloudreve 这种猜测一率不做。
var iconFBAlias = map[string]string{
	"openlist-beta": "openlist",    // 上游目录名 openlist
	"adguard":       "adguardhome", // 上游目录名 adguardhome
}

// iconFBCandidates 生成确定性兜底候选（原始 raw 地址，镜像由调用方展开）。
// 顺序即优先级：源仓大小写变体最便宜，其次同 owner 构建仓。
func iconFBCandidates(appName, sourceBase string) []string {
	owner, repo, ok := iconAliasRepo(sourceBase)
	if !ok {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(appName))
	if lower == "" || strings.ContainsAny(lower, `/\`) || strings.Contains(lower, "..") {
		return nil
	}
	raw := func(o, r, br, p string) string {
		return strings.TrimRight(iconAliasRawBase, "/") + "/" + o + "/" + r + "/" + br + "/" + p
	}
	var out []string
	// ① 源仓同名目录的小写变体
	if lower != strings.TrimSpace(appName) {
		for _, br := range []string{"main", "master"} {
			out = append(out, raw(owner, repo, br, lower+"/ICON.PNG"))
		}
	}
	// ② 同 owner 的自动构建仓（只认这两个固定仓名，不猜其它命名）
	names := []string{lower}
	if alt, has := iconFBAlias[lower]; has {
		names = append(names, alt)
	}
	for _, build := range []string{"fnos-apps", "fn-apps"} {
		for _, n := range names {
			out = append(out, raw(owner, build, "main", "apps/"+n+"/fnos/ICON.PNG"))
		}
	}
	if len(out) > iconFBMaxCands {
		out = out[:iconFBMaxCands]
	}
	return out
}

// resolveIconFallbackExtra 主流程全败后的最后兜底。
// 复用 raceAliasCandidates（内部自展开镜像 + 占位图校验 + defMiss 记录）。
func (s *Server) resolveIconFallbackExtra(ctx context.Context, a *source.App, defMissPaths map[string]bool) ([]byte, string, bool) {
	if a == nil || defMissPaths == nil {
		return nil, "", false
	}
	cands := iconFBCandidates(a.Name, s.sourceBaseURL(a.Source))
	if len(cands) == 0 {
		return nil, "", false
	}
	fctx, cancel := context.WithTimeout(ctx, iconFBWindow)
	defer cancel()
	data, ct, ok := s.raceAliasCandidates(fctx, cands, defMissPaths)
	if !ok {
		return nil, "", false
	}
	small, sct, _ := downsampleIcon(data, ct)
	return small, sct, true
}
