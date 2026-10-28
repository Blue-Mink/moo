package api

// lineage.go —— 应用溯源（0.6.326 批次）。
//
// 目标：把「某个源搬运了上游原作者的应用」这件事在目录里显式表达出来，
// 让用户看得见（详情页「溯源」行）、可禁用（列表消失）、并可选自动纠正
//（已装应用改跟上游原作者的版本）。
//
// 判定口径 = 内容血缘，不用 GitHub fork API：
//   应用包地址（download_url）的发布仓库 owner 与本源仓库 owner 不同
//   且 origin 非空 ⇒ 该包来自「上游来源」。
//   必须按 owner 比：yuexps/FnDepot 里的应用包指向 yuexps/其他仓，
//   同 owner = 作者自建仓（只是分仓发布），**不算**搬运。
//
// 铁律：本文件只做展示层派生字段 + 一个独立开关（auto_correct），
// 不引入新依赖、不改 UpdatePolicy 既有语义、不动版本号。

import (
	"strings"

	"moo/internal/platform"
	"moo/internal/source"
)

// ownerOf 取「owner」段（owner/repo → owner）。
//
// 兼容带主机前缀的目录 key（github.com/owner/repo → owner）：
// source.SourceURLKey 保留主机前缀、releaseOrigin 不保留，两者要能用
// 同一口径比较 owner，故这里容忍首段为主机形态（含 "."）时跳过。
func ownerOf(repo string) string {
	parts := strings.Split(strings.Trim(strings.TrimSpace(repo), "/"), "/")
	if len(parts) >= 2 && strings.Contains(parts[0], ".") {
		parts = parts[1:]
	}
	if len(parts) < 2 || parts[0] == "" {
		return ""
	}
	return strings.ToLower(parts[0])
}

// trimHost 去掉归一 key 的主机前缀（github.com/alice/x → alice/x）；
// 无主机形态原样返回。releaseOrigin 产出不带主机，而 SourceURLKey 带，
// 两者对照前必须同口径。
func trimHost(k string) string {
	if i := strings.Index(k, "/"); i > 0 && strings.Contains(k[:i], ".") {
		return k[i+1:]
	}
	return k
}

// isGitHubRepoKey 判断归一仓库 key 是不是 GitHub 系（github.com/owner/repo）。
//
// 0.6.326k（F2=B2）：fork 关系查询器（GHForks）只能装 GitHub 仓库的数据。
// 非 GitHub 主机（自建 Gitea / 内网源）的 key 经 trimHost 后就是「裸 owner/repo」，
// 送进 Lookup 会与 GitHub 上同名仓库串味（错的「上游」链接 + 错的「禁用fork源」
// 按钮）。故凡本源仓库 key 不是 github.com/ 前缀，一律跳过 fork 判定：
// 不查缓存、不置位、不入队。
//
// 注意必须在 **trimHost 之前**判：trimHost 之后两类 key 都长成 owner/repo，
// 无从区分。
func isGitHubRepoKey(k string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(k)), "github.com/")
}

// sameRepo 两个裸 owner/repo 是否同仓（忽略大小写与首尾空白）。
// 0.6.326k（F3=B3）自环守卫用：fork parent 指向本仓时不得把自己写成上游。
func sameRepo(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// validOwnerRepo 判断裸 key 是不是「像仓库」的 owner/repo（0.6.353 / F3）。
//
// 背景：source.SourceURLKey 对非 GitHub 主机只做「剥主机」的粗归一，源地址
// 是索引文件时（本地 http 源的 …/moo/mate.json）会产出 moo/mate.json 这种
// 伪 owner/repo，再拼上 https://github.com/ 就是一个必然 404 的假地址。
// 这里做**形态**校验：恰好两段、owner 非空、repo 不是 .json/.yaml/.yml/.txt
// 这类索引文件名（GitHub 上真把仓库叫 mate.json 的情形可忽略不计）。
// 只用于「拼 GitHub 链接之前」的放行判断，不影响任何既有 key 的归一。
func validOwnerRepo(k string) bool {
	parts := strings.Split(strings.Trim(strings.TrimSpace(k), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	low := strings.ToLower(parts[1])
	for _, ext := range []string{".json", ".yaml", ".yml", ".txt", ".toml"} {
		if strings.HasSuffix(low, ext) {
			return false
		}
	}
	return true
}

// normDisplayName 显示名归一（0.6.326y / G3a）：小写 + 只保留 [a-z0-9] 与 CJK
// （U+4E00–U+9FFF）。用于「同名不同应用」兼容性判定的宽松口径（见
// displayNameCompatible）。标点/空白/全角符号一律丢弃。
func normDisplayName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
			b.WriteRune(r)
		}
	}
	return b.String()
}

