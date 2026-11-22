package api

// P1-1：手动更新队列（借鉴 FnDepot 的 /api/updates/manual-count）。
//
// 数据源 = 自动更新的「需人工处理」负缓存（auto_update_skips.json）：
//   wizard  —— 升级要先在界面确认必填项（12h）
//   blocked —— 平台标记自动更新失败/依赖占用（2h）
//   unknown —— 任务结果未知，大应用升级超预算但平台可能仍在跑（6h）
//
// 这些应用自动通道跑不通，但用户仍需要看到「哪几个、为什么、什么时候可以
// 再试」。这里只做只读聚合；真正的更新仍走原有手动更新链路。

import (
	"net/http"
	"sort"
)

type manualUpdateItem struct {
	App         string `json:"app"`
	Version     string `json:"version"`
	Sha         string `json:"sha,omitempty"`
	Reason      string `json:"reason"`
	ReasonLabel string `json:"reason_label"`
	Until       string `json:"until"`
	Missing     string `json:"missing,omitempty"`
}

func manualReasonLabel(reason string) string {
	switch reason {
	case autoUpdateSkipReasonWizard:
		return "升级需先在界面确认向导"
	case autoUpdateSkipReasonBlocked:
		return "平台标记需人工处理（依赖占用等）"
	case autoUpdateSkipReasonUnknown:
		return "平台任务结果未知（可能仍在执行）"
	default:
		return "需人工处理"
	}
}

// listManualUpdates GET /api/updates/manual（requireAdmin）
// 返回当前处于「需人工处理」静默期的应用清单，按到期时间升序（先解禁的在前）。
func (s *Server) listManualUpdates(w http.ResponseWriter, r *http.Request) {
	skips := s.autoUpdateSkips()
	items := make([]manualUpdateItem, 0, len(skips))
	for _, e := range skips {
		if e.App == "" {
			continue
		}
		// 下架黑名单闸：已停止分发的应用不该再出现在「需人工处理」队列里
		// 提示用户去更新（负缓存里可能还留着它过期前的判定）。本地队列拿不到
		// 源名，按应用名整名拦截。
		if _, hit := s.blacklistHitByAppName(e.App, e.Sha); hit {
			continue
		}
		items = append(items, manualUpdateItem{
			App:         e.App,
			Version:     e.Version,
			Sha:         e.Sha,
			Reason:      e.Reason,
			ReasonLabel: manualReasonLabel(e.Reason),
			Until:       e.Until,
			Missing:     e.Missing,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Until != items[j].Until {
			return items[i].Until < items[j].Until
		}
		return items[i].App < items[j].App
	})
	writeJSON(w, map[string]any{"count": len(items), "items": items})
}
