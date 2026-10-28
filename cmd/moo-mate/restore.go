package main

// restore.go —— 0.2.29：备份列表 + 恢复备份（本 NAS / 本机设备上传）。
//
// 备份是数据目录的 tar.gz（根层含 index.json / config.json / backups/…）。
// 恢复只取 index.json（应用索引数据）并热加载，不动当前的数据路径、同步
// 设置与备份目录——避免「恢复一份旧备份把刚改好的路径改回去」。
// 安全：只认根层 index.json，逐条跳过其它条目（防目录穿越）；体积上限 64MB。

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxRestoreIndexBytes = 64 << 20   // 单个 index.json 上限
const maxRestoreUploadBytes = 256 << 20 // 上传的 tar.gz 上限

// backupEntry 备份文件条目（设置页「恢复备份」列表用）。
type backupEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime string    `json:"mtime"`
	sortAt  time.Time `json:"-"`
}

// backupNameTime 从备份文件名解析生成时间（moo-mate-backup-YYYYMMDD-HHMMSS.tar.gz）。
// 数据目录迁移只复制内容不改文件名，文件名里的时间才是这份备份的真实时间。
var backupNameRE = regexp.MustCompile(`^moo-mate-backup-(\d{8})-(\d{6})\.tar\.gz$`)

func backupNameTime(name string) (time.Time, bool) {
	m := backupNameRE.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102-150405", m[1]+"-"+m[2], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// isBackupFileName 合法备份文件名（防目录穿越）：只允许数据目录里生成的
// moo-mate-backup-*.tar.gz 形态，或用户上传后落盘的同类名字。
func isBackupFileName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") || filepath.Base(name) != name {
		return false
	}
	low := strings.ToLower(name)
	return strings.HasSuffix(low, ".tar.gz") || strings.HasSuffix(low, ".tgz")
}

// handleBackupList GET /api/backups：列出备份目录里的 tar.gz（新→旧）。
func (c *companion) handleBackupList(w http.ResponseWriter, r *http.Request) {
	dir := c.backupDirOf()
	entries := []backupEntry{}
	list, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range list {
			if e.IsDir() || !isBackupFileName(e.Name()) {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			at := info.ModTime()
			if t, ok := backupNameTime(e.Name()); ok {
				at = t
			}
			entries = append(entries, backupEntry{
				Name:    e.Name(),
				Size:    info.Size(),
				ModTime: at.Format(time.RFC3339),
				sortAt:  at,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sortAt.After(entries[j].sortAt) })
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "backups": entries})
}

// handleBackupRestore POST /api/backup/restore {"name":"…"}：恢复本 NAS 上的备份。
func (c *companion) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if !isBackupFileName(name) {
		writeErr(w, http.StatusBadRequest, "非法备份文件名")
		return
	}
	f, err := os.Open(filepath.Join(c.backupDirOf(), name))
	if err != nil {
		writeErr(w, http.StatusNotFound, "备份不存在: "+name)
		return
	}
	defer f.Close()
	n, err := c.restoreIndexFromTar(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apps": n, "source": "nas"})
}

// handleBackupRestoreUpload POST /api/backup/restore-upload（multipart，字段名 file）：
// 恢复本机设备（手机/电脑）上传的备份。
func (c *companion) handleBackupRestoreUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUploadBytes)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "未收到备份文件（form 字段名 file）: "+err.Error())
		return
	}
	defer f.Close()
	if hdr != nil && !isBackupFileName(hdr.Filename) {
		writeErr(w, http.StatusBadRequest, "请选择 .tar.gz / .tgz 备份文件")
		return
	}
	n, err := c.restoreIndexFromTar(f)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "apps": n, "source": "upload"})
}

// restoreIndexFromTar 从 tar.gz 里取根层 index.json，校验后原子替换并热加载。
func (c *companion) restoreIndexFromTar(rd io.Reader) (int, error) {
	gz, err := gzip.NewReader(rd)
	if err != nil {
		return 0, errors.New("不是有效的 gzip 备份文件")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var data []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("读取备份失败: %v", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if name != "index.json" {
			continue // 只认根层 index.json，其它条目（含 ../ 穿越）一律跳过
		}
		if hdr.Size <= 0 || hdr.Size > maxRestoreIndexBytes {
			return 0, errors.New("备份里的 index.json 体积异常")
		}
		data, err = io.ReadAll(io.LimitReader(tr, maxRestoreIndexBytes+1))
		if err != nil {
			return 0, fmt.Errorf("读取 index.json 失败: %v", err)
		}
		break
	}
	if len(data) == 0 {
		return 0, errors.New("备份里没有 index.json（不是 Moo Mate 数据备份）")
	}
	var d indexDoc
	if err := json.Unmarshal(data, &d); err != nil {
		return 0, fmt.Errorf("index.json 解析失败: %v", err)
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	path := c.idx.filePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, fmt.Errorf("数据目录不可写: %v", err)
	}
	tmp := path + ".restore.tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return 0, fmt.Errorf("写入恢复文件失败: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return 0, fmt.Errorf("替换索引失败: %v", err)
	}
	if err := c.idx.reload(); err != nil {
		return 0, fmt.Errorf("重新加载索引失败: %v", err)
	}
	if _, err := c.rebuildAggregate(); err != nil {
		return 0, fmt.Errorf("重建聚合源失败: %v", err)
	}
	return len(d.Apps), nil
}

// handleBackupDownload GET /api/backup/download?name=…：下载备份目录里指定的
// 一份备份（手机/电脑上可直接取 NAS 上已有的历史备份）。
func (c *companion) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !isBackupFileName(name) {
		writeErr(w, http.StatusBadRequest, "非法备份文件名")
		return
	}
	p := filepath.Join(c.backupDirOf(), name)
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, http.StatusNotFound, "备份不存在: "+name)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, "备份不存在: "+name)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	_, _ = io.Copy(w, f)
}