// displayNameCompatible 显示名兼容判定。
//
// 0.6.326k（F4=B4）：与 store_catalog.go applyLineageUpdates 的「显示名否决」
// 同口径——任一侧为空视为无可比（放过），两侧都非空且不相等即「同名不同应用」。
//
// 0.6.326y（G3a）：由「严格相等」放宽为「归一化相等 **或** 词前缀包含」。
// 实测 21 张卡（`Hermes`↔「Hermes Studio」、`Teamspeaker`↔「Teamspeaker3」）
// 因上游改名被误否决 → 有上游源却拿不到 upstream_version，「强制上游」对它们
// 永久失效。新口径（归一化后比较）：
//   - 任一侧归一化为空 → 放过；
//   - 相等 → 放过；
//   - 短者长度 ≥3 且是长者的前缀 → 放过（同一应用改名/加后缀）；
//   - 否则否决（保留 `Fluxor`↔「三体甜甜圈」型硬否决）。
//
// 注意：本函数已放宽，与 store_catalog.go applyLineageUpdates 的**严格相等**
// 口径暂不一致（策略层维持严格，下一批统一）。
func displayNameCompatible(a, b string) bool {
	na, nb := normDisplayName(a), normDisplayName(b)
	if na == "" || nb == "" {
		return true
	}
	if na == nb {
		return true
	}
	short, long := na, nb
	if len([]rune(short)) > len([]rune(long)) {
		short, long = long, short
	}
	// 短者长度按**字符**（rune）计：≥3 且是长者的前缀才放过。按字节会
	// 让单个 CJK 字（3 字节）误满足「≥3」，把「云」⊂「云盘」这类放过。
	if len([]rune(short)) >= 3 && strings.HasPrefix(long, short) {
		return true
	}
	return false
}

