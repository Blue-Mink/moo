package task

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"moo/internal/netguard"
)

// ProbeContentLength 按候选顺序探测文件总大小（Content-Length）。
// 用途：aria2 从没有 Content-Length 的镜像（chunked 响应）下载时不会
// 报告总大小，任务 Size 恒为 0，前端算不出下载百分率——用启动前的
// HEAD 探测把尺寸补上。全部失败返回 0（前端回退显示「已下载大小」）。
func ProbeContentLength(candidates []string) int64 {
	client := &http.Client{Timeout: 5 * time.Second}
	for _, u := range candidates {
		if u == "" {
			continue
		}
		if _, gerr := netguard.Public(u); gerr != nil {
			continue // 内网/非法协议候选不探测
		}
		if n := probeOne(client, u); n > 0 {
			return n
		}
	}
	return 0
}

func probeOne(client *http.Client, u string) int64 {
	req, err := http.NewRequest(http.MethodHead, u, nil)
	if err != nil {
		return 0
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		cl := resp.Header.Get("Content-Length")
		resp.Body.Close()
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil && n > 0 {
			return n
		}
		return 0
	case http.StatusMethodNotAllowed:
		// 部分 CDN 对 HEAD 返回 405：降级 GET + Range: bytes=0-0，
		// 从 Content-Range 的 total 或 Content-Length 取尺寸。
		resp.Body.Close()
		return probeRange(client, u)
	default:
		resp.Body.Close()
		return 0
	}
}

func probeRange(client *http.Client, u string) int64 {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// "bytes 0-0/12345678"
		cr := resp.Header.Get("Content-Range")
		if i := strings.LastIndex(cr, "/"); i >= 0 && i+1 < len(cr) {
			if n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64); err == nil && n > 0 {
				return n
			}
		}
	case http.StatusOK:
		// 不支持 Range：返回全量，Content-Length 即总大小
		if n, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}
