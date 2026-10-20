package source

import (
	"strconv"
	"strings"
)

// 0.6.330：版本比较的**唯一实现**（此前 api 与 source 各有一套，口径不同：
// api 丢弃 `-N` 修订号 → 判等；source 按字符串比后缀 → `-10 < -9`。
// wb2api 1.13.0→1.13.0-1 徽章不亮、以及「亮了却装不上」都源于此。）
//
// 解析规则（全部小写、去首尾空白、剥前导 v）：
//  1. 数字核心：开头连续的数字与 `.`；`_` 两侧都是数字时视作分隔符（1.8.0_492）。
//  2. 核心之后：
//     - `-rc.N` / `-rc1` / `-beta2` / `-alpha` / `-dev` / `-pre` / `-snapshot` → 预发布；
//     - `-<纯数字>` → 数字修订号（fnOS/Debian 的 `-1`、`-10`），参与数值比较；
//     - 其它后缀（`-x86`、`-panel`、`+build5`、`_stable_2026-10-07`）→ 忽略；
//  3. 标签前缀（`latest-2026.10.17`、`release-…`、`stable-…`）→ 不可解析：
//     这类串无法与真实版本比大小，一律按「不可比」处理，绝不据此判更新。
//
// 比较顺序：核心（缺段按 0）→ 预发布（有 pre < 无 pre；两者都有按标识符比）
// → 数字修订号（缺省 0）。任一侧不可解析 → 返回 0（不可比，不判更新）。

// Version 版本解析结果。
type Version struct {
	Core []int
	Pre  []string
	Rev  int
	OK   bool
}

var preTags = map[string]bool{
	"rc": true, "beta": true, "alpha": true, "dev": true,
	"pre": true, "preview": true, "snapshot": true, "next": true,
}

var labelPrefixes = []string{"latest-", "release-", "stable-", "nightly-", "master-", "head-", "dev-"}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// ParseVersion 解析版本串。OK=false 表示不可解析（内容为空 / 无数字核心 / 段畸形 /
// 标签前缀），调用方据此跳过版本判定。
func ParseVersion(raw string) Version {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return Version{}
	}
	s = strings.TrimPrefix(s, "v")
	for _, p := range labelPrefixes {
		if strings.HasPrefix(s, p) {
			return Version{}
		}
	}
	// 数字核心
	var b strings.Builder
	i := 0
	for i < len(s) {
		c := s[i]
		if isDigitByte(c) || c == '.' {
			b.WriteByte(c)
			i++
			continue
		}
		if c == '_' && i > 0 && i+1 < len(s) && isDigitByte(s[i-1]) && isDigitByte(s[i+1]) {
			b.WriteByte('.')
			i++
			continue
		}
		break
	}
	if b.Len() == 0 {
		return Version{}
	}
	parts := strings.Split(b.String(), ".")
	core := make([]int, len(parts))
	for k, p := range parts {
		if p == "" {
			return Version{} // "1..2" / "1.2." / ".1"
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return Version{}
		}
		core[k] = n
	}
	v := Version{Core: core, OK: true}
	rest := s[i:]
	rest = strings.TrimPrefix(rest, "-")
	rest = strings.TrimPrefix(rest, "_")
	if rest == "" {
		return v
	}
	// 预发布标记：字母前缀 + 可选数字标识符
	j := 0
	for j < len(rest) && (rest[j] >= 'a' && rest[j] <= 'z') {
		j++
	}
	if j > 0 && preTags[rest[:j]] {
		v.Pre = []string{rest[:j]}
		tail := rest[j:]
		tail = strings.TrimLeft(tail, ".-_")
		for _, seg := range strings.FieldsFunc(tail, func(r rune) bool { return r == '.' || r == '-' || r == '_' }) {
			if seg != "" {
				v.Pre = append(v.Pre, seg)
			}
		}
		return v
	}
	// 纯数字后缀 = 修订号
	if n, err := strconv.Atoi(rest); err == nil {
		v.Rev = n
	}
	// 其它后缀忽略（-x86 / +build5 / _stable_2026-10-07 等）
	return v
}

// CompareVersion 返回 -1/0/1。任一侧不可解析时返回 0（不可比）。
func CompareVersion(a, b string) int {
	av, bv := ParseVersion(a), ParseVersion(b)
	if !av.OK || !bv.OK {
		return 0
	}
	n := len(av.Core)
	if len(bv.Core) > n {
		n = len(bv.Core)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(av.Core) {
			x = av.Core[i]
		}
		if i < len(bv.Core) {
			y = bv.Core[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	// 预发布 < 正式版
	switch {
	case len(av.Pre) == 0 && len(bv.Pre) > 0:
		return 1
	case len(av.Pre) > 0 && len(bv.Pre) == 0:
		return -1
	case len(av.Pre) > 0:
		if c := comparePre(av.Pre, bv.Pre); c != 0 {
			return c
		}
	}
	if av.Rev != bv.Rev {
		if av.Rev < bv.Rev {
			return -1
		}
		return 1
	}
	return 0
}

// comparePre 预发布标识符比较：数字段按数值、数字 < 非数字、段数少者更小。
func comparePre(a, b []string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // 数字 < 非数字
		case yerr == nil:
			return 1
		default:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// VersionLess 报告版本号 a 是否低于 b（不可解析视为相等）。
func VersionLess(a, b string) bool { return CompareVersion(a, b) < 0 }

// VersionEqual 版本相等（不可解析视为相等）。
func VersionEqual(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return CompareVersion(a, b) == 0
}

// IsNewer 报告 a 是否严格高于 b（任一为空 / 不可解析视为不比对方新）。
func IsNewer(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return CompareVersion(a, b) > 0
}
