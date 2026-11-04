package main

// 0.2.38：应用图标补齐。
// 背景（用户实报「应用源列表里只有软仓有图标，其他都获取不了」）：
//   - 软仓 personal 源（/api/apps）自带 icon 字段 → 索引里有 icon_url → 有图；
//   - Gitee 软仓源与所有 GitHub/Gitee release 扫描路径**从不产生 icon_url**；
//   - 更早生成的索引条目同样没有图标。
// 方案（三层，成本从低到高）：
//  1. 构建索引时从 FPK 包内抽图标（下载 FPK 本来就为了解析 manifest，零额外流量）；
//  2. handleIcon 候选链：磁盘缓存 → 内存 → icon_url → 仓库根常见路径（ICON.PNG 等）；
//  3. 最后兜底：按 download_url 下包抽图标（≤20MB + 并发 2 + 失败负缓存 30 分钟）。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mateFpkIconMaxEntry  = 2 << 20   // 单个图标文件上限
	mateFpkIconMaxUnpack = 32 << 20  // 解压总量上限（防压缩炸弹）
	mateFpkIconMaxPkg    = 100 << 20 // 兜底上限（流式解析：命中图标即停；实测 QwenPaw 包 48MB）
	mateIconFailTTL      = 30 * time.Minute
)

// mateFpkIconSem 限制同时「为图标下包」的数量（前端列表会并发请求几十张图标）。
var mateFpkIconSem = make(chan struct{}, 2)

// 失败负缓存：appname → 失败时间（避免同一应用每轮都重下包）。
var (
	mateIconFailMu sync.Mutex
	mateIconFailAt = map[string]time.Time{}
)

func mateIconRecentlyFailed(appname string) bool {
	mateIconFailMu.Lock()
	defer mateIconFailMu.Unlock()
	at, ok := mateIconFailAt[appname]
	if !ok {
		return false
	}
	if time.Since(at) > mateIconFailTTL {
		delete(mateIconFailAt, appname)
		return false
	}
	return true
}

func markMateIconFailed(appname string) {
	mateIconFailMu.Lock()
	mateIconFailAt[appname] = time.Now()
	mateIconFailMu.Unlock()
}

// detectImageExt 按魔数识别图片类型（只认浏览器 <img> 安全的位图格式）。
func detectImageExt(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "jpg", true
	case len(b) >= 4 && string(b[:4]) == "GIF8":
		return "gif", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp", true
	}
	return "", false
}

// iconCacheName 生成安全且不碰撞的缓存文件名：appname 归一 + appname 哈希前缀。
func iconCacheName(appname string) string {
	sum := sha256.Sum256([]byte(appname))
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, appname)
	if len(safe) > 64 {
		safe = safe[:64]
	}
	if safe == "" {
		safe = "icon"
	}
	return safe + "-" + hex.EncodeToString(sum[:4])
}

// iconDir 图标磁盘缓存目录（PKGVAR/icons：不随数据目录切换，也不进备份）。
func (c *companion) iconDir() string {
	dir := strings.TrimSpace(c.bootDir)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "icons")
}

// loadIconFile 读磁盘缓存（png/jpg/gif/webp）。
func (c *companion) loadIconFile(appname string) ([]byte, string, bool) {
	base := filepath.Join(c.iconDir(), iconCacheName(appname))
	for _, ext := range []string{"png", "jpg", "gif", "webp"} {
		if b, err := os.ReadFile(base + "." + ext); err == nil && len(b) > 0 {
			return b, ext, true
		}
	}
	return nil, "", false
}

// saveIconFile 原子写磁盘缓存（写 .tmp 后 rename），同时更新内存缓存。
func (c *companion) saveIconFile(appname string, data []byte, ext string) {
	if len(data) == 0 || ext == "" {
		return
	}
	dir := c.iconDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	final := filepath.Join(dir, iconCacheName(appname)+"."+ext)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return
	}
	c.iconMu.Lock()
	c.icons[appname] = data
	c.iconMu.Unlock()
}

