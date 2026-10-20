package api

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// installLocation 是已安装应用的真实落盘位置（0.6.337）。
//   - Volume > 0：应用装在 /volN/@appcenter/<appname>（存储空间 N）
//   - Volume = 0：应用装在 /var/apps/<appname>（系统空间）
//
// 详情页「安装位置」直接展示它（存储空间 N · /volN），比源声明更可信：
// 源里的 install_type 语义混杂（root / package / 系统空间 / 分类名），
// 而目录是否存在是本地事实。
type installLocation struct {
	Volume int
	Path   string
}

type installLocCacheT struct {
	mu     sync.Mutex
	at     time.Time
	byName map[string]installLocation
}

var installLocCache = &installLocCacheT{}

// lookup 按 appname 查安装位置（大小写不敏感）。缓存 1 分钟；
// 命中后仍 os.Stat 校验目录存在，卸载/迁移后立刻失效。
func (c *installLocCacheT) lookup(appName string) installLocation {
	key := strings.ToLower(strings.TrimSpace(appName))
	if key == "" {
		return installLocation{}
	}
	c.mu.Lock()
	if c.byName == nil || time.Since(c.at) > time.Minute {
		c.byName = scanInstallLocations()
		c.at = time.Now()
	}
	loc := c.byName[key]
	c.mu.Unlock()
	if loc.Path != "" {
		if fi, err := os.Stat(loc.Path); err == nil && fi.IsDir() {
			return loc
		}
	}
	return installLocation{}
}

// scanInstallLocations 扫 /vol1..8/@appcenter 与 /var/apps 建 <小写 appname> → 位置。
// 每个卷一次 ReadDir（几十~几百条），按目录名去重；@appcenter 优先于 /var/apps。
func scanInstallLocations() map[string]installLocation {
	m := make(map[string]installLocation, 128)
	for v := 1; v <= 8; v++ {
		base := fmt.Sprintf("/vol%d/@appcenter", v)
		ents, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			k := strings.ToLower(e.Name())
			if _, ok := m[k]; !ok {
				m[k] = installLocation{Volume: v, Path: base + "/" + e.Name()}
			}
		}
	}
	if ents, err := os.ReadDir("/var/apps"); err == nil {
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			k := strings.ToLower(e.Name())
			if _, ok := m[k]; !ok {
				m[k] = installLocation{Volume: 0, Path: "/var/apps/" + e.Name()}
			}
		}
	}
	return m
}
