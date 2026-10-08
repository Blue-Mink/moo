package api

import (
	"moo/internal/reco"
	"sort"
	"strings"
)

// 0.6.314 C：发现页推荐固定前三（fnos-apps-store / fndepot / fn-knock，
// 顺序用户定稿、不随机）。前三位每请求按当前目录判定（不粘性）：
// 源在目录里=实时条目；源被删/抓取失败=回落内嵌完整元数据快照
// （internal/reco，构建期生成）。源恢复后下一请求自动切回实时。
//
// SWR 兼容（0.6.313 B4）：目录缓存过期有旧时（最长 TTL，默认 60s），
// 「源刚删」的判定可能滞后到下次重建——可接受（注释说明：源删了最多多
// 60s 仍显示实时卡，下一轮重建后回落快照），不为此破坏缓存失效路径。

// fixedSlotLive 在目录里找固定推荐位的实时条目。
// 判定口径（大小写/多源重名歧义同现有 resolveKey 规则：appname 归一匹配）：
//  1. 精确 AppName 匹配，优先 Source=快照规范源名（源在设备上的常态名，
//     如 fnos-store / Blue-Mink / kci-lnk）；
//  2. 精确匹配无首选源 → 取任一精确匹配条目（源被改名/仅剩镜像源时，
//     实时数据仍优于快照）；
//  3. 精确无果 → 归一化（大小写/分隔符漂移）匹配兜底。
//
// 命中返回条目；目录里完全没有该应用（源删+抓取失败/从未入库）→ ok=false，
// 调用方回落快照。
func fixedSlotLive(catalog []AppInfo, appName string) (AppInfo, bool) {
	prefer := ""
	if snap := reco.Lookup(appName); snap != nil {
		prefer = snap.Source
	}
	var exactAny, normHit *AppInfo
	for i := range catalog {
		c := &catalog[i]
		if c.AppName == appName {
			if c.Source == prefer {
				return *c, true
			}
			if exactAny == nil {
				exactAny = c
			}
			continue
		}
		if normHit == nil && strings.EqualFold(c.AppName, appName) {
			normHit = c
		}
	}
	if exactAny != nil {
		return *exactAny, true
	}
	if normHit != nil {
		return *normHit, true
	}
	return AppInfo{}, false
}

// snapshotForKey 把目录 key（appname 或 appname@源名）映射到固定推荐快照。
// 匹配只取 appname 段（@ 前，大小写不敏感）——源删除/改名后源名段不是
// 可靠依据（深链 hash 可能携带旧源名，如 fn-knock@kci-lnk）。
// 仅当该 key 不在目录中时才应调用（调用方负责先查目录）。
func snapshotForKey(key string) *reco.Snapshot {
	name := key
	if i := strings.LastIndex(key, "@"); i > 0 {
		name = key[:i]
	}
	return reco.Lookup(name)
}

// snapshotAppInfo 把快照转成与实时条目字段形态一致的 AppInfo
// （前端零改动渲染）。版本/包选择语义同 source.bestRelease：
// 版本降序取首个含当前架构（回退 all）可安装包。
func snapshotAppInfo(snap *reco.Snapshot) AppInfo {
	best := snap.Releases
	ver, hitPkg := "", &reco.SnapshotPackage{}
	archs := []string{}
	if len(best) > 0 {
		// 文件内 releases 已按版本降序（生成器保证）；防御性再排一次。
		sorted := make([]reco.SnapshotRelease, len(best))
		copy(sorted, best)
		sort.SliceStable(sorted, func(i, j int) bool {
			return compareVersions(sorted[i].Version, sorted[j].Version) > 0
		})
		want := localArch()
		for _, r := range sorted {
			for i := range r.Packages {
				p := &r.Packages[i]
				if p.DownloadURL == "" {
					continue
				}
				if p.Arch == want || p.Arch == "all" {
					ver, hitPkg = r.Version, p
					archs = archsOfRelease(r)
					break
				}
			}
			if ver != "" {
				break
			}
		}
	}
	ai := AppInfo{
		Key:            snap.Key,
		AppName:        snap.AppName,
		DisplayName:    snap.DisplayName,
		Description:    snap.Desc,
		Installed:      false, // 快照回落=目录无此应用（含已装卡必然在目录）
		LatestVersion:  ver,
		ReleaseURL:     hitPkg.DownloadURL,
		SizeBytes:      hitPkg.Size,
		Sha256:         hitPkg.Sha256,
		Arch:           hitPkg.Arch,
		Archs:          archs,
		Homepage:       snap.Homepage,
		IconURL:        snap.IconURL,
		UpdatedAt:      snap.UpdatedAt,
		AppType:        orNative(snap.AppType),
		Source:         snap.Source,
		Maintainer:     snap.Author,
		MaintainerURL:  snap.AuthorURL,
		Distributor:    snap.Distributor,
		ServicePort:    snap.ServicePort,
		InstallType:    snap.InstallType,
		FirstReleaseAt: snap.FirstReleaseAt,
		DescHTML:       snap.DescHTML,
		License:        snap.License,
		MinFnos:        snap.MinFnos,
		HasReadme:      snap.Readme != "",
		PreviewCount:   len(snap.PreviewURLs),
		PreviewURLs:    snap.PreviewURLs,
		Platform:       "fnos",
	}
	// 分类后处理与目录组装末尾的 classifyAll 完全同口径（精选表 →
	// 有效 13 键保留 → 标签映射/关键词归类），保证快照卡与实时卡分类一致。
	cat := mapCategory(snap.Labels)
	if c, ok := curatedCategories[strings.ToLower(strings.TrimSpace(snap.AppName))]; ok {
		cat = c
	} else if !validCategoryKeys[cat] {
		cat = classifyApp(snap.AppName, snap.DisplayName, snap.Desc, cat)
	}
	ai.Category = cat
	// 更新内容列表：与实时详情同口径——顶层 changelog 空（releases 源不回填），
	// 条目由版本化 releases 说明生成（parseChangelogEntries 同款逻辑）。
	rel := make(map[string]string, len(snap.Releases))
	for _, r := range snap.Releases {
		if r.Changelog != "" {
			rel[r.Version] = r.Changelog
		}
	}
	ai.ChangelogEntries = parseChangelogEntries("", ver, rel)
	if n := snap.DownloadCount; n > 0 {
		ai.DownloadCount = &n
	}
	return ai
}

// archsOfRelease 取某版本全部架构键的展示序（x86→arm→all→字母序，
// 与 source.archDisplayKeys 同规则）。
func archsOfRelease(r reco.SnapshotRelease) []string {
	keys := make([]string, 0, len(r.Packages))
	for _, p := range r.Packages {
		if p.DownloadURL != "" {
			keys = append(keys, p.Arch)
		}
	}
	rank := map[string]int{"x86": 0, "arm": 1, "all": 2}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, oki := rank[keys[i]]
		rj, okj := rank[keys[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}

// orNative app_type 兜底（快照缺省时与 toAppInfo 默认一致）。
func orNative(s string) string {
	if s == "" {
		return "native"
	}
	return s
}
