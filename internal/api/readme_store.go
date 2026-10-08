package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// README 两级缓存——与图标同架构（2026-09 实测：gh-proxy 限流窗口内
// 30 个 README 19 个 502 死等 13s；上游健康时 3s 全过。限流是常态，
// 缓存是唯一可靠的解法）：
//
//	① 内存层 1h；② 磁盘层 7 天（<DataDir>/cache/readmes/，重启不丢）；
//	③ 负缓存 30min（仅原始 URL 明确 404 才记，纯超时不记）；
//	④ 后台预热（图标预热之后补 README 轮，3 并发）。
//
// stale-while-revalidate：竞速失败但磁盘层有旧内容时直接回退旧版，
// 详情页永远优先出内容而不是转圈 13s。
const (
	readmeMemTTL  = time.Hour
	readmeDiskTTL = 7 * 24 * time.Hour
	readmeNegTTL  = 30 * time.Minute
	readmeMemMax  = 512 // ~10KB × 512 ≈ 5MB
	readmeDiskMax = 2000
	readmeWarmSem = 3
)

type readmeMemEntry struct {
	body    []byte
	ct      string
	expires time.Time
}

type readmeDiskRef struct {
	File string `json:"f"`
	CT   string `json:"ct"`
	TS   int64  `json:"ts"`
}

type readmeStore struct {
	dir string

	mu    sync.Mutex
	mem   map[string]readmeMemEntry
	index map[string]readmeDiskRef
	neg   map[string]time.Time

	loaded   bool
	savMu    sync.Mutex
	dirty    bool
	saveOnce sync.Once
	saveStop chan struct{}
}

func newReadmeStore(dir string) *readmeStore {
	return &readmeStore{
		dir:   dir,
		mem:   map[string]readmeMemEntry{},
		index: map[string]readmeDiskRef{},
		neg:   map[string]time.Time{},
	}
}

// readmeStore 懒初始化（Server 字面量构造，不动构造点）。
func (s *Server) readmeStore() *readmeStore {
	s.readmeStoreOnce.Do(func() {
		dir := ""
		if s.Cfg != nil {
			dir = filepath.Join(dataDirOf(s), "cache", "readmes")
		}
		s.readmeStoreV = newReadmeStore(dir)
	})
	return s.readmeStoreV
}

// StopReadmeStore 优雅退出收尾落盘。
func (s *Server) StopReadmeStore() { s.readmeStore().stop() }

func (st *readmeStore) load() {
	if st.dir == "" || st.loaded {
		st.loaded = true
		return
	}
	st.loaded = true
	b, err := os.ReadFile(filepath.Join(st.dir, "index.json"))
	if err != nil {
		return
	}
	var idx map[string]readmeDiskRef
	if err := json.Unmarshal(b, &idx); err != nil {
		log.Printf("[readme-store] 索引损坏，重置: %v", err)
		return
	}
	now := time.Now()
	dropped := 0
	for k, r := range idx {
		if now.Sub(time.Unix(r.TS, 0)) > readmeDiskTTL {
			_ = os.Remove(filepath.Join(st.dir, r.File))
			dropped++
			continue
		}
		st.index[k] = r
	}
	log.Printf("[readme-store] 磁盘层加载: %d 个 README（%s，过期 %d）", len(st.index), st.dir, dropped)
	go st.cleanupOrphans()
}

func (st *readmeStore) cleanupOrphans() {
	time.Sleep(5 * time.Second)
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for k, r := range st.index {
		if _, err := os.Stat(filepath.Join(st.dir, r.File)); err != nil {
			delete(st.index, k)
			n++
		}
	}
	if n > 0 {
		st.dirty = true
		st.scheduleSave()
	}
}

// Get 两级读：内存 → 磁盘（命中回灌内存）。
func (st *readmeStore) Get(key string) ([]byte, string, bool) {
	st.mu.Lock()
	if st.dir != "" {
		st.load()
	}
	if e, ok := st.mem[key]; ok && time.Now().Before(e.expires) {
		st.mu.Unlock()
		return e.body, e.ct, true
	}
	if r, ok := st.index[key]; ok {
		b, err := os.ReadFile(filepath.Join(st.dir, r.File))
		if err == nil && len(b) > 0 {
			st.mem[key] = readmeMemEntry{body: b, ct: r.CT, expires: time.Now().Add(readmeMemTTL)}
			st.mu.Unlock()
			return b, r.CT, true
		}
		_ = os.Remove(filepath.Join(st.dir, r.File))
		delete(st.index, key)
		st.dirty = true
		st.scheduleSave()
	}
	st.mu.Unlock()
	return nil, "", false
}

// Put 两级写（磁盘失败静默）。
func (st *readmeStore) Put(key string, body []byte, ct string) {
	if len(body) == 0 {
		return
	}
	if ct == "" {
		ct = "text/markdown; charset=utf-8"
	}
	st.mu.Lock()
	if len(st.mem) >= readmeMemMax {
		st.evictMemLocked()
	}
	st.mem[key] = readmeMemEntry{body: body, ct: ct, expires: time.Now().Add(readmeMemTTL)}
	if st.dir != "" {
		st.load()
		sum := sha1.Sum([]byte(key))
		st.index[key] = readmeDiskRef{File: hex.EncodeToString(sum[:8]) + ".md", CT: ct, TS: time.Now().Unix()}
		if len(st.index) > readmeDiskMax {
			type kv struct {
				k string
				r readmeDiskRef
			}
			var list []kv
			for k, r := range st.index {
				list = append(list, kv{k, r})
			}
			for i := len(list) - 1; i > 0; i-- {
				for j := i; j > 0 && list[j].r.TS < list[j-1].r.TS; j-- {
					list[j], list[j-1] = list[j-1], list[j]
				}
			}
			for i := readmeDiskMax; i < len(list); i++ {
				_ = os.Remove(filepath.Join(st.dir, list[i].r.File))
				delete(st.index, list[i].k)
			}
		}
		st.dirty = true
		st.scheduleSave()
	}
	st.mu.Unlock()
}

