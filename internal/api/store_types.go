package api

import (
	"moo/internal/config"
	"moo/internal/source"
)

// 本文件镜像 New Store 前端 src/api/client.ts 的数据契约（Moo 后端按此提供）。

// ChangelogEntry 是一条版本化更新说明（详情页「更新内容」列表渲染用）。
type ChangelogEntry struct {
	Version string `json:"version,omitempty"`
	Text    string `json:"text"`
}

// AppInfo 是目录应用的统一视图（合并源元数据 + daemon 安装状态）。
type AppInfo struct {
	Key              string `json:"key"`
	AppName          string `json:"appname"`
	DisplayName      string `json:"display_name"`
	Description      string `json:"description,omitempty"`
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	InstalledFPKVer  string `json:"installed_fpk_version,omitempty"`
	AvailableVersion string `json:"available_version,omitempty"`
	// UpdateFromSource（0.6.272）：update_policy=origin/lineage 时，更新目标
	// 来自跨源同宗卡片——记录该来源源名供 UI/通知展示「来自 XX 源」；
	// 空 = 更新来自安装源自身（默认 strict 下恒为空）。
	UpdateFromSource string `json:"update_from_source,omitempty"`
	HasUpdate        bool   `json:"has_update"`
	// UpdateIgnored 更新已被用户忽略（0.6.181）：has_update 已同步置假，
	// 详情页/行卡显示「已忽略」徽章并提供「取消忽略」；available_version
	// 保留供发现页「忽略更新」列表展示旧→新版本。
	UpdateIgnored bool `json:"update_ignored,omitempty"`
	// UpdateIgnoredPending（0.6.197）：已忽略应用确实存在被压住的更新
	//（源版本 > 已装版本）。dock「有更新」列表/角标计数含这类应用
	//（行上带「已忽略」标记）；已忽略且已最新的 app 不计入。
	UpdateIgnoredPending bool   `json:"update_ignored_pending,omitempty"`
	Platform             string `json:"platform,omitempty"`
	// Arch 当前选中安装包的架构（fnpack 约定 x86/arm/all；0.6.303：
	// 源未提供架构信息时为空。arm 设备上 bestRelease 按 currentArch()=arm
	// 自动选中 arm 包（x86→all 回退），此字段=实际会装的包架构）。
	Arch            string   `json:"arch,omitempty"`
	Archs           []string `json:"archs,omitempty"` // 源提供的全部架构（x86/arm/all，展示序）
	ReleaseURL      string   `json:"release_url,omitempty"`
	ReleaseNotes    string   `json:"release_notes,omitempty"`
	Status          string   `json:"status,omitempty"`
	StartStop       *bool    `json:"start_stop,omitempty"`
	Uninstallable   *bool    `json:"uninstallable,omitempty"`
	WebProtocol     string   `json:"web_protocol,omitempty"`
	WebURL          string   `json:"web_url,omitempty"`
	WebPort         int      `json:"web_port,omitempty"`
	WebPath         string   `json:"web_path,omitempty"`
	WebOnWebUI      bool     `json:"web_on_webui,omitempty"`
	WebServiceName  string   `json:"web_service_name,omitempty"`
	ServicePort     int      `json:"service_port,omitempty"`
	// InstallVolume / InstallPath（0.6.337）：已安装应用的真实落盘位置。
	// InstallVolume>0 = /volN/@appcenter/<app>；=0 且 InstallPath 非空 =
	// 系统空间 /var/apps/<app>（root 应用）。未安装为空。
	InstallVolume int    `json:"install_volume,omitempty"`
	InstallPath   string `json:"install_path,omitempty"`
	Homepage        string   `json:"homepage,omitempty"`
	IconURL         string   `json:"icon_url,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	DownloadCount   *int     `json:"download_count,omitempty"`
	LocalInstalls   int      `json:"local_installs,omitempty"`
	AppType         string   `json:"app_type,omitempty"`
	Category        string   `json:"category,omitempty"`
	PostInstallNote string   `json:"post_install_note,omitempty"`
	Source          string   `json:"source,omitempty"`
	Maintainer      string   `json:"maintainer,omitempty"`
	MaintainerURL   string   `json:"maintainer_url,omitempty"`
	Distributor     string   `json:"distributor,omitempty"`
	DistributorURL  string   `json:"distributor_url,omitempty"`
	Changelog       string   `json:"changelog,omitempty"`
	// ChangelogEntries：changelog 解析后的版本化条目（最新在前），
	// 供详情页「更新内容」列表渲染；无 changelog 时为空。
	ChangelogEntries []ChangelogEntry `json:"changelog_entries,omitempty"`
	SizeBytes        int64            `json:"size_bytes,omitempty"`
	// InstallType 应用运行方式/安装位置（如 root / 用户空间 / 系统空间）。
	InstallType string `json:"install_type,omitempty"`
	// FirstReleaseAt 该应用最早发布时间（源提供时展示）。
	FirstReleaseAt string `json:"first_release_at,omitempty"`
	Sha256         string `json:"sha256,omitempty"`
	// ── moo.json 扩展（0.6.269 起实现，见 docs/Moo应用源协议.md §4.2/§4.5）──
	// DescHTML 富文本简介（详情页消毒后渲染）。
	DescHTML string `json:"desc_html,omitempty"`
	// License 许可协议（如 MIT）。
	License string `json:"license,omitempty"`
	// MinFnos 最低 fnOS 版本（详情页提示）。
	MinFnos string `json:"min_fnos,omitempty"`
	// Wizard 源声明的安装向导参数（安装时前端收集后传 ?wizard=）。
	Wizard       *source.SourceWizard `json:"wizard,omitempty"`
	PreviewCount int                  `json:"preview_count,omitempty"`
	// PreviewURLs 预览图直链（详情页灯箱按 index 取 /asset?type=preview）；
	// 与 PreviewCount 配套，仅详情用（列表瘦身时若剔除不影响计数展示）。
	PreviewURLs []string `json:"preview_urls,omitempty"`
	HasReadme   bool     `json:"has_readme,omitempty"`
	// ── 溯源与禁用（0.6.326）──
	// UserHidden：该卡被用户「禁用」（config.HiddenApps）——前端从列表/
	// 搜索/推荐过滤，但「已安装」tab 保留（保证可恢复）；详情按 key 可达。
	UserHidden bool `json:"user_hidden,omitempty"`
	// UpstreamSource/UpstreamRepo/UpstreamVersion/UpstreamURL：应用溯源
	//（0.6.326）。该卡的发布仓库 owner 与本源仓库 owner 不同 ⇒ 判定为
	// 搬运上游：UpstreamRepo=上游 owner/repo（恒有）、UpstreamSource=Moo
	// 已添加的该上游源名（未添加则为空）、UpstreamVersion=上游源同名卡
	// 最新版本、UpstreamURL=上游仓库地址。非搬运卡全空。
	UpstreamSource  string `json:"upstream_source,omitempty"`
	UpstreamRepo    string `json:"upstream_repo,omitempty"`
	UpstreamVersion string `json:"upstream_version,omitempty"`
	UpstreamURL     string `json:"upstream_url,omitempty"`
	// UpstreamAddURL/UpstreamAddNote（0.6.326e）：上游源未收录时「添加为源」
	// 的可用性预判（探测器用与 Fetch 同源的候选展开探测，7 天缓存，不阻塞）：
	//   AddURL 非空 ⇒ 显示按钮，点它添加该地址（本仓可加=上游仓地址；
	//                 本仓只是打包仓而 owner 有索引仓=owner 的索引仓地址）；
	//   AddNote 非空 ⇒ 不显示按钮，显示该一行原因；
	//   两者皆空 ⇒ 尚未探测（前端维持现状，照旧显示按钮指向 UpstreamRepo）。
	UpstreamAddURL  string `json:"upstream_add_url,omitempty"`
	UpstreamAddNote string `json:"upstream_add_note,omitempty"`
	// ── 本源仓库属性（0.6.326f）──
	// SourceIsFork：该卡所属源（Source）对应的**本源仓库**是 GitHub fork
	// 且 parent 非空。前端据此在溯源第二行出「禁用fork源」按钮（非 fork /
	// fork 关系未知则不渲染）。与上游判定独立：不看包地址血缘。
	SourceIsFork bool `json:"source_is_fork,omitempty"`
	// SourceDisabled：该卡所属源当前 enabled=false（用户已禁用该 fork 源）。
	// 前端据此把按钮切成「恢复fork源」。禁用源的卡不在列表下发（聚合跳过
	// 禁用源），此字段主要用于详情端点/边界态显示。
	SourceDisabled bool `json:"source_disabled,omitempty"`
	// ── 去重展示层（0.6.312 B2，仅展示；数据层全量卡不变）──
	// Hidden：当前 dedup_policy 下非代表卡——列表（/api/apps 无源过滤时）
	// 与推荐不展示；按 key 的详情/显式引用不受影响（逃生门）。
	Hidden bool `json:"-"`
	// SameNameCount：同名组卡片数（仅代表卡承载；>1 = 有重复，列表可加
	// 小角标、详情页出候选表）。
	SameNameCount int `json:"same_name_count,omitempty"`
	// SameName：同名组全部卡片（仅详情下发；列表瘦身时剥离）。
	SameName []SameNameEntry `json:"same_name,omitempty"`
	// InstallConflict（0.6.312 B2）：详情卡与已装卡的安装冲突分支——
	// ""=无冲突 / same=同源同版本 / lineage=可验证同宗 / different=不同源构建 /
	// official=已装卡为官方平台应用。仅详情下发（组>1 且本卡未装时非空）。
	InstallConflict string `json:"install_conflict,omitempty"`
}

// SameNameEntry 同名组条目（详情页候选表 + 安装冲突判定数据源）。
type SameNameEntry struct {
	Key         string `json:"key"`
	Source      string `json:"source"`
	DisplayName string `json:"display_name,omitempty"`
	Version     string `json:"version,omitempty"` // 该卡最新可用版本
	Arch        string `json:"arch,omitempty"`
	Installed   bool   `json:"installed"`
	IsRep       bool   `json:"is_rep"`           // 当前代表卡（= 列表里显示的那张）
	Origin      string `json:"origin,omitempty"` // 归一发布仓库（同宗判定）
	Sha256      string `json:"sha256,omitempty"` // 最新包 sha（同构建判定）
}

// AppsResponse 是 GET /api/apps 的响应。
type AppsResponse struct {
	Apps           []AppInfo `json:"apps"`
	LastCheck      string    `json:"last_check"`
	UpgradeAllowed bool      `json:"upgrade_allowed"`
}

// SourceEntry 是应用源视图（GET /api/sources 的元素）。
type SourceEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	AppCount    int    `json:"app_count"`
	Error       string `json:"error,omitempty"`
	LastFetched string `json:"last_fetched,omitempty"`
	Enabled     bool   `json:"enabled"`
	EmptyStreak int    `json:"empty_streak"`
	// Favorite 是否关注（星标，0.6.143）；关注源新增应用时推通知。
	Favorite bool `json:"favorite,omitempty"`
	// ── 源重合率（0.6.326z，只读信号）──
	// FnDepot 式内容重合率：该源与「最相似的对方源」的应用名/版本重合情况。
	// 与 fork 标志无关，用于把「同 owner 且 fork=false 的整仓复制源」变成
	// 源级可见信号。**绝非停源依据**：不加按钮、不改启用/禁用语义、不进
	// 应用详情页「溯源」行。官方源恒不带这些字段。口径见 overlap_326z.go。
	OverlapRisk        string  `json:"overlap_risk,omitempty"`         // none|low|medium|high
	OverlapWith        string  `json:"overlap_with,omitempty"`         // 最相似的对方源名
	OverlapNameCount   int     `json:"overlap_name_count,omitempty"`   // appname 重合数
	OverlapExactCount  int     `json:"overlap_exact_count,omitempty"`  // appname|version 重合数
	OverlapNameRatio   float64 `json:"overlap_name_ratio,omitempty"`   // 名称重合率（分母 max）
	OverlapExactRatio  float64 `json:"overlap_exact_ratio,omitempty"`  // 精确重合率（分母 max）
	OverlapUniqueNames int     `json:"overlap_unique_names,omitempty"` // 本方独有 appname 数
	OverlapSelfCount   int     `json:"overlap_self_count,omitempty"`   // 本方应用数（分母依据）
}

// BackgroundTask 是后台任务视图（GET /api/tasks 的元素）。
type BackgroundTask struct {
	ID         string  `json:"id,omitempty"` // 任务唯一 ID（下载任务）；前端据此区分「新任务」与「旧终态残留」
	AppName    string  `json:"appname"`
	Op         string  `json:"op"` // install / update / download
	Status     string  `json:"status"`
	Step       string  `json:"step,omitempty"`
	Progress   float64 `json:"progress,omitempty"`
	Message    string  `json:"message,omitempty"`
	NewVer     string  `json:"new_version,omitempty"`
	Downloaded int64   `json:"downloaded,omitempty"`
	Total      int64   `json:"total,omitempty"`
	Speed      int64   `json:"speed,omitempty"`
}

// MirrorHealth 是加速源健康监测快照。
type MirrorHealth struct {
	Mirrors    []MirrorStat  `json:"mirrors"`
	Selected   string        `json:"selected"`
	Active     string        `json:"active"`
	LastProbe  string        `json:"last_probe"`
	LastSwitch *MirrorSwitch `json:"last_switch,omitempty"`
	IntervalS  int           `json:"interval_s"`
}

// VolumeOption 是安装卷选项。
type VolumeOption struct {
	Index      int    `json:"index"`
	Path       string `json:"path"`
	TotalBytes int64  `json:"total_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	// Name 用户命名的卷友好名（0.6.325 C1，issue#5 遗留改进项）。
	// 2026-10-14 主 NAS 实机侦察结论：本版本 fnOS（1.2.1）找不到可靠的
	// 卷友好名来源——面板存储卷渲染恒为「存储空间 {id}」且无重命名入口
	// （locale 无 rename 项）；PG open_gateway 无卷表、trim.mount 的
	// comment 列恒空、LVM/btrfs label 均只有 UUID/空。故本字段当前
	// 恒为空（omitempty 不透出），前端回落「存储空间 N · /volN」；
	// 字段保留=来源一旦可用即可透传，前端无需再改。详见
	// FIX_325C_IMPL.md 侦察节。
	Name string `json:"name,omitempty"`
}

