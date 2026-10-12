package api

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pathEntry 按文件路径缓存的 manifest 解析结果（以 mtime 判定失效）。
type pathEntry struct {
	mod  time.Time
	info localFpkInfo
	ok   bool
}

// localFpkInfo 本地 FPK 包 manifest 读出的元数据。
type localFpkInfo struct {
	AppName        string
	DisplayName    string
	Desc           string
	Version        string
	Maintainer     string
	MaintainerURL  string
	Distributor    string
	DistributorURL string
}

// fpkLocal 读取本机 FPK 包缓存目录里各应用的 manifest，
// 为不属于任何源的已装应用（本地安装 / 第三方 FPK）提供
// 简介/维护者/发布者的本地兜底——不依赖源、不依赖网络。
// 包目录：<vol>/1000/fpk（用户放置）与 <vol>/1000-<uid>-<dbid>/fpk（面板托管）。
//
// 两级策略：
//  1. 全量索引：后台并发解析目录下所有 FPK 的 manifest，
//     按包内 appname 建索引（覆盖文件名与 appname 不一致的包，
//     如 istoreos.fpk → com.istoreos.vm）；
//  2. 按应用惰性解析：索引未就绪时，先解析文件名含该应用名的候选包。
type fpkLocal struct {
	mu       sync.Mutex
	files    []string
	listedAt time.Time
	index    map[string]localFpkInfo // 归一化 appname → manifest（全量扫描，版本最高者）
	cache    map[string]localFpkInfo // 归一化 appname → manifest（按应用惰性解析）
	parsing  map[string]bool         // 惰性解析进行中标记
	missed   map[string]time.Time    // 未命中记录（目录刷新周期内不重试）
	scanning bool
	scanFor  time.Time          // 索引对应的目录快照时间
	byPath   map[string]pathEntry // 文件路径 → manifest（按 mtime 失效；DisplayFor/NotePath 用）
	parsingPath map[string]bool   // 路径级后台解析进行中标记（防并发重复解析同一文件）
}

var fpkLocalInstance = &fpkLocal{
	index:       map[string]localFpkInfo{},
	cache:       map[string]localFpkInfo{},
	parsing:     map[string]bool{},
	missed:      map[string]time.Time{},
	byPath:      map[string]pathEntry{},
	parsingPath: map[string]bool{},
}

const fpkDirListTTL = 10 * time.Minute

var (
	extraDirsMu sync.Mutex
	extraDirs   []string // 额外 FPK 目录（Moo 下载缓存目录，运行时可切换）
)

// SetExtraDirs 注册额外的 FPK 目录（幂等；目录变化时失效列表快照，
// 供下一次 EnsureIndex 重新扫描）。
func (f *fpkLocal) SetExtraDirs(dirs []string) {
	extraDirsMu.Lock()
	same := len(extraDirs) == len(dirs)
	if same {
		for i := range dirs {
			if dirs[i] != extraDirs[i] {
				same = false
				break
			}
		}
	}
	if !same {
		extraDirs = append([]string(nil), dirs...)
	}
	extraDirsMu.Unlock()
	if !same {
		f.mu.Lock()
		f.listedAt = time.Time{}
		f.mu.Unlock()
	}
}

// fpkStorageDirs 扫描各卷的 FPK 存放目录 + 运行时注册的额外目录。
func fpkStorageDirs() []string {
	var dirs []string
	for _, pat := range []string{"/vol*/1000/fpk", "/vol*/1000-*/fpk"} {
		ms, _ := filepath.Glob(pat)
		for _, m := range ms {
			if st, err := os.Stat(m); err == nil && st.IsDir() {
				dirs = append(dirs, m)
			}
		}
	}
	extraDirsMu.Lock()
	extra := append([]string(nil), extraDirs...)
	extraDirsMu.Unlock()
	for _, m := range extra {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			dirs = append(dirs, m)
		}
	}
	return dirs
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

// listFiles 刷新包列表（TTL 缓存）。
func (f *fpkLocal) listFiles() {
	f.mu.Lock()
	if !f.listedAt.IsZero() && time.Since(f.listedAt) < fpkDirListTTL {
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()

	var files []string
	for _, d := range fpkStorageDirs() {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".fpk") {
				continue
			}
			files = append(files, filepath.Join(d, e.Name()))
		}
	}
	f.mu.Lock()
	f.files = files
	f.listedAt = time.Now()
	for k, t := range f.missed {
		if time.Since(t) > fpkDirListTTL {
			delete(f.missed, k)
		}
	}
	f.mu.Unlock()
}

