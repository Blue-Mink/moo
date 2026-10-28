package main

// datadir.go —— 0.2.29：数据路径可选（点击路径选目录 → 迁移 → 热切换）。
//
// 语义对齐 Moo 的目录切换：把现有数据（index.json / config.json / 默认
// backups/）复制到新目录，然后**当前进程直接切过去**（重载索引），不重启；
// 同时在固定引导目录写 datadir 指针，ctl 脚本与进程下次启动都按新路径。
// 旧目录保留原样作为回滚点（不删除）。

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleDataDirSet POST /api/datadir {"path":"/volN/…"}：
// 空字符串 = 恢复默认数据目录（引导目录）。
func (c *companion) handleDataDirSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := filepath.Clean(c.dataDirOf())
	target := strings.TrimSpace(body.Path)
	if target == "" {
		target = c.bootDir
	}
	if target == "" {
		target = cur
	}
	target = filepath.Clean(target)
	if !filepath.IsAbs(target) {
		writeErr(w, http.StatusBadRequest, "数据路径必须是绝对路径")
		return
	}
	// 允许：引导目录（@appdata 下，默认位置）或 /volN 可见目录。
	if target != filepath.Clean(c.bootDir) && !isValidBackupDir(target) {
		writeErr(w, http.StatusBadRequest, "只能选择 /volN 存储卷下的可见目录")
		return
	}
	if target == cur {
		writeErr(w, http.StatusBadRequest, "已经是当前数据目录")
		return
	}
	// 目标不能位于当前数据目录内部（会边复制边把复制结果再套进去）。
	if strings.HasPrefix(target+string(filepath.Separator), cur+string(filepath.Separator)) {
		writeErr(w, http.StatusBadRequest, "不能选择当前数据目录内部的子目录")
		return
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建数据目录失败: "+err.Error())
		return
	}
	probe := filepath.Join(target, ".moo-mate-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		writeErr(w, http.StatusBadRequest, "目标目录不可写: "+err.Error())
		return
	}
	_ = os.Remove(probe)

	if err := c.idx.save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存当前索引失败: "+err.Error())
		return
	}
	if err := copyFileIfExists(filepath.Join(cur, "index.json"), filepath.Join(target, "index.json")); err != nil {
		writeErr(w, http.StatusInternalServerError, "迁移 index.json 失败: "+err.Error())
		return
	}
	_ = copyFileIfExists(filepath.Join(cur, "config.json"), filepath.Join(target, "config.json"))
	// 默认备份目录在数据目录内时，把已有备份一并带过去（自定义备份目录不动）。
	if defBackup := filepath.Join(cur, "backups"); c.backupDirOf() == defBackup {
		if fi, err := os.Stat(defBackup); err == nil && fi.IsDir() {
			if err := copyDir(defBackup, filepath.Join(target, "backups")); err != nil {
				writeErr(w, http.StatusInternalServerError, "迁移 backups 失败: "+err.Error())
				return
			}
		}
	}

	// 热切换：索引路径 + 配置文件路径 + 引导指针。
	newIndexPath := filepath.Join(target, "index.json")
	newCfgPath := filepath.Join(target, "config.json")
	c.idx.setPath(newIndexPath)
	c.cfgPath = newCfgPath
	if c.cfg != nil {
		_ = c.cfg.save(newCfgPath)
	}
	if c.bootDir != "" {
		if err := os.WriteFile(filepath.Join(c.bootDir, "datadir"), []byte(target+"\n"), 0o600); err != nil {
			writeErr(w, http.StatusInternalServerError, "写入数据目录指针失败: "+err.Error())
			return
		}
	}
	if err := c.idx.reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, "重载索引失败: "+err.Error())
		return
	}
	if _, err := c.rebuildAggregate(); err != nil {
		writeErr(w, http.StatusInternalServerError, "重建聚合源失败: "+err.Error())
		return
	}
	apps := len(c.idx.list())
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "dir": target, "apps": apps, "previous": cur,
	})
}

// decodeJSONBody 限制请求体（10MB）并解 JSON。
func decodeJSONBody(r *http.Request, v any) error {
	r.Body = io.NopCloser(io.LimitReader(r.Body, 10<<20))
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		return errors.New("请求体解析失败: " + err.Error())
	}
	return nil
}

// copyFileIfExists 复制普通文件（保留 0644/0600 近似权限）；源不存在视为成功。
func copyFileIfExists(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi.Mode().Perm()&0o077 == 0 {
		mode = 0o600
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// 保留原 mtime（备份列表按时间排序，迁移后不能全变成迁移时刻）。
	_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	return nil
}

// copyDir 递归复制目录（跳过符号链接与特殊文件；同名覆盖）。
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFileIfExists(p, target)
	})
}

// bootstrapDir 引导目录（写 datadir 指针的地方）：环境注入优先，否则用当前数据目录。
func (c *companion) bootstrapDir() string {
	if c.bootDir != "" {
		return c.bootDir
	}
	return c.dataDirOf()
}
