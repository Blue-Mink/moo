package api

// 2026-11-26 B-SRC-LISTSYNC：源列表自动同步的运行痕迹 + 失败退避。
//
// 背景（BACKLOG §B-SRC-LISTSYNC）：后台每轮（默认 1h）都会跑
// sourceListSyncOnce，但此前「Added=0 就不打日志」+「失败只进响应体」+
// 「失败源不落库也无退避」三件事叠在一起，导致：
//   - 用户无法区分「在跑」与「摆设」（生产 200 源全已存在 = 每轮静默）；
//   - 清单里的死链每小时被 AddSource→Refresh 真抓一次（单条约 25s 预算），永久重试。
//
// 本文件负责：状态落盘 + 失败退避；对应日志见 source_listsync_log.go。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	// listSyncBackoffBase 同一 URL 首次失败后的静默期；之后每次翻倍。
	listSyncBackoffBase = 6 * time.Hour
	// listSyncBackoffMax 退避上限：死链最多每 24h 试一次（而非每小时）。
	listSyncBackoffMax = 24 * time.Hour
	// listSyncFailCap 退避表上限（清单里的死链不该把状态文件撑大）。
	listSyncFailCap = 500
)

// listSyncFail 单个清单地址的失败退避记录。
type listSyncFail struct {
	Count  int    `json:"count"`            // 连续失败轮数（成功即整条删除）
	NextAt int64  `json:"next_at"`          // unix 秒；早于此刻才允许重试
	Reason string `json:"reason,omitempty"` // 最近一次失败原因（截断、单行）
}

// listSyncState 源列表自动同步的运行痕迹（落盘 dataDir/cache/source_list_sync.json）。
type listSyncState struct {
	LastAt      int64                   `json:"last_at"`      // 上次清单抓取成功时刻（unix 秒）
	LastFetched int                     `json:"last_fetched"` // 上轮清单条数
	LastAdded   int                     `json:"last_added"`   // 上轮新增源数
	LastAlready int                     `json:"last_already"` // 上轮已存在数
	LastFailed  int                     `json:"last_failed"`  // 上轮失败数
	LastBackoff int                     `json:"last_backoff"` // 上轮因退避跳过数
	Fails       map[string]listSyncFail `json:"fails,omitempty"`
}

// SetListSyncPath 注入退避状态落盘路径（main 启动时调用；空串 = 只存内存，
// 单测与未接线场景不落盘）。
func (s *Server) SetListSyncPath(p string) {
	s.listSyncMu.Lock()
	defer s.listSyncMu.Unlock()
	s.listSyncPath = p
	s.listSyncLoad = false
	s.listSync = listSyncState{}
	s.loadListSyncLocked()
}

// loadListSyncLocked 惰性载入状态文件。缺失/损坏/不可读 = 空状态（安全降级，
// 最坏退化成「没有退避」，绝不阻塞同步）。
func (s *Server) loadListSyncLocked() {
	if s.listSyncLoad || s.listSyncPath == "" {
		return
	}
	s.listSyncLoad = true
	b, err := os.ReadFile(s.listSyncPath)
	if err != nil {
		return
	}
	var st listSyncState
	if json.Unmarshal(b, &st) != nil {
		return
	}
	if st.Fails == nil {
		st.Fails = map[string]listSyncFail{}
	}
	s.listSync = st
}

// saveListSyncLocked 原子写（临时名 + rename）。失败静默：状态仅是观测辅助，
// 写不进去不该影响同步本身。
func (s *Server) saveListSyncLocked() {
	if s.listSyncPath == "" {
		return
	}
	b, err := json.Marshal(s.listSync)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.listSyncPath), 0o755); err != nil {
		return
	}
	tmp := s.listSyncPath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if os.Rename(tmp, s.listSyncPath) != nil {
		_ = os.Remove(tmp)
	}
}
