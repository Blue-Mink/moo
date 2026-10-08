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
	"strconv"
	"strings"
	"sync"
	"time"

	"moo/internal/source"
)

// 图标两级缓存 + 负缓存 + 后台预热——对齐 New Store 的加载架构：
//
//	① 内存层 1h（热应用秒回；0.6.313 B4：容量 env 化 MOO_ICON_MEM_MAX，
//	   默认 256，0=禁用内存层纯磁盘，1024=回到 0.6.312 旧行为）；
//	② 磁盘层 7 天（<DataDir>/cache/icons/，进程重启/重新部署不冷瀑布，
//	   此前 Moo 只有 24h 内存缓存，每次部署后整面图标墙重抓；
//	   0.6.313 B4：Put/healEntry 直接 os.WriteFile 写文件，不再靠
//	   saveIndex 的 mem 快照批量落盘）；
//	③ 负缓存 30min（所有候选都失败的结论，坏图标不再每次点击重赛 15s）；
//	④ 后台预热（目录就绪后 6 并发拉取，首轮 10min 预算，之后每 30min
//	   一轮 3min，把用户没点过的应用图标也提前灌进磁盘层）。
const (
	// 内存层 1h（目录 24h 才变一次；NAS 上单图标磁盘读 ~100ms，
	// 1h 内反复浏览列表全走内存毫秒级；10min 实测不够——warm 流量
	// 会先把整层冲掉）
	iconMemTTL = time.Hour
	// 0.6.313 B4：默认容量 1024 → 256（典型 1-20KB/条 ≈ 3-5MB 常驻；
	// 机械盘深滚未命中内存时靠 page cache 兜底）。env MOO_ICON_MEM_MAX
	// 可覆盖（0=禁用，1024=旧行为）。
	iconMemMax = 256
	// 磁盘层 LRU 上限（2000 文件 ≈ ~40MB，正常 NAS 的 page cache 装得下）
	iconDiskTTL = 7 * 24 * time.Hour
	iconDiskMax = 2000
	// 负缓存 TTL
	iconNegTTL = 30 * time.Minute
	// 预热并发：3（而非 NS 的 6）——每应用最多 9 个候选并发，6 路预热
	// = 54 路出站，实测会把 gh-proxy 镜像打进限流、反噬用户点击的图标
	iconWarmSem = 3
)

type iconMemEntry struct {
	data    []byte
	ctype   string
	expires time.Time
}

type iconDiskRef struct {
	File string `json:"f"`
	CT   string `json:"ct"`
	TS   int64  `json:"ts"`
}

// iconStore 图标两级缓存（dir 为空 = 仅内存层，单元测试用）。
type iconStore struct {
	dir string
	// 0.6.313 B4：内存层容量（env MOO_ICON_MEM_MAX，默认 iconMemMax=256；
	// 0=禁用内存层纯磁盘，1024=0.6.312 旧行为）。Get/Put/Has/GetStale/
	// evictMemLocked 的 mem 分支都跟随此值。
	memMax int

	mu    sync.Mutex
	mem   map[string]iconMemEntry
	index map[string]iconDiskRef
	neg   map[string]time.Time // 负缓存：key → 过期时刻

	loaded bool
	savMu  sync.Mutex // 落盘串行（唯一 tmp + rename，防并发写损坏）
	dirty  bool

	saverOnce sync.Once
	saverStop chan struct{}
}

// newIconStore 构造图标缓存。0.6.313 B4：内存层容量读 env
// MOO_ICON_MEM_MAX（默认 iconMemMax=256；0=禁用内存层纯磁盘，
// 1024=0.6.312 旧行为；非法值 log 警告用默认）。
func newIconStore(dir string) *iconStore {
	st := &iconStore{
		dir:    dir,
		memMax: iconMemMax,
		mem:    map[string]iconMemEntry{},
		index:  map[string]iconDiskRef{},
		neg:    map[string]time.Time{},
	}
	if v := os.Getenv("MOO_ICON_MEM_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			st.memMax = n
		} else {
			log.Printf("[icon-store] MOO_ICON_MEM_MAX=%q 非法（非负整数，0=禁用），用默认 %d", v, iconMemMax)
		}
	}
	log.Printf("[icon-store] 内存层容量 = %d（env MOO_ICON_MEM_MAX：0=禁用纯磁盘 / 1024=旧行为）", st.memMax)
	return st
}