// comparableVersion 版本号是否可解析（0.6.326k / F5=K1）：splitVer 之后每段都是
// 非空纯数字。不可解析（""、"abc"、"1.2.x"）按「版本未知」处理——既不点亮更新
// 角标，也不当更新目标（旧实现按字符串比较，"abc" > "1" 会点亮假更新）。
func comparableVersion(v string) bool {
	if strings.TrimSpace(v) == "" {
		return false
	}
	parts := splitVer(v)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// isPrereleaseVersion 版本号是否预发布（0.6.326k / F6=K2）：含 -rc/_rc/-beta/
// -alpha/-dev/-pre 等标记（大小写不敏感）即算。命中则自动纠正一律跳过——上游
// 只发了 rc/beta 时不能推给用户。
func isPrereleaseVersion(v string) bool {
	s := strings.ToLower(strings.TrimSpace(v))
	for _, m := range []string{
		"-rc", "_rc", "-beta", "_beta", "-alpha", "_alpha", "-dev", "_dev", "-pre", "_pre",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// sourceRepoByKey 本源仓库归一 key（github.com/owner/repo，全小写）→ 源名。
// 数据源 = 已配置源列表（含禁用源：禁用源里的包仍在目录里，溯源照样成立）。
// 同一仓库多个源名时先到先得（源顺序稳定 → 输出确定，ETag 不漂移）。
func (s *Server) sourceRepoByKey() map[string]string {
	m := map[string]string{}
	if s == nil || s.Src == nil {
		return m
	}
	for _, st := range s.Src.Sources() {
		k := source.SourceURLKey(st.URL)
		if k == "" {
			continue
		}
		if _, ok := m[k]; !ok {
			m[k] = st.Name
		}
	}
	return m
}

// originSourceOf 取卡片的「本源来源」源名。
//
// 0.6.326z3（用户规则「飞牛官方源不参与溯源」）：官方目录是权威来源，官方
// 收录的应用（Source == fnos-official）不显示「溯源/搬运」标记。
// 故 source 为官方时直接返回空，applyUpstreamLineage 据此整卡跳过。
// 0.6.336（C 方案）起官方卡与同名的社区卡各自独立成卡：社区卡按自己的
// Source 正常溯源，官方卡保持不溯源——不再需要 0.6.326y 的「原来源」中转。
func originSourceOf(a *AppInfo) string {
	if a == nil {
		return ""
	}
	if a.Source == OfficialSourceID {
		return ""
	}
	return a.Source
}

// upstreamCard 上游源里的一张同名卡（版本 + 显示名）。
// 显示名是 0.6.326k（F4）新增的校验维度：同名不同应用不能互相当上游。
type upstreamCard struct {
	version     string
	displayName string
}

// upstreamVersionOf 取上游源里同名卡的版本（按源缓存，避免逐卡全源扫描）。
//
// 0.6.326k：①（F4=B4）同名 + **显示名兼容**才算同一应用（与 applyLineageUpdates
// 的显示名否决同口径）——同名不同应用一律返回 ""，不得当上游版本；
// ②（F5=K1）版本不可解析（comparableVersion=false）视为「版本未知」返回 ""。
func upstreamVersionOf(cache map[string]map[string]upstreamCard, s *Server, srcName, appName, displayName string) string {
	m, ok := cache[srcName]
	if !ok {
		m = map[string]upstreamCard{}
		for _, a := range s.Src.Apps(srcName, "") {
			if a == nil {
				continue
			}
			// 0.6.326y（G3b）：appname 大小写不敏感（下表与查表同口径）。搬运源
			// 与上游源可能对同一应用写不同大小写（BitComet vs Bitcomet）——旧实现
			// 精确匹配 → 同一批卡里只有大小写不同的这几张拿不到 upstream_version，
			// 「强制上游」对它们同时失效（纠正层按 AppName 找 sibling 也找不到）。
			k := strings.ToLower(a.Name)
			if _, dup := m[k]; !dup {
				// 同名多卡先到先得（确定性）
				m[k] = upstreamCard{version: a.Version, displayName: a.DisplayName}
			}
		}
		cache[srcName] = m
	}
	uc, ok := m[strings.ToLower(appName)]
	if !ok {
		return ""
	}
	if !displayNameCompatible(displayName, uc.displayName) {
		return ""
	}
	if !comparableVersion(uc.version) {
		return ""
	}
	return uc.version
}

// applyUpstreamLineage 给每张卡派生溯源字段（UpstreamRepo / UpstreamSource /
// UpstreamVersion / UpstreamURL）。
//
// 调用位置：buildCatalog 内、markInstalledCanonical 之后、applyDedupPolicy
// 之前——appsResponse 会把 ReleaseURL 剥离（列表瘦身），必须在此之前算完。
//
// 不判上游的情形：本源仓库未知（源已删/非 GitHub 主机）、包里没有可识别的
// GitHub 发布仓库（releaseOrigin="")且 fork-parent 交叉印证不成立（0.6.326i/P3，
// 见 applyForkParentFallback）、owner 相同且本源仓库不是 fork（作者自建仓）、
// owner 相同且本源是 fork 但打包者就是本源 owner（0.6.326c 整仓 fork 判定，
// 见 republishedByOther）。
//
// 0.6.326i（P2）补充：上游仓是「上游作者的打包仓」（如 shuangji66/komga）时，
// 只要 owner 的索引仓已在源列表，就把 UpstreamSource 一并设上 —— 与
// applyUpstreamAddHint 同一口径，避免同一批卡「上游」取值摇摆。
func (s *Server) applyUpstreamLineage(out []AppInfo) {
	if s == nil || s.Src == nil {
		return
	}
	repoToSource := s.sourceRepoByKey()
	if len(repoToSource) == 0 {
		return
	}
	srcByRepo := map[string]string{} // 源名 → 归一仓库 key（反向表）
	bareToSource := make(map[string]string, len(repoToSource))
	for k, n := range repoToSource {
		if _, ok := srcByRepo[n]; !ok {
			srcByRepo[n] = k
		}
		// 溯源判定按 owner（ownerOf 容忍主机前缀），但「该 origin 有没有
		// 对应源」按裸 owner/repo 精确查——源 key 带主机前缀，故另建索引。
		bareToSource[trimHost(k)] = n
	}
	verCache := map[string]map[string]upstreamCard{}
	for i := range out {
		a := &out[i]
		// 按**本源来源**反查本源仓库（见 originSourceOf）：官方卡不溯源，
		// 其余卡按自身 Source 反查（0.6.336 起官方卡与社区卡各自独立）。
		srcName := originSourceOf(a)
		if srcName == "" {
			continue
		}
		srcRepo := srcByRepo[srcName]
		if srcRepo == "" {
			continue // 本源仓库未知（源已删/非 GitHub），无比较基准
		}
		// 0.6.326 溯源批次（作者声明上游 / maintainer_url）：
		// maintainer_url 指向一个**已收录**的打包者索引仓、且其 owner ≠ 本源
		// owner 时，作者声明优先于 fork 转口——覆盖「mah1618 非 fork 搬运 RROrg、
		// 其 fork ganx2024 被 fork 溯源停在 mah1618 转口」这类链断场景。
		// 只认「已收录」仓库：shuangji66 家族 maintainer_url 指向开源项目
		//（AdguardTeam/AdGuardHome、aria2/aria2…），未收录 → 不在此处理，交给
		// 下方 fork/releaseOrigin 路径维持正确的「打包者溯源」。同 owner 声明
		//（作者自建仓）也放过。同名卡版本读原始源数据（upstreamVersionOf 用
		// s.Src.Apps，天然不受 mergeOfficialCards 改写 source 影响）；源内无同名卡
		// 时只填 repo/url/source、不填 version（诚实：上游已下架该应用）。
		if a.MaintainerURL != "" {
			if ar := releaseOrigin(a.MaintainerURL); ar != "" {
				if name, ok := bareToSource[ar]; ok && ownerOf(ar) != ownerOf(srcRepo) {
					a.UpstreamRepo = ar
					a.UpstreamURL = "https://github.com/" + ar
					a.UpstreamSource = name
					a.UpstreamVersion = upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName)
					continue
				}
			}
		}
		origin := ""
		if a.ReleaseURL != "" {
			origin = releaseOrigin(a.ReleaseURL)
		}
		if origin == "" {
			// 0.6.326i（P3）：包地址拿不到（索引里 download_url 为空 / 非 GitHub
			// 主机）→ 不再直接放弃，改走「本源仓库是 fork 搬运源」的兜底推断，
			// 见 applyForkParentFallback（带交叉印证硬前提）。
			s.applyForkParentFallback(a, srcRepo, bareToSource, verCache)
			continue
		}
		if ownerOf(origin) == ownerOf(srcRepo) {
			// 0.6.326c「整仓 fork 式搬运」：包就放在本源仓库里（owner 相同），
			// 但本源仓库本身可能是别人搬运仓的 fork——此时卡片里的
			// distributor/maintainer 仍写着原打包者（≠ 本源 owner），据此改判为
			// 搬运，上游 = fork parent。作者自建仓（fork=false）与「打包者就是
			// 本源 owner」（如 shuangji66 自己打的 105 张卡）都维持放过。
			//
			// 0.6.326k（F2=B2）：fork 查询器只装 GitHub 仓库——非 GitHub 主机
			// （自建 Gitea/内网源）的同名仓库会与 GitHub 串味，一律跳过本分支。
			if s.Forks == nil || !isGitHubRepoKey(srcRepo) {
				continue
			}
			bare := trimHost(srcRepo) // owner/repo（去主机前缀）
			parent, isFork, known := s.Forks.Lookup(bare)
			if !known {
				s.Forks.Enqueue(bare) // 本轮未知：排队后台拉，先保守放过
				continue
			}
			// 0.6.326k（F3=B3）：parent 指向本仓 = 自环脏数据，不得把自己写成上游。
			if !isFork || parent == "" || sameRepo(parent, bare) || !republishedByOther(a, ownerOf(bare)) {
				continue
			}
			// 0.6.326z2：交叉印证——fork parent 已收录但源里没有同名卡时，是
			// fork 者**自加的二创项目**（GenOffice/LobeHub/Lucky 实测：作者 fork
			// 了 EWEDLCM/FnDepot 索引框架往里加自己打包的应用），不是搬运 parent
			// 的内容，不判搬运（与 applyForkParentFallback 同一交叉印证口径）。
			if name, ok := bareToSource[parent]; ok {
				if upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName) == "" {
					continue
				}
			}
			a.UpstreamRepo = parent
			a.UpstreamURL = "https://github.com/" + parent
			if name, ok := bareToSource[parent]; ok {
				a.UpstreamSource = name
				a.UpstreamVersion = upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName)
			} else {
				s.applyUpstreamAddHint(a, bareToSource, verCache)
			}
			continue
		}
		a.UpstreamRepo = origin
		a.UpstreamURL = "https://github.com/" + origin
		if name, ok := bareToSource[origin]; ok {
			a.UpstreamSource = name
			a.UpstreamVersion = upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName)
		} else {
			// 0.6.326e：上游源未收录 → 预判「添加为源」到底加不加得上，
			// 免得按钮点了必然 404（用户实测 710850609/easytier-eui）。
			s.applyUpstreamAddHint(a, bareToSource, verCache)
		}
	}
}

