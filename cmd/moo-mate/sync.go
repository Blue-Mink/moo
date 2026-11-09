package main

import (
	"context"
	"strings"
	"time"
)

// runFullSync 执行一次完整同步：索引里全部 GitHub/Gitee 来源应用的上游检查，
// 最后统一重建聚合并触发 Moo 源同步。记录同步状态供 /api/status 与定时循环复用。
// 单个应用上游检查失败不阻塞整体（下一轮重试）。
func (c *companion) runFullSync() (checked, updated int, err error) {
	ctx, cancel := timeoutCtx(context.Background(), 15*time.Minute)
	defer cancel()

	// 同一轮里同一来源仓库只扫一次：多应用共源时省掉重复的 release/文件树请求。
	discCache := map[string][]discoveredApp{}
	for _, ia := range c.idx.list() {
		if strings.TrimSpace(ia.Source) == "" {
			continue
		}
		checked++
		res, cerr := c.checkAppUpdateCoreCached(ctx, ia, discCache)
		if cerr != nil {
			continue
		}
		if res.updated {
			updated++
		}
	}

	// 重建聚合 + 触发 Moo 源同步（整轮一次）。
	_, rerr := c.rebuildAggregate()
	c.syncStateMu.Lock()
	c.lastSync = time.Now()
	if rerr != nil {
		c.syncErr = rerr.Error()
	} else {
		c.syncErr = ""
	}
	c.syncStateMu.Unlock()
	if rerr != nil {
		return checked, updated, rerr
	}
	if c.moo.ping() == nil {
		if src, serr := c.ensureMateSource(); serr == nil {
			_, _ = c.moo.syncSource(src.ID)
		}
	}
	return checked, updated, nil
}

// syncState 返回当前同步状态（供 /api/status 透出）。
func (c *companion) syncState() (last time.Time, errMsg string) {
	c.syncStateMu.Lock()
	defer c.syncStateMu.Unlock()
	return c.lastSync, c.syncErr
}
