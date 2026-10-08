package api

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"moo/internal/lang"
	"moo/internal/source"
)

// 应用详情两级缓存（0.6.312 B3/F8，与 README 两级缓存同构）：
//
//	① 内存层 1h（小容量，常驻）；② 磁盘层 7 天（<DataDir>/cache/details/，
//	   进程重启/重新部署不丢，详情打开懒载命中）。
//
// 内容 = 应用的 changelog 全文 + releases 逐版本说明 + 预解析条目。
// 源同步时这些字段从 source.App 常驻内存移出（F8），目录快照
// （catalog.json）也不再携带——详情对话框打开时按「源+应用+版本」
// 懒载（readme_store 同款模式，用户实测无感）。
const (
	detailMemTTL  = time.Hour
	detailDiskTTL = 7 * 24 * time.Hour
)

// detailPayload 磁盘层存储结构（单应用单版本的详情重字段）。
type detailPayload struct {
	Cl      string            `json:"cl,omitempty"`          // changelog 全文
	Rel     map[string]string `json:"rel,omitempty"`         // releases 版本 → 说明
	Entries []ChangelogEntry  `json:"entries,omitempty"`     // 预解析条目（F7②）
}

type detailMemEntry struct {
	payload *detailPayload
	expires time.Time
}

type detailDiskRef struct {
	File string `json:"f"`
	TS   int64  `json:"ts"`
}

// detailStore 详情两级缓存（dir 为空 = 仅内存层，单元测试用）。
type detailStore struct {
	dir string
	memMax  int // 内存层容量（缺省 512，测试可调）
	diskMax int // 磁盘层容量（缺省 4096，测试可调）

	mu    sync.Mutex
	mem   map[string]*detailMemEntry
	index map[string]detailDiskRef

	loaded   bool
	savMu    sync.Mutex
	dirty    bool
	saveOnce sync.Once
	saveStop chan struct{}
}

func newDetailStore(dir string) *detailStore {
	return &detailStore{
		dir:     dir,
		memMax:  512,
		diskMax: 4096,
		mem:     map[string]*detailMemEntry{},
		index:   map[string]detailDiskRef{},
	}
}

// detailStore 懒初始化（Server 字面量构造，不动构造点）。
func (s *Server) detailStore() *detailStore {
	s.detailStoreOnce.Do(func() {
		dir := ""
		if s.Cfg != nil {
			dir = filepath.Join(dataDirOf(s), "cache", "details")
		}
		s.detailStoreV = newDetailStore(dir)
	})
	return s.detailStoreV
}

// StopDetailStore 优雅退出收尾落盘。
func (s *Server) StopDetailStore() { s.detailStore().stop() }

func (st *detailStore) load() {
	if st.dir == "" || st.loaded {
		st.loaded = true
		return
	}
	st.loaded = true
	b, err := os.ReadFile(filepath.Join(st.dir, "index.json"))
	if err != nil {
		return
	}
	var idx map[string]detailDiskRef
	if err := json.Unmarshal(b, &idx); err != nil {
		log.Printf("[detail-store] 索引损坏，重置: %v", err)
		return
	}
	now := time.Now()
	dropped := 0
	for k, r := range idx {
		if now.Sub(time.Unix(r.TS, 0)) > detailDiskTTL {
			_ = os.Remove(filepath.Join(st.dir, r.File))
			dropped++
			continue
		}
		st.index[k] = r
	}
	log.Printf("[detail-store] 磁盘层加载: %d 个详情条目（%s，过期 %d）", len(st.index), st.dir, dropped)
	go st.cleanupOrphans()
}

