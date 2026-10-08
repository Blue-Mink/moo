// Package reco 提供发现页「固定前三推荐」的完整元数据快照（0.6.314 C）。
//
// 用户定稿：发现页推荐前三固定位（上→下，不随机）：
//  1. fnos-apps-store（源 conversun/fnos-apps，源名 fnos-store）
//  2. fndepot（源 Blue-Mink/FnDepot fnpack.json，源名 Blue-Mink）
//  3. fn-knock（源 kci-lnk/fn-knock-turborepo，源名 kci-lnk）
//
// 源在目录里=实时数据；源被删/抓取失败=回落内嵌完整元数据快照
// （全部展示字段 + README 全文 + 完整 releases 明细 + 图标二进制 base64）；
// 源恢复后自动切回实时（调用方每请求按当前目录判定，本包无状态、不粘性）。
//
// 快照文件 snapshots.json 由仓库根 gen_reco_snapshots.py 在构建期抓取生成
// （build.sh Step 1.5；失败时保留仓库已有文件，不阻塞构建）。
package reco

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"log"
	"strings"
	"sync"
)

//go:embed snapshots.json
var snapshotsJSON []byte

// SnapshotPackage 快照里单个安装包（架构 → 直链/校验和/体积）。
type SnapshotPackage struct {
	Arch        string `json:"arch"`
	DownloadURL string `json:"download_url"`
	Sha256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// SnapshotRelease 快照里一个版本（完整 releases 明细，版本降序）。
type SnapshotRelease struct {
	Version   string            `json:"version"`
	Changelog string            `json:"changelog,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
	Packages  []SnapshotPackage `json:"packages"`
}

// Snapshot 单个固定推荐应用的完整元数据快照（字段与实时条目对齐）。
type Snapshot struct {
	AppName        string            `json:"appname"`
	Source         string            `json:"source"`
	SourceURL      string            `json:"source_url"`
	Key            string            `json:"key"`
	DisplayName    string            `json:"display_name"`
	Desc           string            `json:"desc"`
	DescHTML       string            `json:"desc_html,omitempty"`
	Author         string            `json:"author,omitempty"`
	AuthorURL      string            `json:"author_url,omitempty"`
	Distributor    string            `json:"distributor,omitempty"`
	DistributorURL string            `json:"distributor_url,omitempty"`
	Homepage       string            `json:"homepage,omitempty"`
	BugReportURL   string            `json:"bug_report_url,omitempty"`
	License        string            `json:"license,omitempty"`
	MinFnos        string            `json:"min_fnos,omitempty"`
	Labels         string            `json:"labels,omitempty"`
	Platforms      []string          `json:"platforms,omitempty"`
	AppType        string            `json:"app_type,omitempty"`
	InstallType    string            `json:"install_type,omitempty"`
	ServicePort    int               `json:"service_port,omitempty"`
	UpdatedAt      string            `json:"updated_at,omitempty"`
	FirstReleaseAt string            `json:"first_release_at,omitempty"`
	DownloadCount  int               `json:"download_count,omitempty"`
	IconURL        string            `json:"icon_url,omitempty"`
	IconB64        string            `json:"icon_b64,omitempty"`
	Readme         string            `json:"readme,omitempty"`
	ReadmeURL      string            `json:"readme_url,omitempty"`
	PreviewURLs    []string          `json:"preview_urls,omitempty"`
	Releases       []SnapshotRelease `json:"releases"`
}

// doc 是 snapshots.json 的顶层结构（generated_at=抓取时间戳，文件首字段）。
type doc struct {
	GeneratedAt string     `json:"generated_at"`
	Generator   string     `json:"generator"`
	Note        string     `json:"note"`
	Apps        []Snapshot `json:"apps"`
}

var (
	loadOnce sync.Once
	byNorm   map[string]*Snapshot
	order    []string // 固定顺序 appname（文件序=用户定稿序）
)

// NormAppName 应用名归一（小写、只留字母数字）——与目录层大小写/分隔符
// 漂移匹配同一规则（buildCatalog 的 normName 闭包同款）。
func NormAppName(s string) string {
	return normAppName(s)
}

func normAppName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func load() {
	var d doc
	if err := json.Unmarshal(snapshotsJSON, &d); err != nil {
		log.Printf("[reco] 快照解析失败（固定推荐将无回落能力）: %v", err)
		return
	}
	byNorm = make(map[string]*Snapshot, len(d.Apps))
	for i := range d.Apps {
		a := &d.Apps[i]
		byNorm[normAppName(a.AppName)] = a
		order = append(order, a.AppName)
	}
	if len(order) != 0 {
		log.Printf("[reco] 固定推荐快照已载入: %s（%d 应用，生成于 %s）",
			strings.Join(order, " / "), len(order), d.GeneratedAt)
	}
}

// FixedOrder 返回固定前三推荐的 appname（上→下，用户定稿，不随机）。
// 快照文件损坏时返回 nil（调用方退化为纯随机推荐，不阻塞）。
func FixedOrder() []string {
	loadOnce.Do(load)
	return order
}

// Lookup 按 appname 查快照（大小写/分隔符漂移不敏感）。未收录返回 nil。
func Lookup(appName string) *Snapshot {
	loadOnce.Do(load)
	return byNorm[normAppName(appName)]
}

// Icon 解码内嵌图标二进制（PNG/GIF）。ok=false 表示无内嵌图标。
func (s *Snapshot) Icon() (data []byte, ctype string, ok bool) {
	if s == nil || s.IconB64 == "" {
		return nil, "", false
	}
	b, err := base64.StdEncoding.DecodeString(s.IconB64)
	if err != nil || len(b) < 8 {
		return nil, "", false
	}
	ctype = "image/png"
	if bytes.HasPrefix(b, []byte("GIF")) {
		ctype = "image/gif"
	}
	return b, ctype, true
}

// ReadmeText 返回内嵌 README 全文（markdown）。
func (s *Snapshot) ReadmeText() string {
	if s == nil {
		return ""
	}
	return s.Readme
}
