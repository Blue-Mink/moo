package api

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// processMemoryStats 采集进程级内存快照：
//   - rss_bytes：/proc/self/statm 的常驻集（Linux；读不到时 0，前端隐藏该行）
//   - heap_bytes：Go 堆已分配（HeapAlloc）
//   - sys_bytes：Go 运行时向 OS 申请的总内存（Sys）
//   - goroutines：当前协程数
//
// 只读轻量，不做任何网络/磁盘扫描；供关于页「内存占用」实时轮询使用。
func processMemoryStats() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	var rss uint64
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 2 {
			if pages, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				rss = pages * uint64(os.Getpagesize())
			}
		}
	}
	return map[string]any{
		"rss_bytes":  rss,
		"heap_bytes": ms.HeapAlloc,
		"sys_bytes":  ms.Sys,
		"goroutines": runtime.NumGoroutine(),
	}
}

// aboutMemGet GET /api/about/mem：关于页版本信息里的「内存占用」实时数据。
// 与 /api/about 分离，轮询不会触发远程清单刷新。
func (s *Server) aboutMemGet(w http.ResponseWriter, r *http.Request) {
	// 实时轮询：禁止浏览器/中间层缓存，保证读到的是当前进程快照。
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, processMemoryStats())
}