// Server 上的懒初始化（Server 以字面量构造，避免动所有构造点）。
func (s *Server) iconStore() *iconStore {
	s.iconStoreOnce.Do(func() {
		dir := ""
		if s.Cfg != nil {
			dir = filepath.Join(dataDirOf(s), "cache", "icons")
		}
		s.iconStoreV = newIconStore(dir)
	})
	return s.iconStoreV
}

// load 首次读前加载磁盘索引（容错：索引损坏回空索引）。
// 不做逐文件 stat（1283 个条目在 NAS 上约 2s，会把首请求拖到 2.3s+）：
// 时间过期按 ts 直接删；文件是否存在由 Get 按键校验 + 后台孤儿清理兜底。
func (st *iconStore) load() {
	if st.dir == "" || st.loaded {
		st.loaded = true
		return
	}
	st.loaded = true
	b, err := os.ReadFile(filepath.Join(st.dir, "index.json"))
	if err != nil {
		return
	}
	var idx map[string]iconDiskRef
	if err := json.Unmarshal(b, &idx); err != nil {
		log.Printf("[icon-store] 索引损坏，重置: %v", err)
		return
	}
	now := time.Now()
	dropped := 0
	for k, r := range idx {
		if now.Sub(time.Unix(r.TS, 0)) > iconDiskTTL {
			_ = os.Remove(filepath.Join(st.dir, r.File))
			dropped++
			continue
		}
		st.index[k] = r
	}
	log.Printf("[icon-store] 磁盘层加载: %d 个图标（%s，过期 %d）", len(st.index), st.dir, dropped)
	go st.cleanupOrphans()
}

// cleanupOrphans 后台清掉索引里文件已丢失的条目（避开启动峰值，5s 后跑）。
func (st *iconStore) cleanupOrphans() {
	time.Sleep(5 * time.Second)
	st.mu.Lock()
	defer st.mu.Unlock()
	orphans := 0
	for k, r := range st.index {
		if _, err := os.Stat(filepath.Join(st.dir, r.File)); err != nil {
			delete(st.index, k)
			orphans++
		}
	}
	if orphans > 0 {
		st.dirty = true
		st.scheduleSave()
	}
}

// Get 两级读：内存（memMax>0 时）→ 磁盘（磁盘字节回灌内存）。
// 0.6.201：超尺寸存量条目（降采样上线前缓存的全尺寸原图）读时自动
// 缩样自愈——每个条目只触发一次（缩完变小，后续读走小图路径）。
// 0.6.313 B4：memMax=0（env MOO_ICON_MEM_MAX=0）时跳过 mem 分支与回灌。
func (st *iconStore) Get(key string) ([]byte, string, bool) {
	st.mu.Lock()
	if st.dir != "" {
		st.load()
	}
	var data []byte
	var ctype string
	var hit bool
	if st.memMax > 0 {
		if e, ok := st.mem[key]; ok && time.Now().Before(e.expires) {
			data, ctype, hit = e.data, e.ctype, true
		}
	}
	if !hit {
		if r, ok := st.index[key]; ok {
			p := filepath.Join(st.dir, r.File)
			b, err := os.ReadFile(p)
			if err == nil && len(b) >= 64 && looksLikeImage(b) {
				if st.memMax > 0 {
					st.mem[key] = iconMemEntry{data: b, ctype: r.CT, expires: time.Now().Add(iconMemTTL)}
				}
				data, ctype, hit = b, r.CT, true
			} else {
				// 损坏/缺失：丢弃该磁盘条目
				_ = os.Remove(p)
				delete(st.index, key)
				st.dirty = true
				st.scheduleSave()
			}
		}
	}
	st.mu.Unlock()
	if !hit {
		return nil, "", false
	}
	// 自愈在锁外做（解码不阻塞其他图标读写）
	if len(data) >= iconDownsampleMin {
		if small, ct, changed := downsampleIcon(data, ctype); changed {
			st.healEntry(key, small, ct)
			return small, ct, true
		}
	}
	return data, ctype, true
}

