package main

// MM-02（0.2.52）：浏览器侧 CSRF 防线。
//
// 背景：Mate 的写接口（改数据目录、清空索引、批量删除、恢复备份等）没有
// 任何同源校验；任意网页都能向 http://127.0.0.1:38108 发简单请求（text/plain
// 或空体 form）触发它们。本文件只解决「请求是否来自本站页面」，鉴权属 MM-01。
//
// 判据优先级：
//  1. Sec-Fetch-Site 跨站/同站 → 拒绝（浏览器不可由 JS 伪造）；
//  2. 无 Sec-Fetch-Site 时比 Origin（缺则 Referer）与请求 Host 是否同源；
//  3. 带请求体必须是 application/json（multipart 仅限上传备份白名单）；
//  4. TCP 直连通道的 Host 必须是回环/白名单，防 DNS rebinding 绕过。
// 非浏览器客户端（curl / 脚本 / Moo 后端）既无 Sec-Fetch-* 也无 Origin，
// 一律放行：本层只处理浏览器 CSRF，不能代替鉴权。

import (
	"log"
	"mime"
	"net/http"
	"strings"
)

// csrfPolicy 是一条入口通道的防 CSRF 策略。
type csrfPolicy struct {
	name           string
	loopbackOnly   bool             // 要求 Host 为回环/白名单（TCP 直连通道）
	allowedHosts   []string         // 额外允许的 host[:port]（selfBase / 监听地址）
	multipartPaths map[string]bool  // 允许 multipart/form-data 的路径
	maxBodyBytes   int64            // 0.2.53：默认请求体上限（0 = 不限制）
	bodyLimits     map[string]int64 // 0.2.53：单路径覆盖（上传备份等）
}

// csrfGuard 包装 handler：先做通道级 Host 校验（含读方法），再按方法判定。
func csrfGuard(next http.Handler, p csrfPolicy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 0.2.54（B1）：所有响应统一禁止 MIME 嗅探（页面 / JSON / 图标类型都正确，
		// 加这条不影响渲染，只防浏览器把未知类型当 HTML/JS 执行）。
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// 0.2.53（A1）：Host 校验覆盖**所有方法**。此前只在写方法分支执行，
		// 浏览器被 DNS rebinding 后可读 /api/index、/api/logs、/api/config（实测 200）。
		if p.loopbackOnly && !hostAllowed(r.Host, p.allowedHosts) {
			csrfDeny(w, r, p, http.StatusForbidden, "Host 不在允许范围（防 DNS rebinding）")
			return
		}
		// 0.2.53（A2）：统一请求体上限（默认 1MB；上传备份单独放宽）。
		limitRequestBody(w, r, p)
		if methodIsSafe(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
		case "same-origin", "none":
			// 浏览器声明同源（同源 fetch / 地址栏直达），继续做体检查。
		case "cross-site", "same-site":
			csrfDeny(w, r, p, http.StatusForbidden, "跨站请求被拒绝")
			return
		default:
			if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
				if !sameOrigin(origin, r.Host, p.allowedHosts) {
					csrfDeny(w, r, p, http.StatusForbidden, "Origin 与本站不同源")
					return
				}
			} else if ref := strings.TrimSpace(r.Header.Get("Referer")); ref != "" {
				if !sameOrigin(ref, r.Host, p.allowedHosts) {
					csrfDeny(w, r, p, http.StatusForbidden, "Referer 与本站不同源")
					return
				}
			}
		}
		if hasRequestBody(r) && !p.multipartPaths[r.URL.Path] {
			if !isJSONContentType(r.Header.Get("Content-Type")) {
				csrfDeny(w, r, p, http.StatusUnsupportedMediaType, "Content-Type 必须是 application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func methodIsSafe(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

func hasRequestBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	if r.ContentLength > 0 {
		return true
	}
	return r.ContentLength < 0 // -1 = chunked
}

// limitRequestBody 0.2.53（A2）：给请求体套上限，防单个巨大 JSON 把内存放大。
// 正常接口的载荷都是 KB 级；上传备份走 bodyLimits 单独放宽（与 restore.go 内的
// maxRestoreUploadBytes 一致，内层还会再限一次，先触发者生效）。
func limitRequestBody(w http.ResponseWriter, r *http.Request, p csrfPolicy) {
	if r.Body == nil || r.Body == http.NoBody {
		return
	}
	limit := p.maxBodyBytes
	if v, ok := p.bodyLimits[r.URL.Path]; ok {
		limit = v
	}
	if limit > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
}

func isJSONContentType(ct string) bool {
	ct = strings.TrimSpace(ct)
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return strings.EqualFold(mt, "application/json")
}

func csrfDeny(w http.ResponseWriter, r *http.Request, p csrfPolicy, code int, reason string) {
	log.Printf("拒绝可疑请求[%s]：%s %s（%s；Origin=%q Referer=%q Sec-Fetch-Site=%q Host=%q）",
		p.name, r.Method, r.URL.Path, reason,
		r.Header.Get("Origin"), r.Header.Get("Referer"),
		r.Header.Get("Sec-Fetch-Site"), r.Host)
	writeErr(w, code, reason)
}
