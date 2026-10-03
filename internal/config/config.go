// Package config 管理 Moo 的持久化配置（数据源列表等）。
package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"moo/internal/secret"
)

// ── 敏感字段落盘加密（0.6.220「方案 X」）──────────────────────────────
//
// 内存里一律明文（面板客户端、通知发送直接用）；只在 **写盘时加密**、
// **读盘后解密**。加密器由 cmd/server 启动时注入（见 internal/secret）。
//
// 为避免 config → notify 的循环依赖（notify 反向 import 本包），通知渠道
// 参数的加解密由 notify 包通过 SetExtraCodec 注册钩子完成。
//
// 未注入加密器时（如单测、旧嵌入场景）行为与旧版一致：凭据明文读写。

// Codec 敏感字段加解密器（internal/secret.Sealer 满足该接口）。
type Codec interface {
	Seal(string) (string, error)
	Open(string) (string, error)
}

var codec Codec
var extraCodec func(c *Config, seal bool) (foundPlaintext bool, err error)
var migrationNeeded bool

// SetCodec 注入加解密器（启动时调用）。
func SetCodec(c Codec) { codec = c }

// SetExtraCodec 注册附加字段处理钩子（notify 渠道参数）。
// seal=true 加密、false 解密；返回「是否遇到明文」（供迁移判定）。
func SetExtraCodec(f func(c *Config, seal bool) (bool, error)) { extraCodec = f }

// CodecReady 是否已注入加解密器。
func CodecReady() bool { return codec != nil }

// SealValue / OpenValue / IsSealedValue 供 notify 包处理渠道参数。
// 未注入加密器时 Seal/Open 原样返回（保持旧行为）。
func SealValue(v string) (string, error) {
	if codec == nil {
		return v, nil
	}
	return codec.Seal(v)
}

func OpenValue(v string) (string, error) {
	if codec == nil {
		return v, nil
	}
	return codec.Open(v)
}

// IsSealedValue 判断值是否为密文（`enc:v1:` 前缀）。
func IsSealedValue(v string) bool { return secret.IsSealed(v) }

// MigrationNeeded 上次 Load 是否发现「明文凭据」——上层据此触发一次 Save
// 把存量明文改写为密文（完成后自动归 false，因为 Save 后内存值不变、
// 下次 Load 读到的是密文）。
func MigrationNeeded() bool { return migrationNeeded }

