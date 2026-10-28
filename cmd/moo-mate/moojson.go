package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// appEntry 是生成 moo.json 时单个应用的条目（协议字段见 docs/Moo应用源协议.md）。
type appEntry struct {
	DisplayName string   `json:"display_name,omitempty"`
	Version     string   `json:"version,omitempty"`
	AppType     string   `json:"app_type,omitempty"`
	Platform    []string `json:"platform,omitempty"`
	Size        float64  `json:"size,omitempty"`       // MB
	SizeBytes   int64    `json:"size_bytes,omitempty"` // 精确字节
	Sha256      string   `json:"sha256,omitempty"`
	DownloadURL string   `json:"download_url"`
	IconURL     string   `json:"icon_url,omitempty"`
	Desc        string   `json:"desc,omitempty"`
	Author      string   `json:"author,omitempty"`
	AuthorURL   string   `json:"author_url,omitempty"`
	Distributor string   `json:"distributor,omitempty"`
	ServicePort string   `json:"service_port,omitempty"`
	InstallType string   `json:"install_type,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	BugReport   string   `json:"bug_report_url,omitempty"`
	Labels      string   `json:"labels,omitempty"`
}

// fpkMeta 从一份 FPK 字节流解析出的元数据（复用 gen-moo-json 同口径）。
type fpkMeta struct {
	Manifest map[string]string
	Sha256   string
	Size     int64
}

// inspectFPK 解析 FPK（tar.gz）内的 manifest，并计算 sha256 与体积。
// 逻辑与 tools/gen-moo-json 的 parseFPK 对齐（字节流版本，供伴侣复用）。
func inspectFPK(r io.Reader) (*fpkMeta, error) {
	h := sha256.New()
	tee := io.TeeReader(r, h)
	// 先完整读一遍算 sha256 与体积（需缓冲，因为要二次解 tar）。
	data, err := io.ReadAll(tee)
	if err != nil {
		return nil, fmt.Errorf("读取 FPK 失败: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("不是合法的 tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var manifest string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == "manifest" && hdr.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, err
			}
			manifest = string(b)
			break
		}
	}
	if strings.TrimSpace(manifest) == "" {
		return nil, fmt.Errorf("FPK 内没有 manifest")
	}
	return &fpkMeta{
		Manifest: parseManifest(manifest),
		Sha256:   hex.EncodeToString(h.Sum(nil)),
		Size:     int64(len(data)),
	}, nil
}

// parseManifest 解析 fnOS 应用清单（key = value 行，# 注释）。
func parseManifest(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		out[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
	}
	return out
}

// appType 从 manifest 推断应用类型（docker / fpk），与 gen-moo-json 一致。
func appType(kv map[string]string) string {
	for _, k := range []string{"isdocker", "is_docker", "docker"} {
		if v, ok := kv[k]; ok {
			if strings.EqualFold(v, "true") || v == "1" {
				return "docker"
			}
		}
	}
	if strings.Contains(kv["install_type"], "docker") || strings.Contains(kv["install_type"], "容器") {
		return "docker"
	}
	return "fpk"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '/' || r == ' '
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// metaToEntry 把 fpkMeta 转成 moo.json 的应用条目。
func metaToEntry(m *fpkMeta, downloadURL, defDistributor string) *appEntry {
	kv := m.Manifest
	appname := strings.TrimSpace(kv["appname"])
	ver := kv["version"]
	return &appEntry{
		DisplayName: firstNonEmpty(kv["display_name"], appname),
		Version:     ver,
		AppType:     appType(kv),
		Platform:    splitList(firstNonEmpty(kv["platform"], "x86")),
		Size:        round2(float64(m.Size) / (1024 * 1024)),
		SizeBytes:   m.Size,
		Sha256:      m.Sha256,
		DownloadURL: downloadURL,
		Desc:        kv["desc"],
		Author:      firstNonEmpty(kv["author"], kv["maintainer"]),
		AuthorURL:   firstNonEmpty(kv["author_url"], kv["maintainer_url"]),
		Distributor: firstNonEmpty(kv["distributor"], defDistributor),
		ServicePort: kv["service_port"],
		InstallType: kv["install_type"],
		Homepage:    kv["homepage"],
		BugReport:   kv["bug_report_url"],
		Labels:      firstNonEmpty(kv["labels"], kv["categories"]),
	}
}

// appPackage 单个架构的包（moo.json releases.<version>.packages.<arch> 条目）。
type appPackage struct {
	DownloadURL string `json:"download_url"`
	Sha256      string `json:"sha256,omitempty"`
	SizeBytes   int64  `json:"size,omitempty"` // 包级 size 协议单位 = 字节
}

// normArch 把平台串归一为架构键 x86 / arm / all；识别不出返回空串。
func normArch(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(l, "x86"), strings.Contains(l, "amd64"):
		return "x86"
	case strings.Contains(l, "arm"), strings.Contains(l, "aarch"):
		return "arm"
	case l == "all":
		return "all"
	}
	return ""
}

// archFromFilename 从 FPK 文件名识别架构（motrix_1.9.8_arm.fpk → arm）。
// 仅在 manifest 未声明 platform/arch 时兜底使用。
func archFromFilename(name string) string {
	l := strings.ToLower(name)
	for _, tok := range []string{"x86_64", "amd64", "x86-64", "x86", "aarch64", "arm64", "arm"} {
		if strings.Contains(l, tok) {
			if tok == "aarch64" || tok == "arm64" || tok == "arm" {
				return "arm"
			}
			return "x86"
		}
	}
	return ""
}

// archForFPK 确定某个 FPK 的架构：优先 manifest 的 platform/arch，回落文件名。
func archForFPK(kv map[string]string, filename string) string {
	for _, k := range []string{"platform", "arch"} {
		if v := kv[k]; v != "" {
			for _, part := range splitList(v) {
				if a := normArch(part); a != "" {
					return a
				}
			}
		}
	}
	return archFromFilename(filename)
}

// archKeys 返回 packages 的架构键按 x86 → arm → all → 其余字母序排序。
func archKeys(pkgs map[string]*appPackage) []string {
	keys := make([]string, 0, len(pkgs))
	for k := range pkgs {
		keys = append(keys, k)
	}
	rank := map[string]int{"x86": 0, "arm": 1, "all": 2}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, oki := rank[keys[i]]
		rj, okj := rank[keys[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}

// buildMooJSON 把应用条目打包成 moo.json（V2 包裹格式）。
// 多架构（Packages 内 >1 个架构）时补 releases.<version>.packages.<arch>，
// 顶层 download_url 保留为便捷链接（Moo 0.6.352 起会按本机架构覆盖选包）。
func buildMooJSON(name, distributor, homepage string, apps map[string]*indexedApp) ([]byte, error) {
	appDocs := make(map[string]any, len(apps))
	for appname, ia := range apps {
		// 扁平条目（含 omitempty）先 marshal 回 map，再按需补 releases。
		raw, err := json.Marshal(ia.appEntry)
		if err != nil {
			return nil, err
		}
		var em map[string]any
		if err := json.Unmarshal(raw, &em); err != nil {
			return nil, err
		}
		if len(ia.Packages) > 1 && ia.Version != "" {
			pkgs := make(map[string]any, len(ia.Packages))
			for _, arch := range archKeys(ia.Packages) {
				p := ia.Packages[arch]
				pkgs[arch] = map[string]any{
					"download_url": p.DownloadURL,
					"sha256":       p.Sha256,
					"size":         p.SizeBytes,
				}
			}
			em["releases"] = map[string]any{
				ia.Version: map[string]any{"packages": pkgs},
			}
		}
		appDocs[appname] = em
	}
	doc := map[string]any{
		"schema_version": "moo",
		"source_info": map[string]any{
			"name":         name,
			"distributor":  distributor,
			"homepage":     homepage,
			"generated_by": "moo-mate",
		},
		"apps": appDocs,
	}
	return json.MarshalIndent(doc, "", "  ")
}
