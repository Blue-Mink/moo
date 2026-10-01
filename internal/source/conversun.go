package source

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"moo/internal/config"
)

// conversun 源：conversun/fnos-apps —— conversun 商店（fnos-apps-store /
// conversun/fnos-store，内置端口 8011）的唯一内置目录：
//   - apps.json 单文件索引（160+ 应用，schema_version/apps 包裹结构）
//   - FPK 在同仓库 releases：
//     https://github.com/conversun/fnos-apps/releases/download/<release_tag>/<file_prefix>_<fpk_version>_<arch>.fpk
//
// 可被识别为源的地址形态（任一）：
//   - 仓库地址：https://github.com/conversun/fnos-apps（带不带 .git / 子路径均可）
//   - apps.json 直链：任何主机/镜像前缀下 .../conversun/fnos-apps/<path>/apps.json
//
// 下载链接保持 github.com 纯直链，实际下载走 Moo 的 GitHub 镜像竞速
// （ghOrderedKeys），与其他社区源一致。
const (
	conversunRepoOwner     = "conversun"
	conversunRepoName      = "fnos-apps"
	conversunReleaseBase   = "https://github.com/conversun/fnos-apps/releases/download"
	conversunRawRepo       = "https://raw.githubusercontent.com/conversun/fnos-apps"
)

// Conversun 是 conversun/fnos-apps 源适配器（apps.json 格式，区别于 FnDepot 的 fnpack.json）。
type Conversun struct {
	name string
}

// NewConversun 创建 conversun/fnos-apps 源适配器。
func NewConversun(name string) *Conversun {
	return &Conversun{name: name}
}

func (c *Conversun) Name() string { return c.name }

// IsConversunURL 判断用户给的源地址是否指向 conversun/fnos-apps 目录
// （仓库形态或 apps.json 直链，含镜像前缀）。
// 注意与 conversun/fnos-apps-store（商店代码仓库）做边界区分，避免误匹配。
func IsConversunURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimRight(u, "/")
	if u == "" {
		return false
	}
	const repo = "conversun/fnos-apps"
	i := strings.Index(u, repo)
	if i < 0 {
		return false
	}
	rest := u[i+len(repo):]
	switch {
	case rest == "" || rest == ".git": // 仓库根（含镜像前缀）
		return true
	case strings.HasPrefix(rest, "/"):
		// 仓库子路径（/tree/...、/blob/...）视为仓库形态；
		// 直链形态仅认 apps.json（releases 下的 FPK 链接不是源）。
		return strings.HasSuffix(rest, "apps.json") ||
			strings.HasPrefix(rest, "/tree/") ||
			strings.HasPrefix(rest, "/blob/")
	case strings.HasPrefix(rest, "@"):
		// jsDelivr 形态：/gh/conversun/fnos-apps@<ref>/apps.json
		return strings.HasSuffix(rest, "/apps.json")
	}
	return false
}

// candidateURLs 展开 apps.json 候选链接，优先级与 FnDepot 一致：
// raw main/master 一级 → 配置的 GitHub 镜像前缀一级 → jsDelivr 二级兜底。
func (c *Conversun) candidateURLs() (primary, fallback []string) {
	for _, branch := range []string{"main", "master"} {
		primary = append(primary, fmt.Sprintf("%s/%s/apps.json", conversunRawRepo, branch))
	}
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL == "" { // auto = 非直连项
			continue
		}
		m := strings.TrimRight(opt.URL, "/")
		for _, branch := range []string{"main", "master"} {
			primary = append(primary, fmt.Sprintf("%s/%s/%s/apps.json", m, conversunRawRepo, branch))
		}
	}
	fallback = append(fallback, fmt.Sprintf("https://cdn.jsdelivr.net/gh/%s/%s/apps.json",
		conversunRepoOwner, conversunRepoName))
	return primary, fallback
}

type conversunPayload struct {
	// schema_version 为数字（如 1），source 为对象 {name,url}——宽松类型防上游改形态。
	SchemaVersion json.Number     `json:"schema_version"`
	Source        json.RawMessage `json:"source"`
	Apps          []conversunApp  `json:"apps"`
}