// evictMemLocked 到顶两段淘汰：先清过期，仍超保留最近一半（调用方持锁）。
func (st *readmeStore) evictMemLocked() {
	now := time.Now()
	type kv struct {
		k string
		e readmeMemEntry
	}
	var live []kv
	for k, e := range st.mem {
		if now.Before(e.expires) {
			live = append(live, kv{k, e})
		}
	}
	for i := len(live) - 1; i > 0; i-- {
		for j := i; j > 0 && live[j].e.expires.After(live[j-1].e.expires); j-- {
			live[j], live[j-1] = live[j-1], live[j]
		}
	}
	keep := readmeMemMax / 2
	if keep > len(live) {
		keep = len(live)
	}
	kept := make(map[string]readmeMemEntry, keep)
	for i := 0; i < keep; i++ {
		kept[live[i].k] = live[i].e
	}
	st.mem = kept
}

func (st *readmeStore) scheduleSave() {
	if st.dir == "" {
		return
	}
	st.saveOnce.Do(func() {
		st.saveStop = make(chan struct{})
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-st.saveStop:
					return
				case <-t.C:
					st.Flush()
				}
			}
		}()
	})
}

// Flush 同步落盘（脏时）。
func (st *readmeStore) Flush() {
	st.mu.Lock()
	d := st.dirty
	st.dirty = false
	st.mu.Unlock()
	if !d || st.dir == "" {
		return
	}
	st.savMu.Lock()
	defer st.savMu.Unlock()
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return
	}
	st.mu.Lock()
	idx := make(map[string]readmeDiskRef, len(st.index))
	for k, r := range st.index {
		idx[k] = r
	}
	memSnap := make(map[string][]byte, len(st.mem))
	for k, e := range st.mem {
		if len(e.body) > 0 && time.Now().Before(e.expires) {
			memSnap[k] = e.body
		}
	}
	st.mu.Unlock()
	b, err := json.Marshal(idx)
	if err != nil {
		return
	}
	for k, r := range idx {
		if data, ok := memSnap[k]; ok {
			if err := os.WriteFile(filepath.Join(st.dir, r.File), data, 0o644); err != nil {
				log.Printf("[readme-store] 落盘失败 %s: %v", r.File, err)
			}
		}
	}
	tmp := filepath.Join(st.dir, "index.json."+time.Now().Format("20060102150405.000000000"))
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(st.dir, "index.json"))
}

// stop 收尾：落盘 + 停 saver。
func (st *readmeStore) stop() {
	st.Flush()
	if st.saveStop != nil {
		close(st.saveStop)
		st.saveStop = nil
	}
}

// MarkNegative 明确 404 记 30min（纯超时不记）。
func (st *readmeStore) MarkNegative(key string) {
	st.mu.Lock()
	if len(st.neg) > 512 {
		st.neg = map[string]time.Time{}
	}
	st.neg[key] = time.Now().Add(readmeNegTTL)
	st.mu.Unlock()
}

func (st *readmeStore) IsNegative(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.neg[key]
	return ok && time.Now().Before(t)
}

// Has 预热跳过判断。
func (st *readmeStore) Has(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if e, ok := st.mem[key]; ok && time.Now().Before(e.expires) {
		return true
	}
	if st.dir != "" {
		st.load()
		_, ok := st.index[key]
		return ok
	}
	return false
}

// Preflight 提前加载磁盘索引（避免首请求承担扫描）。
func (st *readmeStore) Preflight() {
	st.mu.Lock()
	st.load()
	st.mu.Unlock()
}

// warmReadmesPass README 预热一轮（图标预热之后跑，3 并发、预算内）。
// 0.6.312 B3/F9：范围 = 当前去重档位可见集（此前全目录）。
func (s *Server) warmReadmesPass(ctx context.Context, budget time.Duration) {
	st := s.readmeStore()
	var pending []*appRef
	for _, a := range s.visibleSourceApps() {
		if a.ReadmeURL == "" {
			continue
		}
		key := "readme|" + a.ReadmeURL
		if st.Has(key) || st.IsNegative(key) {
			continue
		}
		pending = append(pending, &appRef{a: a, key: key})
	}
	if len(pending) == 0 {
		return
	}
	log.Printf("[readme-warm] %d 个 README 待预热（预算 %s）", len(pending), budget)
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	sem := make(chan struct{}, readmeWarmSem)
	var wg sync.WaitGroup
	hit := 0
	var hitMu sync.Mutex
	for _, ref := range pending {
		if pctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(ref *appRef) {
			defer wg.Done()
			defer func() { <-sem }()
			body, mime, dead, err := s.fetchReadmeRace(pctx, ref.a.ReadmeURL)
			if err == nil && len(body) > 0 {
				st.Put(ref.key, body, mime)
				hitMu.Lock()
				hit++
				hitMu.Unlock()
				return
			}
			if dead {
				st.MarkNegative(ref.key)
			}
		}(ref)
	}
	wg.Wait()
	log.Printf("[readme-warm] 预热完成: %d/%d 命中", hit, len(pending))
}