// healEntry 用降采样后的字节替换条目（幂等）。0.6.313 B4 纯磁盘版：
// 降采样字节直接 WriteFile 落新文件成功后再更新 index（写序同 Put：
// 先文件后索引，中途崩溃=孤儿文件，cleanupOrphans 兜底）；ctype 变化
// 导致文件名变化时删旧文件。memMax>0 时内存层一并刷新。
func (st *iconStore) healEntry(key string, data []byte, ctype string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.memMax > 0 {
		st.mem[key] = iconMemEntry{data: data, ctype: ctype, expires: time.Now().Add(iconMemTTL)}
	}
	if st.dir != "" {
		if r, ok := st.index[key]; ok {
			old := r.File
			nr := iconDiskRef{File: diskIconFile(key, ctype), CT: ctype, TS: r.TS}
			if err := os.WriteFile(filepath.Join(st.dir, nr.File), data, 0o644); err == nil {
				st.index[key] = nr
				if old != nr.File {
					_ = os.Remove(filepath.Join(st.dir, old))
				}
				st.dirty = true
				st.scheduleSave()
			} else {
				log.Printf("[icon-store] 降采样落盘失败 %s: %v（保留旧文件，下次读重试）", nr.File, err)
			}
		}
	}
}

// Put 两级写：内存层（memMax>0）+ 磁盘直接写。
// 0.6.313 B4：磁盘文件字节由本函数直接 os.WriteFile（承接原 saveIndex 的
// mem 快照批量落盘职责；写的是调用方 resolveIcon 已降采样的字节）。
// 写序=先文件成功后再更 index（中途崩溃=孤儿文件，cleanupOrphans 兜底）；
// 文件命名走 diskIconFile（格式零变化，存量用户零迁移）。
// 磁盘写失败只记日志不拖垮请求（缓存永不挡业务）。
func (st *iconStore) Put(key string, data []byte, ctype string) {
	if len(data) == 0 {
		return
	}
	st.mu.Lock()
	if st.memMax > 0 {
		if len(st.mem) >= st.memMax {
			st.evictMemLocked()
		}
		st.mem[key] = iconMemEntry{data: data, ctype: ctype, expires: time.Now().Add(iconMemTTL)}
	}
	if st.dir != "" {
		st.load()
		f := filepath.Join(st.dir, diskIconFile(key, ctype))
		if werr := os.WriteFile(f, data, 0o644); werr != nil {
			log.Printf("[icon-store] 图标落盘失败 %s: %v（仅内存层生效）", f, werr)
			st.mu.Unlock()
			return
		}
		st.index[key] = iconDiskRef{File: diskIconFile(key, ctype), CT: ctype, TS: time.Now().Unix()}
		// 超量淘汰：按 ts 保留最新的 iconDiskMax 个（文件删与索引删配对）
		if len(st.index) > iconDiskMax {
			refs := make([]iconDiskRef, 0, len(st.index))
			keys := make([]string, 0, len(st.index))
			for k, r := range st.index {
				refs = append(refs, r)
				keys = append(keys, k)
			}
			for i := len(refs) - 1; i > 0; i-- {
				for j := i; j > 0 && refs[j].TS < refs[j-1].TS; j-- {
					refs[j], refs[j-1] = refs[j-1], refs[j]
					keys[j], keys[j-1] = keys[j-1], keys[j]
				}
			}
			for i := iconDiskMax; i < len(keys); i++ {
				_ = os.Remove(filepath.Join(st.dir, refs[i].File))
				delete(st.index, keys[i])
			}
		}
		st.dirty = true
		st.scheduleSave()
	}
	st.mu.Unlock()
}