// applySourceLineage 给每张卡派生「本源仓库」的两个展示字段（0.6.326f）：
//
//	SourceIsFork   本源仓库（该卡 Source 对应的源 URL）是 GitHub fork
//	               且 parent 非空 → 详情页出「禁用fork源」按钮；
//	SourceDisabled 该卡所属源当前 enabled=false → 按钮切成「恢复fork源」。
//
// 与上游（搬运）判定**完全独立**：只看卡片自身的 Source 与源列表，
// 不看包地址血缘（ReleaseURL=="" 的卡照样算）——「整仓 fork 式搬运」
// 的卡其包就在本源仓库里，upstream_* 判定走的是另一条分支，但「本源是
// fork」这件事对它们同样成立。
//
// 不设值：卡片无源（Source==""）、本源不是 GitHub 系源（归一 key 为空）、
// 或该源不在已配置源列表里（官方源/系统自装卡）——前端维持不出按钮。
// fork 关系未知时保守不置位并排队后台拉取（等缓存，不阻塞目录构建）。
func (s *Server) applySourceLineage(out []AppInfo) {
	if s == nil || s.Src == nil || len(out) == 0 {
		return
	}
	// 源名 → enabled（配置层单一事实源；缺省启用）。
	enabledBySource := map[string]bool{}
	if s.Cfg != nil {
		for _, st := range s.Cfg.Sources {
			enabledBySource[st.Name] = st.IsEnabled()
		}
	}
	// 源名 → 归一仓库 key（含禁用源：源列表来自 manager，与 Sources() 同源）。
	repoBySource := map[string]string{}
	for _, st := range s.Src.Sources() {
		if _, ok := repoBySource[st.Name]; ok {
			continue
		}
		if k := source.SourceURLKey(st.URL); k != "" {
			repoBySource[st.Name] = k
		}
	}
	for i := range out {
		a := &out[i]
		// 0.6.326y（G1）：本函数**故意不使用** originSourceOf —— 官方合并
		//（mergeOfficialCards）把这类卡的 Source 改写成 fnos-official 后，它在
		// UI 上顶着官方徽章；对顶着官方徽章的卡执行源级动作（「禁用fork源」会去
		// 禁用其隐藏的原来源）会误导用户。按钮门控保持只看 a.Source（本轮不动）。
		if a.Source == "" {
			continue // 无源卡（官方源/系统自装）：两个字段都不设
		}
		if en, ok := enabledBySource[a.Source]; ok && !en {
			a.SourceDisabled = true
		}
		bare := trimHost(repoBySource[a.Source])
		// 0.6.326k（F2=B2）：非 GitHub 主机源不查 GitHub fork 缓存（跨主机同名
		// 仓串味）。SourceDisabled 是配置层的独立事实，上面已置位，不受影响。
		if bare == "" || s.Forks == nil || !isGitHubRepoKey(repoBySource[a.Source]) {
			continue // 非 GitHub 源 / 未接线 fork 查询器：无可比
		}
		parent, isFork, known := s.Forks.Lookup(bare)
		if !known {
			s.Forks.Enqueue(bare) // 未知：保守不出按钮，排队后台补
			continue
		}
		// 0.6.326k（F3=B3）：自环 parent 不算「本源是 fork」，不出按钮。
		if isFork && parent != "" && !sameRepo(parent, bare) {
			a.SourceIsFork = true
		}
	}
}

