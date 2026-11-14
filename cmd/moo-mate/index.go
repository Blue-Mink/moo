package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// indexedApp 索引里的一条应用：moo.json 条目 + 来源 + 同步状态。
// appEntry 值内嵌（json 平铺），Appname 单独存（map key）。
// Packages 存「架构 → 包」多架构映射（x86/arm/all）；空/单架构时保持
// 顶层扁平 download_url 形态（与 gen-moo-json 单架构输出一致）。
type indexedApp struct {
	appEntry
	Appname      string                 `json:"appname"`
	Source       string                 `json:"source"`                  // 来源仓库 URL（「应用来源」展示）
	Publisher    string                 `json:"publisher"`               // 发布者（owner，归类用）
	Synced       bool                   `json:"synced"`                  // 是否已同步到 Moo
	SyncedName   string                 `json:"synced_name,omitempty"`   // Moo 里的源名
	Packages     map[string]*appPackage `json:"packages,omitempty"`      // 架构 → 包（多架构）
	DownloadURLs []string               `json:"download_urls,omitempty"` // 候选下载链（测速选优用；不进 mate.json）
	// 0.2.62 上游检查状态：供界面逐应用显示「有更新 / 已最新 / 检查失败」。
	// 三者一起写、一起清，缺一不可（只写 LatestVersion 前端无法区分「没查过」与「查过但无更新」）。
	LatestVersion string `json:"latest_version,omitempty"` // 最近一次检查到的上游最新版本
	PrevVersion   string `json:"prev_version,omitempty"`   // 最近一次「成功更新」前的版本（用于界面显示 v旧→v新）
	UpdatedAt     int64  `json:"updated_at,omitempty"`     // 最近一次「成功更新」的时间（unix 秒，红点按它算 24h 生命期）
	LastCheck     int64  `json:"last_check,omitempty"`     // 最近一次检查时间（unix 秒）
	LastCheckErr  string `json:"last_check_err,omitempty"` // 无法确认更新的原因（网络失败/上游无包/应用已不存在）
	// 0.2.63：红点改「未读」模型 —— 有没有更新是客观事实（updated_at），
	// 用户看没看过是主观状态（updated_seen_at）。红点判 updated_at > updated_seen_at，
	// 用户点一下即已读；反复发版会刷新 updated_at，但不会把已读的旧版本重新点亮。
	UpdatedSeenAt int64 `json:"updated_seen_at,omitempty"` // 用户确认过「已更新」的时间（unix 秒）
	// LatestSeenAt：「上游有新版」这条记录首次写入的时间。超过 7 天仍未处理
	// （既没升级也没标记已读）就降级成「重新检查」，避免任何漏写版本号的路径造成永久红。
	LatestSeenAt int64 `json:"latest_seen_at,omitempty"`
}

// indexDoc 索引文件磁盘结构。
type indexDoc struct {
	Version int                    `json:"version"`
	Apps    map[string]*indexedApp `json:"apps"` // key = appname
	// RemovedDefaults 用户主动删除过的 key：补默认清单时跳过，避免「删了又回来」。
	// 用户重新添加同名应用（handleBuild）时从该表移除。
	RemovedDefaults []string `json:"removed_defaults,omitempty"`
}

// index 持久化应用索引（内存 + 落盘）。
type index struct {
	mu   sync.RWMutex
	path string
	doc  *indexDoc
	// gen 索引代次：用户主动「清除同步索引」时 +1，正在跑的同步据此放弃写回。
	gen uint64
	// 0.2.55（F4）：启动加载诊断。/api/status 暴露 last_index_error，
	// 让「索引损坏后被自动重建」不再静默。
	loadErr string // 最近一次启动加载失败原因（空 = 正常）
	badFile string // 损坏索引被隔离出来的文件名（同目录 index.json.bad-<ts>）
}

func newIndex(path string) *index {
	ix := &index{path: path, doc: &indexDoc{Version: 1, Apps: map[string]*indexedApp{}}}
	// 内置默认索引的写入**不在这里**：由 main 启动路径调 bootstrapDefaultIndex，
	// 避免把所有「空索引」单测污染成 486 条（那是出厂数据，不是 v 单元行为）。
	_ = ix.load()
	return ix
}