// mateIconCandidates 从源地址推导仓库根常见图标路径（对齐 Moo iconCandidates 口径）。
// 命中率最高的布局：<repo>/<appname>/ICON.PNG、<repo>/ICON.PNG、<repo>/<appname>/fnos/ICON_256.PNG。
func mateIconCandidates(appname, source string) []string {
	appname = strings.TrimSpace(appname)
	source = strings.TrimSpace(source)
	if appname == "" || source == "" {
		return nil
	}
	if owner, repo, ok := parseGHRepo(source); ok {
		var out []string
		for _, br := range []string{"main", "master"} {
			base := "https://raw.githubusercontent.com/" + owner + "/" + repo + "/" + br + "/"
			out = append(out,
				base+appname+"/fnos/ICON_256.PNG",
				base+appname+"/ICON_256.PNG",
				base+appname+"/ICON.PNG",
				base+appname+"/icon.png",
				base+"ICON.PNG",
				base+"ICON_256.PNG",
			)
		}
		return out
	}
	if owner, repo, ok := parseGiteeRepo(source); ok {
		var out []string
		for _, br := range []string{"master", "main"} {
			base := "https://gitee.com/" + owner + "/" + repo + "/raw/" + br + "/"
			out = append(out,
				base+appname+"/ICON_256.PNG",
				base+appname+"/ICON.PNG",
				base+appname+"/icon.png",
				base+"ICON.PNG",
				base+"ICON_256.PNG",
			)
		}
		return out
	}
	return nil
}

// fetchMateIcon 下载并校验一张图标（≤2MB，只认位图魔数）。
func (c *companion) fetchMateIcon(ctx context.Context, u string) ([]byte, string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("User-Agent", "Moo-Mate/"+buildVersion)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, mateFpkIconMaxEntry+1))
	if err != nil || len(b) == 0 || len(b) > mateFpkIconMaxEntry {
		return nil, "", false
	}
	ext, ok := detectImageExt(b)
	if !ok {
		return nil, "", false
	}
	return b, ext, true
}

// urlHostIsGitHub 判断地址是否 GitHub 系（镜像候选只对 GitHub 有意义）。
func urlHostIsGitHub(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.Contains(raw, "github")
	}
	return strings.Contains(u.Host, "github")
}

// fpkIconNameScore 常见图标文件名 → 优先级（越小越优先；解析不出尺寸时的兜底排序）。
func fpkIconNameScore(base string) (int, bool) {
	switch strings.ToUpper(base) {
	case "ICON_512.PNG":
		return 0, true
	case "ICON_256.PNG":
		return 1, true
	case "ICON.PNG":
		return 2, true
	case "ICON_128.PNG":
		return 3, true
	case "ICON_64.PNG":
		return 4, true
	case "ICON.JPG", "ICON.JPEG", "ICON.WEBP", "ICON.GIF":
		return 5, true
	}
	return 0, false
}

// iconPixelScore 解析图片尺寸返回面积（宽×高）；解析失败返回 0。
func iconPixelScore(b []byte, ext string) int {
	switch ext {
	case "png":
		if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return cfg.Width * cfg.Height
		}
	case "jpg":
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(b)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
			return cfg.Width * cfg.Height
		}
	}
	return 0
}

// considerFpkIcon 在候选中择优（面积大者优先；面积相同按文件名优先级）。
type fpkIconPick struct {
	data  []byte
	ext   string
	score int
	name  int
}

func (p *fpkIconPick) consider(name string, data []byte) {
	if len(data) == 0 || len(data) > mateFpkIconMaxEntry {
		return
	}
	ext, ok := detectImageExt(data)
	if !ok {
		return
	}
	nameScore, okName := fpkIconNameScore(filepath.Base(name))
	if !okName {
		nameScore = 9
	}
	score := iconPixelScore(data, ext)
	if p.data != nil {
		if score < p.score || (score == p.score && nameScore >= p.name) {
			return
		}
	}
	p.data, p.ext, p.score, p.name = data, ext, score, nameScore
}

// extractFPKIcon 从 FPK（tar.gz）里抽应用图标：先看包根/包内图标文件，
// 再看 app.tgz 内的 ui/images/*。返回 (字节, 扩展名, 找到)。
// 全部走内存字节（调用方已经为解析 manifest 读过整包），不额外下载。
func extractFPKIcon(data []byte) ([]byte, string, bool) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var pick fpkIconPick
	var appTgz []byte
	var unpacked int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF || err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "app.tgz" {
			if hdr.Size > 0 && hdr.Size <= 64<<20 {
				appTgz, _ = io.ReadAll(io.LimitReader(tr, 64<<20))
			}
			continue
		}
		if _, ok := fpkIconNameScore(filepath.Base(name)); !ok {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > mateFpkIconMaxEntry || unpacked > mateFpkIconMaxUnpack {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, mateFpkIconMaxEntry+1))
		if err != nil || len(b) > mateFpkIconMaxEntry {
			continue
		}
		unpacked += int64(len(b))
		pick.consider(name, b)
	}
	if pick.data == nil && len(appTgz) > 0 {
		pick = extractAppTgzIcon(appTgz)
	}
	if pick.data == nil {
		return nil, "", false
	}
	return pick.data, pick.ext, true
}