// Settings 是设置页数据。
type Settings struct {
	CheckIntervalHours int    `json:"check_interval_hours"`
	Mirror             string `json:"mirror"`
	// 0.6.321 F7：「智能选源包含直连」（不带 omitempty：false 也要透出，
	// 前端据此渲染开关；omitempty 会把「显式关」吞成 undefined 恒显关）。
	MirrorIncludeDirect bool                  `json:"mirror_include_direct"`
	MirrorOptions       []config.MirrorOption `json:"mirror_options,omitempty"`
	DockerMirror        string                `json:"docker_mirror"`
	DockerMirrorOptions []config.MirrorOption `json:"docker_mirror_options,omitempty"`
	CustomGitHubMirror  string                `json:"custom_github_mirror,omitempty"`
	CustomDockerMirror  string                `json:"custom_docker_mirror,omitempty"`
	// 用户自定义加速源（增删改走 PUT /api/settings）。
	CustomMirrors []config.MirrorOption `json:"custom_mirrors,omitempty"`
	InstallVolume       int                   `json:"install_volume"`
	VolumeOptions       []VolumeOption        `json:"volume_options,omitempty"`
	DownloadDir         string                `json:"download_dir,omitempty"`
	AutoUpdate          bool                  `json:"auto_update"`
	// AutoCorrect（0.6.326 引入；0.6.326x 起**已弃用**）：语义并入
	// UpdatePolicy="upstream"（强制上游）。仍透出（不带 omitempty）仅为兼容
	// 旧前端；保存设置后恒为 false。
	AutoCorrect bool `json:"auto_correct"`
	// CatalogLanguage 目录语言（0.6.269）：auto（默认，跟随请求
	// Accept-Language）/ zh-CN / en-US。影响官方目录的名称/简介语言。
	CatalogLanguage string `json:"catalog_language"`
	// UpdatePolicy 已装应用跨源更新判定策略（0.6.272，设置页三选一）：
	// "" / strict = 只认安装源自身最新（0.6.174 行为，默认零变化）；
	// origin = 同一发布仓库（download_url 归一 owner/repo）的新版也算更新；
	// lineage = origin 之外，author / distributor 相等也算同宗。
	// 平台跟踪应用（sourceID 非空 / official）不受本策略影响。
	UpdatePolicy string `json:"update_policy"`
	// DedupPolicy 列表去重展示策略（0.6.312 B2，首页胶囊三选一）：
	// all（默认，零变化）/ merge（同宗簇留一代表）/ one（每 appname 一张）。
	DedupPolicy       string `json:"dedup_policy"`
	SourceAutoCareOff bool   `json:"source_auto_care_disabled"`
	SourceListURL     string `json:"source_list_url,omitempty"`
	SourceListOff     bool   `json:"source_list_disabled"`
	// 0.6.325 安全加固 R1：允许「源列表同步」添加非公共地址的源（默认 false）。
	SourceListAllowPrivate bool `json:"source_list_allow_private"`
	// GitHubTokenSet 0.6.334：是否已配置 GitHub API token（**只透出是否设置，
	// 绝不回传 token 本身**）。有 token 时 fork 关系查询额度 5000/h，
	// 冷装 2 分钟内可补满溯源；未知时匿名 60/h、需数小时。
	GitHubTokenSet bool `json:"github_token_set"`
	// 0.6.255：面板账号字段（panel_enabled/panel_username/panel_base_url/
	// panel_has_password/panel_decrypt_failed）已从设置中彻底移除——官方源
	// 改为纯 OAuth，授权时临时输入面板账号（不落地存储）。
	// 备份设置（备份设置 tab）
	BackupDir           string `json:"backup_dir,omitempty"`
	BackupAuto          bool   `json:"backup_auto"`
	BackupIntervalDays  int    `json:"backup_interval_days"`
	CacheCleanDays      int    `json:"cache_clean_days"`
	CacheCleanEveryDays int    `json:"cache_clean_every_days"`
	// 日志页显示行数（0.6.261）：0 = 未设置（前端默认 200）。
	LogLines int `json:"log_lines"`
	// 加速源自动测速间隔（0.6.148，设置页双齿轮选择框）：每组小时/分钟。
	// 0h0m = 未设置（后端按默认 5 分钟执行）。
	GhProbeHours   int `json:"gh_probe_hours"`
	GhProbeMinutes int `json:"gh_probe_minutes"`
	DkProbeHours   int `json:"dk_probe_hours"`
	DkProbeMinutes int `json:"dk_probe_minutes"`
	// 科学加速（0.6.206）：本机代理（仅 GitHub 域名改道）
	ProxyEnabled bool   `json:"proxy_enabled"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	// Dock 主导航 / 设置 tab 的当前生效顺序（解析后：缺省或非法 = 默认顺序）
	DockOrder        []string `json:"dock_order"`
	SettingsTabOrder []string `json:"settings_tab_order"`
}

// FpkDownloadFile 是已下载 FPK 缓存条目。
type FpkDownloadFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModAt   string `json:"mod_at"`
	AppName string `json:"appname,omitempty"`
	// DisplayName 包 manifest 的 display_name（中文名/正式名；索引未就绪时为空）。
	DisplayName string `json:"display_name,omitempty"`
	// Parsing 0.6.332 F4：manifest 正在后台解析（首次打开下载列表的那几秒）。
	// 前端据此显示「解析中…」而不是留空；解析完成（成功或损坏）后即 false。
	Parsing   bool `json:"parsing,omitempty"`
	Installed bool `json:"installed"`
}
