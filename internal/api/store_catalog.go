package api

import (
	"context"
	"log"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"moo/internal/lang"
	"moo/internal/platform"
	"moo/internal/source"
)

// catalogRebuildWait 0.6.313 B4/F11：重建进行中时并发请求的等待上限——
// 超时也返回旧数据，绝不阻塞请求（重建本身不受影响，完成后自然换上新数据）。
const catalogRebuildWait = 2 * time.Second

// cachedCatalog 返回目录缓存（共享只读切片）。
// 0.6.313 B4/F11：锁内只判新鲜度；重建在锁外（singleflight：原子标志 +
// done channel，无第三方库）+ stale-while-revalidate：
//   - 新鲜（TTL 内，默认 60s，env MOO_CATALOG_TTL 可覆盖）→ 返回缓存；
//   - 过期且有旧数据 → **立即返回旧数据**（不阻塞），锁外触发后台重建；
//     并发调用方等 done channel（等待上限 2s，超时也返回旧数据不阻塞）；
//   - 过期且 catalogData==nil（刚失效）→ 同步重建（失效后无旧数据可给，
//     保安装/更新/档位切换反馈即时）。
//
// 重建（ListInstalled unix socket RPC + 1700+ 条目合并，实测 200-750ms）
// 在锁外跑，其他目录端点（详情/dedup/recommended）不再排队等它。
// l = 目录语言（0.6.269）：请求路径传 lang.From(r.Context())，
// 后台路径传 s.bgLang()。只读约束：调用方不得修改元素字段
// （列表瘦身等写操作先 cachedCatalogCopy）。
func (s *Server) cachedCatalog(l string) []AppInfo {
	s.catalogMu.Lock()
	ttl := s.catalogTTLValueLocked()
	if s.catalogData != nil && time.Since(s.catalogAt) < ttl {
		data := s.catalogData
		s.catalogMu.Unlock()
		return data // 新鲜：直接返回
	}
	stale := s.catalogData
	if s.catalogBuilding {
		// 重建已在进行（SWR 后台或同步路径）：有上限等待
		done := s.catalogRebuildDone
		s.catalogMu.Unlock()
		select {
		case <-done:
		case <-time.After(catalogRebuildWait): // 超时不阻塞
		}
		s.catalogMu.Lock()
		defer s.catalogMu.Unlock()
		if s.catalogData != nil {
			return s.catalogData // 重建已完成（原子交换）
		}
		if stale != nil {
			return stale // 超时/重建未完成：给旧数据
		}
		// 极端情况（重建 >2s 且刚失效无旧数据）：自己重建，保证响应不空。
		// 并发双重建在此角落可接受（buildCatalog 幂等，结果一致）。
		s.catalogMu.Unlock()
		data := s.buildCatalog(l)
		ov := computeSourceOverlap(data) // 0.6.326z：源重合率与目录同步重算（锁外）
		s.catalogMu.Lock()
		s.catalogData = data
		s.catalogAt = time.Now()
		s.catalogOverlap = ov
		// 0.6.326z5 R1：本路径必然是 nil→首次构建（上一分支已排除非 nil），
		// 属同一代（那次失效已推进过代际），故不重复推进 body 代际。
		return data
	}
	// 本调用方是唯一构建者：标记进行中，锁外重建。
	s.catalogBuilding = true
	done := make(chan struct{})
	s.catalogRebuildDone = done
	s.catalogMu.Unlock()
	if stale != nil {
		// SWR：立即返回旧数据，后台重建（并发调用方在 done 上等待）。
		go s.finishCatalogRebuild(l, done)
		return stale
	}
	// 刚失效/首次构建：同步重建（安装/更新/档位切换的即时反馈）。
	s.finishCatalogRebuild(l, done)
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	return s.catalogData
}

// finishCatalogRebuild 锁外执行重建，完成后锁内原子交换结果
// （building 标志 + done channel 统一在此收尾）。buildCatalog 自身不取
// catalogMu，锁外调用安全。
func (s *Server) finishCatalogRebuild(l string, done chan struct{}) {
	defer close(done)
	data := s.buildCatalog(l)
	ov := computeSourceOverlap(data) // 0.6.326z：源重合率与目录同生命周期（锁外算，毫秒级）
	s.catalogMu.Lock()
	// 0.6.326z5 R1：本次是否**覆盖已有目录切片**（真正意义上的「换入新
	// 一代内容」）。nil→首次构建（刚失效后的同步重建）不算新一代：那次
	// 失效本身已经清过 body 缓存并推进过代际，而「自己触发的重建」若也
	// 推进代际，会把本请求刚 marshal 出来的当前代体判成旧代 → 白丢一次
	// 缓存（0.6.313 B4 的「失效后下次请求重建 body」契约随之破坏）。
	replaced := s.catalogData != nil
	s.catalogData = data
	s.catalogAt = time.Now()
	s.catalogBuilding = false
	// 0.6.326z：与目录数据同一次原子交换，读方（listSources）只会看到同一代信号。
	if ov == nil {
		ov = map[string]SourceOverlap{}
	}
	s.catalogOverlap = ov
	s.catalogMu.Unlock()
	if replaced {
		// 0.6.326z5 R1：目录前进（换入含新卡的切片）→ body 缓存同代作废。
		// 旧实现此处不清体缓存：SWR 后台重建换入新目录后既不作废 body，也
		// 没有后续失效事件 ⇒ 基于旧代目录 marshal 的响应体长期驻留。
		s.advanceCatalogBodyGen()
	}
}