// SourceRef 是一个应用源引用（名称 + fnpack.json 地址或仓库地址）。
type SourceRef struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// IsEnabled 源是否启用（缺省启用）。
func (s SourceRef) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// Config 是 Moo 的运行时配置，持久化为 DataDir 下的 config.json。
type Config struct {
	WebPort       string      `json:"web_port"`
	Sources       []SourceRef `json:"sources"`
	DownloadDir   string      `json:"download_dir"`
	InstallVolume int         `json:"install_volume,omitempty"`
	// 加速源选择（auto/镜像key/direct/custom + 自定义地址）
	Mirror             string `json:"mirror,omitempty"`
	DockerMirror       string `json:"docker_mirror,omitempty"`
	CustomGitHubMirror string `json:"custom_github_mirror,omitempty"`
	CustomDockerMirror string `json:"custom_docker_mirror,omitempty"`
	// 加速源自动测速间隔（0.6.148，设置页双齿轮时间选择框）：
	// Gh* = GitHub 加速组，Dk* = Docker 镜像组。0h0m（未设置）= 默认 5 分钟；
	// 非法值（越界/总分钟 <1）解析为 5 分钟，上限 24 小时。
	GhProbeHours   int `json:"gh_probe_hours,omitempty"`
	GhProbeMinutes int `json:"gh_probe_minutes,omitempty"`
	DkProbeHours   int `json:"dk_probe_hours,omitempty"`
	DkProbeMinutes int `json:"dk_probe_minutes,omitempty"`
	// 内置源列表自动同步
	SourceListURL string `json:"source_list_url,omitempty"`
	SourceListOff bool   `json:"source_list_disabled,omitempty"`
	// 应用源自动监测（设置页「应用源自动监测」开关，off=停用该策略）：
	// 每轮全量刷新后，成功但 0 应用连续 5 次的源自动停用，空源在源列表沉底。
	SourceAutoCareOff bool `json:"source_auto_care_disabled,omitempty"`
	// aria2c 可执行文件路径（空 = 自动探测 fnOS 内置/通用路径；设为 "off" 禁用）
	Aria2Bin string `json:"aria2_bin,omitempty"`
	// 应用源自动检查间隔（小时）。0 按默认 24 小时。
	CheckIntervalHours int `json:"check_interval_hours,omitempty"`
	// 自动更新应用：周期源刷新后对「有更新」的已装应用自动走标准更新管线
	// （官方应用走平台 cloud 通道、社区应用走 FPK 升级管线，均保留 @appdata）。
	// 排除商店自身（moo，防自更新环）。依赖 Moo 服务保持运行。
	AutoUpdate bool `json:"auto_update,omitempty"`
	// 本机安装次数（appname → 经 moo 安装/更新的累计次数）。
	// 第三方源应用无全局下载量数据，列表/详情用「本机 N 次」回退展示。
	LocalInstalls map[string]int `json:"local_installs,omitempty"`
	// 内嵌 Docker 镜像加速引擎（iStoreEnhance）开关
	// 官方应用中心直连（面板账号）
	PanelEnabled  bool   `json:"panel_enabled,omitempty"`
	PanelUsername string `json:"panel_username,omitempty"`
	PanelPassword string `json:"panel_password,omitempty"`
	// 运行期状态（不落盘，0.6.220）：有凭据存在但解不开（换机/密钥文件丢失）
	// → 已按「未设置」处理；设置页据此提示用户重填，而不是静默失效。
	SecretDecryptFailed bool `json:"-"`
	PanelBaseURL  string `json:"panel_base_url,omitempty"`
	// 设置备份目录（config.json 全量快照的存放位置；空 = 本机应用数据目录下 backups/）。
	// 可选 /volN 下共享目录 = 外部存储，手机/电脑可同步备份走。
	BackupDir string `json:"backup_dir,omitempty"`
	// 自动备份：开启后每 BackupIntervalDays 天写一份快照（0 = 默认 7 天）
	BackupAuto         bool `json:"backup_auto,omitempty"`
	BackupIntervalDays int  `json:"backup_interval_days,omitempty"`
	// Moo 应用缓存自动清理：>0 = 每 CacheCleanEveryDays 天删除应用缓存
	// （图标/README 等）中超过 CacheCleanDays 天的文件（0 = 关闭）。
	// 只动 @appdata/moo/cache，绝不触碰已下载 FPK 与已安装应用。
	CacheCleanDays      int `json:"cache_clean_days,omitempty"`
	CacheCleanEveryDays int `json:"cache_clean_every_days,omitempty"`
	// 日志页显示行数（0.6.261：此前纯前端本地态，切 tab 即复位 200）。
	// 0/缺省 = 未设置（前端按默认 200 处理）。
	LogLines int `json:"log_lines,omitempty"`
	// 科学加速（0.6.206，加速源设置末卡片）：GitHub 上游流量的本机代理
	// （http/https/socks4/socks5，如 socks5://127.0.0.1:1080）。
	// 仅 GitHub 域名改道，镜像/其余流量直连；地址只存本机配置不外发。
	ProxyEnabled bool   `json:"proxy_enabled,omitempty"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	// 应用收藏（目录 key = appname 或 appname@源名，同名共存时带源后缀）。
	// 列表/详情星标切换；发现页「收藏列表」按此渲染。上限 500 条防误操作膨胀。
	Favorites []string `json:"favorites,omitempty"`
	// 应用源收藏（关注源，0.6.143）：源名列表；源行星标切换。
	// 关注源新增应用时触发 favorite_source_apps 事件通知。
	FavoriteSources []string `json:"favorite_sources,omitempty"`
	// 忽略更新（0.6.181）：appname 列表，顺序 = 忽略先后；发现页「忽略更新」
	// 列表按此渲染。被忽略应用更新信号在目录层抑制（has_update 置假），
	// 更新角标/计数/自动更新/更新摘要通知全部自动排除。
	UpdateIgnored []string `json:"update_ignored,omitempty"`
	// 通知设置（0.6.121「通知设置」tab，形式对齐 fn-knock 事件中心：渠道/规则/记录）。
	// NotifyEnabled 总开关；指针 nil = 默认开（老配置无此字段时行为不变）。
	NotifyEnabled *bool `json:"notify_enabled,omitempty"`
	// 逐事件开关：key = 事件 key，false = 关闭；key 缺失 = 用目录缺省。
	NotifyEvents map[string]bool `json:"notify_events,omitempty"`
	// 通知渠道（外部推送：企微/钉钉/飞书/Server酱/PushPlus/Bark/通用 Webhook）。
	// 参数在 Params（0.6.220 起敏感字段加密落盘 enc:v1:；GET 返回时脱敏）。
	// 内存里始终是明文，加密只作用于写盘（见本文件顶部「敏感字段落盘加密」）。
	NotifyChannels []NotifyChannel `json:"notify_channels,omitempty"`
	// 通知详情页基础地址（0.6.144，卡片形式跳转用）：
	// 卡片消息的跳转链接 = {NotifyViewBase}/api/notify-view/{id}?t={token}。
	// 如 http://<NAS_IP>:8090（appcenter 网关）或 fn-knock 公网域名；
	// 空 = 未配置，卡片形式自动降级为默认 markdown。
	NotifyViewBase string `json:"notify_view_base,omitempty"`
	// 资源告警阈值：内存 MB（0 = 默认 256）；CPU 百分比（0 = 默认 80）。
	// 仅当事件 resource_mem_alert / resource_cpu_alert 开启时生效。
	NotifyMemAlertMB  int `json:"notify_mem_alert_mb,omitempty"`
	NotifyCPUPctAlert int `json:"notify_cpu_alert_pct,omitempty"`
	// 收藏应用「有更新」推送去重：app key → 已通知过的版本（防每轮重复推）。
	NotifyFavNotified map[string]string `json:"notify_fav_notified,omitempty"`
	// 关注源「新增应用」推送去重（0.6.143）：源名 → 已见过的应用 key 集合。
	// 首次关注记基线不推，之后每个 源×应用 只推一次。
	NotifyFavSourceSeen map[string]map[string]bool `json:"notify_fav_source_seen,omitempty"`
	// 关注源报表（0.6.190）：源名 → 上一轮应用集合（key → 「展示名（版本）」）。
	// 本轮集合与其比对 → 有新增/移除才推报表（总数 + 全清单 + 变化项）；
	// 首次关注记基线不推（与 NotifyFavSourceSeen 同款语义）。
	NotifyFavSourcePrev map[string]map[string]string `json:"notify_fav_source_prev,omitempty"`
	// 源同步轮次状态（D1/D2/E1 去重用；重启后沿用，防重复告警）。
	NotifyPrevFailed map[string]bool `json:"notify_prev_failed,omitempty"`
	NotifyPrevTotal  int             `json:"notify_prev_total,omitempty"`
	// 0.6.133：源同步摘要 / 应用更新摘要的去重指纹（内容不变不重复推）
	NotifyPrevSummaryFP string `json:"notify_prev_summary_fp,omitempty"`
	NotifyPrevUpdatesFP string `json:"notify_prev_updates_fp,omitempty"`
	// 通知记录：实际通知流水（含各渠道发送结果），上限 NotifyLogCap 条（先进先出）。
	NotifyLog []NotifyLogEntry `json:"notify_log,omitempty"`
	// 欢迎语首启标记（0.6.155）：非 0 = 安装后首次启动已自动推送过
	// 「欢迎使用Moo」（重启/升级不重复）；0 = 尚未发送。手动重发不受此字段影响。
	WelcomeSentAt int64 `json:"welcome_sent_at,omitempty"`
	// Dock 主导航排序（0.6.122）：4 个 tab key 的自定义顺序；空 = 默认顺序。
	// 非法值（非全排列）在后端解析时回退默认，不信任前端。
	DockOrder []string `json:"dock_order,omitempty"`
	// 设置页 tab 排序：6 个 tab key 的自定义顺序；同上。
	SettingsTabOrder []string `json:"settings_tab_order,omitempty"`
}

// 加速源自动测速间隔（0.6.148）。默认 5 分钟（与 0.6.62 起的行为一致）；
// 0h0m = 未设置 → 默认；越界或总分钟 <1 = 非法 → 默认；上限 24 小时。
const DefaultProbeIntervalMinutes = 5

func (c *Config) GhProbeIntervalMinutes() int {
	return probeIntervalMinutes(c.GhProbeHours, c.GhProbeMinutes)
}

func (c *Config) DkProbeIntervalMinutes() int {
	return probeIntervalMinutes(c.DkProbeHours, c.DkProbeMinutes)
}

func probeIntervalMinutes(h, m int) int {
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return DefaultProbeIntervalMinutes
	}
	mins := h*60 + m
	if mins < 1 {
		return DefaultProbeIntervalMinutes
	}
	if mins > 24*60 {
		mins = 24 * 60
	}
	return mins
}

// NotifyLogCap 通知记录条数上限（防 config.json 膨胀）。
const NotifyLogCap = 200

// NotifyLogEntry 一条通知记录（时间戳为 Unix 秒）。
type NotifyLogEntry struct {
	TS    int64  `json:"ts"`
	Event string `json:"event"` // 事件 key（见 api 包 notifyEventCatalog）
	Msg   string `json:"msg"`
	OK    bool   `json:"ok"`
	// Content 完整通知正文（0.6.143：应用内记录用，方格折叠卡展示；
	// 外部渠道发的可能是其紧凑版，两者可不同）。
	Content  string            `json:"content,omitempty"`
	Channels map[string]string `json:"channels,omitempty"` // 渠道名 → 错误信息；空 = 未发/全成功
	// ID/Token 详情页短链（0.6.144，卡片形式跳转用）：
	// {NotifyViewBase}/api/notify-view/{ID}?t={Token}，token 校验防伪。
	ID    string `json:"id,omitempty"`
	Token string `json:"token,omitempty"`
}

// NotifyChannel 一个外部推送渠道（0.6.121）。
type NotifyChannel struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"` // wecom|dingtalk|feishu|serverchan|pushplus|bark|webhook
	Name    string            `json:"name"`
	Enabled bool              `json:"enabled"`
	Params  map[string]string `json:"params,omitempty"` // 连接参数（webhook_url/sendkey/token/...）
	Timeout int               `json:"timeout,omitempty"` // 秒，0 = 默认 10
	// Format 消息形式（0.6.144；0.6.177 全部渠道生效，webhook 除外）：
	// ""/markdown = 默认；markdown_v2 = 表格/富文本形式（企微 markdown_v2 表格；
	// 钉钉/飞书不支持表格→列表呈现）；card = 卡片（企微 text_notice / 钉钉
	// actionCard / 飞书 interactive / Server酱询链接行 / PushPlus html链接 / Bark url
	// 点击跳转，需 NotifyViewBase 详情页地址，未配置时自动降级默认形式）。
	Format string `json:"format,omitempty"`
	// Verbosity 通知信息长度（0.6.175，全部渠道生效，与 Format 正交）：
	// ""/friendly = 友好（默认，现有行为：Top5/前 8 条等紧凑摘要）；
	// concise = 简洁（只发关键内容，如源同步摘要只发「应用源 N 个 | 应用 M 个」）；
	// full = 完整详细（全部信息不截断不隐藏；超渠道单条长度上限时按行截断并显式标注）。
	Verbosity string `json:"verbosity,omitempty"`
}