// applyUpstreamAddHint 给「有 upstream_repo 但上游源未收录」的卡预判
// 「添加为源」可用性（0.6.326e）。只读探测器缓存；未知即入队、本轮到不了
// 结论就什么都不设（前端维持现状），绝不在这里发网络请求。
//
// 三种落点：
//   - 上游仓自己有索引       → UpstreamAddURL = 上游仓地址；
//   - 上游仓只是作者打包仓、
//     owner 名下有索引仓      → 该索引仓已收录时给一行说明（无需再加）；
//     未收录时 UpstreamAddURL = 该索引仓地址（举一反三：真正能加的是它）；
//   - 两处都无索引           → UpstreamAddNote = 一行原因（前端不出按钮）。
func (s *Server) applyUpstreamAddHint(a *AppInfo, bareToSource map[string]string, verCache map[string]map[string]upstreamCard) {
	if s == nil || s.AddProbes == nil || a.UpstreamRepo == "" {
		return
	}
	self, ownerRepo, known := s.AddProbes.Lookup(a.UpstreamRepo)
	if !known {
		s.AddProbes.Enqueue(a.UpstreamRepo)
		return // 未知：维持现状（前端照旧显示按钮）
	}
	if self != "" {
		a.UpstreamAddURL = "https://github.com/" + a.UpstreamRepo
		return
	}
	if ownerRepo != "" {
		// bareToSource 的键为小写（SourceURLKey 全文小写），探测结果按 GitHub
		// 目录名拼（可能含大写）——对照必须同口径。
		if name, ok := bareToSource[strings.ToLower(ownerRepo)]; ok {
			// 0.6.326i（P2）：上游 owner 的索引仓**已经收录**时，除了那行说明，
			// 还要把 UpstreamSource 也设上。旧实现只写 note 不设源 —— 结果同一批
			// 卡里「上游」有的显示源名、有的显示 owner/repo（用户报「有些又有」）；
			// 更要命的是 applyUpstreamCorrection 首行判 UpstreamSource=="" 直接
			// 跳过，这 32 张卡永远不参与自动纠正。upstream_repo/url 保持为包实际
			// 所在的打包仓（信息更准），note 保留说明这层关系。
			a.UpstreamSource = name
			a.UpstreamVersion = upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName)
			a.UpstreamAddNote = "上游应用已由已添加的源「" + name + "」收录"
			return
		}
		a.UpstreamAddURL = "https://github.com/" + ownerRepo
		return
	}
	a.UpstreamAddNote = "该上游仓库未发布应用源索引，无法添加为源"
}

// applyForkParentFallback（0.6.326i / P3）：**包地址拿不到时**的兜底血缘推断。
//
// 适用场景：索引里某个条目的 download_url 为空或非 GitHub 主机 → 常规路径
// 拿不到 origin，旧实现直接放弃 → 卡片连「溯源」行都不渲染（现网 22 张
// flatnas 散布在 22 个 fork 源里，全是这样）。
//
// 依据（与「整仓 fork 式搬运」同源两条）：
//  1. 本源仓库（该卡的 Source 对应仓库）是 GitHub fork；2. 发布者（distributor
//     为空回落 maintainer）不等于本源 owner —— 即「这张卡不是本源自己打的」。
//
// **硬前提（交叉印证）**：fork parent 那个仓库必须是**已收录的源**，且该源里
// **确实存在同名卡**。缺任一条件一律不设值 —— 宁可溯源行不显示，也不给出没有
// 证据的上游。实测负例：SurveyKing / OrangeSurvey@PlayWithAndyJin 的 parent 源
// EWEDLCM 里没有同名卡 → 正确排除；正例：flatnas@22 个 fork 源 → EWEDLCM 里有
// flatnas v1.1.3-dev3 → 补上。
func (s *Server) applyForkParentFallback(a *AppInfo, srcRepo string, bareToSource map[string]string, verCache map[string]map[string]upstreamCard) {
	if s == nil || s.Src == nil || s.Forks == nil {
		return
	}
	bare := trimHost(srcRepo)
	if bare == "" {
		return
	}
	// 0.6.326k（F2=B2）：fork 关系只对 GitHub 仓库有意义，非 GitHub 主机
	// （自建 Gitea/内网源）不查缓存——同名 GitHub 仓库的 fork 关系会串味。
	if !isGitHubRepoKey(srcRepo) {
		return
	}
	parent, isFork, known := s.Forks.Lookup(bare)
	if !known {
		s.Forks.Enqueue(bare) // 未知：排队后台补，本轮不设值
		return
	}
	// 0.6.326k（F3=B3）：自环 parent 不构成「别人搬运」的证据。
	if !isFork || parent == "" || sameRepo(parent, bare) || !republishedByOther(a, ownerOf(bare)) {
		return
	}
	name, ok := bareToSource[parent]
	if !ok {
		return // fork parent 未收录 → 无同名卡可印证，不臆断
	}
	ver := upstreamVersionOf(verCache, s, name, a.AppName, a.DisplayName)
	if ver == "" {
		return // 上游源里没有同名卡：证据不足，不设值
	}
	a.UpstreamRepo = parent
	a.UpstreamURL = "https://github.com/" + parent
	a.UpstreamSource = name
	a.UpstreamVersion = ver
}

