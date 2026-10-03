package api

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"moo/internal/platform"
	"moo/internal/source"
)

// cachedCatalog 返回目录缓存（共享只读切片，5s 内复用；过期自动重建）。
// 只读约束：调用方不得修改元素字段（列表瘦身等写操作先 cachedCatalogCopy）。
func (s *Server) cachedCatalog() []AppInfo {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	ttl := s.catalogTTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	if s.catalogData != nil && time.Since(s.catalogAt) < ttl {
		return s.catalogData
	}
	s.catalogData = s.buildCatalog()
	s.catalogAt = time.Now()
	return s.catalogData
}

// cachedCatalogCopy 返回可安全修改的副本（1700+ 结构体浅拷贝，约百微秒）。
func (s *Server) cachedCatalogCopy() []AppInfo {
	c := s.cachedCatalog()
	out := make([]AppInfo, len(c))
	copy(out, c)
	return out
}

// invalidateCatalog 安装/更新/卸载/启停/源同步等改变目录内容的操作后调用。
func (s *Server) invalidateCatalog() {
	s.catalogMu.Lock()
	s.catalogData = nil
	s.catalogAt = time.Time{}
	s.catalogMu.Unlock()
}

// buildCatalog 合并应用源元数据与 daemon 安装状态，生成前端 AppInfo 列表。
// 目录里存在但本机未装的 → installed=false；本机已装但不在任何源里的
// （系统自带 / 源删了）也列入，保证「已装」页完整。
func (s *Server) buildCatalog() []AppInfo {
	all := s.Src.Apps("", "")
	installed, _ := platform.ListInstalled(context.Background())
	byName := make(map[string]platform.InstalledApp, len(installed))
	for _, ia := range installed {
		byName[ia.AppName] = ia
	}

	out := make([]AppInfo, 0, len(all)+len(installed))
	seen := map[string]bool{}

	// 同名条目预计数（O(n)）——KeyOf 逐个扫 all 是 O(n²) 热点。
	nameCount := make(map[string]int, len(all))
	for _, a := range all {
		nameCount[a.Name]++
	}

	for _, a := range all {
		ai := toAppInfo(a, nameCount[a.Name])
		if ia, ok := byName[a.Name]; ok {
			fillInstalled(&ai, ia)
		}
		out = append(out, ai)
		seen[a.Name] = true
	}

	for name, ia := range byName {
		if seen[name] {
			continue
		}
		ai := AppInfo{
			Key:              name,
			AppName:          name,
			DisplayName:      ia.Name,
			IconURL:          ia.Icon, // 面板静态图标 /app-center-static/icon/<app>/icon.png
			Installed:        true,
			InstalledVersion: ia.Version,
			InstalledFPKVer:  ia.Version,
		}
		fillInstalled(&ai, ia)
		out = append(out, ai)
	}

	// 模糊匹配：已装与目录 appname 存在大小写/分隔符漂移时
	// （如已装 "KmsActivator" ↔ 目录 "kms-activator"），把目录元数据
	// （简介/源/版本/下载链接）链接到已装条目，并移除重复的未装条目
	{
		normName := func(s string) string {
			var b strings.Builder
			for _, r := range strings.ToLower(s) {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					b.WriteRune(r)
				}
			}
			return b.String()
		}
		catalogIdx := make(map[string][]int) // 归一化名 → out 下标列表（仅未装条目）
		for i := range out {
			nm := normName(out[i].AppName)
			if !out[i].Installed {
				catalogIdx[nm] = append(catalogIdx[nm], i)
			}
		}
		i := 0
		for i < len(out) {
			ai := &out[i]
			linked := false
			if ai.Installed && ai.Source == "" {
				if idxs, ok := catalogIdx[normName(ai.AppName)]; ok {
					// 0.6.174：同名候选优先连「与已装版本一致的条目」（同一
					// 源作者最可能），其余按原顺序；逐一过显示名防误连。
					var order []int
					for _, k := range idxs {
						if out[k].LatestVersion == ai.InstalledVersion {
							order = append(order, k)
						}
					}
					for _, k := range idxs {
						if out[k].LatestVersion != ai.InstalledVersion {
							order = append(order, k)
						}
					}
					for _, ci := range order {
						c := &out[ci]
						// 显示名也要对上，防止归一化碰撞误连
						if c.DisplayName == "" || strings.EqualFold(c.DisplayName, ai.DisplayName) {
							ai.Description = c.Description
							ai.Source = c.Source
							ai.Category = c.Category
							ai.AppType = c.AppType
							ai.DownloadCount = c.DownloadCount
							ai.ReleaseURL = c.ReleaseURL
							ai.ReleaseNotes = c.ReleaseNotes
							ai.Changelog = c.Changelog
							ai.Homepage = c.Homepage
							ai.SizeBytes = c.SizeBytes
							ai.Sha256 = c.Sha256
							ai.HasReadme = c.HasReadme
							ai.PreviewCount = c.PreviewCount
							ai.PreviewURLs = c.PreviewURLs
							ai.PostInstallNote = c.PostInstallNote
							ai.Maintainer = c.Maintainer
							ai.MaintainerURL = c.MaintainerURL
							ai.Distributor = c.Distributor
							ai.DistributorURL = c.DistributorURL
							if c.LatestVersion != "" {
								ai.LatestVersion = c.LatestVersion
								if ai.InstalledVersion != "" && c.LatestVersion != ai.InstalledVersion &&
									compareVersions(c.LatestVersion, ai.InstalledVersion) > 0 {
									ai.HasUpdate = true
									ai.AvailableVersion = c.LatestVersion
								}
							}
							// 移除重复的未装目录条目
							out = append(out[:ci], out[ci+1:]...)
							for k, v := range catalogIdx {
								for m := len(v) - 1; m >= 0; m-- {
									if v[m] == ci {
										v = append(v[:m], v[m+1:]...)
									} else if v[m] > ci {
										v[m]--
									}
								}
								if len(v) == 0 {
									delete(catalogIdx, k)
								} else {
									catalogIdx[k] = v
								}
							}
							linked = true
							break
						}
					}
				}
			}
			if !linked {
				i++
			}
		}
	}

	// 官方应用中心目录（0.6.255 起恒并入，纯 OAuth 通道；未授权时
	// Apps 返回空列表 + 错误，下方 len>0 守卫自然跳过）：
	// - 已装但无源 → 补官方源信息（可更新判断/官方徽章）
	// - 社区源也有 → 跨源取最高版本（官方版本更高时更新 has_update）
	if s.Panel != nil {
		if official, _ := s.Panel.Apps(context.Background()); len(official) > 0 {
			for _, oa := range official {
				idx := -1
				for i := range out {
					if out[i].AppName == oa.AppName {
						idx = i
						break
					}
				}
				if idx < 0 {
					if seen[oa.AppName] {
						continue
					}
					seen[oa.AppName] = true
					out = append(out, oa)
					continue
				}
				e := &out[idx]
				if e.Source == "" {
					// 已装无源 → 采用官方条目元数据（保留已装状态）
					e.Category = oa.Category
					e.AppType = oa.AppType
					e.DownloadCount = oa.DownloadCount
					if e.LatestVersion == "" {
						e.LatestVersion = oa.LatestVersion
					}
				}
				// 应用存在于官方目录 → 源徽章统一「飞牛应用中心源」：
				// 官方版本已并入本卡作为更新目标，卡片代表官方应用，
				// 不能顶着社区源的徽章（1Panel/OpenList 等官方重名应用）
				e.Source = OfficialSourceID
				// 官方下载量是面板权威统计，有则覆盖（社区源无此数据或不准）
				if oa.DownloadCount != nil {
					e.DownloadCount = oa.DownloadCount
				}
				// 官方详情回填（ensureBackfill 预热）：目录条目缺简介/归属时
				// 用官方条目补齐——否则官方应用（如 1Panel）列表里永远没有简介
				if e.Description == "" {
					e.Description = oa.Description
				}
				// 开发者/发布者以官方为准（官方值非空时覆盖社区值）；
				// 回填预热完成前官方值为空则保留社区值
				if oa.Maintainer != "" {
					e.Maintainer = oa.Maintainer
					if oa.MaintainerURL != "" {
						e.MaintainerURL = oa.MaintainerURL
					}
				}
				if oa.Distributor != "" {
					e.Distributor = oa.Distributor
					if oa.DistributorURL != "" {
						e.DistributorURL = oa.DistributorURL
					}
				}
				if e.Installed && oa.LatestVersion != "" {
					// 已装官方应用：卡片即官方应用，更新判定只认官方目录版本。
					// 社区源同名条目的更高版本不构成「更新」——官方应用中心
					// 不显示更新时 Moo 也不能显示，更不能用社区包冒充官方
					// 应用的更新目标（实测 nodejs_v22/python312 等 5 个官方
					// 应用被社区同名条目污染出假更新，更新目标还是社区包）。
					applyOfficialUpdateForInstalled(e, oa)
				} else if !e.Installed {
					// 未安装卡：跨源取最高版本（保留既有行为）
					if oa.LatestVersion != "" && compareVersions(oa.LatestVersion, e.LatestVersion) > 0 {
						e.LatestVersion = oa.LatestVersion
					}
				}
			}
		}
	}

	// 官方卡预览借用：官方合并只并入「第一个同名社区条目」，那个源可能
	// 没声明 preview_urls，而别的源声明了（实测 EasyTier：合并目标
	// @Geeeeeker 0 张、@shuangji66 2 张）。官方目录本身 355 应用里只有
	// 25 个自研应用有官方 poster（面板 API 实测），其余第三方官方应用
	// 平台不提供预览——同名社区条目的 preview_urls 是唯一可借数据。
	// 规则：无预览的官方卡 ← 精确同名（预览最多的社区条目）；否则
	// 归一化同名（大小写/分隔符漂移，如 OpenList~Openlist）且显示名
	// 一致才借，防归一化碰撞误配。
	borrowOfficialPreviews(out)

	// 本地兜底：不属于任何源（源里查不到）的已装应用，
	// 从本机 FPK 包缓存目录的 manifest 读简介/归属（不走源、不走网络）。
	// 全量索引后台渐进构建，随后请求即可取到（与官方详情回填同款渐进式）。
	fpkLocalInstance.EnsureIndex()
	for i := range out {
		ai := &out[i]
		if !ai.Installed || ai.Description != "" {
			continue
		}
		if info, ok := fpkLocalInstance.Lookup(ai.AppName); ok && info.Desc != "" {
			ai.Description = info.Desc
			if ai.Maintainer == "" {
				ai.Maintainer = info.Maintainer
			}
			if ai.MaintainerURL == "" {
				ai.MaintainerURL = info.MaintainerURL
			}
			if ai.Distributor == "" {
				ai.Distributor = info.Distributor
			}
			if ai.DistributorURL == "" {
				ai.DistributorURL = info.DistributorURL
			}
		}
	}

	// GitHub Release 资产下载量：第三方源目录按规范不统计下载量，
	// 但 FPK 挂在 GitHub Releases 上——Release 资产 download_count 是
	// 真实全网下载次数（GHStats 后台 30 分钟一轮同步，ETag/限流感知）。
	// 官方应用已有面板全局统计，不覆盖。
	if s.GHStats != nil {
		for i := range out {
			ai := &out[i]
			if ai.DownloadCount != nil || ai.ReleaseURL == "" {
				continue
			}
			if o, r, asset, ok := source.ParseGhReleaseURL(ai.ReleaseURL); ok {
				if n, ok := s.GHStats.Lookup(o, r, asset); ok && n > 0 {
					dl := n
					ai.DownloadCount = &dl
				}
			}
		}
	}
	// 本机安装次数（下载量展示回退链：源/Release 都无下载量时）。
	// 已安装的应用本机至少装过一次 → 下限 1，避免「装过但不显示任何数字」。
	for i := range out {
		n := s.Cfg.LocalInstalls[out[i].AppName]
		if out[i].Installed && n < 1 {
			n = 1
		}
		if n > 0 {
			out[i].LocalInstalls = n
		}
	}

	// 已装规范条目：每个已装应用（appname）只保留一个代表「实际下载的那个」
	// 的条目，其余同名条目（其他源的包并未下载）降级为未装状态。
	// 0.6.174：传入平台升级目标版本表，规范条目识别优先命中真实来源源，
	// 更新判定只认同一源作者（规范卡自身来源）的最新版本。
	upgVer := make(map[string]string, len(byName))
	for n, ia := range byName {
		if ia.UpgradeInfo != nil && ia.UpgradeInfo.Version != "" {
			upgVer[n] = ia.UpgradeInfo.Version
		}
	}
	markInstalledCanonical(out, upgVer)

	// 平台权威更新信号（daemon upgradeInfo，与应用中心 UI 同源），
	// 补齐目录版本滞后漏掉的「有更新」。
	applyPlatformUpgrades(out, byName)

	// 平台更新权威收敛（0.6.257）：appcenter 跟踪的应用（sourceID 非空 或
	// source=official）更新判定完全交给 daemon 权威——官方目录断连时社区同名
	// 条目的更高版本不得冒充平台应用的更新目标。手动 FPK（无 sourceID）保留
	// 社区源更新。必须放在 applyPlatformUpgrades 之后（尊重 daemon 真升级目标）、
	// applyIgnoredUpdates 之前（被忽略应用仍由后者统一抑制）。
	applyPlatformUpdateAuthority(out, byName)

	// 忽略更新（0.6.181）：必须放在 applyPlatformUpgrades 之后，
	// 否则平台信号会把被忽略应用的 HasUpdate 再 OR 回来。
	applyIgnoredUpdates(out, s.Cfg.UpdateIgnored)

	// 智能自动归类：原始标签/空值 → 前端 8 分类（标签映射 + 关键词兜底），
	// 让「AI/媒体自动化/下载传输/网络工具/浏览器」等分类有内容可筛。
	classifyAll(out)

	// 确定性排序：显示名相同（同名应用不去重后的多源条目）时按源/appname/key
	// 逐级定序。Map 遍历顺序随机，若只按显示名排序，同名条目每次构建都会换位，
	// 响应字节漂移 → /api/apps 的 ETag 永远变化、304 重校验失效。
	sort.Slice(out, func(i, j int) bool {
		if out[i].DisplayName != out[j].DisplayName {
			return out[i].DisplayName < out[j].DisplayName
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].AppName < out[j].AppName
	})
	return out
}

