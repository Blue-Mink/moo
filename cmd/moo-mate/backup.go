package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dataDirOf 数据目录 = config.json 所在目录；未初始化时回落 index 路径目录。
func (c *companion) dataDirOf() string {
	if c.cfgPath != "" {
		return filepath.Dir(c.cfgPath)
	}
	return filepath.Dir(c.idx.path)
}

// backupDirOf 生效的备份目录：配置优先，回落数据目录下的 backups/（对齐 Moo）。
func (c *companion) backupDirOf() string {
	c.mu.RLock()
	d := strings.TrimSpace(c.cfg.BackupDir)
	c.mu.RUnlock()
	if d == "" {
		return filepath.Join(c.dataDirOf(), "backups")
	}
	return d
}

// isValidBackupDir 备份目录白名单：空（默认）或 /volN 卷根及卷下任意层可见子目录。
// 路径中任何一层以 . 或 @ 开头（隐藏目录 / 平台数据 @appdata/@appcenter 等）一律拒绝。
func isValidBackupDir(path string) bool {
	for i := 1; i <= 8; i++ {
		vp := "/vol" + strconv.Itoa(i)
		if vfi, err := os.Stat(vp); err != nil || !vfi.IsDir() {
			continue
		}
		if path == vp {
			return true
		}
		if strings.HasPrefix(path, vp+"/") {
			for _, comp := range strings.Split(strings.TrimPrefix(path, vp+"/"), "/") {
				if strings.HasPrefix(comp, ".") || strings.HasPrefix(comp, "@") {
					return false
				}
			}
			return true
		}
	}
	return false
}

// handleDataDir GET /api/datadir：数据目录信息（路径 + 体积 + 文件清单）。
func (c *companion) handleDataDir(w http.ResponseWriter, r *http.Request) {
	dir := c.dataDirOf()
	var total int64
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		// 跳过日志（可能正在写）
		if filepath.Ext(p) == ".log" {
			return nil
		}
		total += info.Size()
		rel, _ := filepath.Rel(dir, p)
		files = append(files, rel)
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"dir":         dir,
		"default_dir": c.bootstrapDir(),
		"backup_dir":  c.backupDirOf(),
		"total_bytes": total,
		"files":       files,
	})
}

// handleDirs GET /api/dirs?path=…：目录浏览器单级列表（设置页「选择备份目录」用）。
// path 为 "/" 时列出现存卷根；否则列出该目录下的可见子目录（跳过 . / @ 隐藏目录）。
func (c *companion) handleDirs(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	if path != "/" && !strings.HasPrefix(path, "/vol") {
		writeErr(w, http.StatusBadRequest, "只能浏览 /volN 存储卷下的目录")
		return
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	entries := []entry{}
	parent := ""
	if path == "/" {
		for i := 1; i <= 8; i++ {
			vp := "/vol" + strconv.Itoa(i)
			if vfi, err := os.Stat(vp); err == nil && vfi.IsDir() {
				entries = append(entries, entry{Name: fmt.Sprintf("存储 %d", i), Path: vp})
			}
		}
	} else {
		if vfi, err := os.Stat(path); err != nil || !vfi.IsDir() {
			writeErr(w, http.StatusNotFound, "目录不存在: "+path)
			return
		}
		parent = filepath.Dir(path)
		if parent == path || parent == "" {
			parent = "/"
		}
		list, _ := os.ReadDir(path)
		for _, e := range list {
			if !e.IsDir() {
				continue
			}
			n := e.Name()
			if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "@") {
				continue
			}
			entries = append(entries, entry{Name: n, Path: filepath.Join(path, n)})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       path,
		"parent":     parent,
		"is_allowed": path == "/" || isValidBackupDir(path),
		"current":    c.backupDirOf(),
		"entries":    entries,
	})
}

// writeBackupTar 把数据目录打成 tar.gz 写进 w（共享：下载与写盘备份）。
// 备份目录（默认在数据目录内 backups/）必须排除：否则每份备份把历史备份
// 一起打包，体积滚雪球（实测第二份已把第一份 80KB 吞进去）。
func (c *companion) writeBackupTar(dir string, w io.Writer) error {
	backupDir := c.backupDirOf()
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// 排除备份目录整体（含其子层）
		if absBackup, err := filepath.Abs(backupDir); err == nil {
			if absP, err2 := filepath.Abs(p); err2 == nil && (absP == absBackup || strings.HasPrefix(absP, absBackup+string(filepath.Separator))) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil // 日志等文件可能正在写，跳过
		}
		defer f.Close()
		_, _ = io.Copy(tw, f)
		return nil
	})
	_ = tw.Close()
	_ = gz.Close()
	return err
}

// backupFileName 备份文件名（moo-mate-backup-YYYYMMDD-HHMMSS.tar.gz）。
func backupFileName() string {
	return fmt.Sprintf("moo-mate-backup-%s.tar.gz", time.Now().Format("20060102-150405"))
}

// handleBackup GET /api/backup：打包数据目录为 tar.gz 下载到浏览器。
func (c *companion) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir := c.dataDirOf()
	name := backupFileName()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	_ = c.writeBackupTar(dir, w)
}

// createBackupToDir 把数据目录打成 tar.gz 写进备份目录，返回文件名与大小。
// 自动备份循环与「立即备份」共用；写完按保留策略淘汰更早的份数。
func (c *companion) createBackupToDir() (string, int64, error) {
	dir := c.dataDirOf()
	outDir := c.backupDirOf()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("备份目录不可写: %w", err)
	}
	name := backupFileName()
	dst := filepath.Join(outDir, name)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("创建备份文件失败: %w", err)
	}
	if err := c.writeBackupTar(dir, f); err != nil {
		f.Close()
		os.Remove(dst)
		return "", 0, fmt.Errorf("备份失败: %w", err)
	}
	f.Close()
	var size int64
	if fi, err := os.Stat(dst); err == nil {
		size = fi.Size()
	}
	if removed := c.pruneBackups(); removed > 0 {
		log.Printf("备份保留策略：淘汰 %d 份旧备份（保留最近 %d 份）", removed, backupRetention)
	}
	return name, size, nil
}