// IsNotifyEnabled 通知总开关（缺省开）。
func (c *Config) IsNotifyEnabled() bool {
	return c.NotifyEnabled == nil || *c.NotifyEnabled
}

// IsNotifyEventOn 单事件开关（缺省开；仅显式 false 为关）。
func (c *Config) IsNotifyEventOn(key string) bool {
	if c.NotifyEvents == nil {
		return true
	}
	on, ok := c.NotifyEvents[key]
	return !ok || on
}

// DefaultSourceListURL 内置的社区 FnDepot 应用源列表（每行一个 GitHub 仓库地址）。
const DefaultSourceListURL = "https://raw.githubusercontent.com/710850609/FnDepot/main/repo_list.txt"

// Default 返回默认配置（含默认社区源：首装即有可浏览目录）。
func Default() *Config {
	return &Config{
		WebPort:     "38100",
		DownloadDir: "downloads",
		// 0.6.247：首装实际填充内置默认源全集（见 main.go，156 源）；
		// 这两个种子仅作内置列表缺失时的兜底，地址必须带协议。
		Sources: []SourceRef{
			{Name: "Blue-Mink", URL: "https://github.com/Blue-Mink/FnDepot"},
			{Name: "ew", URL: "https://github.com/EWEDLCM/FnDepot"},
		},
	}
}