// applyUpstreamCorrection 自动纠正（auto_correct，独立开关，默认关）：
// 已安装且来自「搬运上游」的应用，若上游原作者版本比已装新（且比现有
// 更新目标新），直接把更新目标改指上游源——无视 update_policy 三档。
//
// 跳过（平台/安装源权威优先）：
//   - 平台跟踪应用（daemon sourceID 非空 或 source=official）——更新判定
//     归 daemon 权威（0.6.257）；
//   - 没有任何可用的上游源卡（UpstreamSource 空，即 Moo 未添加该 origin 源）；
//   - 已装版本未知。
//
// 调用位置：applyLineageUpdates 之后（策略给的目标先立）、applyIgnoredUpdates
// 之前（用户「忽略更新」仍能压住自动纠正的更新信号）。
//
// 包字段复制尾段与 applyLineageUpdates 完全同款：更新会按 ReleaseURL 下载，
// 不跟随上游卡就会下回搬运源的旧包（被版本交叉校验挡住或空转）。
// upstreamCorrection 一次「已装搬运应用被纠正到上游原作者版本」的记录
// （0.6.326 通知批次）：applyUpstreamCorrection 只收集、不改判定——供
// buildCatalog 锁外异步推 upstream_corrected 通知用。
type upstreamCorrection struct {
	AppName        string // 卡片 appname（通知去重键）
	DisplayName    string // 展示名（可读标签，空回落 appname）
	Source         string // 纠正前所在源（搬运本源）
	From           string // 纠正前的更新目标（已装版本兜底）
	To             string // 纠正后目标 = 上游源最新版本
	UpstreamSource string // 上游源名（Moo 源列表中的源名）
}

func applyUpstreamCorrection(out []AppInfo, byName map[string]platform.InstalledApp, on bool) []upstreamCorrection {
	if !on {
		return nil
	}
	// 本轮真正走完「改更新目标」那段的应用（未通过的早退分支不计入）——
	// 仅用于通知，不参与任何判定。
	var hits []upstreamCorrection
	for i := range out {
		c := &out[i]
		if !c.Installed || c.UpstreamSource == "" || c.InstalledVersion == "" {
			continue
		}
		if ia, ok := byName[c.AppName]; ok && (ia.SourceID != "" || ia.Source == "official") {
			continue
		}
		var s *AppInfo
		for j := range out {
			if j == i {
				continue
			}
			// 0.6.326y：①（G3b）appname 大小写不敏感（大小写漂移时 sibling 找
			// 不到 → 纠正永久失效）；②（G1）上游卡来源按 originSourceOf 取 ——
			// 官方合并把 sibling 的 Source 改写成 fnos-official 时，仍要能用它的
			// 真来源与 c.UpstreamSource 比较。
			if !strings.EqualFold(out[j].AppName, c.AppName) || originSourceOf(&out[j]) != c.UpstreamSource {
				continue
			}
			// 0.6.326k（F4=B4）：显示名否决（与 applyLineageUpdates 同口径）——
			// 上游源里同 appname 但实为另一个应用时，不得拿它的版本当目标，
			// 更不得把它的包字段抄过来（否则更新会下错包）。
			if !displayNameCompatible(c.DisplayName, out[j].DisplayName) {
				continue
			}
			s = &out[j]
			break
		}
		if s == nil || s.LatestVersion == "" {
			continue
		}
		// 0.6.326k（F5=K1）：任一侧版本不可解析 = 版本未知 → 不点亮、不作目标。
		// 旧实现按字符串比较（"abc" > "1"）会点亮假更新并把它写成安装目标。
		if !comparableVersion(s.LatestVersion) || !comparableVersion(c.InstalledVersion) {
			continue
		}
		// 0.6.326k（F6=K2）：上游只发了预发布版（rc/beta/alpha/dev/pre）时不推给
		// 用户——自动纠正的机会成本是「换源更新」，宁可等上游发正式版。
		if isPrereleaseVersion(s.LatestVersion) {
			continue
		}
		if compareVersions(s.LatestVersion, c.InstalledVersion) <= 0 {
			continue // 上游不比已装新
		}
		if c.AvailableVersion != "" && compareVersions(s.LatestVersion, c.AvailableVersion) <= 0 {
			continue // 已有不低于它的更新目标，不降级
		}
		// 0.6.326 通知批次：先取纠正前快照（AvailableVersion/Source 下一段即被
		// 改写），再落字段——纠正行为本身逐字节不变，只多一条收集记录。
		from := c.InstalledVersion
		if from == "" {
			from = c.AvailableVersion
		}
		label := c.DisplayName
		if label == "" {
			label = c.AppName
		}
		srcBefore := c.Source
		c.HasUpdate = true
		c.AvailableVersion = s.LatestVersion
		c.UpdateFromSource = s.Source
		c.ReleaseURL = s.ReleaseURL
		c.ReleaseNotes = s.ReleaseNotes
		c.SizeBytes = s.SizeBytes
		c.Sha256 = s.Sha256
		// 0.6.326（审计 L-5）：架构必须与包同源——上游卡是按本机架构选出的
		// 包（bestRelease → currentArch），纠正后若不跟随，arm 设备会看到
		// 「目标是 arm 包、卡片却标 x86」的错标（详情页架构行失真）。
		c.Arch = s.Arch
		c.Archs = s.Archs
		c.UpdatedAt = s.UpdatedAt
		c.DownloadCount = s.DownloadCount
		hits = append(hits, upstreamCorrection{
			AppName:        c.AppName,
			DisplayName:    label,
			Source:         srcBefore,
			From:           from,
			To:             s.LatestVersion,
			UpstreamSource: c.UpstreamSource,
		})
	}
	return hits
}