var changelogVerRe = regexp.MustCompile(`^(\d+(?:\.\d+)+(?:[A-Za-z0-9.\-]*)?)\s*[:：]\s*(.+)$`)

// parseChangelogEntries 把 FnDepot 顶层 changelog 拼接串解析为版本化条目（最新在前）。
// 约定格式：「1.20.16: 说明；1.20.15: 说明；…」——版本段以「数字.数字[:：]」开头，
// 不带版本前缀的片段并入前一条（如「当前仅发布 x86 版」）。
// releases[最新版本].changelog 更干净，存在时覆盖对应条目。
// 完全解析不出版本模式时退回单条（版本=最新版本）。
func parseChangelogEntries(raw, latest string, rel map[string]string) []ChangelogEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" && len(rel) == 0 {
		return nil
	}
	var entries []ChangelogEntry
	anyVersioned := false
	if raw != "" {
		for _, seg := range strings.Split(raw, "；") {
			seg = strings.TrimSpace(seg)
			seg = strings.TrimSuffix(seg, "；")
			seg = strings.TrimSpace(seg)
			if seg == "" {
				continue
			}
			if m := changelogVerRe.FindStringSubmatch(seg); m != nil {
				anyVersioned = true
				entries = append(entries, ChangelogEntry{Version: m[1], Text: strings.TrimSpace(m[2])})
			} else if len(entries) > 0 {
				entries[len(entries)-1].Text += "；" + seg
			} else {
				entries = append(entries, ChangelogEntry{Text: seg})
			}
		}
	}
	if !anyVersioned {
		if len(rel) > 0 {
			for ver, t := range rel {
				entries = append(entries, ChangelogEntry{Version: ver, Text: strings.TrimSpace(t)})
			}
			sort.SliceStable(entries, func(i, j int) bool {
				return compareVersions(entries[i].Version, entries[j].Version) > 0
			})
		} else {
			entries = []ChangelogEntry{{Version: latest, Text: raw}}
		}
		return entries
	}
	// releases 里最新版说明更干净 → 覆盖/补首位
	if t := strings.TrimSpace(rel[latest]); t != "" {
		replaced := false
		for i := range entries {
			if compareVersions(entries[i].Version, latest) == 0 {
				entries[i].Text = t
				replaced = true
				break
			}
		}
		if !replaced {
			entries = append([]ChangelogEntry{{Version: latest, Text: t}}, entries...)
		}
	}
	return entries
}