// parseManifestText 解析 fnpack manifest（key = value 行格式）。
func parseManifestText(data []byte) localFpkInfo {
	info := localFpkInfo{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "appname":
			info.AppName = v
		case "display_name":
			info.DisplayName = v
		case "desc", "description":
			info.Desc = v
		case "version":
			info.Version = v
		case "maintainer":
			info.Maintainer = v
		case "maintainer_url":
			info.MaintainerURL = v
		case "distributor":
			info.Distributor = v
		case "distributor_url":
			info.DistributorURL = v
		}
	}
	return info
}

// readFpkManifestEntry 从 FPK 归档（tar.gz 或 zip）里流式读取 manifest 条目。
func readFpkManifestEntry(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()

	// 优先按 tar.gz（fnpack 标准格式）
	fh.Seek(0, io.SeekStart)
	gr, gerr := gzip.NewReader(fh)
	if gerr == nil {
		tr := tar.NewReader(gr)
		for {
			hdr, terr := tr.Next()
			if terr != nil {
				break
			}
			if hdr.Typeflag == tar.TypeReg &&
				(hdr.Name == "manifest" || strings.HasSuffix(hdr.Name, "/manifest")) {
				data, rerr := io.ReadAll(io.LimitReader(tr, 1<<20))
				if rerr != nil {
					return nil, rerr
				}
				gr.Close()
				return data, nil
			}
		}
		gr.Close()
	}

	// 回退 zip 格式
	fh.Seek(0, io.SeekStart)
	st, serr := fh.Stat()
	if serr != nil {
		return nil, serr
	}
	zr, zerr := zip.NewReader(fh, st.Size())
	if zerr != nil {
		return nil, zerr
	}
	for _, zf := range zr.File {
		name := strings.TrimPrefix(zf.Name, "./")
		if name == "manifest" || name == "manifest.json" || name == "fnpack.json" ||
			strings.HasSuffix(name, "/manifest") {
			rc, err := zf.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if err != nil {
				return nil, err
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("manifest not found in %s", filepath.Base(path))
}

// parseFpkManifest 解析一个 FPK 包的 manifest。
func parseFpkManifest(path string) (localFpkInfo, error) {
	data, err := readFpkManifestEntry(path)
	if err != nil {
		return localFpkInfo{}, err
	}
	return parseManifestText(data), nil
}

// EnsureIndex 确保全量索引在后台进行（目录快照变化时重新扫描）。
func (f *fpkLocal) EnsureIndex() {
	f.mu.Lock()
	if f.scanning {
		f.mu.Unlock()
		return
	}
	if !f.listedAt.IsZero() && f.scanFor.Equal(f.listedAt) {
		f.mu.Unlock()
		return
	}
	f.scanning = true
	f.mu.Unlock()

	go func() {
		defer func() {
			f.mu.Lock()
			f.scanning = false
			f.scanFor = f.listedAt
			f.mu.Unlock()
		}()
		start := time.Now()
		f.listFiles()
		f.mu.Lock()
		files := append([]string(nil), f.files...)
		f.mu.Unlock()

		sem := make(chan struct{}, 4)
		var wg sync.WaitGroup
		for _, p := range files {
			wg.Add(1)
			sem <- struct{}{}
			go func(path string) {
				defer wg.Done()
				defer func() { <-sem }()
				info, err := parseFpkManifest(path)
				if err != nil {
					return
				}
				n := normAppName(info.AppName)
				if n == "" {
					return
				}
				f.mu.Lock()
				if old, ok := f.index[n]; !ok || compareVersions(info.Version, old.Version) > 0 {
					f.index[n] = info
				}
				f.mu.Unlock()
			}(p)
		}
		wg.Wait()
		f.mu.Lock()
		n := len(f.index)
		f.mu.Unlock()
		log.Printf("[fpk-local] 全量索引完成: %d 个包 / %d 个应用, 耗时 %s", len(files), n, time.Since(start).Round(time.Millisecond))
	}()
}

// Lookup 查某应用的本地 FPK manifest（全量索引 → 惰性缓存 → 触发惰性解析）。
func (f *fpkLocal) Lookup(appName string) (localFpkInfo, bool) {
	n := normAppName(appName)
	if n == "" {
		return localFpkInfo{}, false
	}
	f.mu.Lock()
	if info, ok := f.index[n]; ok {
		f.mu.Unlock()
		return info, true
	}
	if info, ok := f.cache[n]; ok {
		f.mu.Unlock()
		return info, true
	}
	if f.parsing[n] {
		f.mu.Unlock()
		return localFpkInfo{}, false
	}
	if t, ok := f.missed[n]; ok && time.Since(t) < fpkDirListTTL {
		f.mu.Unlock()
		return localFpkInfo{}, false
	}
	f.parsing[n] = true
	f.mu.Unlock()

	go f.parseCandidates(n, appName)
	return localFpkInfo{}, false
}

// parseCandidates 解析文件名包含该应用名的候选包（全量索引未就绪时的快路径）。
func (f *fpkLocal) parseCandidates(n, appName string) {
	defer func() {
		f.mu.Lock()
		delete(f.parsing, n)
		if _, hit := f.cache[n]; !hit {
			f.missed[n] = time.Now()
		}
		f.mu.Unlock()
	}()

	f.listFiles()
	f.mu.Lock()
	files := append([]string(nil), f.files...)
	f.mu.Unlock()

	lowerName := strings.ToLower(appName)
	var best *localFpkInfo
	for _, p := range files {
		if !strings.Contains(strings.ToLower(filepath.Base(p)), lowerName) {
			continue
		}
		info, err := parseFpkManifest(p)
		if err != nil {
			log.Printf("[fpk-local] 解析失败 %s: %v", filepath.Base(p), err)
			continue
		}
		if normAppName(info.AppName) != n {
			continue
		}
		if best == nil || compareVersions(info.Version, best.Version) > 0 {
			cp := info
			best = &cp
		}
	}
	if best != nil {
		f.mu.Lock()
		f.cache[n] = *best
		f.mu.Unlock()
	}
}

// fillIndex 把解析出的 info 回填 appname 索引/缓存并清 missed（调用方须持锁）。
func (f *fpkLocal) fillIndex(info localFpkInfo) {
	n := normAppName(info.AppName)
	if n == "" {
		return
	}
	if old, ok := f.index[n]; !ok || compareVersions(info.Version, old.Version) > 0 {
		f.index[n] = info
	}
	f.cache[n] = info
	delete(f.missed, n)
}

// byPathCap 322R4：路径缓存上限——超过后新增键前触发 gcByPath（清文件已不存在的
// 孤儿条目），防 byPath 随历史 FPK 路径无界增长（条目小，属轻微内存卫生）。
const byPathCap = 256

// gcByPath 清理 byPath 中「文件已不存在」的孤儿条目（调用方须持 f.mu）。
// 仅在「新增键且已达上限」时由写入路径触发，避免每次写入都扫盘；文件都在则
// 无条目可清（此时增长受下载目录磁盘容量天然约束）。
func (f *fpkLocal) gcByPath() {
	for p := range f.byPath {
		if _, err := os.Stat(p); err != nil {
			delete(f.byPath, p)
		}
	}
}

// NotePath 同步解析指定 FPK 文件的 manifest，写入路径缓存并回填 appname 索引。
// 供上传流程落盘后调用：上传本身已有 IO 等待，同步解析一次可接受，使新上传
// 的 FPK 在「已下载的 FPK」列表立即拿到应用名（display_name），无需等后台全量
// 扫描或 10 分钟目录列表 TTL。解析失败静默返回、不改动现有状态。
func (f *fpkLocal) NotePath(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	info, perr := parseFpkManifest(path)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.byPath[path]; !exists && len(f.byPath) >= byPathCap {
		f.gcByPath()
	}
	if perr == nil {
		f.byPath[path] = pathEntry{mod: fi.ModTime(), info: info, ok: true}
		f.fillIndex(info)
	} else {
		f.byPath[path] = pathEntry{mod: fi.ModTime(), ok: false}
	}
}

// DisplayFor 返回指定 FPK 文件的 manifest（路径 + mtime 缓存）。
// 命中缓存直接返回（快）；未命中则后台解析（非阻塞），本次返回空、下次请求
// 补齐。用于 listDownloads 确保每个已下载 FPK 最终都能拿到 display_name——
// 直接解析「正在展示的那个文件」，绕过「文件名派生 appname 与包内 appname
// 不一致」导致的漏名（如 moo_0.6.321_x86.fpk 包内 appname=moo，文件名归一
// 却是 moo06321x86，按 appname 的 Lookup 永远匹配不上）。
func (f *fpkLocal) DisplayFor(path string) (localFpkInfo, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return localFpkInfo{}, false
	}
	f.mu.Lock()
	if e, ok := f.byPath[path]; ok && e.mod.Equal(fi.ModTime()) {
		f.mu.Unlock()
		if e.ok {
			return e.info, true
		}
		return localFpkInfo{}, false
	}
	if f.parsingPath[path] {
		f.mu.Unlock()
		return localFpkInfo{}, false
	}
	f.parsingPath[path] = true
	f.mu.Unlock()

	go func() {
		defer func() {
			f.mu.Lock()
			delete(f.parsingPath, path)
			f.mu.Unlock()
		}()
		info, perr := parseFpkManifest(path)
		st, serr := os.Stat(path)
		if perr != nil || serr != nil {
			return
		}
		f.mu.Lock()
		if _, exists := f.byPath[path]; !exists && len(f.byPath) >= byPathCap {
			f.gcByPath()
		}
		f.byPath[path] = pathEntry{mod: st.ModTime(), info: info, ok: true}
		f.fillIndex(info)
		f.mu.Unlock()
	}()
	return localFpkInfo{}, false
}
