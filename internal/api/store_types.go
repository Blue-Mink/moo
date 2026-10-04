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
	ReleaseURL           string `json:"release_url,omitempty"`
	ReleaseNotes         string `json:"release_notes,omitempty"`
	Status               string `json:"status,omitempty"`
	StartStop            *bool  `json:"start_stop,omitempty"`
	Uninstallable        *bool  `json:"uninstallable,omitempty"`
	WebProtocol          string `json:"web_protocol,omitempty"`
	WebURL               string `json:"web_url,omitempty"`
	WebPort              int    `json:"web_port,omitempty"`
	WebPath              string `json:"web_path,omitempty"`
	WebOnWebUI           bool   `json:"web_on_webui,omitempty"`
	WebServiceName       string `json:"web_service_name,omitempty"`
	ServicePort          int    `json:"service_port,omitempty"`
	Homepage             string `json:"homepage,omitempty"`
	IconURL              string `json:"icon_url,omitempty"`
	UpdatedAt            string `json:"updated_at,omitempty"`
	DownloadCount        *int   `json:"download_count,omitempty"`
	LocalInstalls        int    `json:"local_installs,omitempty"`
	AppType              string `json:"app_type,omitempty"`
	Category             string `json:"category,omitempty"`
	PostInstallNote      string `json:"post_install_note,omitempty"`
	Source               string `json:"source,omitempty"`
	Maintainer           string `json:"maintainer,omitempty"`
	MaintainerURL        string `json:"maintainer_url,omitempty"`
	Distributor          string `json:"distributor,omitempty"`
	DistributorURL       string `json:"distributor_url,omitempty"`
	Changelog            string `json:"changelog,omitempty"`
	// ChangelogEntries：changelog 解析后的版本化条目（最新在前），
	// 供详情页「更新内容」列表渲染；无 changelog 时为空。
	ChangelogEntries []ChangelogEntry `json:"changelog_entries,omitempty"`
	SizeBytes        int64            `json:"size_bytes,omitempty"`
	// InstallType 应用运行方式/安装位置（如 root / 用户空间 / 系统空间）。
	InstallType string `json:"install_type,omitempty"`
	// FirstReleaseAt 该应用最早发布时间（源提供时展示）。
	FirstReleaseAt string `json:"first_release_at,omitempty"`
	Sha256         string `json:"sha256,omitempty"`
	// ── moo.json 扩展（0.6.269 起实现，见 docs/MOO-PROTOCOL.md §4.2/§4.5）──
	// DescHTML 富文本简介（详情页消毒后渲染）。
	DescHTML string `json:"desc_html,omitempty"`
	// License 许可协议（如 MIT）。
	License string `json:"license,omitempty"`
	// MinFnos 最低 fnOS 版本（详情页提示）。
	MinFnos string `json:"min_fnos,omitempty"`
	// Wizard 源声明的安装向导参数（安装时前端收集后传 ?wizard=）。
	Wizard *source.SourceWizard `json:"wizard,omitempty"`
	PreviewCount   int    `json:"preview_count,omitempty"`
	// PreviewURLs 预览图直链（详情页灯箱按 index 取 /asset?type=preview）；
	// 与 PreviewCount 配套，仅详情用（列表瘦身时若剔除不影响计数展示）。
	PreviewURLs []string `json:"preview_urls,omitempty"`
	HasReadme   bool     `json:"has_readme,omitempty"`
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
}

// Settings 是设置页数据。
type Settings struct {
	CheckIntervalHours  int                   `json:"check_interval_hours"`
	Mirror              string                `json:"mirror"`
	MirrorOptions       []config.MirrorOption `json:"mirror_options,omitempty"`
	DockerMirror        string                `json:"docker_mirror"`
	DockerMirrorOptions []config.MirrorOption `json:"docker_mirror_options,omitempty"`
	CustomGitHubMirror  string                `json:"custom_github_mirror,omitempty"`
	CustomDockerMirror  string                `json:"custom_docker_mirror,omitempty"`
	InstallVolume       int                   `json:"install_volume"`
	VolumeOptions       []VolumeOption        `json:"volume_options,omitempty"`
	DownloadDir         string                `json:"download_dir,omitempty"`
	AutoUpdate          bool                  `json:"auto_update"`
	// CatalogLanguage 目录语言（0.6.269）：auto（默认，跟随请求
	// Accept-Language）/ zh-CN / en-US。影响官方目录的名称/简介语言。
	CatalogLanguage string `json:"catalog_language"`
	// UpdatePolicy 已装应用跨源更新判定策略（0.6.272，设置页三选一）：
	// "" / strict = 只认安装源自身最新（0.6.174 行为，默认零变化）；
	// origin = 同一发布仓库（download_url 归一 owner/repo）的新版也算更新；
	// lineage = origin 之外，author / distributor 相等也算同宗。
	// 平台跟踪应用（sourceID 非空 / official）不受本策略影响。
	UpdatePolicy string `json:"update_policy"`
	SourceAutoCareOff   bool                  `json:"source_auto_care_disabled"`
	SourceListURL       string                `json:"source_list_url,omitempty"`
	SourceListOff       bool                  `json:"source_list_disabled"`
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
	Installed   bool   `json:"installed"`
}