// load 读盘；文件不存在视为空索引，不报错。
//
// 0.2.55（F4）：解析失败不再静默留空——先把原文件改名隔离为
// index.json.bad-<YYYYMMDD-HHMMSS>，再返回错误，由 /api/status 暴露
// last_index_error。旧实现忽略错误直接留空索引，随后自动同步用远程默认
// 清单重建并 save()，把损坏文件连同用户原数据一起覆盖掉（无法取证）。
// 隔离是 rename 不是删除，原件始终保留。
func (ix *index) load() error {
	b, err := os.ReadFile(ix.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 首次启动：空索引
		}
		ix.setLoadDiag("读取索引失败: "+err.Error(), "")
		return err
	}
	var d indexDoc
	if uerr := json.Unmarshal(b, &d); uerr != nil {
		bad := quarantineIndex(ix.path)
		msg := "索引解析失败: " + uerr.Error()
		if bad != "" {
			msg += "（原文件已隔离为 " + filepath.Base(bad) + "）"
		} else {
			msg += "（隔离原文件失败，已保留在原路径）"
		}
		log.Printf("%s", msg)
		ix.setLoadDiag(msg, bad)
		return uerr
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	ix.doc = &d
	return nil
}

// setLoadDiag 记录启动加载诊断（损坏索引隔离 / 读取失败）。
func (ix *index) setLoadDiag(msg, badFile string) {
	ix.mu.Lock()
	ix.loadErr = msg
	ix.badFile = badFile
	ix.mu.Unlock()
}

// diag 返回加载诊断（错误描述、隔离文件名基名）。空串表示无异常。
func (ix *index) diag() (string, string) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	bad := ""
	if ix.badFile != "" {
		bad = filepath.Base(ix.badFile)
	}
	return ix.loadErr, bad
}

// quarantineIndex 把损坏的索引文件改名保留（index.json.bad-<ts>[-n]），
// 返回新路径；失败返回空串。只用 rename，绝不删除原文件。
func quarantineIndex(path string) string {
	ts := time.Now().Format("20060102-150405")
	for i := 0; i < 20; i++ {
		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("-%d", i)
		}
		dst := path + ".bad-" + ts + suffix
		if _, err := os.Stat(dst); err == nil {
			continue // 同名已存在（同秒多次损坏），换序号
		}
		if err := os.Rename(path, dst); err != nil {
			return ""
		}
		return dst
	}
	return ""
}

// reload 从当前 path 重新读盘并替换内存文档（恢复备份 / 切换数据目录后热加载）。
func (ix *index) reload() error {
	b, err := os.ReadFile(ix.path)
	if err != nil {
		return err
	}
	var d indexDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	if d.Apps == nil {
		d.Apps = map[string]*indexedApp{}
	}
	ix.mu.Lock()
	ix.doc = &d
	ix.mu.Unlock()
	return nil
}

// filePath 当前索引文件路径。
func (ix *index) filePath() string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.path
}

// setPath 切换索引文件路径（调用方负责先把数据写到新位置）。
func (ix *index) setPath(p string) {
	ix.mu.Lock()
	ix.path = p
	ix.mu.Unlock()
}

// save 落盘（自动建目录）。
// 0.2.34：原子写（同目录临时文件 + fsync + rename + 目录 fsync）。os.WriteFile 会先
// truncate，期间任何读取方（备份、部署核对脚本、外部工具）都会读到 0 字节空文件；
// 只 rename 不 fsync 时断电仍可能丢掉 tmp 内容或 rename 本身，所以写前 sync 文件、
// rename 后 sync 目录。
func (ix *index) save() error {
	dir := filepath.Dir(ix.path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	b, err := json.MarshalIndent(ix.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := ix.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, ix.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// list 返回全部应用副本（注入 appname），按 appname 升序排序。
//
// 0.2.55（F2）：map 迭代顺序随机 → /api/index 每次响应字节不同
// （同长度不同 md5），客户端缓存/ETag/diff/字节级比对全部失效。
// 输出前排序，保证同一索引内容永远产出同一份 JSON。
func (ix *index) list() []*indexedApp {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]*indexedApp, 0, len(ix.doc.Apps))
	for name, a := range ix.doc.Apps {
		cp := *a
		cp.Appname = name
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Appname < out[j].Appname })
	return out
}

// get 取单个应用（不含 appname，调用方已知 key）。
func (ix *index) get(appname string) *indexedApp {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	a := ix.doc.Apps[appname]
	if a == nil {
		return nil
	}
	cp := *a
	return &cp
}