// toAppInfo 把源元数据映射为前端契约。
func toAppInfo(a *source.App, sameNameCount int) AppInfo {
	appType := "native"
	if v := a.IsDocker; v == "1" || strings.EqualFold(v, "true") {
		appType = "docker"
	}
	sizeBytes := a.SizeBytes
	if sizeBytes <= 0 {
		sizeBytes = parseSizeBytes(a.SizeMB)
	}
	dlCount := a.DownloadCount
	ai := AppInfo{
		Key:            source.KeyOfCounted(a, sameNameCount),
		AppName:        a.Name,
		DisplayName:    a.DisplayName,
		Description:    a.Desc,
		LatestVersion:  a.Version,
		ReleaseURL:     a.DownloadURL,
		ReleaseNotes:   a.Changelog,
		Homepage:       a.HomePage,
		IconURL:        a.IconURL,
		UpdatedAt:      a.UpdatedAt,
		AppType:        appType,
		Category:       mapCategory(a.Labels),
		Source:         a.Source,
		Maintainer:     a.Author,
		MaintainerURL:  a.AuthorURL,
		Distributor:    a.Distributor,
		Changelog:      a.Changelog,
		SizeBytes:      sizeBytes,
		InstallType:    a.InstallType,
		FirstReleaseAt: a.FirstReleaseAt,
		Sha256:         a.Sha256,
		PreviewCount:   len(a.PreviewURLs),
		PreviewURLs:    a.PreviewURLs,
		HasReadme:      a.ReadmeURL != "",
		Platform:       "fnos",
	}
	if dlCount > 0 {
		ai.DownloadCount = &dlCount
	}
	ai.ChangelogEntries = parseChangelogEntries(a.Changelog, a.Version, a.ReleaseChangelogs)
	return ai
}

