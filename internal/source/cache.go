package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// saveMu 串行化快照落盘：启动后 120 源并发刷新会同时触发 saveIfConfigured，
// 若多个 goroutine 共用同一 tmp 路径，WriteFile 会字节交错写出损坏 JSON
// （实测 2026-09-23 出现「完整 JSON + 脏尾巴」，导致 LoadCache 严格解析失败）。
var saveMu sync.Mutex

// 目录落盘快照（对标 FnDepot v0.5.1「修复首次打开客户端时内容显示不完整」：
// 它把整个目录持久化在 SQLite，首开/重启后 UI 立即从本地库渲染，后台再同步。
// Moo 此前目录纯内存态——重启后社区目录为空，直到用户手动检查。
// 这里用单文件 JSON 快照做等价效果：启动即恢复，自动刷新后自动落盘。）

type sourceCacheSnapshot struct {
	UpdatedAt string          `json:"updated_at"`
	Apps      map[string]*App `json:"apps"`
}

type cacheFile struct {
	SavedAt string                   `json:"saved_at"`
	Sources map[string]sourceCacheSnapshot `json:"sources"`
}

// SetCachePath 设置落盘路径（main 在构造后调用；空 = 禁用持久化，测试友好）。
func (m *Manager) SetCachePath(path string) {
	m.mu.Lock()
	m.cachePath = path
	m.mu.Unlock()
}

// LoadCache 启动时从快照恢复目录（只恢复仍配置中的源；
// 返回恢复的源数）。恢复的源带原拉取时间——「上次检查」显示真实旧值，
// 后台自动刷新跑完后自然变新。
func (m *Manager) LoadCache(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var cf cacheFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		// 容错：快照曾被并发写损坏（完整 JSON + 脏尾巴）时取第一个完整文档，
		// 避免启动秒开全丢、退化成 120 源全量重拉
		if derr := json.NewDecoder(bytes.NewReader(raw)).Decode(&cf); derr != nil {
			return 0
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for name, sc := range cf.Sources {
		if len(sc.Apps) == 0 {
			continue
		}
		inCfg := false
		for i := range m.cfg.Sources {
			if m.cfg.Sources[i].Name == name {
				inCfg = true
				break
			}
		}
		if !inCfg {
			continue
		}
		m.cache[name] = sc.Apps
		if t, perr := time.Parse(time.RFC3339, sc.UpdatedAt); perr == nil {
			m.fresh[name] = t
		}
		n++
	}
	return n
}

// SaveCache 把当前目录缓存原子落盘（后台异步调用；缓存损坏不致命，
// 丢失一次快照只影响下次启动的秒开，不影响运行）。
func (m *Manager) SaveCache(path string) error {
	m.mu.RLock()
	cf := cacheFile{
		SavedAt: time.Now().Format(time.RFC3339),
		Sources: make(map[string]sourceCacheSnapshot, len(m.cache)),
	}
	for name, apps := range m.cache {
		sc := sourceCacheSnapshot{Apps: apps}
		if t, ok := m.fresh[name]; ok {
			sc.UpdatedAt = t.Format(time.RFC3339)
		}
		cf.Sources[name] = sc
	}
	m.mu.RUnlock()

	saveMu.Lock()
	defer saveMu.Unlock()
	raw, err := json.Marshal(cf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 唯一 tmp 文件名：即使互斥失效（如未来并发实例），也不会与别的写者交错
	tmp := fmt.Sprintf("%s.tmp-%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// saveIfConfigured 有落盘路径时异步保存（Refresh 成功后调用）。
func (m *Manager) saveIfConfigured() {
	m.mu.RLock()
	path := m.cachePath
	m.mu.RUnlock()
	if path == "" {
		return
	}
	go func() { _ = m.SaveCache(path) }()
}