// extractAppTgzIcon 从 app.tgz 的 ui/images/* 里选图标（同样按面积择优）。
func extractAppTgzIcon(data []byte) fpkIconPick {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fpkIconPick{}
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var pick fpkIconPick
	var unpacked int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF || err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg || !strings.Contains(hdr.Name, "ui/images/") {
			continue
		}
		if _, ok := fpkIconNameScore(filepath.Base(hdr.Name)); !ok {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > mateFpkIconMaxEntry || unpacked > mateFpkIconMaxUnpack {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, mateFpkIconMaxEntry+1))
		if err != nil || len(b) > mateFpkIconMaxEntry {
			continue
		}
		unpacked += int64(len(b))
		pick.consider(hdr.Name, b)
	}
	return pick
}

// extractAppTgzIconStream 在 app.tgz（嵌套 gzip）流里找 ui/images 图标：命中即停。
// 实测（2026-11-04）：新版 FPK 常把 app.tgz 放在包根第一个条目、包根不放 ICON.PNG，
// 因此不能「遇到 app.tgz 就放弃」——必须进到 app.tgz 内找。
func extractAppTgzIconStream(r io.Reader) ([]byte, string, bool) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, "", false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var pick fpkIconPick
	for i := 0; i < 500; i++ {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg || !strings.Contains(hdr.Name, "ui/images/") {
			continue
		}
		if _, ok := fpkIconNameScore(filepath.Base(hdr.Name)); !ok {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > mateFpkIconMaxEntry {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, mateFpkIconMaxEntry+1))
		if err != nil || len(b) > mateFpkIconMaxEntry {
			continue
		}
		pick.consider(hdr.Name, b)
		if pick.data != nil && pick.score >= 256*256 {
			break
		}
	}
	if pick.data == nil {
		return nil, "", false
	}
	return pick.data, pick.ext, true
}

// extractFpkIconStream 从 HTTP 流里顺序解析 FPK 抽图标：命中即返回，不读整包。
// 顺序：包根图标（ICON.PNG / ICON_256.PNG …）→ 遇到 app.tgz 则进其内部找 ui/images。
func extractFpkIconStream(r io.Reader) ([]byte, string, bool) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, "", false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var pick fpkIconPick
	for i := 0; i < 400; i++ {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "app.tgz" {
			if data, ext, ok := extractAppTgzIconStream(io.LimitReader(tr, 64<<20)); ok {
				return data, ext, true
			}
			continue
		}
		if _, ok := fpkIconNameScore(filepath.Base(name)); !ok {
			continue
		}
		if hdr.Size <= 0 || hdr.Size > mateFpkIconMaxEntry {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, mateFpkIconMaxEntry+1))
		if err != nil || len(b) > mateFpkIconMaxEntry {
			continue
		}
		pick.consider(name, b)
		if pick.data != nil && pick.score >= 256*256 {
			break // 已拿到高清图标
		}
	}
	if pick.data == nil {
		return nil, "", false
	}
	return pick.data, pick.ext, true
}

// fetchIconFromFPKStream 依次尝试镜像候选 + 直连，流式解析抢图标。
func (c *companion) fetchIconFromFPKStream(ctx context.Context, raw string) ([]byte, string, bool) {
	for _, u := range mirrorCandidates(raw) {
		if ctx.Err() != nil {
			return nil, "", false
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("Accept", "application/octet-stream")
		if tok := c.githubToken(); tok != "" && strings.Contains(u, "api.github.com") {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.httpc.Do(req)
		if err != nil {
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			continue
		}
		data, ext, ok := extractFpkIconStream(resp.Body)
		_ = resp.Body.Close()
		cancel()
		if ok {
			return data, ext, true
		}
	}
	return nil, "", false
}

// fallbackIconFromFPK 最后兜底：按 download_url 下包抽包内图标。
// 门限与 Moo fpk_icon_326 一致（体积已知且 ≤20MB），并发 2，失败负缓存 30 分钟。
func (c *companion) fallbackIconFromFPK(ctx context.Context, ia *indexedApp) ([]byte, string, bool) {
	if ia == nil || strings.TrimSpace(ia.DownloadURL) == "" {
		return nil, "", false
	}
	if ia.SizeBytes <= 0 || ia.SizeBytes > mateFpkIconMaxPkg {
		return nil, "", false
	}
	if mateIconRecentlyFailed(ia.Appname) {
		return nil, "", false
	}
	select {
	case mateFpkIconSem <- struct{}{}:
		defer func() { <-mateFpkIconSem }()
	case <-ctx.Done():
		return nil, "", false
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	icon, ext, found := c.fetchIconFromFPKStream(dctx, ia.DownloadURL)
	if !found {
		markMateIconFailed(ia.Appname)
		return nil, "", false
	}
	return icon, ext, true
}
