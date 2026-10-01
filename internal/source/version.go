package source

// VersionLess 报告版本号 a 是否低于 b（复用 fndepot.go 的 versionLess 语义：
// 去 v 前缀、点分数字段比较、rc 后缀字典序兜底）。
func VersionLess(a, b string) bool { return versionLess(a, b) }

// VersionEqual 版本相等（忽略 v 前缀与 rc 后缀差异）。
func VersionEqual(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return !VersionLess(a, b) && !VersionLess(b, a)
}

// IsNewer 报告 a 是否严格高于 b（版本比较；任一为空视为不比对方新）。
func IsNewer(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return VersionLess(b, a)
}
