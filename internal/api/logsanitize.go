package api

// 2026-11-28 门 2 审计 SHOULD-1/SHOULD-2：日志脱敏。
//
// 背景：测速摘要与源列表同步的失败行会把 URL / 远端错误原文写进 moo.log，
// 而自定义镜像或清单地址可能带 `?token=…&sig=…` 或 userinfo —— 直接落盘等于
// 把凭据写进日志。internal/source 侧已有 sanitizeSourceError（不导出），
// 这里给 api 包一份同口径实现：
//   1. 去掉 URL userinfo（`scheme://user:pass@` → `scheme://`）
//   2. 丢弃整个 query 段（token / sig / access_token 常藏于此）
//   3. 压平空白（远端错误里的换行会把一行日志拆成多行）
//   4. 按 rune 截断（中文不被砍成半个字符）

import (
	"regexp"
	"strings"
)

var (
	// logURLUserinfoRe 匹配 `scheme://userinfo@`，保留 scheme 与后续路径。
	logURLUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s@]+@`)
	// logURLQueryRe 匹配 `?…` 到空白/引号/右括号/中文标点为止。
	logURLQueryRe = regexp.MustCompile(`\?[^\s"'\x{2019}\x{201D}）)，,]*`)
	// logSpaceRe 把任意连续空白（含换行、制表）压成一个空格。
	logSpaceRe = regexp.MustCompile(`\s+`)
)

// sanitizeLogText 脱敏 + 压平 + 截断，供日志直接拼串使用（maxRunes<=0 不截断）。
func sanitizeLogText(s string, maxRunes int) string {
	if s == "" {
		return ""
	}
	s = logURLUserinfoRe.ReplaceAllString(s, "$1")
	s = logURLQueryRe.ReplaceAllString(s, "")
	s = logSpaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	if maxRunes <= 0 {
		return s
	}
	return truncateRunes(s, maxRunes)
}
