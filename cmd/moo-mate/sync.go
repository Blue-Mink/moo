package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

// errSyncBusy 已有一轮同步在跑时返回：不排队、不并行，让正在跑的那轮自然覆盖。
var errSyncBusy = errors.New("同步正在进行中")

// runFullSync 执行一次完整同步：索引里全部 GitHub 来源应用的上游检查，
// 最后统一重建聚合并触发 Moo 源同步。记录同步状态供 /api/status 与定时循环复用。
// 单个应用上游检查失败不阻塞整体（下一轮重试）。
func (c *companion) runFullSync() (checked, updated int, err error) {
	// 0.2.63（B8）：整轮同步互斥。两轮并行会各自持有代次、各自 upsert + applyCheckResults，
	// 后写者冲掉前者刚写的红点/检查状态（表现为「红点闪一下就没了」），也重复打上游接口。
	if !c.syncMu.TryLock() {
		return 0, 0, errSyncBusy
	}
	c.syncBusy.Store(true)
	defer func() {
		c.syncBusy.Store(false)
		c.syncMu.Unlock()
	}()
	ctx, cancel := timeoutCtx(context.Background(), 15*time.Minute)
	defer cancel()

	// 同一轮里同一来源仓库只扫一次：多应用共源时省掉重复的 release/文件树请求。
	discCache := map[string][]discoveredApp{}
	// 0.2.61b：整轮只在开头取一次索引代次。中途用户「清除同步索引」会推进代次，
	// 本轮的任何写回都会比对失败并放弃 —— 否则清除后几十秒应用又被旧快照写回来。
	gen := c.idx.generation()
	outs := make([]checkOutcome, 0, 8)
	for _, ia := range c.idx.list() {
		if strings.TrimSpace(ia.Source) == "" {
			continue
		}
		checked++
		res, cerr := c.checkAppUpdateCoreCachedGen(ctx, ia, discCache, gen)
		if cerr != nil {
			// 检查失败也要留痕：界面据此显示琥珀感叹号（可点击重试）。
			outs = append(outs, checkOutcome{Appname: ia.Appname, ErrMsg: cerr.Error()})
			continue
		}
		if res == nil {
			continue
		}
		outs = append(outs, checkOutcome{Appname: ia.Appname, Latest: res.to, Prev: res.prev, ErrMsg: res.checkErr})
		if res.updated {
			updated++
		}
	}
	// 整轮只落盘一次（应用多时避免几百次全量写 index.json）。
	if _, aerr := c.idx.applyCheckResults(outs, gen); aerr != nil {
		log.Printf("写入上游检查状态失败: %v", aerr)
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
		// 0.2.62：这里只同步「已经存在的」moo-mate 源，绝不自动创建。
		// 以前无条件 ensureMateSource() 会在用户删掉源之后又给建回来，
		// 「删除」形同虚设；建源只允许发生在写入新应用（handleBuild）那几个点。
		if src, serr := c.findMateSource(); serr == nil && src != nil {
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