// put 写入/覆盖一条（调用方须已持写锁）：重复发现时保留已有同步状态。
//
// 0.2.63（B7）：检查类字段必须「继承」—— 新构造的 indexedApp 里这些字段都是零值，
// 直接整体替换会把用户刚看到的红点/琥珀、以及上次真正成功检查的时间静默抹掉
// （「更新索引」发现更高版本时走的就是这条 upsert 路径，实测点一次红点就没了）。
// 规则：新值为空/零 → 沿用旧值；新值有内容 → 以新值为准（真升级由 applyCheckResults 显式写入）。
func (ix *index) put(a *indexedApp) error {
	if prev, ok := ix.doc.Apps[a.Appname]; ok {
		if prev.Synced && !a.Synced {
			a.Synced = true
			a.SyncedName = prev.SyncedName
		}
		if a.LatestVersion == "" {
			a.LatestVersion = prev.LatestVersion
		}
		if a.LatestSeenAt == 0 {
			a.LatestSeenAt = prev.LatestSeenAt
		}
		if a.PrevVersion == "" {
			a.PrevVersion = prev.PrevVersion
		}
		if a.UpdatedAt == 0 {
			a.UpdatedAt = prev.UpdatedAt
		}
		if a.UpdatedSeenAt == 0 {
			a.UpdatedSeenAt = prev.UpdatedSeenAt
		}
		if a.LastCheck == 0 {
			a.LastCheck = prev.LastCheck
		}
		if a.LastCheckErr == "" {
			a.LastCheckErr = prev.LastCheckErr
		}
	}
	ix.doc.Apps[a.Appname] = a
	return ix.save()
}

// upsert 写入/覆盖一条；重复发现时保留已有同步状态（不抹掉 synced）。
//
// 0.2.61b：**不再**在这里清「用户主动清除」标记 —— 自动同步也走 upsert，
// 清了会把用户的「清除同步索引」意图抹掉（实测清除后 38s 应用被写回、标记消失）。
// 清标记只由用户主动新增（handleBuild）触发。
func (ix *index) upsert(a *indexedApp) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.put(a)
}

// generation 返回当前索引代次。用户「清除同步索引」会推进一代，
// 进行中的自动同步据此放弃写回（见 upsertIfGen）。
func (ix *index) generation() uint64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.gen
}

// upsertIfGen 仅当代次未变时写入：代次变了说明索引已被用户清空，
// 此时写回旧快照等于「清除不彻底」。返回 wrote=false 表示主动放弃。
func (ix *index) upsertIfGen(a *indexedApp, gen uint64) (bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.gen != gen {
		log.Printf("索引已被用户清除（代次 %d→%d），放弃写回 %s", gen, ix.gen, a.Appname)
		return false, nil
	}
	return true, ix.put(a)
}

// delete 从索引移除一条。
func (ix *index) delete(appname string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	delete(ix.doc.Apps, appname)
	return ix.save()
}

// clear 清空全部应用（保留空索引结构；供测试与内部重建使用）。
// 0.2.61b：同时推进代次，令正在跑的同步放弃把旧快照写回。
// 0.2.62：不再清空 RemovedDefaults ——「清除」不等于「允许被删的应用复活」。
func (ix *index) clear() error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.doc.Apps = map[string]*indexedApp{}
	ix.gen++
	return ix.save()
}

// clearByUser 用户主动「清除同步索引」：清空应用，并把这些应用名记入 RemovedDefaults，
// 让随后的默认清单补齐（mergeDefaultList / seedDefaultIfEmpty）不能把它们加回来。
// extraRemoved 用于「索引已经被清空过一次」的场景（如清数据时补充源里的应用名）。
func (ix *index) clearByUser(extraRemoved []string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	names := make([]string, 0, len(ix.doc.Apps)+len(extraRemoved))
	for name := range ix.doc.Apps {
		names = append(names, name)
	}
	names = append(names, extraRemoved...)
	ix.doc.Apps = map[string]*indexedApp{}
	ix.doc.RemovedDefaults = mergeRemovedNames(ix.doc.RemovedDefaults, names)
	ix.gen++
	return ix.save()
}

// removedSet 返回「用户主动删除/清除过」的应用名集合（快照，调用者无需持锁）。
func (ix *index) removedSet() map[string]bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return removedSetOf(ix.doc.RemovedDefaults)
}