// borrowOfficialPreviews 给无预览图的官方卡从同名社区条目借预览图。
// 社区条目保留原样（用户仍可从任意源安装），只把预览 URL 集合（指向
// 社区源仓库的截图，内容是应用界面，与源无关）复制到官方卡上。
func borrowOfficialPreviews(out []AppInfo) {
	normName := func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	// 收集捐赠者：非官方、有预览的条目；同名取预览最多的
	best := func(donor, cand *AppInfo) *AppInfo {
		if donor == nil {
			return cand
		}
		if cand.PreviewCount > donor.PreviewCount {
			return cand
		}
		return donor
	}
	byExact := make(map[string]*AppInfo)
	byNorm := make(map[string]*AppInfo)
	for i := range out {
		e := &out[i]
		if e.Source == OfficialSourceID || e.PreviewCount == 0 {
			continue
		}
		byExact[e.AppName] = best(byExact[e.AppName], e)
		byNorm[normName(e.AppName)] = best(byNorm[normName(e.AppName)], e)
	}
	for i := range out {
		e := &out[i]
		if e.Source != OfficialSourceID || e.PreviewCount > 0 {
			continue
		}
		var donor *AppInfo
		if d := byExact[e.AppName]; d != nil {
			donor = d
		} else if d := byNorm[normName(e.AppName)]; d != nil &&
			strings.EqualFold(d.DisplayName, e.DisplayName) {
			donor = d
		}
		if donor == nil {
			continue
		}
		e.PreviewCount = donor.PreviewCount
		e.PreviewURLs = donor.PreviewURLs
	}
}