// catalogTTLValueLocked 返回生效的目录 TTL（调用方须持有 s.catalogMu）。
// 优先级：显式设置 s.catalogTTL（测试用）> env MOO_CATALOG_TTL（逃生阀：
// 设 "5s" 即回到 0.6.312 行为）> 默认 60s。env 只解析一次并记忆化；
// 非法值 log 警告并回退 60s。
func (s *Server) catalogTTLValueLocked() time.Duration {
	if s.catalogTTL > 0 {
		return s.catalogTTL
	}
	if s.catalogTTLMemo > 0 {
		return s.catalogTTLMemo
	}
	d := 60 * time.Second // 0.6.313 B4：5s → 60s（事件驱动失效，见 invalidateCatalog 纪律）
	if v := os.Getenv("MOO_CATALOG_TTL"); v != "" {
		if p, err := time.ParseDuration(v); err == nil && p > 0 {
			d = p
		} else {
			log.Printf("[catalog] MOO_CATALOG_TTL=%q 非法（应为 90s/1m/60s 等 duration），用默认 60s", v)
		}
	}
	s.catalogTTLMemo = d
	return d
}

// cachedCatalogCopy 返回可安全修改的副本（1700+ 结构体浅拷贝，约百微秒）。
func (s *Server) cachedCatalogCopy(l string) []AppInfo {
	c := s.cachedCatalog(l)
	out := make([]AppInfo, len(c))
	copy(out, c)
	return out
}

// invalidateCatalog 安装/更新/卸载/启停/源同步等改变目录内容的操作后调用。
// 纪律（0.6.313 B4）：任何改变目录内容/已装状态/官方卡的新写入口必须调
// 本函数（含官方 OAuth 授权完成/登出）——漏调 = 目录 + body 缓存最长
// TTL（默认 60s）陈旧。
func (s *Server) invalidateCatalog() {
	s.catalogMu.Lock()
	s.catalogData = nil
	s.catalogAt = time.Time{}
	s.catalogOverlap = nil // 0.6.326z：重合率与目录同生共死（重建时重算）
	// F11：若重建正进行，置回未构建态；在途的 finishCatalogRebuild 随后
	// 会写入新数据（比失效点更新，直接覆盖无害）。
	s.catalogBuilding = false
	s.catalogRebuildDone = nil
	s.catalogMu.Unlock()
	// 0.6.313 B4：body 缓存与目录缓存同一钩子一并失效。
	// 0.6.326z5 R1：同时推进代际号（在途的 marshal 落盘前会复核它）。
	s.advanceCatalogBodyGen()
}

// advanceCatalogBodyGen 目录代际前进：作废 /api/apps body 缓存并 ++ 代际号。
// 不变量（0.6.326z5 R1）：**body 缓存必须与目录同代**——任何换入新目录
// 切片的路径都必须调用本函数，否则基于旧代目录 marshal 出来的响应体会被
// 长期驻留（无后续失效事件，直到下一次 invalidateCatalog，实测可 >150s）。
// 调用方不得持 catalogBodyMu；不得持 catalogMu 后再调（本函数只取
// catalogBodyMu，锁序与 invalidateCatalog 一致：catalogMu → catalogBodyMu）。
func (s *Server) advanceCatalogBodyGen() {
	s.catalogBodyMu.Lock()
	s.catalogBodyGen++
	s.catalogBody = nil
	s.catalogBodyETag = ""
	s.catalogBodyMu.Unlock()
}

