package task

import (
	"strconv"
	"strings"
)

// SizeTolerance 成品大小与源声明大小的允许偏差（~5KB 舍入 + 少量源误差）。
const SizeTolerance = 1 << 20

// ParseSizeMB 把 "12.3" MB 字符串同时按十进制（1e6）与二进制（1MiB）解释，
// 返回全部 >0 的候选字节数。
// 2026-09-22 实测：同一源（Blue-Mink）内单位不一致——daidai "19.31" 是十进制
// MB（差 3.9KB），new-api "38.47" 是 MiB（差 620B）。单单位解释会误判。
func ParseSizeMB(s string) []int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return nil
	}
	out := []int64{int64(v * 1000 * 1000)}
	bi := int64(v * 1048576)
	if bi != out[0] {
		out = append(out, bi)
	}
	return out
}

// SizeMatches 成品大小是否与任一声明候选一致（无候选时恒真）。
func SizeMatches(actual int64, declaredCandidates ...int64) bool {
	if len(declaredCandidates) == 0 {
		return true
	}
	for _, d := range declaredCandidates {
		if d <= 0 {
			continue
		}
		diff := actual - d
		if diff < 0 {
			diff = -diff
		}
		if diff <= SizeTolerance {
			return true
		}
	}
	return false
}