// mapCategory 把 FnDepot labels（"工具，网络" / 数组）映射到前端分类键
// （与 New Store mapFndepotCategory 一致，未匹配返回空）。
func mapCategory(labels string) string {
	labels = strings.ReplaceAll(strings.TrimSpace(labels), "，", ",")
	parts := strings.Split(labels, ",")
	if len(parts) == 0 {
		return ""
	}
	switch strings.TrimSpace(parts[0]) {
	case "影音娱乐", "娱乐", "影音", "音乐", "视频", "游戏地带", "游戏":
		return "media"
	case "AI赋能", "AI", "ai":
		return "ai"
	case "系统工具", "工具", "安全", "编程开发", "硬件驱动", "智能智控", "系统":
		return "system"
	case "生活服务", "教育学习", "教育":
		return "content"
	}
	// 未收录的标签（网络/下载/浏览器/实用效率…）透传原始值，
	// 由 buildCatalog 末尾的 classifyAll 统一走 标签映射→关键词 归类。
	return strings.TrimSpace(parts[0])
}

// fillInstalled 合并 daemon 安装状态（状态、web 入口、版本比较）。
func fillInstalled(ai *AppInfo, ia platform.InstalledApp) {
	ai.Installed = true
	ai.InstalledVersion = ia.Version
	ai.InstalledFPKVer = ia.Version
	if ai.LatestVersion == "" {
		ai.LatestVersion = ia.Version
	}
	ai.Status = ia.Status
	ai.StartStop = &ia.IsStartStop
	ai.Uninstallable = &ia.IsUninstall

	// Web 入口：iframe 类型多为面板网关路径（/app/xxx 或 /cgi/xxx，无端口），
	// url 类型是应用自有服务端口；也可能 iframe 带端口（kspeeder:5003 等）。
	// daemon 不下发主机（=当前访问主机），协议/端口/path 结构化透传，
	// 完整 URL 由前端 appWebUrl 按当前访问上下文重建（http/https/fn 壳）。
	switch ia.Web.Type {
	case "iframe":
		ai.WebOnWebUI = true
		ai.WebProtocol = ia.Web.Proto
		ai.WebPort = ia.Web.Port
		ai.WebPath = ia.Web.Path
	case "url":
		ai.WebOnWebUI = false
		ai.WebProtocol = ia.Web.Proto
		ai.WebPort = ia.Web.Port
		ai.WebPath = ia.Web.Path
	default:
		if ia.Web.URL != "" {
			ai.WebURL = ia.Web.URL
		}
	}
	ai.WebServiceName = ia.ServiceName

	// 源版本高于已装版本 → 可更新
	if ai.LatestVersion != "" && ia.Version != "" && ai.LatestVersion != ia.Version {
		ai.HasUpdate = compareVersions(ai.LatestVersion, ia.Version) > 0
		ai.AvailableVersion = ai.LatestVersion
	}
}