// handleBackupWrite POST /api/backup：把数据目录打成 tar.gz 写进备份目录
// （NAS 本机路径，供外部同步/备份走）。返回文件路径与大小。
func (c *companion) handleBackupWrite(w http.ResponseWriter, r *http.Request) {
	name, size, err := c.createBackupToDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"name": name,
		"path": filepath.Join(c.backupDirOf(), name),
		"size": size,
	})
}

// handleClearData POST /api/clear-data：清空应用索引列表（应用归零）。
// 保留配置（token/间隔等），不动 Moo 里已加的源；空索引 → 空聚合
// moo.json → Moo 下次同步 moo-mate 源时内容随之清空（属预期，UI 二次确认已提示）。
func (c *companion) handleClearData(w http.ResponseWriter, r *http.Request) {
	if err := c.idx.clear(); err != nil {
		writeErr(w, http.StatusInternalServerError, "清除失败: "+err.Error())
		return
	}
	if _, err := c.rebuildAggregate(); err != nil {
		log.Printf("清除数据后重建聚合失败: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listBackupFiles 备份目录里的备份文件，按文件名时间旧→新排序。
func (c *companion) listBackupFiles() []string {
	dir := c.backupDirOf()
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		name string
		at   time.Time
	}
	items := make([]item, 0, len(list))
	for _, e := range list {
		if e.IsDir() || !isBackupFileName(e.Name()) {
			continue
		}
		at := time.Time{}
		if t, ok := backupNameTime(e.Name()); ok {
			at = t
		} else if info, ierr := e.Info(); ierr == nil {
			at = info.ModTime()
		}
		items = append(items, item{name: e.Name(), at: at})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.name)
	}
	return names
}

// autoBackupNames 只挑出「本程序自己生成的」备份（文件名能解析出时间戳）。
//
// 为什么不能直接用 listBackupFiles：它的过滤条件是 isBackupFileName，只认
// 「非点文件 + 无路径分隔符 + 后缀 .tar.gz/.tgz」，用户把自己打包的归档放进
// 备份目录同样算数。自动淘汰是无人值守的删除，绝不能删不是自己写的东西，
// 因此淘汰池必须收紧到 moo-mate-backup-YYYYMMDD-HHMMSS.tar.gz 这一种命名。
func (c *companion) autoBackupNames() []string {
	all := c.listBackupFiles()
	out := make([]string, 0, len(all))
	for _, n := range all {
		if _, ok := backupNameTime(n); ok {
			out = append(out, n)
		}
	}
	return out
}

// pruneBackups 只保留最近 backupRetention 份「自动备份」，返回淘汰数量。
// 非本程序命名的归档（用户自己放的 tar.gz）不参与淘汰，也不会被删。
func (c *companion) pruneBackups() int {
	names := c.autoBackupNames()
	if len(names) <= backupRetention {
		return 0
	}
	dir := c.backupDirOf()
	removed := 0
	for _, n := range names[:len(names)-backupRetention] {
		if err := os.Remove(filepath.Join(dir, n)); err == nil {
			removed++
		}
	}
	return removed
}

// lastBackupAt 最近一份备份的生成时间（RFC3339，无备份返回空串）。
func (c *companion) lastBackupAt() string {
	names := c.listBackupFiles()
	if len(names) == 0 {
		return ""
	}
	last := names[len(names)-1]
	if t, ok := backupNameTime(last); ok {
		return t.Format(time.RFC3339)
	}
	if info, err := os.Stat(filepath.Join(c.backupDirOf(), last)); err == nil {
		return info.ModTime().Format(time.RFC3339)
	}
	return ""
}

// handleBackupDelete DELETE /api/backup?name=xxx：删除一份备份。
// 名称必须通过 isBackupFileName（禁路径分隔符与前导点），防止穿越删其它文件。
func (c *companion) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !isBackupFileName(name) {
		writeErr(w, http.StatusBadRequest, "备份文件名非法")
		return
	}
	path := filepath.Join(c.backupDirOf(), name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "备份不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
}

// backupLoop 自动备份循环（参考 Moo StartBackupLoop）：每小时检查一次，
// 距上一份备份超过配置天数就写一份；关闭开关时挂起等待唤醒。
func (c *companion) backupLoop() {
	time.Sleep(10 * time.Second) // 让启动期的数据重建先完成
	for {
		c.mu.RLock()
		auto := c.cfg.BackupAuto
		days := c.cfg.backupIntervalDays()
		c.mu.RUnlock()
		if !auto {
			select {
			case <-c.syncWake:
			case <-time.After(time.Hour):
			}
			continue
		}
		due := true
		if last := c.lastBackupAt(); last != "" {
			if t, err := time.Parse(time.RFC3339, last); err == nil {
				due = time.Since(t) >= time.Duration(days)*24*time.Hour
			}
		}
		if due {
			name, size, err := c.createBackupToDir()
			if err != nil {
				log.Printf("自动备份失败: %v", err)
			} else {
				log.Printf("自动备份完成: %s（%d 字节，周期 %d 天）", name, size, days)
			}
		}
		select {
		case <-c.syncWake:
		case <-time.After(time.Hour):
		}
	}
}
