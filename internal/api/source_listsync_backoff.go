package api

// 2026-11-26 B-SRC-LISTSYNC：失败退避的纯逻辑（时长计算 + 过滤），
// 与状态落盘分文件，便于单测直接断言（不依赖时间源）。

import (
	"sort"
	"time"

	"moo/internal/source"
)

// listSyncBackoffOf 连续失败 count 次后的退避时长：6h → 12h → 24h（封顶）。
func listSyncBackoffOf(count int) time.Duration {
	if count < 1 {
		count = 1
	}
	d := listSyncBackoffBase
	for i := 1; i < count; i++ {
		d *= 2
		if d >= listSyncBackoffMax {
			return listSyncBackoffMax
		}
	}
	if d > listSyncBackoffMax {
		d = listSyncBackoffMax
	}
	return d
}

func (s *Server) listSyncFilter(urls []string) (allowed []string, skipped int) {
	now := time.Now().Unix()
	s.listSyncMu.Lock()
	defer s.listSyncMu.Unlock()
	s.loadListSyncLocked()
	allowed = make([]string, 0, len(urls))
	for _, u := range urls {
		if f, ok := s.listSync.Fails[source.NormalizeSourceURL(u)]; ok && f.NextAt > now {
			skipped++
			continue
		}
		allowed = append(allowed, u)
	}
	return allowed, skipped
}

// listSyncRecord 记录一轮结果：名单内「非失败」的 URL 清退避（成功即清），
// 失败按次递增退避，并刷新「上次同步」摘要字段（落盘）。
func (s *Server) listSyncRecord(res sourceListSyncResult, allowed []string, failURL map[string]string) {
	now := time.Now()
	s.listSyncMu.Lock()
	defer s.listSyncMu.Unlock()
	s.loadListSyncLocked()
	if s.listSync.Fails == nil {
		s.listSync.Fails = map[string]listSyncFail{}
	}
	for _, u := range allowed {
		if _, bad := failURL[u]; bad {
			continue
		}
		delete(s.listSync.Fails, source.NormalizeSourceURL(u))
	}
	for u, reason := range failURL {
		key := source.NormalizeSourceURL(u)
		f := s.listSync.Fails[key]
		f.Count++
		f.NextAt = now.Add(listSyncBackoffOf(f.Count)).Unix()
		f.Reason = shortenListSyncText(reason, 120)
		s.listSync.Fails[key] = f
	}
	if n := len(s.listSync.Fails) - listSyncFailCap; n > 0 {
		s.pruneListSyncFailsLocked(n)
	}
	s.listSync.LastAt = now.Unix()
	s.listSync.LastFetched = res.Fetched
	s.listSync.LastAdded = res.Added
	s.listSync.LastAlready = res.Already
	s.listSync.LastFailed = res.Failed
	s.listSync.LastBackoff = res.Backoff
	s.saveListSyncLocked()
}

// pruneListSyncFailsLocked 超上限时按「最晚到期」淘汰（保留最需要重试的）。
//
// 批次 1 F3（N7）：旧实现按 NextAt **升序**排后删前 n 条 = 恰好先把「马上到期、
// 最该重试」的键删掉，留下全是「一天后才重试」的死链 —— 淘汰方向反了。
// 现在按 NextAt 降序排后删前 n 条，被淘汰的一定是最不紧急的那些。
func (s *Server) pruneListSyncFailsLocked(n int) {
	type kv struct {
		k  string
		at int64
	}
	all := make([]kv, 0, len(s.listSync.Fails))
	for k, v := range s.listSync.Fails {
		all = append(all, kv{k, v.NextAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at > all[j].at })
	for i := 0; i < n && i < len(all); i++ {
		delete(s.listSync.Fails, all[i].k)
	}
}

// listSyncSnapshot 「上次同步 X 前 · 新增 N」的只读快照（API 用）。
func (s *Server) listSyncSnapshot() (at int64, added, fetched, failed, backoff int) {
	s.listSyncMu.Lock()
	defer s.listSyncMu.Unlock()
	s.loadListSyncLocked()
	return s.listSync.LastAt, s.listSync.LastAdded, s.listSync.LastFetched,
		s.listSync.LastFailed, s.listSync.LastBackoff
}
