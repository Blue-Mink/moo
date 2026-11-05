package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexedApp 索引里的一条应用：moo.json 条目 + 来源 + 同步状态。
// appEntry 值内嵌（json 平铺），Appname 单独存（map key）。
// Packages 存「架构 → 包」多架构映射（x86/arm/all）；空/单架构时保持
// 顶层扁平 download_url 形态（与 gen-moo-json 单架构输出一致）。
type indexedApp struct {
	appEntry
	Appname      string                 `json:"appname"`
	Source       string                 `json:"source"`                  // 来源仓库 URL（「应用来源」展示）
	Publisher    string                 `json:"publisher"`               // 发布者（owner，归类用）
	Synced       bool                   `json:"synced"`                  // 是否已同步到 Moo
	SyncedName   string                 `json:"synced_name,omitempty"`   // Moo 里的源名
	Packages     map[string]*appPackage `json:"packages,omitempty"`      // 架构 → 包（多架构）
	DownloadURLs []string               `json:"download_urls,omitempty"` // 候选下载链（原始链 + 软仓服务器，测速选优用；不进 mate.json）
}

// indexDoc 索引文件磁盘结构。
type indexDoc struct {
	Version int                    `json:"version"`
	Apps    map[string]*indexedApp `json:"apps"` // key = appname
}

// index 持久化应用索引（内存 + 落盘）。
type index struct {
	mu   sync.RWMutex
	path string
	doc  *indexDoc
	// 0.2.55（F4）：启动加载诊断。/api/status 暴露 last_index_error，
	// 让「索引损坏后被自动重建」不再静默。
	loadErr string // 最近一次启动加载失败原因（空 = 正常）
	badFile string // 损坏索引被隔离出来的文件名（同目录 index.json.bad-<ts>）
}

func newIndex(path string) *index {
	ix := &index{path: path, doc: &indexDoc{Version: 1, Apps: map[string]*indexedApp{}}}
	// 内置默认索引的写入**不在这里**：由 main 启动路径调 bootstrapDefaultIndex，
	// 避免把所有「空索引」单测污染成 486 条（那是出厂数据，不是 v 单元行为）。
	_ = ix.load()
	return ix
}

// load 读盘；文件不存在视为空索引，不报错。
//
// 0.2.55（F4）：解析失败不再静默留空——先把原文件改名隔离为
// index.json.bad-<YYYYMMDD-HHMMSS>，再返回错误，由 /api/status 暴露
// last_index_error。旧实现忽略错误直接留空索引，随后自动同步用远程默认
// 清单重建并 save()，把损坏文件连同用户原数据一起覆盖掉（无法取证）。
// 隔离是 rename 不是删除，原件始终保留。
func (ix *index) load() error {
	b, err := os.ReadFile(ix.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动：空索引
		}
		ix.setLoadDiag("读取索引失败: "+err.Error(), "")
		return err
	}
	var d indexDoc
	if uerr := json.Unmarshal(b, &d); uerr != nil {
		bad := quarantineIndex(ix.path)
		msg := "索引解析失败: " + uerr.Error()
		if bad != "" {
			msg += "（原文件已隔离为 " + filepath.Base(bad) + "）"
		} else {
			msg += "（隔离原文件失败，已保留在原路径）"
		}
		log.Printf("%s", msg)
		ix.setLoadDiag(msg, bad)
		return uerr
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	ix.doc = &d
	return nil
}

// setLoadDiag 记录启动加载诊断（损坏索引隔离 / 读取失败）。
func (ix *index) setLoadDiag(msg, badFile string) {
	ix.mu.Lock()
	ix.loadErr = msg
	ix.badFile = badFile
	ix.mu.Unlock()
}

// diag 返回加载诊断（错误描述、隔离文件名基名）。空串表示无异常。
func (ix *index) diag() (string, string) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	bad := ""
	if ix.badFile != "" {
		bad = filepath.Base(ix.badFile)
	}
	return ix.loadErr, bad
}

// quarantineIndex 把损坏的索引文件改名保留（index.json.bad-<ts>[-n]），
// 返回新路径；失败返回空串。只用 rename，绝不删除原文件。
func quarantineIndex(path string) string {
	ts := time.Now().Format("20060102-150405")
	for i := 0; i < 20; i++ {
		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("-%d", i)
		}
		dst := path + ".bad-" + ts + suffix
		if _, err := os.Stat(dst); err == nil {
			continue // 同名已存在（同秒多次损坏），换序号
		}
		if err := os.Rename(path, dst); err != nil {
			return ""
		}
		return dst
	}
	return ""
}

// reload 从当前 path 重新读盘并替换内存文档（恢复备份 / 切换数据目录后热加载）。
func (ix *index) reload() error {
	b, err := os.ReadFile(ix.path)
	if err != nil {
		return err
	}
	var d indexDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	ix.mu.Lock()
	ix.doc = &d
	ix.mu.Unlock()
	// 恢复备份/切换数据目录后也修正旧索引里的相对下载地址。
	_, err = ix.normalizeDownloads()
	return err
}