// evictMemLocked 内存层到顶时的两段淘汰（调用方已持 st.mu；
// memMax=0 时 Put 不会进来，本函数天然 no-op）：
// 先清过期；仍超则按 expires 保留最近的 memMax/2。
// 0.6.313 B4：淘汰判定跟随 env 容量 st.memMax（默认 256）。
// 不用 NS 的「整体清空」——warm 一轮 ~1300 Put 会把用户正在浏览的
// 热图标连锅端，实测二次浏览 60 图标仍 103ms/个（全落回 NAS 磁盘）。
func (st *iconStore) evictMemLocked() {
	now := time.Now()
	type kv struct {
		k string
		e iconMemEntry
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
	kept := make(map[string]iconMemEntry, keep)
	for i := 0; i < keep; i++ {
		kept[live[i].k] = live[i].e
	}
	st.mem = kept
}

// scheduleSave 标记脏并触发防抖 saver（2s 批量落盘，避免每 Put 一次
// 索引重写；异步竞态不会与请求路径互相阻塞）。
func (st *iconStore) scheduleSave() {
	if st.dir == "" {
		return
	}
	st.ensureSaver()
}

// ensureSaver 启动唯一的后台 saver（2s ticker 批量 flush）。
func (st *iconStore) ensureSaver() {
	st.saverOnce.Do(func() {
		st.saverStop = make(chan struct{})
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-st.saverStop:
					return
				case <-t.C:
					st.Flush()
				}
			}
		}()
	})
}

// Flush 同步落盘（dirty 时）；测试与优雅退出用。
func (st *iconStore) Flush() {
	st.mu.Lock()
	d := st.dirty
	st.dirty = false
	st.mu.Unlock()
	if d && st.dir != "" {
		st.saveIndex()
	}
}

// Stop 停止后台 saver（优雅退出前 Flush 收尾）。
func (st *iconStore) Stop() {
	st.Flush()
	if st.saverStop != nil {
		close(st.saverStop)
		st.saverStop = nil
	}
}

func (st *iconStore) saveIndex() {
	if st.dir == "" {
		return
	}
	st.savMu.Lock()
	defer st.savMu.Unlock()
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return
	}
	st.mu.Lock()
	idx := make(map[string]iconDiskRef, len(st.index))
	for k, r := range st.index {
		idx[k] = r
	}
	st.mu.Unlock()
	b, err := json.Marshal(idx)
	if err != nil {
		return
	}
	// 0.6.313 B4：图标文件字节改由 Put/healEntry 直接 os.WriteFile
	//（不再从 mem 快照批量落盘——内存层可被 env 禁用，磁盘层必须独立
	// 成立）；本函数只剩 index.json 原子写，2s 防抖继续生效。
	tmp := filepath.Join(st.dir, "index.json."+time.Now().Format("20060102150405.000000000"))
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(st.dir, "index.json"))
}

// MarkNegative 记录「所有候选失败」30 分钟。
func (st *iconStore) MarkNegative(key string) {
	st.mu.Lock()
	if len(st.neg) > 512 {
		st.neg = map[string]time.Time{}
	}
	st.neg[key] = time.Now().Add(iconNegTTL)
	st.mu.Unlock()
}

func (st *iconStore) IsNegative(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	t, ok := st.neg[key]
	return ok && time.Now().Before(t)
}

// Has 判断两级是否已有该应用图标（预热跳过用）。
// 0.6.313 B4：memMax=0 时跳过 mem 扫描，只走 index/磁盘。
func (st *iconStore) Has(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.memMax > 0 {
		if e, ok := st.mem[key]; ok && time.Now().Before(e.expires) {
			return true
		}
	}
	if st.dir != "" {
		st.load()
		_, ok := st.index[key]
		return ok
	}
	return false
}