// republishedByOther 判断这张卡的包是不是「别人打的」：distributor（为空时
// 回落 maintainer）非空且不等于本源仓库 owner。
//
// 为什么需要它：整仓 fork 走别人索引时这两个字段会原样保留——实测 picoclaw
// 33 张卡的 distributor 全部 = EWEDL，而各自本源 owner（Chordi0124…）都不同，
// 是「fork + 搬运」的可靠信号；而作者自建仓通常写着 owner 自己（shuangji66
// 的 105 张卡 distributor=shuangji66），据此避免误判。
func republishedByOther(a *AppInfo, owner string) bool {
	sig := strings.TrimSpace(a.Distributor)
	if sig == "" {
		sig = strings.TrimSpace(a.Maintainer)
	}
	if sig == "" {
		return false
	}
	return !strings.EqualFold(sig, owner)
}

// applyMirrorFingerprint 0.6.351（方案 A）：「脚本同步（非 fork 搬运）」溯源兜底。
//
// 背景：fork=false、download_url 改写/清空、maintainer_url 非 repo 形态的
// 脚本同步镜像仓，四条现有信号（内容血缘 / fork / 作者声明 / 打包者字段）
// 全失效 → 漏判。本函数用内容指纹：若一张**尚无 UpstreamRepo** 的卡，其
// (appname, sha256) 与**另一已收录源**的同名卡完全相同、且两源 owner 不同，
// 则判为搬运，上游 = 对方源。
//
// 安全边界：
//   - 只作用于 UpstreamRepo=="" 的卡（绝不覆盖已正确溯源）；
//   - sha256 非空才参与（flat fnpack 老格式 sha256 为空 → 跳过，诚实不可比）；
//   - 同 owner 不判（分仓发布）；
//   - 组内选「根」用打包者字段交叉印证 + 确定性排序打破对称，根自身不指向他人，
//     无环。
//
// 现网影响：当前 160 源里无 (appname,sha256) 完全相同的跨源组（唯一 7 张
// 双胞胎卡 sha256 为空），故对现有目录零改动；价值是封住未来脚本同步镜像。
func (s *Server) applyMirrorFingerprint(out []AppInfo) {
	if s == nil || s.Src == nil {
		return
	}
	repoToSource := s.sourceRepoByKey()
	if len(repoToSource) == 0 {
		return
	}
	srcByRepo := map[string]string{}
	for k, n := range repoToSource {
		if _, ok := srcByRepo[n]; !ok {
			srcByRepo[n] = k
		}
	}

	// 指纹分组：(appname 归一, sha256) → 各源一张卡的下标。
	type fpKey struct{ app, sha string }
	groups := map[fpKey]map[string]int{}
	for i := range out {
		a := &out[i]
		if a.Sha256 == "" {
			continue
		}
		sn := originSourceOf(a)
		if sn == "" {
			continue
		}
		// F1（0.6.353）：本源仓库必须是 GitHub 系才参与指纹配对。
		//
		// 动机（用户实测 Plex@shuangji66「上游 = moo-mate」误判）：聚合/索引源
		// （如 moo-mate 的本地 http 源）自己就是从各打包仓抄来的，包 sha256
		// 与真源**天然完全相同**；它是「下游」却与真源同组配对，一旦被选成根，
		// 真打包者的卡就被反向标成「搬运上游」。索引源没有「上游」这一个概念，
		// 只有「数据来源」——两者不能混用同一套指纹。
		// 与 applyForkParentFallback 的 isGitHubRepoKey 守卫同口径（0.6.326k F2）。
		if !isGitHubRepoKey(srcByRepo[sn]) {
			continue
		}
		key := fpKey{normAppName(a.AppName), a.Sha256}
		m := groups[key]
		if m == nil {
			m = map[string]int{}
			groups[key] = m
		}
		if _, dup := m[sn]; !dup {
			m[sn] = i
		}
	}

	verCache := map[string]map[string]upstreamCard{}
	for _, bySource := range groups {
		if len(bySource) < 2 {
			continue
		}
		root := chooseMirrorRoot(bySource, out, srcByRepo)
		if root == "" {
			continue
		}
		rootRepo := trimHost(srcByRepo[root])
		// F3（0.6.353）：兜底校验根的仓库 key 形态。源地址是索引文件
		// （…/moo/mate.json）时 trimHost 会产出 moo/mate.json 这种「伪 owner/repo」，
		// 拼成 https://github.com/moo/mate.json 是个不存在的地址——宁可不设溯源，
		// 也不给用户一个点开 404 的「上游」。
		if rootRepo == "" || !validOwnerRepo(rootRepo) {
			continue
		}
		for sn, i := range bySource {
			if sn == root {
				continue
			}
			a := &out[i]
			if a.UpstreamRepo != "" {
				continue // 已有正确溯源，不覆盖
			}
			if ownerOf(srcByRepo[sn]) == ownerOf(rootRepo) {
				continue // 同 owner 分仓，不算搬运
			}
			a.UpstreamRepo = rootRepo
			a.UpstreamURL = "https://github.com/" + rootRepo
			a.UpstreamSource = root
			a.UpstreamVersion = upstreamVersionOf(verCache, s, root, a.AppName, a.DisplayName)
		}
	}
}

