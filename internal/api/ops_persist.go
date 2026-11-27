package api

// P0-2：操作状态落盘（借鉴 FnDepot 的 downloads/updates 持久化）。
//
// 背景：Moo 的 operation.Queue 只在内存里—服务重启（应用更新自重启、
// 崩溃、面板重载）后 UI 什么都看不到，用户无法判断「刚才那次操作到底
// 成功没有」。这里把 Queue.Snapshot() 定期落盘到 <data>/operations.json，
// 启动时恢复；恢复时上一条 running/queued 必然已中断，由
// Queue.RestoreHistory 改写为 error（原因写明「服务重启中断」）。
//
// 写盘策略：只在「入队 / 终态 / 删除」时写（不进度的每 700ms 抖动），
// 文件 0600、tmp+rename 原子替换；失败只记日志，不影响操作本身。

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"moo/internal/config"
	"moo/internal/operation"
)

const opsPersistFile = "operations.json"

// opsPersistMaxBytes 读回上限：正常快照（history ≤ 20 条）< 100KB，
// 这里给 2MB 余量。超过视为损坏/被替换，直接按「无快照」处理，
// 避免异常大文件被整体读进内存（黑名单已有同类上限，P2 对齐）。
const opsPersistMaxBytes int64 = 2 << 20

// OpsPersistPath 返回操作快照的默认落盘路径（<data>/operations.json）。
func OpsPersistPath() string {
	return filepath.Join(config.DataDir(), opsPersistFile)
}

// opsPersister 串行化写盘：Start/终态/删除可能并发触发回调，避免互相踩踏。
type opsPersister struct {
	path string
	mu   sync.Mutex
}

// NewOpsPersister 创建一个落盘器；path 为空时用 OpsPersistPath()。
func NewOpsPersister(path string) *opsPersister {
	if path == "" {
		path = OpsPersistPath()
	}
	return &opsPersister{path: path}
}

// Save 原子写入一份快照（tmp + rename）。失败只记日志：持久化是增强功能，
// 不能让它反过来影响安装/更新主流程。
func (p *opsPersister) Save(vs []operation.View) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if vs == nil {
		vs = []operation.View{}
	}
	// 2026-11-26（批次 B）：写盘前过数据目录守卫。shell 继承的 TRIM_PKGVAR
	// 若指向别的应用目录，单测（以及误启动的进程）会把 operations.json
	// 覆盖进别人的数据目录（实测把某目录的 operations.json 写成 []）。
	if dir := filepath.Dir(p.path); dir != "" {
		if err := config.CheckDataDir(dir); err != nil {
			log.Printf("[ops-persist] 拒绝写入 %s: %v", dir, err)
			return
		}
	}
	b, err := json.Marshal(vs)
	if err != nil {
		log.Printf("[ops-persist] 序列化失败: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		log.Printf("[ops-persist] 建目录失败: %v", err)
		return
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("[ops-persist] 写临时文件失败: %v", err)
		return
	}
	if err := os.Rename(tmp, p.path); err != nil {
		log.Printf("[ops-persist] 原子替换失败: %v", err)
	}
}

// RestoreOperations 读取上一次的快照并恢复到队列里。
// 返回 (恢复条数, 被标记为「重启中断」的条数)。文件不存在/损坏 = 静默跳过
// （损坏时记一条日志），不影响启动。
func RestoreOperations(q *operation.Queue, path string) (int, int) {
	if q == nil {
		return 0, 0
	}
	if path == "" {
		path = OpsPersistPath()
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > opsPersistMaxBytes {
		log.Printf("[ops-persist] 快照过大（%d 字节 > %d），按无快照处理", fi.Size(), opsPersistMaxBytes)
		return 0, 0
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ops-persist] 读取快照失败（忽略）: %v", err)
		}
		return 0, 0
	}
	var vs []operation.View
	if err := json.Unmarshal(b, &vs); err != nil {
		log.Printf("[ops-persist] 快照损坏（忽略）: %v", err)
		return 0, 0
	}
	kept := make([]operation.View, 0, len(vs))
	for _, v := range vs {
		if v.ID == "" { // 防御：空条目会污染 history / 前端 key
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == 0 {
		return 0, 0
	}
	return len(kept), q.RestoreHistory(kept)
}