// mergeRemovedNames 合并排除名单：去重、去空、保持稳定顺序（旧在前，新在后）。
func mergeRemovedNames(old []string, add []string) []string {
	seen := make(map[string]bool, len(old)+len(add))
	out := make([]string, 0, len(old)+len(add))
	for _, list := range [][]string{old, add} {
		for _, n := range list {
			n = strings.TrimSpace(n)
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// removedSetOf 把排除名单转成集合。
func removedSetOf(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, n := range list {
		if s := strings.TrimSpace(n); s != "" {
			m[s] = true
		}
	}
	return m
}

// checkOutcome 单个应用一次上游检查的结果（供整轮同步批量落盘，避免逐条写盘）。
type checkOutcome struct {
	Appname string
	Latest  string // 上游最新版本（能确定时）
	Prev    string // 本次把版本从哪个值升上来（空 = 本次无更新，界面据此熄灭红点）
	ErrMsg  string // 无法确认的原因（空 = 正常）
}

// applyCheckResults 批量写入「上游检查状态」：整轮同步只落盘一次。
// 代次不符（用户在同步途中清除索引）则整体放弃，与写回保护同一条线。
func (ix *index) applyCheckResults(outs []checkOutcome, gen uint64) (bool, error) {
	if len(outs) == 0 {
		return false, nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.gen != gen {
		log.Printf("索引已被用户清除（代次 %d→%d），放弃写入检查状态", gen, ix.gen)
		return false, nil
	}
	now := time.Now().Unix()
	n := 0
	for _, o := range outs {
		a, ok := ix.doc.Apps[o.Appname]
		if !ok {
			continue // 已被用户删除：不复活
		}
		// 0.2.63（A6）：检查失败时 Latest 为空 → 保留已知的「上游最新版本」，
		// 否则一次网络抖动就会把「有更新」抹成「未确认」，用户看起来像更新没了。
		if o.Latest != "" {
			// latest 首次出现/变化时开始计 7 天兜底（前端据此把长期未处理的红点降级）。
			if a.LatestVersion != o.Latest {
				a.LatestSeenAt = now
			}
			a.LatestVersion = o.Latest
		}
		a.LastCheck = now
		a.LastCheckErr = o.ErrMsg
		if o.Prev != "" {
			// 只有真的发生更新时才刷新「已更新」标记；无更新的轮次保留旧值，
			// 否则用户点第二次「同步更新」红点就熄了，等于没看见（0.2.62 实测）。
			// 0.2.63：这里不再重算「已读」—— 新 updated_at 天然大于旧 updated_seen_at，
			// 所以一次新升级就是一条新的未读；已读过的旧版本也不会因此重新点亮。
			a.PrevVersion = o.Prev
			a.UpdatedAt = now
		}
		n++
	}
	if n == 0 {
		return false, nil
	}
	return true, ix.save()
}

// ackUpdates 把「已更新」红点标记为已读（updated_seen_at = now）。
// names 为空 = 全部已读。代次不符（索引刚被用户清除）直接放弃，与写回保护同一条线。
// 返回实际标记数量（本来就没有未读红点的应用不计入）。
func (ix *index) ackUpdates(names []string, gen uint64) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.gen != gen {
		return 0, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if s := strings.TrimSpace(n); s != "" {
			want[s] = true
		}
	}
	all := len(want) == 0
	now := time.Now().Unix()
	n := 0
	for name, a := range ix.doc.Apps {
		if !all && !want[name] {
			continue
		}
		// 只处理「有未读」的：updated_at 必须晚于上次已读，避免把从未更新过的应用写脏。
		if a.UpdatedAt == 0 || a.UpdatedAt <= a.UpdatedSeenAt {
			continue
		}
		a.UpdatedSeenAt = now
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, ix.save()
}

// markRemovedDefault 记录「用户主动删除过该应用」：补默认清单时跳过，避免删了又回来。
func (ix *index) markRemovedDefault(appname string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, v := range ix.doc.RemovedDefaults {
		if v == appname {
			return nil
		}
	}
	ix.doc.RemovedDefaults = append(ix.doc.RemovedDefaults, appname)
	return ix.save()
}

// unmarkRemovedDefault 用户重新添加同名应用后撤销排除记录。
func (ix *index) unmarkRemovedDefault(appname string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	out := make([]string, 0, len(ix.doc.RemovedDefaults))
	for _, v := range ix.doc.RemovedDefaults {
		if v != appname {
			out = append(out, v)
		}
	}
	if len(out) == len(ix.doc.RemovedDefaults) {
		return nil
	}
	ix.doc.RemovedDefaults = out
	return ix.save()
}

// setSynced 更新同步状态与 Moo 源名。
func (ix *index) setSynced(appname string, synced bool, name string) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	a, ok := ix.doc.Apps[appname]
	if !ok {
		return fmt.Errorf("索引里没有该应用: %s", appname)
	}
	a.Synced = synced
	if synced {
		a.SyncedName = name
	} else {
		a.SyncedName = ""
	}
	return ix.save()
}

// setSyncedMany 批量更新同步状态：全部改完只落盘一次。
// 0.2.32：原批量推送逐个调 setSynced，每个都把整份 index.json 序列化写一遍
// （N 个应用 = N 次全量写盘），几百个应用要等十几秒。改为一次保存。
func (ix *index) setSyncedMany(appnames []string, synced bool, name string) (int, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	n := 0
	for _, appname := range appnames {
		a, ok := ix.doc.Apps[appname]
		if !ok {
			continue
		}
		a.Synced = synced
		if synced {
			a.SyncedName = name
		} else {
			a.SyncedName = ""
		}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, ix.save()
}