func (st *detailStore) cleanupOrphans() {
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
func (st *detailStore) Get(key string) (*detailPayload, bool) {
	st.mu.Lock()
	if st.dir != "" {
		st.load()
	}
	if e, ok := st.mem[key]; ok && time.Now().Before(e.expires) {
		st.mu.Unlock()
		return e.payload, true
	}
	if r, ok := st.index[key]; ok {
		b, err := os.ReadFile(filepath.Join(st.dir, r.File))
		if err == nil && len(b) > 0 {
			var p detailPayload
			if json.Unmarshal(b, &p) == nil {
				st.mem[key] = &detailMemEntry{payload: &p, expires: time.Now().Add(detailMemTTL)}
				st.mu.Unlock()
				return &p, true
			}
		}
		_ = os.Remove(filepath.Join(st.dir, r.File))
		delete(st.index, key)
		st.dirty = true
		st.scheduleSave()
	}
	st.mu.Unlock()
	return nil, false
}

// Put 两级写（磁盘失败静默——缓存永不拖垮请求）。空载荷不写。
func (st *detailStore) Put(key string, p *detailPayload) {
	if p == nil || (p.Cl == "" && len(p.Rel) == 0 && len(p.Entries) == 0) {
		return
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) == 0 {
		return
	}
	st.mu.Lock()
	if len(st.mem) >= st.memMax {
		st.evictMemLocked()
	}
	st.mem[key] = &detailMemEntry{payload: p, expires: time.Now().Add(detailMemTTL)}
	if st.dir != "" {
		st.load()
		sum := sha1.Sum([]byte(key))
		st.index[key] = detailDiskRef{File: hex.EncodeToString(sum[:8]) + ".json", TS: time.Now().Unix()}
		if len(st.index) > st.diskMax {
			type kv struct {
				k string
				r detailDiskRef
			}
			var list []kv
			for k, r := range st.index {
				list = append(list, kv{k, r})
			}
			sort.Slice(list, func(i, j int) bool {
				if list[i].r.TS != list[j].r.TS {
					return list[i].r.TS < list[j].r.TS
				}
				return list[i].k < list[j].k // 同 ts（同秒写入）按键决胜，淘汰确定性
			})
			for i := 0; i < len(list)-st.diskMax; i++ {
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
func (st *detailStore) evictMemLocked() {
	now := time.Now()
	type kv struct {
		k string
		e *detailMemEntry
	}
	var live []kv
	for k, e := range st.mem {
		if now.Before(e.expires) {
			live = append(live, kv{k, e})
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].e.expires.After(live[j].e.expires) })
	keep := st.memMax / 2
	if keep > len(live) {
		keep = len(live)
	}
	kept := make(map[string]*detailMemEntry, keep)
	for i := 0; i < keep; i++ {
		kept[live[i].k] = live[i].e
	}
	st.mem = kept
}

func (st *detailStore) scheduleSave() {
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
func (st *detailStore) Flush() {
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
	idx := make(map[string]detailDiskRef, len(st.index))
	for k, r := range st.index {
		idx[k] = r
	}
	memSnap := make(map[string]*detailPayload, len(st.mem))
	for k, e := range st.mem {
		if time.Now().Before(e.expires) {
			memSnap[k] = e.payload
		}
	}
	st.mu.Unlock()
	b, err := json.Marshal(idx)
	if err != nil {
		return
	}
	for k, r := range idx {
		if p, ok := memSnap[k]; ok {
			data, merr := json.Marshal(p)
			if merr != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(st.dir, r.File), data, 0o644); err != nil {
				log.Printf("[detail-store] 落盘失败 %s: %v", r.File, err)
			}
		}
	}
	tmp := filepath.Join(st.dir, "index.json."+time.Now().Format("20060102150405.000000000"))
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(st.dir, "index.json"))
}

func (st *detailStore) stop() {
	st.Flush()
	if st.saveStop != nil {
		close(st.saveStop)
		st.saveStop = nil
	}
}

// Preflight 提前加载磁盘索引（避免首请求承担扫描）。
func (st *detailStore) Preflight() {
	st.mu.Lock()
	st.load()
	st.mu.Unlock()
}

// detailKey 0.6.312 B3/F8：详情磁盘层键 = 源 + 应用 + 版本。
// 含版本：应用升级后 changelog 变 → 新键（旧条目由 LRU/TTL 自然淘汰），
// 同图标键 0.6.200 的版本语义。
func detailKey(sourceName, name, version string) string {
	if version == "" {
		version = "latest"
	}
	return sourceName + "\x00" + name + "@" + version
}

// DetailStoreSink 0.6.312 B3/F8：源抓取成功回调（挂在 source.Manager.
// OnFetched，main 接线；导出供 cmd/server 赋值）。对每个带重字段的应用：
//  1. changelog 全文 / releases 明细 / 预解析条目（F7②）写入详情磁盘层；
//  2. 从内存 source.App 上移除这些字段（常驻内存与 catalog.json 快照同瘦）；
//  3. 可能并入官方卡的应用（appname 在官方目录中）在 fnos-official 键下
//     双写一份——官方合并后卡片 Source 变 fnos-official，详情须按合并键
//     懒载命中。
func (s *Server) DetailStoreSink(name string, apps map[string]*source.App) {
	if len(apps) == 0 {
		return
	}
	st := s.detailStore()
	officialSet := map[string]bool{}
	if s.Panel != nil {
		if official, _ := s.Panel.Apps(lang.WithContext(context.Background(), s.bgLang())); len(official) > 0 {
			for i := range official {
				officialSet[official[i].AppName] = true
			}
		}
	}
	n := 0
	for _, a := range apps {
		if a.Changelog == "" && len(a.ReleaseChangelogs) == 0 {
			continue
		}
		p := &detailPayload{
			Cl:      a.Changelog,
			Rel:     a.ReleaseChangelogs,
			Entries: parseChangelogEntries(a.Changelog, a.Version, a.ReleaseChangelogs),
		}
		st.Put(detailKey(name, a.Name, a.Version), p)
		if officialSet[a.Name] && name != OfficialSourceID {
			st.Put(detailKey(OfficialSourceID, a.Name, a.Version), p)
		}
		a.Changelog = ""
		a.ReleaseChangelogs = nil
		n++
	}
	if n > 0 {
		log.Printf("[detail-store] 源「%s」: %d 个应用 changelog 移入磁盘层（移出常驻内存）", name, n)
	}
}
