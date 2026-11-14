package main

import (
	"os"
	"path/filepath"
	"strings"
)

// purgeAppArtifacts 清掉某应用在本机留下的派生痕迹：磁盘图标缓存 / 内存图标缓存 /
// 图标失败负缓存。只删可安全重造的派生文件，不碰用户数据目录。
// 返回「是否真的删掉了东西」，便于接口回包说明。
func (c *companion) purgeAppArtifacts(appname string) (bool, error) {
	appname = strings.TrimSpace(appname)
	if appname == "" {
		return false, nil
	}
	removed := false

	// ① 磁盘图标缓存：文件名是 iconCacheName(appname)，与 appname 不同，不能直接拼。
	base := filepath.Join(c.iconDir(), iconCacheName(appname))
	for _, ext := range []string{"png", "jpg", "gif", "webp"} {
		err := os.Remove(base + "." + ext)
		if err == nil {
			removed = true
			continue
		}
		if !os.IsNotExist(err) {
			return removed, err
		}
	}

	// ② 内存图标缓存
	c.iconMu.Lock()
	if _, ok := c.icons[appname]; ok {
		delete(c.icons, appname)
		removed = true
	}
	c.iconMu.Unlock()

	// ③ 图标失败负缓存（TTL 内会直接 404，不清掉的话重新加入后仍看不到图标）
	mateIconFailMu.Lock()
	if _, ok := mateIconFailAt[appname]; ok {
		delete(mateIconFailAt, appname)
		removed = true
	}
	mateIconFailMu.Unlock()

	return removed, nil
}