type conversunApp struct {
	Slug            string   `json:"slug"`
	AppName         string   `json:"appname"`
	FilePrefix      string   `json:"file_prefix"`
	DisplayName     string   `json:"display_name"`
	Description     string   `json:"description"`
	Version         string   `json:"version"`
	FpkVersion      string   `json:"fpk_version"`
	ReleaseTag      string   `json:"release_tag"`
	ServicePort     int      `json:"service_port"`
	HomepageURL     string   `json:"homepage_url"`
	IconURL         string   `json:"icon_url"`
	Platforms       []string `json:"platforms"`
	UpdatedAt       string   `json:"updated_at"`
	DownloadCount   int      `json:"download_count"`
	AppType         string   `json:"app_type"`
	Category        string   `json:"category"`
	PostInstallNote string   `json:"post_install_note"`
}

// Fetch 拉取并解析 conversun 应用目录。
func (c *Conversun) Fetch() (map[string]*App, error) {
	primary, fallback := c.candidateURLs()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	data, _, err := raceFetch(primary, ctx)
	if err != nil {
		data, _, err = raceFetch(fallback, ctx)
	}
	if err != nil {
		return nil, err
	}
	var doc conversunPayload
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("解析 conversun apps.json: %w", err)
	}
	if len(doc.Apps) == 0 {
		return nil, fmt.Errorf("conversun apps.json 的 apps 为空")
	}
	out := make(map[string]*App, len(doc.Apps))
	for _, it := range doc.Apps {
		if a := translateConversun(it, c.name); a != nil {
			out[a.Name] = a
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("conversun apps.json 无可用应用（架构不匹配或字段缺失）")
	}
	return out, nil
}

// translateConversun 把 apps.json 一条记录转成统一 App（纯函数，便于单测）。
// 跳过条件：appname/slug 均为空、不支持当前架构、缺 FPK 三要素
// （release_tag/file_prefix/fpk_version）。
func translateConversun(it conversunApp, srcName string) *App {
	name := it.AppName
	if name == "" {
		name = it.Slug
	}
	if name == "" || !supportsArch(it.Platforms) {
		return nil
	}
	if it.ReleaseTag == "" || it.FilePrefix == "" || it.FpkVersion == "" {
		return nil
	}
	a := &App{
		Name:          name,
		Source:        srcName,
		DisplayName:   it.DisplayName,
		Desc:          it.Description,
		Version:       it.Version,
		Platform:      strings.Join(it.Platforms, ","),
		Labels:        it.Category,
		IconURL:       it.IconURL,
		HomePage:      it.HomepageURL,
		Author:        "conversun",
		AuthorURL:     "https://github.com/conversun",
		Distributor:   "conversun",
		InstallType:   it.AppType,
		DownloadURL:   fmt.Sprintf("%s/%s/%s_%s_%s.fpk", conversunReleaseBase, it.ReleaseTag, it.FilePrefix, it.FpkVersion, archOfPlatforms(it.Platforms)),
		UpdatedAt:     it.UpdatedAt,
		DownloadCount: it.DownloadCount,
	}
	if a.DisplayName == "" {
		a.DisplayName = name
	}
	if it.ServicePort > 0 {
		a.ServicePort = strconv.Itoa(it.ServicePort)
	}
	if it.AppType == "docker" {
		a.IsDocker = "1"
	}
	return a
}

// supportsArch 判断应用 platforms 是否覆盖当前架构（空 = 全架构可用）。
func supportsArch(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, p := range platforms {
		if p == currentArch() || p == "all" {
			return true
		}
	}
	return false
}

// archOfPlatforms 取当前架构的包架构 token（x86/arm，platforms 声明 all 时
// 也用当前架构名——conversun 的 release 文件按实际架构命名）；都不匹配时
// 回退第一个声明值。
func archOfPlatforms(platforms []string) string {
	if len(platforms) == 0 {
		return currentArch()
	}
	for _, p := range platforms {
		if p == currentArch() || p == "all" {
			return currentArch()
		}
	}
	return platforms[0]
}
