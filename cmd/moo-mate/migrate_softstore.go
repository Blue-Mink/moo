package main

import (
	"os"
	"time"
)

// purgeSoftstoreEntries 一次性历史清理：删除索引里遗留的软仓（softstore）来源条目。
//
// 背景：软仓分发形态已从 Moo Mate 整体移除（后端抓取/前端入口/默认清单），
// 但存量 index.json 里可能仍带 Publisher=="softstore" 的历史条目，留着会：
// ① 在「应用来源」分组里露出已无维护的软仓分组；② 定时同步仍去探测死链。
//
// 行为：先复制 index.json 为 index.json.bak-softstore-<yyyymmdd-HHMMSS>，
// 再删除这些条目并落盘；返回删除条数与备份路径。
// 幂等：无匹配条目时直接返回 (0, "", nil)，不写盘、不打日志。
// 失败不阻断启动：备份失败即放弃删除（宁可不清理，也不做无备份的破坏性删除），
// 由调用方记日志后继续启动。
func (ix *index) purgeSoftstoreEntries() (int, string, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	var names []string
	for name, a := range ix.doc.Apps {
		if a != nil && a.Publisher == "softstore" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return 0, "", nil // 幂等：已清理过或本来就没有
	}

	orig, err := os.ReadFile(ix.path)
	if err != nil {
		return 0, "", err
	}
	backup := ix.path + ".bak-softstore-" + time.Now().Format("20060102-150405")
	if werr := os.WriteFile(backup, orig, 0o644); werr != nil {
		return 0, "", werr
	}

	for _, name := range names {
		delete(ix.doc.Apps, name)
	}
	if serr := ix.save(); serr != nil {
		return 0, backup, serr
	}
	return len(names), backup, nil
}