// Path 返回配置文件路径。
func Path(dataDir string) string {
	return filepath.Join(dataDir, "config.json")
}

// Load 从 dataDir 读取配置；不存在或损坏时返回默认配置。
func Load(dataDir string) (*Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(Path(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return Default(), nil // 配置损坏不致命，回退默认
	}
	openSecrets(cfg)
	return cfg, nil
}

// openSecrets 就地解密敏感字段（0.6.220）；解不开的按「未设置」处理并打
// 标记，绝不因此让启动失败——最坏情况是官方源不可用、需用户重填一次口令。
func openSecrets(cfg *Config) {
	migrationNeeded = false
	if codec == nil {
		return
	}
	if cfg.PanelPassword != "" {
		if secret.IsSealed(cfg.PanelPassword) {
			if plain, err := codec.Open(cfg.PanelPassword); err == nil {
				cfg.PanelPassword = plain
			} else {
				log.Printf("面板口令解密失败（密钥变化或密文损坏），已按未设置处理，请在设置页重新填写: %v", err)
				cfg.PanelPassword = ""
				cfg.SecretDecryptFailed = true
			}
		} else {
			migrationNeeded = true // 存量明文：下次 Save 自动改写为密文
		}
	}
	if extraCodec != nil {
		if found, err := extraCodec(cfg, false); err != nil {
			log.Printf("通知渠道凭据解密出错: %v", err)
		} else if found {
			migrationNeeded = true
		}
	}
}

// storageCopy 返回用于落盘的副本：敏感字段加密，其余原样；**内存实例不被改写**
// （面板客户端/通知发送始终拿到明文）。
func (c *Config) storageCopy() *Config {
	snap := *c
	if c.NotifyChannels != nil {
		snap.NotifyChannels = make([]NotifyChannel, len(c.NotifyChannels))
		copy(snap.NotifyChannels, c.NotifyChannels)
		for i := range snap.NotifyChannels {
			if c.NotifyChannels[i].Params == nil {
				continue
			}
			m := make(map[string]string, len(c.NotifyChannels[i].Params))
			for k, v := range c.NotifyChannels[i].Params {
				m[k] = v
			}
			snap.NotifyChannels[i].Params = m
		}
	}
	if codec != nil {
		if snap.PanelPassword != "" {
			if sealed, err := codec.Seal(snap.PanelPassword); err == nil {
				snap.PanelPassword = sealed
			} else {
				log.Printf("面板口令加密失败（将以明文落盘，请检查数据目录写入权限）: %v", err)
			}
		}
		if extraCodec != nil {
			if _, err := extraCodec(&snap, true); err != nil {
				log.Printf("通知渠道凭据加密出错: %v", err)
			}
		}
	}
	return &snap
}

// Save 将配置写回 dataDir（目录不存在时创建）；敏感字段在此加密（0.6.220）。
func (c *Config) Save(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c.storageCopy(), "", "  ")
	if err != nil {
		return err
	}
	p := Path(dataDir)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		return err
	}
	// 0.6.215 折中 P0：WriteFile 的 mode 仅对新建文件生效——显式 chmod 存量
	// 文件（config 含面板口令/企微 webhook key 等凭据，不依赖 umask/平台默认）。
	return os.Chmod(p, 0o600)
}

// DataDir 按优先级解析数据目录：MOO_DATA > TRIM_PKGVAR > ./moo-data。
func DataDir() string {
	if d := os.Getenv("MOO_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("TRIM_PKGVAR"); d != "" {
		return d
	}
	return "./moo-data"
}

// SocketPath 按优先级解析统一网关 socket 路径：MOO_SOCKET > TRIM_APPDEST/app.sock > ./app.sock。
func SocketPath() string {
	if s := os.Getenv("MOO_SOCKET"); s != "" {
		return s
	}
	if d := os.Getenv("TRIM_APPDEST"); d != "" {
		return filepath.Join(d, "app.sock")
	}
	return "app.sock"
}
