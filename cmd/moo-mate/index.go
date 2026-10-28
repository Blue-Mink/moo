package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
}

func newIndex(path string) *index {
	ix := &index{path: path, doc: &indexDoc{Version: 1, Apps: map[string]*indexedApp{}}}
	_ = ix.load()
	return ix
}

// load 读盘；文件不存在视为空索引，不报错。
func (ix *index) load() error {
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
	ix.doc = &d
	return nil
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
	return nil
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
func (ix *index) save() error {
	if dir := filepath.Dir(ix.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.MarshalIndent(ix.doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ix.path, b, 0o644)
}

// list 返回全部应用副本（注入 appname）。
func (ix *index) list() []*indexedApp {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*indexedApp, 0, len(ix.doc.Apps))
	for name, a := range ix.doc.Apps {
		cp := *a
		cp.Appname = name
		out = append(out, &cp)
	}
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
