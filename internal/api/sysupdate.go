package api

// sysupdate.go —— fnOS 系统更新「只提示不安装」的通知链路。
// 采集见 internal/sysupdate；此处负责 12h 去重、六变体文案与事件推送。
// 触发点：StartSelfMonitor 的 5 分钟循环（Collector 自带 12h TTL，读缓存零成本）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"net/http"

	"moo/internal/notify"
	"moo/internal/sysupdate"
)

func (s *Server) sysUpdateStatePath() string {
	d := dataDirOf(s)
	if d == "" {
		return ""
	}
	return filepath.Join(d, "sysupdate_state.json")
}

// sysUpdateNotifiedVersion 读取「已推送过的可用版本」，用于同版本只推一次。
func (s *Server) sysUpdateNotifiedVersion() string {
	p := s.sysUpdateStatePath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var st struct {
		Notified string `json:"notified"`
	}
	if json.Unmarshal(b, &st) != nil {
		return ""
	}
	return st.Notified
}

func (s *Server) saveSysUpdateNotified(v string) {
	p := s.sysUpdateStatePath()
	if p == "" {
		return
	}
	b, _ := json.Marshal(map[string]any{"notified": v, "at": time.Now().Format(time.RFC3339)})
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

// sysUpdateRun 采集并按需推送（force=true 忽略 12h 缓存）。返回采集结果。
func (s *Server) sysUpdateRun(ctx context.Context, force bool) sysupdate.Status {
	if s.SysUpdate == nil {
		return sysupdate.Status{Err: "系统更新采集未启用"}
	}
	st := s.SysUpdate.Get(ctx, force)
	if !st.OK || !st.Available || st.Latest == "" {
		return st
	}
	if s.sysUpdateNotifiedVersion() == st.Latest {
		return st
	}
	s.notifySystemUpdate(st)
	s.saveSysUpdateNotified(st.Latest)
	return st
}

// sysUpdateTick 由 5 分钟自检循环调用；TTL 未到 == 只读缓存，不发请求。
func (s *Server) sysUpdateTick() {
	if s.SysUpdate == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.sysUpdateRun(ctx, false)
}

// sysUpdateStatus GET /api/sysupdate：读缓存（不触发网络）。
func (s *Server) sysUpdateStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, s.sysUpdateRun(ctx, false))
}

// sysUpdateCheck POST /api/sysupdate/check：强制刷新（设置页手动检查用）。
func (s *Server) sysUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	writeJSON(w, s.sysUpdateRun(ctx, true))
}

// notifySystemUpdate 推送「系统有可用更新」——六变体对齐其它类别的排版规范：
// 标题单行、友好=摘要+明细、简洁=关键数字、完整=全量说明、卡片两列。
func (s *Server) notifySystemUpdate(st sysupdate.Status) {
	const title = "系统有可用更新"
	head := fmt.Sprintf("fnOS %s → **%s**", st.Current, st.Latest)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", head)
	fmt.Fprintf(&b, "- 当前版本：%s\n", st.Current)
	fmt.Fprintf(&b, "- 可用版本：%s\n", st.Latest)
	if st.KernelLatest != "" && st.KernelLatest != st.KernelCurrent {
		fmt.Fprintf(&b, "- 内核：%s → %s\n", orDash(st.KernelCurrent), st.KernelLatest)
	} else if st.KernelCurrent != "" {
		fmt.Fprintf(&b, "- 内核：%s\n", st.KernelCurrent)
	}
	if len(st.Notes) > 0 {
		fmt.Fprintf(&b, "\n更新说明（前 %d 条）：\n", len(st.Notes))
		for _, n := range st.Notes {
			fmt.Fprintf(&b, "- %s\n", strings.ReplaceAll(n, "\n", " "))
		}
	}
	fmt.Fprintf(&b, "\n本通知只做提示，不会自动安装；升级请在 面板 → 设置 → 更新和还原 → 系统更新 操作。")

	var tb strings.Builder
	fmt.Fprintf(&tb, "fnOS 系统更新可用：\n\n| 项目 | 当前 | 最新 |\n| :-- | :-- | :-- |\n")
	fmt.Fprintf(&tb, "| 系统 | %s | %s |\n", mdCell(st.Current), mdCell(st.Latest))
	if st.KernelCurrent != "" || st.KernelLatest != "" {
		fmt.Fprintf(&tb, "| 内核 | %s | %s |\n", mdCell(orDash(st.KernelCurrent)), mdCell(orDash(st.KernelLatest)))
	}
	if st.Branch > 0 {
		fmt.Fprintf(&tb, "| 更新分支 | %d | %d |\n", st.Branch, st.Branch)
	}

	rows := []notify.CardRow{
		{Key: "当前", Value: orDash(st.Current)},
		{Key: "可用", Value: st.Latest},
	}
	if st.KernelLatest != "" {
		rows = append(rows, notify.CardRow{Key: "内核", Value: st.KernelLatest})
	}
	if len(st.Notes) > 0 {
		rows = append(rows, notify.CardRow{Key: "说明", Value: fmt.Sprintf("%d 条", len(st.Notes))})
	}
	rows = append(rows, notify.CardRow{Key: "操作", Value: "面板 → 系统更新"})

	// 完整模式：不截断、给出全部说明行。
	var bFull, tbFull strings.Builder
	fmt.Fprintf(&bFull, "%s\n\n", head)
	fmt.Fprintf(&bFull, "- 当前版本：%s\n- 可用版本：%s\n", st.Current, st.Latest)
	if st.KernelLatest != "" {
		fmt.Fprintf(&bFull, "- 内核：%s → %s\n", orDash(st.KernelCurrent), st.KernelLatest)
	}
	if st.Branch > 0 {
		fmt.Fprintf(&bFull, "- 更新分支：branch %d\n", st.Branch)
	}
	if len(st.Notes) > 0 {
		bFull.WriteString("\n更新说明：\n")
		for _, n := range st.Notes {
			fmt.Fprintf(&bFull, "- %s\n", n)
		}
	}
	bFull.WriteString("\n本通知只做提示，不会自动安装；升级请在 面板 → 设置 → 更新和还原 → 系统更新 操作。")
	tbFull.WriteString(tb.String())

	s.notifyEventRichV("system_update_available", title, strings.TrimSpace(b.String()), tb.String(), rows,
		"", fmt.Sprintf("fnOS %s → %s", st.Current, st.Latest), true,
		&notify.Variants{
			ContentConcise: fmt.Sprintf("fnOS %s → %s", st.Current, st.Latest),
			ContentFull:    bFull.String(),
			TableFull:      tbFull.String(),
			RowsConcise:    rows[:2],
			ConciseSub:     "系统更新只提示，不自动安装",
		})
}
