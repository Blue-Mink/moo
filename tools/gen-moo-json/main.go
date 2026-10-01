// Command gen-moo-json 从一堆 FPK 生成 Moo 应用源索引 `moo.json`（协议见
// docs/SOURCE-PROTOCOL.md）。
//
// 用法：
//
//	cd moo && go run ./tools/gen-moo-json \
//	  -dir /path/to/fpk-store \
//	  -name "Blue-Mink 应用仓" -distributor "Blue-Mink" \
//	  -base-url https://example.com/apps \
//	  -out moo.json
//
// 行为：
//   - 递归扫描 -dir 下的 *.fpk；
//   - 逐个读取 FPK 内的 `manifest`（fnOS 应用清单，`key = value` 行）取
//     appname/version/display_name/desc/author/distributor/platform/…；
//   - 计算 sha256 与体积（字节）——Moo 下载后会校验；
//   - 同目录（FPK 旁）存在 ICON.PNG / ICON_256.PNG 时自动带上 icon_url；
//   - 输出 V2 包裹格式（schema_version=moo + source_info + apps）。
//
// 设计上刻意只用标准库：任何装了 Go 的机器都能直接 `go run`，不需要额外依赖。
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type appEntry struct {
	DisplayName   string   `json:"display_name,omitempty"`
	Version       string   `json:"version,omitempty"`
	AppType       string   `json:"app_type,omitempty"` // fpk | docker | native
	Platform      []string `json:"platform,omitempty"`
	Size          float64  `json:"size,omitempty"`       // MB（兼容 FnDepot 的 size 语义）
	SizeBytes     int64    `json:"size_bytes,omitempty"` // 精确字节数
	Sha256        string   `json:"sha256,omitempty"`
	DownloadURL   string   `json:"download_url"`
	IconURL       string   `json:"icon_url,omitempty"`
	Desc          string   `json:"desc,omitempty"`
	Author        string   `json:"author,omitempty"`
	AuthorURL     string   `json:"author_url,omitempty"`
	Distributor   string   `json:"distributor,omitempty"`
	ServicePort   string   `json:"service_port,omitempty"`
	InstallType   string   `json:"install_type,omitempty"`
	Homepage      string   `json:"homepage,omitempty"`
	BugReportURL  string   `json:"bug_report_url,omitempty"`
	Labels        string   `json:"labels,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	SourceFPKPath string   `json:"-"`
}

func main() {
	dir := flag.String("dir", ".", "扫描目录（递归找 *.fpk）")
	name := flag.String("name", "", "源名称（source_info.name）")
	distributor := flag.String("distributor", "", "分发者（source_info.distributor，同时作为缺省 distributor）")
	homepage := flag.String("homepage", "", "源主页（source_info.homepage）")
	baseURL := flag.String("base-url", "", "生成 download_url/icon_url 的前缀；留空则写相对路径")
	out := flag.String("out", "moo.json", "输出文件")
	flag.Parse()

	apps := map[string]*appEntry{}
	var scanned, skipped int
	err := filepath.WalkDir(*dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".fpk") {
			return nil
		}
		scanned++
		entry, appname, err := parseFPK(path, *dir, *baseURL, *distributor)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [跳过] %s: %v\n", path, err)
			skipped++
			return nil
		}
		if prev, ok := apps[appname]; ok && prev.Version != "" && entry.Version != "" && !versionGreater(entry.Version, prev.Version) {
			fmt.Fprintf(os.Stderr, "  [跳过] %s: 已有更新版本 %s（本包 %s）\n", path, prev.Version, entry.Version)
			skipped++
			return nil
		}
		apps[appname] = entry
		fmt.Printf("  [收录] %-24s %-10s %8.2f MB  %s\n", appname, entry.Version, entry.Size, entry.Sha256[:12])
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "扫描失败:", err)
		os.Exit(1)
	}

	doc := map[string]any{
		"schema_version": "moo",
		"source_info": map[string]any{
			"name":        *name,
			"homepage":    *homepage,
			"distributor": *distributor,
			"generated_by": "gen-moo-json",
			"updated_at":  time.Now().Format(time.RFC3339),
		},
		"apps": apps,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "序列化失败:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		os.Exit(1)
	}
	names := make([]string, 0, len(apps))
	for k := range apps {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Printf("\n扫描 %d 个 FPK，收录 %d 个应用（跳过 %d），输出 %s\n", scanned, len(apps), skipped, *out)
	if len(names) > 0 {
		fmt.Printf("应用：%s\n", strings.Join(names, ", "))
	}
}

// parseFPK 读 FPK（tar.gz）内的 manifest 抽取元数据，并计算 sha256/体积。
func parseFPK(path, root, baseURL, defDistributor string) (*appEntry, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return nil, "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, "", fmt.Errorf("不是合法的 tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	manifest := ""
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		base := filepath.Base(hdr.Name)
		if base == "manifest" && hdr.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return nil, "", err
			}
			manifest = string(b)
			break
		}
	}
	if strings.TrimSpace(manifest) == "" {
		return nil, "", fmt.Errorf("FPK 内没有 manifest")
	}
	kv := parseManifest(manifest)
	appname := strings.TrimSpace(kv["appname"])
	if appname == "" {
		return nil, "", fmt.Errorf("manifest 缺少 appname")
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)
	url := rel
	if baseURL != "" {
		url = strings.TrimRight(baseURL, "/") + "/" + rel
	}

	e := &appEntry{
		DisplayName:   firstNonEmpty(kv["display_name"], appname),
		Version:       kv["version"],
		AppType:       appType(kv),
		Platform:      splitList(firstNonEmpty(kv["platform"], "x86")),
		Size:          round2(float64(size) / (1024 * 1024)),
		SizeBytes:     size,
		Sha256:        hex.EncodeToString(h.Sum(nil)),
		DownloadURL:   url,
		Desc:          kv["desc"],
		Author:        firstNonEmpty(kv["author"], kv["maintainer"]),
		AuthorURL:     firstNonEmpty(kv["author_url"], kv["maintainer_url"]),
		Distributor:   firstNonEmpty(kv["distributor"], defDistributor),
		ServicePort:   kv["service_port"],
		InstallType:   kv["install_type"],
		Homepage:      kv["homepage"],
		BugReportURL:  kv["bug_report_url"],
		Labels:        firstNonEmpty(kv["labels"], kv["categories"]),
		UpdatedAt:     time.Now().Format("2006-01-02"),
		SourceFPKPath: rel,
	}
	// 图标：FPK 同目录若有 ICON.PNG / ICON_256.PNG 就带上（FnDepot 仓库惯例）
	dir := filepath.Dir(path)
	for _, icon := range []string{"ICON.PNG", "ICON_256.PNG", "icon.png"} {
		if _, err := os.Stat(filepath.Join(dir, icon)); err == nil {
			iconRel := filepath.ToSlash(filepath.Join(filepath.Dir(rel), icon))
			if baseURL != "" {
				e.IconURL = strings.TrimRight(baseURL, "/") + "/" + iconRel
			} else {
				e.IconURL = iconRel
			}
			break
		}
	}
	return e, appname, nil
}

// parseManifest 解析 fnOS 应用清单（`key = value` 行，# 注释）。
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
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		out[k] = v
	}
	return out
}

// appType 从 manifest 推断 Moo 的应用类型（docker / fpk）。
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
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '/' || r == ' ' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}

// versionGreater 数值分段比较版本（"0.10.0" > "0.9.0"；非数字后缀按整段字典序兜底）。
func versionGreater(a, b string) bool {
	as := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	bs := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := parseIntPrefix(av)
		bn, berr := parseIntPrefix(bv)
		if aerr == nil && berr == nil {
			if an != bn {
				return an > bn
			}
			continue
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

// parseIntPrefix 取字符串开头的数字段（"3-rc1" → 3）；无数字则报错。
func parseIntPrefix(s string) (int, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("非数字")
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, nil
}
