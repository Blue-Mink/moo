package source

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 0.6.330 WP1：源索引名记忆。
//
// 背景（见 MOO_LOADING_REFACTOR_PLAN_20261018.md）：fetchIndex 每轮都从
// moo.json 开始，按「分支 × 5 个索引名 × 18 个前缀」展开约 195 个候选；
// 只有 fnpack.json 的源每轮也要发 72 个请求，无索引仓库最坏等 24s。
// 绝大多数源的索引名是稳定的，记住上次命中的名字即可把每轮的固定扫描
// 成本降到个位数请求。
//
// 三条写入约束（缺一不可）：
//  1. 只在阶段 1（常规名）命中时写，避免把 jsDelivr/apps.json 兜底当首选；
//  2. 命中地址不能是 cdn.jsdelivr.net；
//  3. TTL 6h：到期强制全量重探一次，防「开发者后来补了 moo.json」被冻住。
//
// key 含源 URL 哈希：源换地址后旧记忆自动失效，无需显式清理。
const (
	indexPrefTTL        = 6 * time.Hour
	indexPrefExploreGap = 10 * time.Minute // 同一源两次后台重探的最小间隔
)

// IndexPref 一个源的索引名记忆。
type IndexPref struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

type indexPrefFile struct {
	SavedAt string               `json:"saved_at"`
	Prefs   map[string]IndexPref `json:"prefs"`
}

// prefStore 记忆存储（内存 + 可选落盘；path 为空 = 不落盘，测试友好）。
type prefStore struct {
	mu    sync.Mutex
	path  string
	prefs map[string]IndexPref
	tried map[string]time.Time // 后台重探的最近一次时间（防风暴）
}

// 回滚开关（方案 §13）：MOO_INDEX_PREF=0 关闭记忆（回到每轮全量）；
// MOO_MIRROR_FAST=0 关闭镜像分级（候选回到全量镜像）。均只读一次。
var (
	prefOnOnce     sync.Once
	prefOn         = true
	mirrorFastOnce sync.Once
	mirrorFast     = true
)

func indexPrefEnabled() bool {
	prefOnOnce.Do(func() { prefOn = os.Getenv("MOO_INDEX_PREF") != "0" })
	return prefOn
}

func mirrorFastEnabled() bool {
	mirrorFastOnce.Do(func() { mirrorFast = os.Getenv("MOO_MIRROR_FAST") != "0" })
	return mirrorFast
}

func newPrefStore() *prefStore {
	return &prefStore{prefs: map[string]IndexPref{}, tried: map[string]time.Time{}}
}

func prefKey(name, url string) string {
	h := sha1.Sum([]byte(url))
	return name + "\x00" + hex.EncodeToString(h[:4])
}

// Get 返回仍在 TTL 内的记忆名（空串 = 无记忆/已过期）。
func (p *prefStore) Get(name, url string) (string, bool) {
	if p == nil {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.prefs[prefKey(name, url)]
	if !ok || e.Name == "" || time.Since(e.At) > indexPrefTTL {
		return "", false
	}
	return e.Name, true
}

// Put 写入/续期一条记忆并异步落盘。
func (p *prefStore) Put(name, url, indexName string) {
	if p == nil || indexName == "" {
		return
	}
	p.mu.Lock()
	p.prefs[prefKey(name, url)] = IndexPref{Name: indexName, At: time.Now()}
	p.mu.Unlock()
	p.saveAsync()
}

// Clear 丢弃一条记忆（记忆失效时调用；下轮自然回到全量探测）。
func (p *prefStore) Clear(name, url string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.prefs, prefKey(name, url))
	p.mu.Unlock()
	p.saveAsync()
}

// AllowedExplore 判断该源是否允许发起一次后台全量重探（同源 10min 一次）。
func (p *prefStore) AllowedExplore(name, url string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := prefKey(name, url)
	if t, ok := p.tried[k]; ok && time.Since(t) < indexPrefExploreGap {
		return false
	}
	p.tried[k] = time.Now()
	return true
}

func (p *prefStore) SetPath(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.path = path
	p.mu.Unlock()
}

// Load 从磁盘恢复记忆，返回条数。
func (p *prefStore) Load(path string) int {
	if p == nil || path == "" {
		return 0
	}
	p.SetPath(path) // 文件不存在时也要记住路径（后续 Put 才能落盘）
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var f indexPrefFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0 // 损坏快照不致命：退化为「无记忆」，下轮会重建
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for k, e := range f.Prefs {
		if e.Name == "" {
			continue
		}
		p.prefs[k] = e
		n++
	}
	return n
}

// saveAsync 原子写盘（复用 cache.go 的 saveMu，避免与目录快照并发交错）。
func (p *prefStore) saveAsync() {
	if p == nil {
		return
	}
	p.mu.Lock()
	path := p.path
	if path == "" {
		p.mu.Unlock()
		return
	}
	raw, err := json.Marshal(indexPrefFile{SavedAt: time.Now().Format(time.RFC3339), Prefs: p.prefs})
	p.mu.Unlock()
	if err != nil {
		return
	}
	go func() {
		saveMu.Lock()
		defer saveMu.Unlock()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			return
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
		}
	}()
}
