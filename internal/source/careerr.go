package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// P1-3（2026-11-26 CARE）：/api/sources 的 last_error 文案。
//
// 只做「展示层」压缩：错误串里的完整候选 URL（含镜像前缀、源地址里的
// userinfo/query）一律脱敏——源列表是给用户看的，不能因为一条错误把
// 带凭据的地址或内网镜像暴露在界面上、日志里。

var (
	// scheme://user:pass@host → scheme://host
	careURLUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s@]+@`)
	// ?token=…&sig=… → 整段丢弃（源索引地址不需要 query 才能表达身份）
	careURLQueryRe = regexp.MustCompile(`\?[^\s"'）)，,]*`)
	careSpaceRe    = regexp.MustCompile(`\s+`)
)

// sanitizeSourceError 去掉凭据与 query、压平空白、截断。
func sanitizeSourceError(s string) string {
	if s == "" {
		return ""
	}
	s = careURLUserinfoRe.ReplaceAllString(s, "$1")
	s = careURLQueryRe.ReplaceAllString(s, "")
	s = careSpaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	return truncateRunes(s, 200)
}

// truncateRunes 按 rune 截断（中文不被砍成半个字符）。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// describeFetchError 把刷新错误压成一句人话（P1-3）。
// 只用于展示，**不参与**「是否永久失效」的判定（判定见 probeCareVerdict）。
func describeFetchError(err error) string {
	if err == nil {
		return ""
	}
	s := sanitizeSourceError(err.Error())
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "http 404") || strings.Contains(low, "404 not found"):
		return "仓库不存在（HTTP 404）"
	case strings.Contains(low, "http 410") || strings.Contains(low, "410 gone"):
		return "源地址已失效（HTTP 410）"
	case strings.Contains(low, "http 429") || strings.Contains(low, "too many requests"):
		return "被镜像限流（HTTP 429），稍后自动重试"
	case containsHTTP5xx(low):
		return "源站服务端错误（HTTP 5xx），稍后自动重试"
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(s, "超时"):
		return "连接超时"
	case strings.Contains(low, "tls") || strings.Contains(low, "x509") || strings.Contains(s, "证书"):
		return "TLS/证书错误"
	case strings.Contains(low, "no such host") || strings.Contains(low, "connection refused") ||
		strings.Contains(low, "connection reset") || strings.Contains(low, "broken pipe"):
		return "网络不可达"
	case strings.Contains(s, "无候选地址"):
		return "源地址无法解析出索引候选"
	}
	return truncateRunes(s, 160)
}

// containsHTTP5xx 判定错误串里是否出现 5xx 状态码。
func containsHTTP5xx(low string) bool {
	for _, code := range []string{"http 500", "http 502", "http 503", "http 504", "http 52"} {
		if strings.Contains(low, code) {
			return true
		}
	}
	return false
}

// writeCareFile 原子写（tmp + rename），目录缺失时创建。
func writeCareFile(path string, f careFile) error {
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 唯一临时名：同步 SaveCare 与异步 saveCareAsync 可能并发落盘，
	// 共用 `<path>.tmp` 会互相覆盖/删除（rename 报 no such file）。
	tf, err := os.CreateTemp(filepath.Dir(path), ".care-*.tmp")
	if err != nil {
		return err
	}
	tmp := tf.Name()
	if _, err := tf.Write(raw); err != nil {
		tf.Close()
		os.Remove(tmp)
		return err
	}
	if err := tf.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 0600（批次 1 F2 / N4）：care 状态文件里含源地址/失败原因，
	// 与 config.json / 会话文件同口径按「仅属主可读写」落盘。
	_ = os.Chmod(tmp, 0o600)
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// careSavedAt 只用于测试断言「文件里确实写了时间戳」。
func careSavedAt(f careFile) time.Time {
	t, _ := time.Parse(time.RFC3339, f.SavedAt)
	return t
}