// buildCatalog 合并应用源元数据与 daemon 安装状态，生成前端 AppInfo 列表。
// 目录里存在但本机未装的 → installed=false；本机已装但不在任何源里的
// （系统自带 / 源删了）也列入，保证「已装」页完整。
// l = 目录语言（0.6.269）：官方目录条目（Panel.Apps）按此语言取名称/简介。
func (s *Server) buildCatalog(l string) []AppInfo {
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
	// Apps 返回空列表 + 错误，下方 len>0 守卫自然跳过）。
	// 0.6.336（C 方案）：官方条目**独立成卡**，不再吞并同名社区卡——社区源
	// 卡恢复正常可见与可筛（fnos-store 175 条全部可见），官方卡走面板通道、
	// 社区卡走 FPK 通道，源徽章与安装通道自洽。byName 用于给新造的官方卡
	// 复用 daemon 安装态（否则官方卡永远显示未装）。
	if s.Panel != nil {
		// 0.6.269：官方目录条目按请求/配置语言取名称与简介
		if official, _ := s.Panel.Apps(lang.WithContext(context.Background(), l)); len(official) > 0 {
			out = mergeOfficialCards(out, official, byName)
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
		// 安装包大小兜底（0.6.336）：源索引不声明 size 时（conversun 的
		// apps.json 全 175 条都没有 size 字段），用同一个 Release 资产的
		// size 补——已有 size（源声明 / 包级解析）优先，绝不被覆盖。
		for i := range out {
			ai := &out[i]
			if ai.SizeBytes > 0 || ai.ReleaseURL == "" {
				continue
			}
			if o, r, asset, ok := source.ParseGhReleaseURL(ai.ReleaseURL); ok {
				if n, ok := s.GHStats.LookupSize(o, r, asset); ok && n > 0 {
					ai.SizeBytes = n
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

	// 应用溯源（0.6.326）：派生 UpstreamSource/Repo/Version/URL（搬运上游判定）。
	// 必须在 applyDedupPolicy 之前、且必须在这里——appsResponse 列表瘦身会
	// 剥掉 ReleaseURL，之后再算就没有比较依据了。
	s.applyUpstreamLineage(out)

	// 本源仓库属性（0.6.326f）：SourceIsFork / SourceDisabled —— 与上游判定
	// 独立（ReleaseURL 为空的卡也要算），供详情页「禁用fork源」按钮。
	s.applySourceLineage(out)

	// 0.6.351（方案 A）：脚本同步（非 fork）镜像仓溯源兜底——内容指纹。
	// 必须在 applyUpstreamLineage 之后（只补尚未有上游的卡、不覆盖正确溯源）、
	// applyDedupPolicy 之前（ReleaseURL/Sha256 仍在，且列表瘦身尚未剥字段）。
	s.applyMirrorFingerprint(out)

	// 详情页信息行兜底（0.6.337）：官网 + 架构两条纯内存补齐（见 detail_fallback_337.go）。
	applyDetailFallback(out)

	// 更新目标（0.6.326x 单轴化）：update_policy 四档互斥
	// strict / origin / lineage / upstream（强制上游）。必须在
	// markInstalledCanonical 之后（规范卡/降级卡已定）、applyPlatformUpgrades
	// 之前（平台信号随后 OR）、applyPlatformUpdateAuthority 之前（平台跟踪
	// 应用由它收敛，双重保险）、applyIgnoredUpdates 之前（用户忽略更新仍能压住）。
	// upstream 档 =「严格 base + 上游纠正」——原先独立的 auto_correct 开关已并入
	// 同一轴，不会再出现「严格 + 自动纠正」这种一个开关静默旁路另一个的组合。
	// resolveUpdateTargetSelection 返回本轮真正被纠正到上游的应用（upstream
	// 档之外恒空）——仅供锁外异步通知用，判定逻辑零变化。
	corrected := resolveUpdateTargetSelection(out, s.Cfg.UpdatePolicy, s.Cfg.AutoCorrect, byName)

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

	// 去重展示层（0.6.312 B2）：在确定性排序之后（代表卡阶梯的「slice
	// 顺序」步要稳定）；只标 Hidden + 填同名组信息，不改版本/更新判定。
	applyDedupPolicy(out, s.Cfg.DedupPolicy)

	// 应用级禁用（0.6.326）：必须最末——applyDedupPolicy 会写 Hidden，
	// 在其后置位才不被覆盖。
	applyHiddenApps(out, s.Cfg.HiddenApps)

	// 溯源纠正通知（0.6.326 通知批次）：目录构建是热路径（可被并发触发、
	// 且调用方可能持锁）——通知一律锁外异步跑，不阻塞构建；事件自带去重表
	//（同一应用×目标版本只推一次），并发重复构建最多多做一次判定。
	if len(corrected) > 0 {
		go s.notifyUpstreamCorrected(corrected)
	}
	return out
}

// mergeOfficialCards 官方应用中心目录**独立成卡**（0.6.336 C 方案）。
//
// 旧行为（≤0.6.335，0.6.312 B3/F7 引入）：官方条目并入「第一个同名卡」并把
// 那张卡的 Source 改写成 fnos-official。后果——社区源那张卡被吞掉：
// fnos-store 175 条里 22 条不可见（按源徽章筛选只剩 153），全目录共 80 条
// 社区卡被吞；且「谁被吞」取决于源列表顺序（同一情形不同源结果不同）。
//
// 新契约：官方条目不再改写/吞并任何社区卡，而是自建一张 fnos-official 卡；
// 同名社区卡原样保留（源徽章、版本、安装通道各自自洽——官方卡走面板通道，
// 社区卡走 FPK 通道）。官方卡缺简介/开发者/发布者时从同名社区卡回填
// （面板 app/list 不带这些字段，详情回填是异步的，回填完成前不能空白）；
// 官方卡按 appname 复用 daemon 安装态，供 markInstalledCanonical 选为已装
// 代表卡（该函数优先官方卡），社区同名卡随即降级为未装。
//
// 性能：官方条目按 appname 建 map，O(官方 + 目录)。
func mergeOfficialCards(out []AppInfo, official []AppInfo, byName map[string]platform.InstalledApp) []AppInfo {
	offIdx := make(map[string]int, len(official))
	for i := range out {
		if out[i].Source != OfficialSourceID {
			continue
		}
		if _, ok := offIdx[out[i].AppName]; !ok {
			offIdx[out[i].AppName] = i
		}
	}
	// 无源卡索引（已装但任何源都查不到 → Source==""）：这类卡**没有社区源
	// 可保留**，官方条目仍按旧口径吸收进去（改写 Source 为官方、补元数据），
	// 否则会多出一张同名的空源卡 + 一张官方卡（实测 21 个系统应用/已装应用）。
	noSrcIdx := make(map[string]int, 8)
	for i := range out {
		if out[i].Source != "" {
			continue
		}
		if _, ok := noSrcIdx[out[i].AppName]; !ok {
			noSrcIdx[out[i].AppName] = i
		}
	}

	// 社区卡捐赠者：同名里**字段最全**的一张（只借展示字段，不借版本/包）。
	type donor struct {
		desc, maint, maintURL, dist, distURL string
	}
	donors := make(map[string]*donor, len(official))
	for i := range out {
		e := &out[i]
		if e.Source == "" || e.Source == OfficialSourceID {
			continue
		}
		d := donors[e.AppName]
		if d == nil {
			d = &donor{}
			donors[e.AppName] = d
		}
		if d.desc == "" {
			d.desc = e.Description
		}
		if d.maint == "" && e.Maintainer != "" {
			d.maint, d.maintURL = e.Maintainer, e.MaintainerURL
		}
		if d.dist == "" && e.Distributor != "" {
			d.dist, d.distURL = e.Distributor, e.DistributorURL
		}
	}
	for _, oa := range official {
		if idx, ok := offIdx[oa.AppName]; ok {
			// 官方目录重名条目（正常不该出现）：只补空字段，不再造卡
			e := &out[idx]
			if e.DisplayName == "" {
				e.DisplayName = oa.DisplayName
			}
			if e.IconURL == "" {
				e.IconURL = oa.IconURL
			}
			if e.Category == "" {
				e.Category = oa.Category
			}
			if e.AppType == "" {
				e.AppType = oa.AppType
			}
			if e.DownloadCount == nil {
				e.DownloadCount = oa.DownloadCount
			}
			if e.Installed && oa.LatestVersion != "" {
				// 已装官方应用：更新目标只认官方目录版本（社区同名条目的
				// 更高版本不得冒充官方更新，0.6.56 规则）。
				applyOfficialUpdateForInstalled(e, oa)
			} else if e.LatestVersion == "" {
				e.LatestVersion = oa.LatestVersion
			}
			continue
		}
		if idx, ok := noSrcIdx[oa.AppName]; ok {
			// 已装但无源（系统应用/源被删）：官方条目直接吸收该卡，
			// 改写 Source 为官方并补齐官方元数据（保留已装状态）。
			e := &out[idx]
			e.Source = OfficialSourceID
			e.Key = oa.Key
			e.Category = oa.Category
			e.AppType = oa.AppType
			if oa.DownloadCount != nil {
				e.DownloadCount = oa.DownloadCount
			}
			if e.Installed && oa.LatestVersion != "" {
				applyOfficialUpdateForInstalled(e, oa)
			} else if e.LatestVersion == "" {
				e.LatestVersion = oa.LatestVersion
			}
			if e.Description == "" {
				e.Description = oa.Description
			}
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
			offIdx[oa.AppName] = idx
			delete(noSrcIdx, oa.AppName)
			continue
		}
		out = append(out, oa)
		i := len(out) - 1
		offIdx[oa.AppName] = i
		if ia, ok := byName[oa.AppName]; ok {
			fillInstalled(&out[i], ia)
		}
	}
	// 社区元数据回填（append 之后，官方卡才在 out 里）
	for i := range out {
		e := &out[i]
		if e.Source != OfficialSourceID {
			continue
		}
		d := donors[e.AppName]
		if d == nil {
			continue
		}
		if e.Description == "" {
			e.Description = d.desc
		}
		if e.Maintainer == "" {
			e.Maintainer, e.MaintainerURL = d.maint, d.maintURL
		}
		if e.Distributor == "" {
			e.Distributor, e.DistributorURL = d.dist, d.distURL
		}
	}
	return out
}

var changelogVerRe = regexp.MustCompile(`^(\d+(?:\.\d+)+(?:[A-Za-z0-9.\-]*)?)\s*[:：]\s*(.+)$`)

// parseChangelogEntries 把 FnDepot 顶层 changelog 拼接串解析为版本化条目（最新在前）。
// 约定格式：「1.20.16: 说明；1.20.15: 说明；…」——版本段以「数字.数字[:：]」开头，
// 不带版本前缀的片段并入前一条（如「当前仅发布 x86 版」）。
// releases[最新版本].changelog 更干净，存在时覆盖对应条目。
// 整串都没有版本前缀时：releases 有有效文本 ⇒ 只回 releases 的版本条目
// （丢弃裸文本条目——0.6.326 B 方案，杜绝「无版本重复条目」）；releases 无
// ⇒ 退回单条（版本=最新版本）。
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
			// 0.6.326（B 方案·治本）：顶层串一个版本前缀都没有 ⇒ 它只是 releases
			// 那几段说明的「另一种写法」，releases 才是权威 → 必须**丢弃**前面由
			// 裸文本生成的 Version:"" 条目。否则该空版本条目会混进列表（UI 对
			// 无版本条目不渲染版本徽章，只留 1px 占位）→ 视觉上「挂」在别的版本
			// 条目下面（0.6.325 用户实抓：0.6.322 下面冒出 325 的日志）。
			prev := entries
			entries = entries[:0] // 丢弃裸文本条目；下面 append 复用同一底层数组
			for ver, t := range rel {
				if t = strings.TrimSpace(t); t != "" {
					entries = append(entries, ChangelogEntry{Version: ver, Text: t})
				}
			}
			if len(entries) == 0 {
				// releases 全为空文本：退回裸文本条目（不丢信息；prev 未被覆写）
				entries = prev
			} else {
				sort.SliceStable(entries, func(i, j int) bool {
					return compareVersions(entries[i].Version, entries[j].Version) > 0
				})
			}
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
		ServicePort:    parseInt(a.ServicePort),
		InstallType:    a.InstallType,
		FirstReleaseAt: a.FirstReleaseAt,
		DescHTML:       a.DescHTML,
		License:        a.License,
		MinFnos:        a.MinFnos,
		Wizard:         a.Wizard,
		Sha256:         a.Sha256,
		Arch:           a.Arch,
		Archs:          a.Archs,
		PreviewCount:   len(a.PreviewURLs),
		PreviewURLs:    a.PreviewURLs,
		HasReadme:      a.ReadmeURL != "",
		Platform:       "fnos",
	}
	if dlCount > 0 {
		ai.DownloadCount = &dlCount
	}
	// 0.6.312 B3/F7+F8：ChangelogEntries 不再每次目录重建（5s TTL 热路径）
	// 逐卡正则解析——源同步时预解析一次（detailStoreSink 写入详情磁盘层，
	// 同时把 changelog 全文/releases 明细移出常驻内存），详情对话框打开
	// 时由 appDetail 懒载回填。
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
	// 0.6.337：真实安装位置（/volN/@appcenter/<app> 或 /var/apps/<app>）。
	// 目录扫描带 1 分钟缓存，仅对已装应用查（≤ 几十个），成本可忽略。
	if loc := installLocCache.lookup(ia.AppName); loc.Path != "" {
		ai.InstallVolume = loc.Volume
		ai.InstallPath = loc.Path
	}

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

// compareVersions 比较版本号：>0 a 更高，<0 a 更低，0 相等或不可比。
// 0.6.330：唯一实现 = internal/source.CompareVersion（见 version.go 的解析规则：
// 数字修订号 `-N`、预发布 rc/beta 递增与「预发布 < 正式版」、段数零填充、
// `+build` 忽略、`latest-…` 这类标签串视为不可比）。这里只做薄封装，
// 目录判定与安装守卫从此共用同一口径，不再出现「徽章与守卫打架」。
func compareVersions(a, b string) int { return source.CompareVersion(a, b) }

// splitVer 把版本串投影成**数字段**（供 comparableVersion 判断「是否可解析」）。
// 注意：0.6.330 起它不再参与大小比较——排序/更新判定统一走
// internal/source.CompareVersion；此处保留仅为兼容既有可解析性契约。
func splitVer(v string) []string {
	v = strings.ToLower(strings.TrimSpace(v))
	// 0.6.326k（F7=K3）：剥前导 `v`。否则 compareVersions("2.5.0","v1.9.0") 的第 0
	// 段是 "2" vs "v1"（一个数字一个不是）→ 落入字符串分支 → '2'(0x32) < 'v'(0x76)
	// =「2.5.0 更低」，v 前缀版本被系统性低估（更新判定、自动纠正、去重排序全会
	// 受影响）。FPK manifest / 索引里的版本常带 v 前缀，必须两口径同值。
	v = strings.TrimPrefix(v, "v")
	// 0.6.330：fnOS/Debian 式**数字修订号** `-N`（1.13.0-1、3.12.4-11）必须参与比较，
	// 转成第 4 段。此前被下面的 `-` 截断规则整段丢弃 → `1.13.0-1` 剥成 `1.13.0`
	// 与已装判等（实测 wb2api：源 1.13.0-1、已装 1.13.0，has_update=false，徽章不亮），
	// 而安装管道用的是 internal/source 的 versionLess（把 `-1` 当修订后缀）→
	// IsNewer("1.13.0-1","1.13.0")=true —— 两套比较器结论相反：点了能装，但不给点。
	// 非数字后缀（`-x86`、`-panel` 等）维持原有「整段忽略」行为，不制造假更新。
	v = expandNumericRevision(v)
	for _, sep := range []string{"-rc", "_rc", "-beta", "-alpha", "-"} {
		if i := strings.Index(v, sep); i > 0 {
			v = v[:i]
		}
	}
	// 0.6.326y（G4）：剥完预发布标记之后，把 `_` 也当分段符。Java 系版本
	//（1.8.0_492）此前整串判「不可解析」（comparableVersion=false）→ 上游版本
	// 不显示 + 自动纠正静默跳过；同处比较器还会把 1.8.0_99 vs 1.8.0_100 退化成
	// 字符串比较（"0_99" > "0_100"）而判反。`_rc` 已在上面按预发布剥掉，行为不变。
	return strings.Split(strings.ReplaceAll(v, "_", "."), ".")
}

// expandNumericRevision 把「尾部纯数字修订后缀」`-N` 变成第 4 段（1.13.0-1 → 1.13.0.1）。
// 只认最后一段且后缀全为数字的情形：`-rc1`/`-beta`/`-x86` 等一律原样返回，
// 交给 splitVer 既有的预发布/忽略规则处理，避免把分支名/架构名当版本号。
func expandNumericRevision(v string) string {
	i := strings.LastIndex(v, "-")
	if i <= 0 || i == len(v)-1 {
		return v
	}
	for _, c := range v[i+1:] {
		if c < '0' || c > '9' {
			return v
		}
	}
	return v[:i] + "." + v[i+1:]
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
			// 0.6.337：降级卡不再代表本机安装态，安装位置也必须清掉
			// （否则未装卡会显示「存储空间 1 · /vol1」）。
			a.InstallVolume = 0
			a.InstallPath = ""
		}
	}
}

// releaseOrigin 把 download_url 归一成发布来源仓库「owner/repo」（0.6.272）。
// 覆盖 GitHub 直链 / raw / jsDelivr gh 路径，并容忍 gh-proxy 类前缀
// （https://proxy/https://raw.githubusercontent.com/… —— 正则在完整 URL
// 里找首个匹配主机段即可）；大小写归一（同一仓库在 URL 里常出现
// FnDepot / Fndepot 两种写法）。非 GitHub 系返回 ""（无证据，保守）。
var releaseOriginPatterns = []*regexp.Regexp{
	regexp.MustCompile(`github\.com/([^/]+)/([^/#?]+)`),
	regexp.MustCompile(`raw\.githubusercontent\.com/([^/]+)/([^/#?]+)`),
	regexp.MustCompile(`cdn\.jsdelivr\.net/gh/([^/]+)/@?([^/#?]+)`),
}

func releaseOrigin(url string) string {
	for _, re := range releaseOriginPatterns {
		if m := re.FindStringSubmatch(url); m != nil {
			owner, repo := m[1], m[2]
			// 0.6.326k（F1=B1）：jsDelivr `gh/owner/repo@ref/path` 形态的 `@ref`
			// 不属于仓库名——不剥掉会得到 "owner/repo@main"：详情页链到 404、
			// 永远匹配不上源列表（upstream_source 恒空、自动纠正失效），且同一
			// 仓库的 GitHub 形态卡与 jsDelivr 形态卡被当成不同宗（跨源更新与
			// 去重同时失效）。GitHub 直链形态不含 `@`，无副作用。
			repo, _, _ = strings.Cut(repo, "@")
			if repo == "" {
				continue
			}
			return strings.ToLower(owner) + "/" + strings.ToLower(repo)
		}
	}
	return ""
}

// resolveUpdateTargetSelection 已装应用更新目标的单一裁决入口（0.6.326x 单轴化）。
//
// update_policy 四档（互斥，取代原「三档策略 + 独立 auto_correct 开关」）：
//   - ""/strict = 只认安装源自身最新（0.6.174 默认，零变化）；
//   - origin   = 同一发布仓库（download_url 归一 owner/repo）的新版也算；
//   - lineage  = origin 之外 maintainer / distributor 相等也算同宗；
//   - upstream = 强制上游：已装的搬运上游应用改用上游原作者版本，其余按 strict。
//
// 调用顺序契约（buildCatalog）：本函数产出更新目标后，applyPlatformUpgrades
// （平台信号 OR）→ applyPlatformUpdateAuthority（平台跟踪应用收敛）→
// applyIgnoredUpdates（用户忽略更新压住）。
//
// 兼容：0.6.326 的独立开关 auto_correct 若为 true（旧配置文件 / 旧客户端），
// 等价于 upstream 档（策略值本身优先，仅当不是 upstream 时提升）。
// 返回本轮**真正被纠正到上游**的应用明细（upstream 档才可能非空；其它档返回
// nil）——调用方（buildCatalog）锁外异步推 upstream_corrected 通知用，不参与
// 任何判定。
func resolveUpdateTargetSelection(out []AppInfo, policy string, autoCorrect bool, byName map[string]platform.InstalledApp) []upstreamCorrection {
	pol := orStrict(policy)
	if autoCorrect && pol != "upstream" {
		pol = "upstream"
	}
	if pol == "upstream" {
		// 严格 base：applyLineageUpdates 对 strict 是空操作，故不调用；
		// 只跑上游纠正（仅对「有 upstream 血缘」的搬运应用生效）。
		return applyUpstreamCorrection(out, byName, true)
	}
	applyLineageUpdates(out, pol, byName)
	return nil
}

// applyLineageUpdates 跨源更新策略（0.6.272，设置页「严格/同源/同宗」）：
// update_policy=origin/lineage 时，已装应用同名组里的「同宗」卡片可以
// 提供更新目标；strict / "" = 空操作（保持 0.6.174 行为）。
//
// 否决（命中则该卡不参与，防跨作者/跨应用误更新）：
//   - 显示名不同 → 同名不同应用（如「三体甜甜圈」vs「赛博甜甜圈」）；
//   - 双方 maintainer 均非空且明显不同、distributor 也不相等 → 不同作者。
//
// 同宗证据（命中任一即视为同一发布谱系）：
//   - origin 策略：releaseOrigin(download_url) 相同（同一发布仓库）；
//   - lineage 策略：origin 相同，或 maintainer 相等，或 distributor 相等。
//
// 平台跟踪应用（sourceID 非空 / source=official）整体跳过：更新判定归
// daemon 权威（0.6.257，applyPlatformUpdateAuthority 在下游收敛）。
//
// 更新目标 = 规范卡自身来源最新版（fillInstalled 已判，可能为空）与同宗卡
// 中高于已装版本的最高者；目标来自跨源卡时 ReleaseURL/说明等跟随该卡
// （否则「更新」会下载规范卡自己的旧包），并记 UpdateFromSource 供
// UI/通知展示「来自 XX 源」。按 slice 顺序遍历、同版本先者胜 →
// 输出确定性，ETag 稳定。
func applyLineageUpdates(out []AppInfo, policy string, byName map[string]platform.InstalledApp) {
	if policy != "origin" && policy != "lineage" {
		return
	}
	type grp struct {
		canon    int
		siblings []int
	}
	var groups []grp
	gi := map[string]int{}
	for i := range out {
		j, ok := gi[out[i].AppName]
		if !ok {
			j = len(groups)
			gi[out[i].AppName] = j
			groups = append(groups, grp{canon: -1})
		}
		if out[i].Installed {
			groups[j].canon = i // 至多一个（markInstalledCanonical 已保证）
		} else {
			groups[j].siblings = append(groups[j].siblings, i)
		}
	}
	for _, g := range groups {
		if g.canon < 0 || len(g.siblings) == 0 {
			continue
		}
		name := out[g.canon].AppName
		if ia, ok := byName[name]; ok && (ia.SourceID != "" || ia.Source == "official") {
			continue // 平台权威（0.6.257），不收社区更新
		}
		c := &out[g.canon]
		if c.InstalledVersion == "" {
			continue
		}
		bestVer := c.AvailableVersion // 基线 = 规范卡自身来源的更新目标（可能为空）
		bestIdx := -1
		for _, j := range g.siblings {
			s := &out[j]
			if s.Source == OfficialSourceID || s.LatestVersion == "" {
				continue
			}
			// 否决 1：显示名不同 → 不同应用
			if c.DisplayName != "" && s.DisplayName != "" &&
				!strings.EqualFold(strings.TrimSpace(c.DisplayName), strings.TrimSpace(s.DisplayName)) {
				continue
			}
			// 否决 2：maintainer 明显不同且 distributor 不救
			if c.Maintainer != "" && s.Maintainer != "" &&
				!strings.EqualFold(c.Maintainer, s.Maintainer) &&
				!(c.Distributor != "" && s.Distributor != "" && strings.EqualFold(c.Distributor, s.Distributor)) {
				continue
			}
			// 同宗证据
			ok := false
			if policy == "lineage" {
				ok = c.Maintainer != "" && s.Maintainer != "" &&
					strings.EqualFold(c.Maintainer, s.Maintainer)
				if !ok {
					ok = c.Distributor != "" && s.Distributor != "" &&
						strings.EqualFold(c.Distributor, s.Distributor)
				}
			}
			if !ok {
				o1 := releaseOrigin(c.ReleaseURL)
				ok = o1 != "" && o1 == releaseOrigin(s.ReleaseURL)
			}
			if !ok {
				continue
			}
			if compareVersions(s.LatestVersion, c.InstalledVersion) <= 0 {
				continue // 不比已装新
			}
			if bestVer == "" || compareVersions(s.LatestVersion, bestVer) > 0 {
				bestVer = s.LatestVersion
				bestIdx = j
			}
		}
		if bestIdx < 0 {
			continue // 无跨源同宗新版（含 strict 语义不变的情形）
		}
		s := &out[bestIdx]
		c.HasUpdate = true
		c.AvailableVersion = bestVer
		c.UpdateFromSource = s.Source
		// 下载目标跟随同宗卡（版本/包/校验和/说明），防下错旧包
		c.ReleaseURL = s.ReleaseURL
		c.ReleaseNotes = s.ReleaseNotes
		c.SizeBytes = s.SizeBytes
		c.Sha256 = s.Sha256
		c.UpdatedAt = s.UpdatedAt
		c.DownloadCount = s.DownloadCount
	}
}

// ────────────────────────────────────────────────────────────────────────
// 去重展示层（0.6.312 B2，首页胶囊；0.6.313 档位文案：默认 / 标准 / 去重，
// 值 all/merge/one 不变——「标准」=同仓同构建组留一张代表，「去重」=每应用一张卡）
//
// 硬约束：
//   - 只并展示身份，不并版本/更新判定：Hidden 卡的 LatestVersion/HasUpdate
//     仍在数据层照常计算，更新角标/忽略更新/平台权威全不受影响；
//   - 按精确 appname 分组（不做大小写/分隔符归一——那是两个 FPK）；
//   - 已装规范卡（markInstalledCanonical 的 Installed 卡）永不隐藏；
//   - 代表卡承载 SameName/SameNameCount（候选表数据源）；按 key 的详情
//     不受 Hidden 影响（显式引用逃生门）。
//
// merge 档：组内「同宗」（仓库/作者，同 0.6.272 lineage 语义+否决规则）
// 或「同版本+同 sha」的卡并簇，每簇留一张代表。
// one 档：每个 appname 留一张代表。
//
// 代表卡阶梯（两档通用）：① 已装规范卡 → ② 官方目录卡 → ③ 本机可装卡
// （archs 含当前架构/all；未提供架构信息不否决）→ ④ 最高版本 → ⑤ 下载量
// → ⑥ slice 顺序（= 源顺序，确定性，ETag 稳定）。
func applyDedupPolicy(out []AppInfo, policy string) (mergedVisible, oneVisible int) {
	groupIdx := map[string][]int{}
	var groupNames []string
	for i := range out {
		n := out[i].AppName
		if n == "" {
			continue
		}
		if _, ok := groupIdx[n]; !ok {
			groupNames = append(groupNames, n)
		}
		groupIdx[n] = append(groupIdx[n], i)
	}
	arch := localArch()

	// 并查集（组内小，局部实现；按卡下标分配，跨组安全）
	parent := make([]int, len(out))
	find := func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}

	for _, n := range groupNames {
		idxs := groupIdx[n]
		if len(idxs) == 1 {
			oneVisible++
			mergedVisible++
			continue
		}
		oneVisible++

		// ── 阶梯选代表 ──
		better := func(cur, cand *AppInfo) bool { // cand 是否优于 cur
			cn, dn := dedupRepClass(cur, arch), dedupRepClass(cand, arch)
			if cn != dn {
				return dn < cn
			}
			cv, dv := cur.LatestVersion, cand.LatestVersion
			if cv != dv {
				if cv == "" {
					return true
				}
				if dv == "" {
					return false
				}
				return compareVersions(dv, cv) > 0
			}
			cc, dc := 0, 0
			if cur.DownloadCount != nil {
				cc = *cur.DownloadCount
			}
			if cand.DownloadCount != nil {
				dc = *cand.DownloadCount
			}
			return dc > cc
		}
		oneRep := idxs[0]
		for _, j := range idxs[1:] {
			if better(&out[oneRep], &out[j]) {
				oneRep = j
			}
		}

		// ── merge 簇（并查集：同宗 或 同版本同 sha）──
		for _, j := range idxs {
			parent[j] = j
		}
		union := func(a, b int) {
			ra, rb := find(a), find(b)
			if ra != rb {
				parent[ra] = rb
			}
		}
		for x := 0; x < len(idxs); x++ {
			for y := x + 1; y < len(idxs); y++ {
				a, b := idxs[x], idxs[y]
				sameBuild := out[a].LatestVersion != "" &&
					out[a].LatestVersion == out[b].LatestVersion &&
					out[a].Sha256 != "" && out[a].Sha256 == out[b].Sha256
				if sameBuild || dedupSameLineage(&out[a], &out[b]) {
					union(a, b)
				}
			}
		}
		clusters := map[int]int{} // find(root) → 簇内代表卡
		var clusterRoots []int
		for _, j := range idxs {
			r := find(j)
			if cur, ok := clusters[r]; ok {
				if better(&out[cur], &out[j]) {
					clusters[r] = j
				}
			} else {
				clusters[r] = j
				clusterRoots = append(clusterRoots, r)
			}
		}
		mergedVisible += len(clusterRoots)

		// ── 隐藏判定（当前策略）──
		repSet := map[int]bool{}
		if policy == "one" {
			repSet[oneRep] = true
		} else if policy == "merge" {
			for _, j := range clusters {
				repSet[j] = true
			}
		}
		// policy = all/""：repSet 空 = 不隐藏（oneRep 仍作候选表宿主）

		// ── 同名组信息（候选表 + 角标）：宿主 = 当前策略的代表卡；
		// all 档下 = 阶梯代表 ──
		entries := make([]SameNameEntry, 0, len(idxs))
		for _, j := range idxs {
			a := &out[j]
			entries = append(entries, SameNameEntry{
				Key:         a.Key,
				Source:      a.Source,
				DisplayName: a.DisplayName,
				Version:     a.LatestVersion,
				Arch:        a.Arch,
				Installed:   a.Installed,
				IsRep:       repSet[j] || (policy != "one" && policy != "merge" && j == oneRep),
				Origin:      releaseOrigin(a.ReleaseURL),
				Sha256:      a.Sha256,
			})
		}
		host := oneRep
		if policy == "merge" {
			// merge 档每簇一个宿主（组内可能多张代表卡都需候选表）
			for _, j := range clusters {
				out[j].SameNameCount = len(idxs)
				out[j].SameName = entries
			}
		} else {
			if !repSet[host] {
				host = oneRep
			}
			out[host].SameNameCount = len(idxs)
			out[host].SameName = entries
		}
		for _, j := range idxs {
			if len(repSet) > 0 && !repSet[j] {
				out[j].Hidden = true
			}
		}
	}
	return mergedVisible, oneVisible
}

// dedupRepClass 代表卡阶梯等级（小者胜）：0 已装规范 / 1 官方目录 /
// 2 本机可装 / 3 本机不可装（archs 非空且不含当前架构/all）。
func dedupRepClass(a *AppInfo, arch string) int {
	if a.Installed {
		return 0
	}
	if a.Source == OfficialSourceID {
		return 1
	}
	if len(a.Archs) == 0 {
		return 2
	}
	for _, x := range a.Archs {
		if x == arch || x == "all" {
			return 2
		}
	}
	return 3
}

// dedupSameLineage 同宗判定（与 0.6.272 lineage 语义+否决规则一致）：
// 显示名不同（同名不同应用）、双方 maintainer 均非空且不同且 distributor
// 也不相等 → 否决；否则 maintainer/distributor 相等或发布仓库相同 → 同宗。
func dedupSameLineage(a, b *AppInfo) bool {
	if a.DisplayName != "" && b.DisplayName != "" &&
		!strings.EqualFold(strings.TrimSpace(a.DisplayName), strings.TrimSpace(b.DisplayName)) {
		return false
	}
	mt := a.Maintainer != "" && b.Maintainer != "" &&
		strings.EqualFold(a.Maintainer, b.Maintainer)
	di := a.Distributor != "" && b.Distributor != "" &&
		strings.EqualFold(a.Distributor, b.Distributor)
	if !mt && !di &&
		a.Maintainer != "" && b.Maintainer != "" &&
		!strings.EqualFold(a.Maintainer, b.Maintainer) {
		return false // maintainer 明显不同且 distributor 不救
	}
	if mt || di {
		return true
	}
	o1 := releaseOrigin(a.ReleaseURL)
	return o1 != "" && o1 == releaseOrigin(b.ReleaseURL)
}

// localArch 本机架构（fnpack 约定 x86/arm，同 source.currentArch 语义）。
func localArch() string {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return "arm"
	}
	return "x86"
}