// GetStale 按前缀（Source@Name@）找**旧版本**图标条目回退。
// iconCacheKey 含版本号（0.6.200：发布者常随版本换图，URL 不变内容变），
// 副作用是源同步刷新版本后整批应用键位移、磁盘层全部 miss——2026-10-06
// 0.6.288 部署后 516 图标重抓又撞本地 DNS 瞬断，整墙空白到下一轮预热。
// 旧版本条目文件仍在盘上（仅键不可达）：所有候选失败时取前缀下 TS 最新的
// 条目兜底——显示旧图标远好于空白墙。不做 Put 灌回（让正常竞速赢新键）。
func (st *iconStore) GetStale(prefix string) ([]byte, string, bool) {
	if prefix == "" {
		return nil, "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	// 内存层优先（刚 Put 未落盘的条目也救得了；mem-only 实例同样生效）。
	// 0.6.313 B4：memMax=0 时跳过 mem 扫描，只走 index/磁盘前缀扫描。
	if st.memMax > 0 {
		var bestKey string
		var bestExp time.Time
		found := false
		for k, e := range st.mem {
			if !strings.HasPrefix(k, prefix) || !time.Now().Before(e.expires) {
				continue
			}
			if !found || e.expires.After(bestExp) {
				bestKey, bestExp, found = k, e.expires, true
			}
		}
		if found {
			e := st.mem[bestKey]
			if len(e.data) > 0 {
				return e.data, e.ctype, true
			}
		}
	}
	if st.dir == "" {
		return nil, "", false
	}
	st.load()
	var best iconDiskRef
	found := false
	for k, r := range st.index {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !found || r.TS > best.TS {
			best = r
			found = true
		}
	}
	if !found {
		return nil, "", false
	}
	b, err := os.ReadFile(filepath.Join(st.dir, best.File))
	if err != nil || len(b) < 64 || !looksLikeImage(b) {
		return nil, "", false
	}
	return b, best.CT, true
}

// diskIconFile 磁盘文件名：<sha1(key)[:16]><扩展名>。
func diskIconFile(key, ctype string) string {
	sum := sha1.Sum([]byte(key))
	ext := ".bin"
	switch {
	case strings.Contains(ctype, "png"):
		ext = ".png"
	case strings.Contains(ctype, "jpeg"):
		ext = ".jpg"
	case strings.Contains(ctype, "gif"):
		ext = ".gif"
	case strings.Contains(ctype, "webp"):
		ext = ".webp"
	case strings.Contains(ctype, "svg"):
		ext = ".svg"
	}
	return hex.EncodeToString(sum[:8]) + ext
}

// StopIcons 优雅退出时收尾：同步落盘未写脏数据并停止 saver。
func (s *Server) StopIcons() {
	s.iconStore().Stop()
}

// Preflight 提前加载磁盘索引（避免首个图标请求承担 1283 个文件的
// 索引 stat 扫描，实测首请求 2.6s → 其余 60ms）。
func (st *iconStore) Preflight() {
	st.mu.Lock()
	st.load()
	st.mu.Unlock()
}

// StartIconWarm 图标预热循环：目录就绪后首轮 10min 预算（3 并发），
// 之后每 30min 一轮 5min，把缺失图标陆续灌进两级缓存。
func (s *Server) StartIconWarm(ctx context.Context) {
	go s.iconStore().Preflight() // 进程启动即后台加载磁盘索引
	go s.readmeStore().Preflight()
	go s.detailStore().Preflight() // 0.6.312 B3/F8：详情磁盘层提前加载
	// 等目录就绪（源管理器出现应用）再开跑
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if len(s.Src.Apps("", "")) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	if ctx.Err() != nil {
		return
	}
	hit0, pend0 := s.warmIconsPass(ctx, 10*time.Minute)
	s.warmReadmesPass(ctx, 10*time.Minute)
	// 0.6.289 快速重试：整轮命中率 <20% 且待补量大 → 大概率处于网络故障
	// 窗口（DNS 瞬断/镜像限流风暴，2026-10-06 部署当日实锤），2 分钟后重试，
	// 图标墙分钟级恢复，而不是让用户对着空白墙等到下一轮 30 分钟。
	delay := 30 * time.Minute
	if pend0 > 20 && hit0*5 < pend0 {
		delay = 2 * time.Minute
		log.Printf("[icon-warm] 首轮命中率过低（%d/%d），2 分钟后快速重试", hit0, pend0)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			// 0.6.312 B3/F6（活性门控）：无 UI 访问 >30min → 本轮预热跳过
			//（图标/readme 是「展示性」后台活动，核心轮次不受影响）；
			// 30min 后再探，UI 一访问即恢复节奏。
			if s.uiInactive() {
				log.Printf("[icon-warm] UI 不活跃（>%s 无访问），跳过本轮预热", uiInactiveGate)
				timer.Reset(30 * time.Minute)
				continue
			}
			hit, pend := s.warmIconsPass(ctx, 5*time.Minute)
			s.warmReadmesPass(ctx, 5*time.Minute)
			if pend > 20 && hit*5 < pend {
				delay = 2 * time.Minute
			} else {
				delay = 30 * time.Minute
			}
			timer.Reset(delay)
		}
	}
}