// filePath 当前索引文件路径。
func (ix *index) filePath() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.path
}

// setPath 切换索引文件路径（调用方负责先把数据写到新位置）。
func (ix *index) setPath(p string) {
	ix.mu.Lock()
	ix.path = p
	ix.mu.Unlock()
}

// save 落盘（自动建目录）。
// 0.2.34：原子写（同目录临时文件 + fsync + rename + 目录 fsync）。os.WriteFile 会先
// truncate，期间任何读取方（备份、部署核对脚本、外部工具）都会读到 0 字节空文件；
// 只 rename 不 fsync 时断电仍可能丢掉 tmp 内容或 rename 本身，所以写前 sync 文件、
// rename 后 sync 目录。
func (ix *index) save() error {
	dir := filepath.Dir(ix.path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.MarshalIndent(ix.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, ix.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// list 返回全部应用副本（注入 appname），按 appname 升序排序。
//
// 0.2.55（F2）：map 迭代顺序随机 → /api/index 每次响应字节不同
// （同长度不同 md5），客户端缓存/ETag/diff/字节级比对全部失效。
// 输出前排序，保证同一索引内容永远产出同一份 JSON。
func (ix *index) list() []*indexedApp {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*indexedApp, 0, len(ix.doc.Apps))
	for name, a := range ix.doc.Apps {
		cp := *a
		cp.Appname = name
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Appname < out[j].Appname })
	return out
}

// get 取单个应用（不含 appname，调用方已知 key）。
func (ix *index) get(appname string) *indexedApp {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	a := ix.doc.Apps[appname]
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// upsert 写入/覆盖一条；重复发现时保留已有同步状态（不抹掉 synced）。
func (ix *index) upsert(a *indexedApp) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if prev, ok := ix.doc.Apps[a.Appname]; ok && prev.Synced && !a.Synced {
		a.Synced = true
		a.SyncedName = prev.SyncedName
	}
	ix.doc.Apps[a.Appname] = a
	return ix.save()
}

// normalizeDownloads 修正旧索引里软仓条目的相对下载地址（B1）。
//
// 只处理 Publisher=softstore 的条目：它们的 Source 是软仓基址，可安全拼接；
// GitHub/Gitee 仓库来源保持原样，避免把仓库内相对路径错误拼成网页地址。
// 返回修正条数；有变更才落盘。
func (ix *index) normalizeDownloads() (int, error) {
	ix.mu.Lock()
	n := 0
	for _, a := range ix.doc.Apps {
		if a == nil || a.Publisher != "softstore" {
			continue
		}
		if normalizeAppDownloads(a) {
			n++
		}
	}
	var err error
	if n > 0 {
		err = ix.save()
	}
	ix.mu.Unlock()
	return n, err
}

// normalizeAppDownloads 把单条软仓应用的顶层/架构/候选下载链补成绝对地址。
// 已是绝对地址的保持不变（幂等）。
func normalizeAppDownloads(a *indexedApp) bool {
	if a == nil || strings.TrimSpace(a.Source) == "" {
		return false
	}
	changed := false
	fix := func(p *string) {
		if p == nil || strings.TrimSpace(*p) == "" {
			return
		}
		v := resolveSoftstoreURL(a.Source, *p)
		if v != *p {
			*p = v
			changed = true
		}
	}
	fix(&a.DownloadURL)
	for _, p := range a.Packages {
		if p != nil {
			fix(&p.DownloadURL)
		}
	}
	for i := range a.DownloadURLs {
		fix(&a.DownloadURLs[i])
	}
	return changed
}

// delete 从索引移除一条。
func (ix *index) delete(appname string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.doc.Apps, appname)
	return ix.save()
}

// clear 清空全部应用（保留空索引结构；供「清除数据」使用）。
func (ix *index) clear() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.doc.Apps = map[string]*indexedApp{}
	return ix.save()
}

// setSynced 更新同步状态与 Moo 源名。
func (ix *index) setSynced(appname string, synced bool, name string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	a, ok := ix.doc.Apps[appname]
	if !ok {
		return fmt.Errorf("索引里没有该应用: %s", appname)
	}
	a.Synced = synced
	if synced {
		a.SyncedName = name
	} else {
		a.SyncedName = ""
	}
	return ix.save()
}

// setSyncedMany 批量更新同步状态：全部改完只落盘一次。
// 0.2.32：原批量推送逐个调 setSynced，每个都把整份 index.json 序列化写一遍
// （N 个应用 = N 次全量写盘），几百个应用要等十几秒。改为一次保存。
func (ix *index) setSyncedMany(appnames []string, synced bool, name string) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	n := 0
	for _, appname := range appnames {
		a, ok := ix.doc.Apps[appname]
		if !ok {
			continue
		}
		a.Synced = synced
		if synced {
			a.SyncedName = name
		} else {
			a.SyncedName = ""
		}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, ix.save()
}
