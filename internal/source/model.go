// Package source 定义统一应用模型与各平台源适配器。
package source

// App 是跨源统一的应用模型（字段与 FnDepot 扁平 fnpack.json 对齐，
// 其他源适配器负责映射进来）。
type App struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	DisplayName  string `json:"display_name"`
	Desc         string `json:"desc"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	Labels       string `json:"labels"`
	IconURL      string `json:"icon_url"`
	HomePage     string `json:"homepage"`
	ReadmeURL    string `json:"readme_url"`
	BugReportURL string `json:"bug_report_url"`
	Author       string `json:"author"`
	AuthorURL    string `json:"author_url"`
	Distributor  string `json:"distributor"`
	InstallType  string `json:"install_type"`
	SizeMB       string `json:"size"`
	IsDocker     string `json:"isdocker"`
	ServicePort  string `json:"service_port"`
	DownloadURL  string `json:"download_url"`
	Changelog    string `json:"changelog"`
	UpdatedAt    string `json:"updated_at"`
	// DownloadCount 源提供的全局下载量（第三方源官方规范不统计，0=未提供）。
	DownloadCount int `json:"download_count,omitempty"`
	// Sha256 来自 releases/arch_diff 条目（详情页展示）。
	Sha256 string `json:"sha256,omitempty"`
	// SizeBytes 包体积（字节，releases 条目提供；0=未提供，回退 SizeMB 解析）。
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// Arch 当前选中安装包的架构（x86/arm/all；0.6.303：源未提供时为空）。
	Arch string `json:"arch,omitempty"`
	// Archs 源提供的全部架构（展示序 x86→arm→all）。
	Archs []string `json:"archs,omitempty"`
	// ReleaseChangelogs 来自 fnpack.json 的 releases 字段（版本 → 该版本说明），
	// 比顶层 changelog 拼接串更干净；仅详情用，不进列表载荷。
	ReleaseChangelogs map[string]string `json:"-"`
	// FirstReleaseAt 该应用在源里的**最早发布时间**（moo.json 扩展字段
	// first_release_at；详情页「最早发布」行用它）。
	FirstReleaseAt string `json:"first_release_at,omitempty"`
	// PreviewURLs 来自 fnpack.json 的 preview_urls 字段（详情页预览图灯箱）。
	// 社区源多数不声明（对齐 New Store 行为：有则显示，无则隐藏该区）。
	PreviewURLs []string `json:"-"`
	// ── moo.json 扩展字段（0.6.269 起实现，见 docs/MOO-PROTOCOL.md §4.2/§4.5）──
	// DescHTML 富文本简介（详情页展示，Moo 消毒后渲染；缺省用纯文本 Desc）。
	DescHTML string `json:"desc_html,omitempty"`
	// License 许可协议（如 MIT），详情页展示。
	License string `json:"license,omitempty"`
	// MinFnos 最低 fnOS 版本（详情页提示；安装期由平台按应用做版本匹配校验）。
	MinFnos string `json:"min_fnos,omitempty"`
	// Wizard 源声明的安装向导参数（示例 7）：安装时向用户收集，
	// 键名由应用自身定义，值经 ?wizard= 传入安装管线。
	Wizard *SourceWizard `json:"wizard,omitempty"`
}

// SourceWizard moo.json 里声明的安装向导（见 docs/MOO-PROTOCOL.md 示例 7）。
type SourceWizard struct {
	Fields []SourceWizardField `json:"fields,omitempty"`
}

// SourceWizardField 单个向导字段：key 为应用约定的参数名，label 为显示名，
// default 为缺省值，required 为必填（空值时前端拦截提交）。
type SourceWizardField struct {
	Key      string `json:"key"`
	Label    string `json:"label,omitempty"`
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// Source 是源适配器的统一接口。
type Source interface {
	// Name 返回源显示名。
	Name() string
	// Fetch 拉取该源的完整应用目录。
	Fetch() (map[string]*App, error)
}