// applyIgnoredUpdates 忽略更新（0.6.181）：config.UpdateIgnored 中已装应用
// 抑制更新信号（HasUpdate=false，更新角标/计数/自动更新/摘要通知全部
// 自动排除）并置 UpdateIgnored=true（行卡/详情页「已忽略」徽章 + 「取消
// 忽略」入口）。AvailableVersion 保留——发现页「忽略更新」列表要展示
// 旧→新版本。忽略集合跨构建稳定（源自 config，非网络数据）→ 不造成
// /api/apps ETag 漂移。
func applyIgnoredUpdates(out []AppInfo, ignored []string) {
	if len(ignored) == 0 {
		return
	}
	set := make(map[string]bool, len(ignored))
	for _, n := range ignored {
		set[n] = true
	}
	for i := range out {
		a := &out[i]
		if a.Installed && set[a.AppName] {
			a.UpdateIgnored = true
			// 0.6.197：确有更新被压住（HasUpdate 刚由版本比较置真）才标
			// Pending——dock「有更新」列表含它（带已忽略标记）；已最新
			// 的忽略 app 不进列表。
			if a.HasUpdate {
				a.UpdateIgnoredPending = true
			}
			a.HasUpdate = false
		}
	}
}

// parseSizeBytes 把 "12.3" MB 字符串转成字节（解析失败返回 0）。
// 源目录的 size 字段是十进制 MB（1e6）：实测 daidai "19.31" 对应 19,306,068B
// （差 3.9KB），按 MiB 解释会偏 ~0.9MB。
func parseSizeBytes(mb string) int64 {
	mb = strings.TrimSpace(mb)
	if mb == "" {
		return 0
	}
	if v, err := strconv.ParseFloat(mb, 64); err == nil {
		return int64(v * 1000 * 1000)
	}
	return 0
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// applyOfficialUpdateForInstalled 已装官方应用的更新判定规则：
// 卡片即官方应用，更新目标只认官方目录版本，key 校正为官方 key。
// 社区源同名条目的更高版本不构成「更新」——官方应用中心不显示更新时
// Moo 也不能显示，更不能用社区包冒充官方应用的更新目标（实测
// nodejs_v22/python312 等 5 个官方应用被社区同名条目污染出假更新）。
// key 校正为官方 key 后，更新/安装/卸载走面板 cloud 通道，防止社区 key
// 把「更新」路由到社区同名包的 FPK 安装。
func applyOfficialUpdateForInstalled(e *AppInfo, oa AppInfo) {
	e.Key = oa.Key
	e.LatestVersion = oa.LatestVersion
	e.HasUpdate = e.InstalledVersion != "" &&
		compareVersions(oa.LatestVersion, e.InstalledVersion) > 0
	if e.HasUpdate {
		e.AvailableVersion = oa.LatestVersion
	} else {
		e.AvailableVersion = ""
	}
}

// applyPlatformUpgrades 用平台 daemon 的 upgradeInfo 补齐已装应用的权威
// 更新信号（与应用中心 UI「更新」角标同一数据源）。面板目录 app/list 的
// version 字段可能滞后于平台实际可升级版本（实测 trim.preview：目录
// 0.1.16 vs 升级目标 0.2.4，仅凭目录版本比较会永久漏掉更新，「有更新」
// 列表不显示而应用中心显示）。
//
// 只 OR 平台信号、不覆盖既有判定：平台未报更新时保持 0.6.59 规则
// （官方卡只认官方目录版本、社区卡跨源最高版本），避免假更新；平台报的
// 目标版本与既有 AvailableVersion 取高者。
func applyPlatformUpgrades(out []AppInfo, byName map[string]platform.InstalledApp) {
	for i := range out {
		a := &out[i]
		if !a.Installed {
			continue
		}
		ia, ok := byName[a.AppName]
		if !ok || ia.UpgradeInfo == nil || ia.UpgradeInfo.Version == "" {
			continue
		}
		uv := ia.UpgradeInfo.Version
		if a.AvailableVersion == "" || compareVersions(uv, a.AvailableVersion) > 0 {
			a.HasUpdate = true
			a.AvailableVersion = uv
		}
		if a.LatestVersion == "" || compareVersions(uv, a.LatestVersion) > 0 {
			a.LatestVersion = uv
		}
		if cl := normalizePlatformChangelog(ia.UpgradeInfo.ChangeLog); cl != "" {
			if a.Changelog == "" {
				a.Changelog = cl
			}
			if a.ReleaseNotes == "" {
				a.ReleaseNotes = cl
			}
		}
	}
}

// applyPlatformUpdateAuthority 平台（appcenter daemon）更新权威收敛
// （0.6.257，扩展 0.6.256 的 applyOfficialAuthority）。
//
// 背景：Moo 的社区源（Moo 级源管理器：shuangji66/LI1223081906/Blue-Mink 等
// FnDepot 源）与 appcenter 的源是**两套系统**。官方/平台应用（nodejs_v22、
// 1Panel、python312、git、java-21-openjdk…）即使从第三方源安装，其「有更新」
// 也应以 appcenter daemon 的权威信号（upgradeInfo，与应用中心 UI 同源）为准——
// 社区同名条目（shuangji66 nodejs_v22 22.23.2 等）不得冒充平台应用的更新目标。
// 0.6.256 只覆盖了 daemon source=official；但实测这些官方应用多从第三方源
// 安装（daemon source=thirdparty），故 0.6.257 扩展到所有 **appcenter 跟踪**
// 的应用。
//
// 判别「appcenter 跟踪」：sourceID 非空（从 appcenter 登记源安装，daemon 会
// 据它判升级）或 source=official。手动 FPK 安装（sourceID 空且 source 非
// official，如 wb2api/global-radio/trek）appcenter 不跟踪 → 保留 Moo 社区源
// 的同作者更新（真社区应用的合法更新，零回归）。
//
// 收敛规则（对 appcenter 跟踪的应用）：
//   - 官方目录可达（条目已被标 OfficialSourceID）→ applyOfficialUpdateForInstalled
//     已按官方版本收敛，本函数跳过；
//   - 断连（非官方卡）→ 更新判定完全交给 daemon 权威：有 upgradeInfo 才是真
//     平台更新（目标强制为 daemon 版本，社区版本不得顶替）；无则清除社区版本
//     驱动的假更新，bogus 高的 LatestVersion 回落已装版本。
func applyPlatformUpdateAuthority(out []AppInfo, byName map[string]platform.InstalledApp) {
	for i := range out {
		a := &out[i]
		if !a.Installed {
			continue
		}
		ia, ok := byName[a.AppName]
		if !ok {
			continue
		}
		tracked := ia.SourceID != "" || strings.EqualFold(ia.Source, "official")
		if !tracked {
			// 手动 FPK：appcenter 不跟踪，保留 Moo 社区源更新
			continue
		}
		if a.Source == OfficialSourceID {
			// 官方目录可达，applyOfficialUpdateForInstalled 已收敛
			continue
		}
		if ia.UpgradeInfo != nil && ia.UpgradeInfo.Version != "" {
			// 真平台更新：目标强制为 daemon 权威版本（社区同名更高版本顶替）
			uv := ia.UpgradeInfo.Version
			a.HasUpdate = true
			a.AvailableVersion = uv
			if a.LatestVersion == "" || compareVersions(uv, a.LatestVersion) > 0 {
				a.LatestVersion = uv
			}
		} else {
			// 无 daemon 升级目标：社区同名版本不构成更新，清除假更新
			a.HasUpdate = false
			a.AvailableVersion = ""
			if a.InstalledVersion != "" && compareVersions(a.LatestVersion, a.InstalledVersion) > 0 {
				a.LatestVersion = a.InstalledVersion
			}
		}
	}
}

// normalizePlatformChangelog 把平台 changeLog 的 <br/> 换行转成纯文本换行
// （前端 ChangelogList 纯文本渲染；应用中心 UI 的 HTML 渲染是它的逆操作）。
func normalizePlatformChangelog(s string) string {
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br>", "\n")
	return strings.TrimSpace(s)
}

// compareVersions 比较点分版本号：>0 a 更高，<0 a 更低，0 相同。
// 忽略 -rc/_ 后缀；数字段逐段比较，非数字段按字符串比较。
func compareVersions(a, b string) int {
	as, bs := splitVer(a), splitVer(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
		default:
			if av != bv {
				if av > bv {
					return 1
				}
				return -1
			}
		}
	}
	return 0
}

