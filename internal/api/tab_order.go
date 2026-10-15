package api

import (
	"errors"
)

// 可排序 tab 清单（0.6.122 系统设置「Dock 栏排序 / 设置 tab 排序」）。
// 前端按相同 key 渲染；新增/删除 tab 时需同步前后端两处清单。

// DockTabKeys 移动端底部 dock 主导航（默认顺序）。
// 0.6.325 C11：settings 末位——0.6.325 C6 把设置移入 dock 末位，C11 起也纳入排序。
var DockTabKeys = []string{"recommended", "all", "installed", "update_available", "settings"}

// legacyDockTabKeys 0.6.322 及以前的 4 键清单（settings 未入 dock）。
// resolveOrderWithLegacy / normalizeOrderField 用它做老配置迁移：
// 老用户存量的 4 键自定义序不被判「非全排列」而重置。
var legacyDockTabKeys = []string{"recommended", "all", "installed", "update_available"}

// SettingsTabKeys 设置页 tab（默认顺序）。
var SettingsTabKeys = []string{"system", "accel", "source", "backup", "notify", "log", "about"}

// resolveOrder 解析自定义顺序：空 = 默认；非默认键的全排列 = 默认（不信任脏数据）。
func resolveOrder(saved, defaults []string) []string {
	return resolveOrderWithLegacy(saved, defaults, nil)
}

// resolveOrderWithLegacy 同 resolveOrder，另支持 legacy 旧清单迁移：
// saved 是 defaults 的全排列 → 原样返回；
// saved 是 legacy 的全排列 → 保留 saved 原有顺序，把 defaults 里 saved
// 没有的键按 defaults 顺序追加到末尾（老用户顺序不丢，新 tab 落其默认相对位）；
// 其它 → 默认。
func resolveOrderWithLegacy(saved, defaults, legacy []string) []string {
	if len(saved) == 0 {
		return defaults
	}
	if isPermutation(saved, defaults) {
		out := make([]string, len(saved))
		copy(out, saved)
		return out
	}
	if len(legacy) > 0 && isPermutation(saved, legacy) {
		return appendMissingKeys(saved, defaults)
	}
	return defaults
}

// appendMissingKeys 保留 kept 原有顺序，追加 defaults 中 kept 未包含的键
// （按 defaults 顺序）。
func appendMissingKeys(kept, defaults []string) []string {
	inKept := make(map[string]bool, len(kept))
	for _, k := range kept {
		inKept[k] = true
	}
	out := make([]string, 0, len(defaults))
	out = append(out, kept...)
	for _, k := range defaults {
		if !inKept[k] {
			out = append(out, k)
		}
	}
	return out
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

// normalizeOrderField PUT /api/settings 的订单「校验并归一化」：nil = 未提交
// 不改动（返回 nil, nil）；非 nil 必须是非空且为 defaults 全排列（原样存），
// 或是 legacy 全排列（归一化：原顺序 + 缺的新键按 defaults 顺序追加，兼容
// 旧前端/旧脚本提交的 4 键）；其它 → 报错。返回可存储的归一化顺序。
func normalizeOrderField(name string, submitted, defaults, legacy []string) ([]string, error) {
	if submitted == nil {
		return nil, nil
	}
	if len(submitted) == 0 {
		return nil, errors.New(name + " 不能为空数组（要恢复默认请传默认顺序）")
	}
	if isPermutation(submitted, defaults) {
		out := make([]string, len(submitted))
		copy(out, submitted)
		return out, nil
	}
	if len(legacy) > 0 && isPermutation(submitted, legacy) {
		return appendMissingKeys(submitted, defaults), nil
	}
	want := make([]string, len(defaults))
	copy(want, defaults)
	return nil, errors.New(name + " 必须是以下 key 的完整排列: " + joinKeys(want))
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