// visibleSourceApps 0.6.312 B3/F9（预热范围=可见集）：当前去重档位下「有
// 可见卡」的源应用——即存在 !Hidden 且实际展示该应用图标的目录卡。
// 全部档退化为全量（fuzzy 匹配移除的卡除外，它们不再展示，预热属浪费）；
// 去重档（one，0.6.313 前文案「一卡」）缩到代表卡（实测 ~923/2556，预热工作量 −64%）。隐藏卡的图标不
// 预灌（详情显式打开时按需 resolveIcon，磁盘 LRU 不清旧条目兜底）。
// 目录未就绪（可见集为空）时回退全量，保冷启动预热。
func (s *Server) visibleSourceApps() []*source.App {
	apps := s.Src.Apps("", "")
	if len(apps) == 0 {
		return apps
	}
	catalog := s.cachedCatalog(s.bgLang())
	pairVis := map[string]bool{}
	iconVis := map[string]bool{}
	for i := range catalog {
		c := &catalog[i]
		if c.Hidden || c.Source == "" {
			continue
		}
		pairVis[c.AppName+"\x00"+c.Source] = true
		if c.IconURL != "" {
			iconVis[c.IconURL] = true
		}
	}
	if len(pairVis) == 0 && len(iconVis) == 0 {
		return apps // 目录未就绪 → 全量预热（旧行为兜底）
	}
	out := apps[:0]
	for _, a := range apps {
		if a.Name == "" {
			continue
		}
		// ① 同（应用, 源）卡可见；② 并入官方目录的卡（Source 已改
		// fnos-official）仍展示该应用图标（IconURL 未变）→ 也预热
		if pairVis[a.Name+"\x00"+a.Source] || (a.IconURL != "" && iconVis[a.IconURL]) {
			out = append(out, a)
		}
	}
	return out
}

// warmVisibleNow 0.6.312 B3/F9：去重档位切换后的增量预热（visibleSourceApps
// 只补缺，Has/负缓存跳过；异步跑，不阻塞设置保存响应）。
func (s *Server) warmVisibleNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	s.warmIconsPass(ctx, 5*time.Minute)
	s.warmReadmesPass(ctx, 5*time.Minute)
}

// warmIconsPass 预热一轮缺失图标（预算内、3 并发）；返回（命中数, 待补数）。
// 0.6.312 B3/F9：范围 = 当前去重档位可见集（此前全目录）。
func (s *Server) warmIconsPass(ctx context.Context, budget time.Duration) (int, int) {
	st := s.iconStore()
	apps := s.visibleSourceApps()
	var pending []*appRef
	for _, a := range apps {
		// 0.6.289：键改为含版本的真实缓存键（此前 2 段键永不命中索引层，
		// Has 形同虚设，每轮全量 1800+ 应用重走 resolveIcon）
		key := iconCacheKey(a)
		if st.Has(key) || st.IsNegative(key) {
			continue
		}
		pending = append(pending, &appRef{a: a, key: key})
	}
	if len(pending) == 0 {
		return 0, 0
	}
	log.Printf("[icon-warm] %d 个应用图标待预热（预算 %s）", len(pending), budget)
	pctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	sem := make(chan struct{}, iconWarmSem)
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
			if data, _, err := s.resolveIcon(pctx, ref.a); err == nil && len(data) > 0 {
				hitMu.Lock()
				hit++
				hitMu.Unlock()
			}
		}(ref)
	}
	wg.Wait()
	log.Printf("[icon-warm] 预热完成: %d/%d 命中", hit, len(pending))
	return hit, len(pending)
}

// appRef 预热用的（应用, 缓存键）对。
type appRef struct {
	a   *source.App
	key string
}