func splitVer(v string) []string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, sep := range []string{"-rc", "_rc", "-beta", "-alpha", "-"} {
		if i := strings.Index(v, sep); i > 0 {
			v = v[:i]
		}
	}
	return strings.Split(v, ".")
}

// markInstalledCanonical 已装规范条目（就地修改）：每个已装应用（appname）
// 只保留一个代表「实际下载的那个」的条目，其余同名条目（其他源的包并未
// 下载）降级为未装状态——否则「已安装」列表会把一个装过的应用显示成 N 张
// 同名卡片（实测 golang x9 / MySQL8 x5），且没下载的卡也顶着已装状态。
// 规范条目选择：①官方条目（官方合并恰好把一个条目标为官方源，与卡片
// 徽章一致）②与平台升级目标版本一致的条目（平台只对登记源报升级，
// 最可能的真实来源，0.6.174 新增）③源版本与已装版本一致的条目
// （最可能的包来源）④顺序首个（调用方保证传入顺序确定性，输出 ETag 稳定）。
//
// 更新判定（0.6.174 用户定稿）：已装应用的更新目标只认「同一源作者的最新
// 版本」——即规范卡自身来源的 LatestVersion + 平台 upgradeInfo。其他源
// 作者发布的同名更高版本不构成更新（此前为对齐 fndepot 采用组内最高版本
// 作更新目标，会造成「A 作者装的应用提示更新到 B 作者的版本」，已废弃）。
// 官方卡不适用本规则——只认官方目录版本（0.6.56 规则，官方合并已先行处理）。
// 前端零改动：已安装 tab 按 installed 过滤自动只剩规范条目；「全部」列表
// 同名条目照旧显示（未装形态，可点装/更新）。
func markInstalledCanonical(out []AppInfo, upgVer map[string]string) {
	type grp struct {
		name string
		idxs []int
	}
	var groups []grp
	gi := map[string]int{}
	for i := range out {
		if !out[i].Installed {
			continue
		}
		nm := out[i].AppName
		if j, ok := gi[nm]; ok {
			groups[j].idxs = append(groups[j].idxs, i)
		} else {
			gi[nm] = len(groups)
			groups = append(groups, grp{nm, []int{i}})
		}
	}
	for _, g := range groups {
		if len(g.idxs) == 1 {
			continue
		}
		pick := g.idxs[0]
		for _, j := range g.idxs {
			if out[j].Source == OfficialSourceID {
				pick = j
				break
			}
		}
		if out[pick].Source != OfficialSourceID {
			picked := false
			// ② 平台升级目标版本命中组内唯一条目 → 真实来源
			// （平台只对登记的来源源报升级；命中即跳过 ③，
			// 防止其他源恰好同版本误夺）
			if uv := upgVer[g.name]; uv != "" {
				for _, j := range g.idxs {
					if out[j].LatestVersion == uv {
						pick = j
						picked = true
						break
					}
				}
			}
			// ③ 源版本与已装版本一致 → 最可能的包来源
			if !picked {
				iv := out[pick].InstalledVersion
				for _, j := range g.idxs {
					if iv != "" && out[j].LatestVersion == iv {
						pick = j
						break
					}
				}
			}
		}
		// 0.6.174：不再采用组内最高版本作更新目标——更新只认规范卡自身
		// 来源（同一源作者）的 LatestVersion（fillInstalled 已按自身
		// 版本判定 HasUpdate），平台 upgradeInfo 在 markInstalledCanonical
		// 之后由 applyPlatformUpgrades 再 OR 进来。其他作者的同名新版
		// 留在「全部」列表里作为独立未装卡，不污染更新判定。
		for _, j := range g.idxs {
			if j == pick {
				continue
			}
			a := &out[j]
			a.Installed = false
			a.InstalledVersion = ""
			a.InstalledFPKVer = ""
			a.Status = ""
			a.StartStop = nil
			a.Uninstallable = nil
			a.HasUpdate = false
			a.AvailableVersion = ""
		}
	}
}