// chooseMirrorRoot 在同 (appname,sha256) 组里选「真源」：
//  1. 若某成员卡片的打包者字段（distributor/maintainer）等于**另一成员** owner，
//     则该成员是「复制方」，不选它当根（交叉印证打破对称）；
//  2. F2（0.6.353）**包地址自持**过滤：只剩「包发布在自己本源仓库里」的成员
//     （releaseOrigin(ReleaseURL) 的 owner == 本源 owner）才有资格当根 ——
//     这是「我就是打包者」的物证。聚合/索引源把别人的包地址原样抄走，包地址
//     owner ≠ 自己，自动出局，不再靠仓库名字典序赌方向。
//     仅在**组内至少有一个自持成员**时启用；全组都不自持（脚本同步把包放自己仓
//     但地址被清空/改写）时退回原规则，保 0.6.351 的判定能力不回归。
//  3. 其余按确定性排序（仓库 key 字典序最小）选根。
//
// 若全体成员都被判复制方（脏数据/自环），放弃交叉印证退回纯确定性。
// 返回根源名；无法决定返回 ""。
func chooseMirrorRoot(bySource map[string]int, out []AppInfo, srcByRepo map[string]string) string {
	ownerOfSource := map[string]string{}
	for sn := range bySource {
		if r := srcByRepo[sn]; r != "" {
			ownerOfSource[sn] = ownerOf(r)
		}
	}
	mirror := map[string]bool{}
	for sn, i := range bySource {
		a := &out[i]
		sig := strings.ToLower(strings.TrimSpace(a.Distributor))
		if sig == "" {
			sig = strings.ToLower(strings.TrimSpace(a.Maintainer))
		}
		if sig == "" || sig == ownerOfSource[sn] {
			continue // 无打包者 或 自述 owner（自建/自打包）→ 不是复制方
		}
		// 打包者名命中**另一**成员 owner → 该成员是复制方（交叉印证打破对称）
		for other, o := range ownerOfSource {
			if other != sn && o == sig {
				mirror[sn] = true
			}
		}
	}
	// 全体成员都被判复制方（互指/脏数据）→ 放弃交叉印证，退回纯确定性。
	allMirror := true
	for sn := range bySource {
		if !mirror[sn] {
			allMirror = false
			break
		}
	}
	if allMirror {
		mirror = map[string]bool{}
	}
	// F2（0.6.353）：包地址自持优先（见函数头第 2 条）。
	pool := map[string]bool{}
	for sn := range bySource {
		if !mirror[sn] {
			pool[sn] = true
		}
	}
	selfHosted := map[string]bool{}
	for sn := range pool {
		own := ownerOfSource[sn]
		if own == "" {
			continue
		}
		if org := releaseOrigin(out[bySource[sn]].ReleaseURL); org != "" && ownerOf(org) == own {
			selfHosted[sn] = true
		}
	}
	if len(selfHosted) > 0 {
		for sn := range pool {
			if !selfHosted[sn] {
				delete(pool, sn)
			}
		}
	}
	best := ""
	for sn := range pool {
		if best == "" || srcByRepo[sn] < srcByRepo[best] {
			best = sn
		}
	}
	return best
}

// applyHiddenApps 应用级「禁用」展示层（0.6.326）：命中 config.HiddenApps
// 的卡只置 UserHidden —— 前端据此从列表/搜索/推荐过滤（但「已安装」tab 保留
// 以便恢复），发现页「禁用列表」按同一字段集中找回。
//
// **不置 Hidden**（0.6.326-ui 修正）：Hidden 是 appsResponse 的「不下发」开关
// （去重策略用）。一旦置位，该卡就从 /api/apps 响应里消失，前端连 user_hidden
// 都拿不到 → 发现页「禁用列表」永远看不到它（用户实测：「详情页禁用的应用不在
// 禁用列表显示」）。禁用语义 = 「列表过滤」而非「目录消失」，故只留 UserHidden，
// 由前端按 user_hidden 过滤（filteredApps / 计数 / 推荐均已同口径）。
//
// 匹配口径：卡片 key 精确匹配，回落 appname（同 appname 的多源卡一起命中）。
//
// 调用位置：buildCatalog 最末（applyDedupPolicy 之后）——去重策略会写
// Hidden，必须在其后置位才不被覆盖。
func applyHiddenApps(out []AppInfo, hidden []string) {
	if len(hidden) == 0 {
		return
	}
	set := make(map[string]bool, len(hidden))
	for _, k := range hidden {
		if k = strings.TrimSpace(k); k != "" {
			set[k] = true
		}
	}
	for i := range out {
		a := &out[i]
		if !set[a.Key] && !set[a.AppName] {
			continue
		}
		a.UserHidden = true
	}
}
