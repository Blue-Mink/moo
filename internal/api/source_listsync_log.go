package api

// 2026-11-26 B-SRC-LISTSYNC：源列表同步的日志口径（与状态/退避分文件）。
// 纪律：**每轮恰一行摘要**，失败逐条一行；单行 ≤200 字，不逐源刷屏。

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// 单行预算：前缀「[list-sync] 跳过 」15 + URL 60 + 「：」1 + 原因 120 = 196 ≤ 200。
const (
	maxListSyncURLRunes    = 60
	maxListSyncReasonRunes = 120
)

// shortenListSyncText 压成单行并截断（URL/错误里可能带换行，日志必须一行）。
func shortenListSyncText(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// logListSyncRound 每轮恰一行摘要；失败逐条「[list-sync] 跳过 <URL>：<原因>」。
func logListSyncRound(res sourceListSyncResult, failURL map[string]string, dur time.Duration) {
	parts := []string{
		fmt.Sprintf("列表 %d 条", res.Fetched),
		fmt.Sprintf("已存在 %d", res.Already),
		fmt.Sprintf("新增 %d", res.Added),
		fmt.Sprintf("失败 %d", res.Failed),
	}
	if res.Backoff > 0 {
		parts = append(parts, fmt.Sprintf("退避跳过 %d", res.Backoff))
	}
	log.Printf("[list-sync] 本轮完成：%s / 耗时 %s", strings.Join(parts, " / "), dur.Round(time.Millisecond))
	if len(failURL) == 0 {
		return
	}
	urls := make([]string, 0, len(failURL))
	for u := range failURL {
		urls = append(urls, u)
	}
	sort.Strings(urls)
	for _, u := range urls {
		// 2026-11-28 门 2 审计 SHOULD-2：URL/reason 都可能带 query 凭据，且不得超单行上限。
		log.Printf("[list-sync] 跳过 %s：%s", sanitizeLogText(u, maxListSyncURLRunes), sanitizeLogText(failURL[u], maxListSyncReasonRunes))
	}
}
