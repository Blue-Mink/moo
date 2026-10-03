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
	// ReleaseChangelogs 来自 fnpack.json 的 releases 字段（版本 → 该版本说明），
	// 比顶层 changelog 拼接串更干净；仅详情用，不进列表载荷。
	ReleaseChangelogs map[string]string `json:"-"`
	// FirstReleaseAt 该应用在源里的**最早发布时间**（moo.json 扩展字段
	// first_release_at；详情页「最早发布」行用它）。
	FirstReleaseAt string `json:"first_release_at,omitempty"`
	// PreviewURLs 来自 fnpack.json 的 preview_urls 字段（详情页预览图灯箱）。
	// 社区源多数不声明（对齐 New Store 行为：有则显示，无则隐藏该区）。
	PreviewURLs []string `json:"-"`
}

// Source 是源适配器的统一接口。
type Source interface {
	// Name 返回源显示名。
	Name() string
	// Fetch 拉取该源的完整应用目录。
	Fetch() (map[string]*App, error)
}
