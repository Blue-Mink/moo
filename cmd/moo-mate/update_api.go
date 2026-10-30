package main

import (
	"net/http"
	"sync"
	"time"
)

// mateUpdateTTL 探测成功结果的缓存时长：6 小时内直接复用（?force=1 强制重探）。
const mateUpdateTTL = 6 * time.Hour

// mateUpdateErrTTL 探测失败的缓存时长：只 10 分钟。
// 出口 IP 的 API 配额是滚动恢复的，失败若也缓存 6 小时，版本窗口会一直挂着
// 「探测失败」直到进程重启——比不缓存还糟。
const mateUpdateErrTTL = 10 * time.Minute

var mateUpdateCache struct {
	mu   sync.Mutex
	at   time.Time
	info mateUpdateInfo
}

// mateCacheTTL 结果该缓存多久：成功 6h，失败 10min。
func mateCacheTTL(info mateUpdateInfo) time.Duration {
	if info.LastError != "" {
		return mateUpdateErrTTL
	}
	return mateUpdateTTL
}

// mateUpdateCached 返回探测结果（进程内缓存 + 单飞：mux 串行保护，不会并发重复探测）。
// token 由调用方从 Moo 侧取（githubTokenWithSource），只用于官方 API 通道。
func mateUpdateCached(force bool, token string) mateUpdateInfo {
	mateUpdateCache.mu.Lock()
	defer mateUpdateCache.mu.Unlock()
	if !force && mateUpdateCache.info.CurrentVersion != "" && time.Since(mateUpdateCache.at) < mateCacheTTL(mateUpdateCache.info) {
		return mateUpdateCache.info
	}
	info := probeMateUpdate(buildVersion, token)
	mateUpdateCache.at = time.Now()
	mateUpdateCache.info = info
	return info
}

// handleUpdateCheck GET /api/update-check[?force=1]：版本窗口三态数据源。
func (c *companion) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	tok, _, _ := c.githubTokenWithSource()
	writeJSON(w, http.StatusOK, mateUpdateCached(force, tok))
}

// warmMateUpdate 启动后后台预热一次，首个请求直接命中缓存（不阻塞首屏）。
func (c *companion) warmMateUpdate() {
	go func() {
		time.Sleep(3 * time.Second)
		tok, _, _ := c.githubTokenWithSource()
		_ = mateUpdateCached(false, tok)
	}()
}
