package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// iconCacheExts 与 loadIconFile 认的扩展名保持一致（统计口径 = 能命中的图标文件）。
var iconCacheExts = map[string]bool{".png": true, ".jpg": true, ".gif": true, ".webp": true}

// cacheStats 统计图标缓存目录（<bootDir>/icons）：
// count/bytes = 图标文件数与字节；tmp = *.tmp 残留数。
func (c *companion) cacheStats() (count, bytes int64, tmp int, err error) {
	entries, err := os.ReadDir(c.iconDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, 0, nil
		}
		return 0, 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(filepath.Base(e.Name())))
		if ext == ".tmp" {
			tmp++
			continue
		}
		if !iconCacheExts[ext] {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		count++
		bytes += info.Size()
	}
	return count, bytes, tmp, nil
}

// handleCacheStats 返回缓存统计，供设置页展示。
func (c *companion) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	count, bytes, tmp, err := c.cacheStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "统计失败: "+err.Error())
		return
	}
	mateIconFailMu.Lock()
	failed := len(mateIconFailAt)
	mateIconFailMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"icons": map[string]any{
			"count": count,
			"bytes": bytes,
			"tmp":   tmp,
		},
		"failed": failed,
	})
}

// handleCacheClean 清理缓存：scope=icons 删全部图标并清内存缓存，scope=tmp 只删 *.tmp，
// scope=all 两者都删（0.2.70：设置页两个清理按钮合并为一个「清理缓存」）。
// 空 body / 空 scope 仍按 icons 处理（旧调用方兼容）。
func (c *companion) handleCacheClean(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scope string `json:"scope"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 空 body 合法，按缺省 icons 处理
	scope := strings.ToLower(strings.TrimSpace(body.Scope))
	if scope == "" {
		scope = "icons"
	}
	if scope != "icons" && scope != "tmp" && scope != "all" {
		writeErr(w, http.StatusBadRequest, "不支持的 scope: "+scope)
		return
	}

	dir := c.iconDir()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		writeErr(w, http.StatusInternalServerError, "读取缓存目录失败: "+err.Error())
		return
	}
	removed, freed := 0, int64(0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// 安全：只认目录内的裸文件名，绝不接受带路径/上跳的名字。
		name := filepath.Base(e.Name())
		if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch scope {
		case "tmp":
			if ext != ".tmp" {
				continue
			}
		case "all":
			// 0.2.70：图标 + 临时残留一起删（合并按钮），其余文件不碰。
			if ext != ".tmp" && !iconCacheExts[ext] {
				continue
			}
		default: // icons
			if !iconCacheExts[ext] {
				continue
			}
		}
		p := filepath.Join(dir, name)
		if filepath.Dir(p) != filepath.Clean(dir) {
			continue
		}
		var size int64
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		if rerr := os.Remove(p); rerr != nil {
			if os.IsNotExist(rerr) {
				continue
			}
			logf("清理缓存 %s 失败：%v", name, rerr)
			continue
		}
		removed++
		freed += size
	}

	if scope != "tmp" {
		// 图标文件已全删（icons / all）→ 内存层与失败负缓存一并清，
		// 否则页面还能从内存拿到旧图标、被负缓存挡住重新拉取。
		c.iconMu.Lock()
		c.icons = map[string][]byte{}
		c.iconMu.Unlock()
		mateIconFailMu.Lock()
		for k := range mateIconFailAt {
			delete(mateIconFailAt, k)
		}
		mateIconFailMu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scope":   scope,
		"removed": removed,
		"freed":   freed,
	})
}
