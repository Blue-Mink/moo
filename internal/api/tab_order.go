package api

import (
	"errors"
)

// 可排序 tab 清单（0.6.122 系统设置「Dock 栏排序 / 设置 tab 排序」）。
// 前端按相同 key 渲染；新增/删除 tab 时需同步前后端两处清单。

// DockTabKeys 移动端底部 dock 主导航（默认顺序）。
var DockTabKeys = []string{"recommended", "all", "installed", "update_available"}

// SettingsTabKeys 设置页 tab（默认顺序）。
var SettingsTabKeys = []string{"system", "accel", "source", "backup", "notify", "log", "about"}

// resolveOrder 解析自定义顺序：空 = 默认；非默认键的全排列 = 默认（不信任脏数据）。
func resolveOrder(saved, defaults []string) []string {
	if len(saved) == 0 {
		return defaults
	}
	if isPermutation(saved, defaults) {
		out := make([]string, len(saved))
		copy(out, saved)
		return out
	}
	return defaults
}

// isPermutation saved 是否为 defaults 的排列（长度相同 + 元素一一对应）。
func isPermutation(saved, defaults []string) bool {
	if len(saved) != len(defaults) {
		return false
	}
	seen := make(map[string]bool, len(saved))
	for _, k := range defaults {
		seen[k] = true
	}
	for _, k := range saved {
		if !seen[k] {
			return false // 未知 key
		}
		delete(seen, k) // 二次出现 → 下次命中已删 key = 重复
	}
	return len(seen) == 0
}

// validateOrderField PUT /api/settings 的订单校验：nil = 未提交不改动；
// 非 nil 必须是非空且为 defaults 的全排列（前端拖完必给全量）。
func validateOrderField(name string, submitted, defaults []string) error {
	if submitted == nil {
		return nil
	}
	if len(submitted) == 0 {
		return errors.New(name + " 不能为空数组（要恢复默认请传默认顺序）")
	}
	if !isPermutation(submitted, defaults) {
		want := make([]string, len(defaults))
		copy(want, defaults)
		return errors.New(name + " 必须是以下 key 的完整排列: " + joinKeys(want))
	}
	return nil
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k
	}
	return out
}
