package api

// P1-2：自更新状态机（借鉴 FnDepot app_updates 的 status / rolled_back 语义）。
//
// 现实约束：Moo 的自更新走飞牛应用中心的 update/task，升级过程 daemon 会杀掉
// 本进程；Moo 没有常驻外部守护，因此「新版本起不来时进程内自动回滚」做不到。
// 本文件落地能确定做到的三件事：
//   1) 提交升级瞬间记 journal（from / to / 时间 / submitted）；
//   2) 新进程启动时对账：版本 == to → confirmed；仍 == from → failed（升级未生效）；
//      其它 → unknown（需人工核对）；
//   3) failed/unknown 通过通知渠道告知一次，并暴露给前端（GET /api/self-update/last），
//      用户可据此重新发起自更新（仍是完整 FPK，配合 minisign 校验）。
//
// 不做的事（避免假装有回滚）：不自动降级、不保留旧二进制（FPK 升级没有可执行
// 的旧二进制可换回）。真要自动回滚需要平台级事务或外部 supervisor，另立项。

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	selfUpdateJournalFile = "self_update_state.json"

	selfUpdateStatusSubmitted = "submitted"
	selfUpdateStatusConfirmed = "confirmed"
	selfUpdateStatusFailed    = "failed"
	selfUpdateStatusUnknown   = "unknown"
)

type selfUpdateJournal struct {
	SchemaVersion int    `json:"schema_version"`
	FromVersion   string `json:"from_version"`
	ToVersion     string `json:"to_version"`
	SubmittedAt   string `json:"submitted_at"`
	Status        string `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Notified      bool   `json:"notified,omitempty"`
}

// lastSelfUpdate GET /api/self-update/last（requireAdmin）
// 返回上次自更新的对账结果；无记录时 status="none"。
func (s *Server) lastSelfUpdate(w http.ResponseWriter, r *http.Request) {
	j := s.readSelfUpdateJournal()
	if j == nil {
		writeJSON(w, map[string]any{"status": "none"})
		return
	}
	writeJSON(w, j)
}

func (s *Server) selfUpdateJournalPath() string {
	d := dataDirOf(s)
	if d == "" {
		return ""
	}
	return filepath.Join(d, selfUpdateJournalFile)
}

func (s *Server) readSelfUpdateJournal() *selfUpdateJournal {
	p := s.selfUpdateJournalPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var j selfUpdateJournal
	if json.Unmarshal(b, &j) != nil || j.ToVersion == "" {
		return nil
	}
	return &j
}

func (s *Server) writeSelfUpdateJournal(j *selfUpdateJournal) {
	p := s.selfUpdateJournalPath()
	if p == "" || j == nil {
		return
	}
	j.SchemaVersion = 1
	b, err := json.Marshal(j)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// recordSelfUpdateSubmitted 在应用中心接受升级任务后调用。
func (s *Server) recordSelfUpdateSubmitted(from, to string) {
	if to == "" {
		return
	}
	s.writeSelfUpdateJournal(&selfUpdateJournal{
		FromVersion: from,
		ToVersion:   to,
		SubmittedAt: time.Now().Format(time.RFC3339),
		Status:      selfUpdateStatusSubmitted,
	})
}

// markSelfUpdateSubmitFailed 提交阶段就失败（daemon 拒绝/无任务 ID）。
func (s *Server) markSelfUpdateSubmitFailed(detail string) {
	j := s.readSelfUpdateJournal()
	if j == nil {
		return
	}
	j.Status = selfUpdateStatusFailed
	j.Detail = detail
	s.writeSelfUpdateJournal(j)
}

// ReconcileSelfUpdateJournal 在新进程启动时对账上次自更新。
// 返回 (journal, changed)；changed=true 表示本次改写了结论。
func (s *Server) ReconcileSelfUpdateJournal() (*selfUpdateJournal, bool) {
	j := s.readSelfUpdateJournal()
	if j == nil || j.Status != selfUpdateStatusSubmitted {
		return j, false
	}
	cur := s.Version
	switch {
	case cur == j.ToVersion:
		j.Status = selfUpdateStatusConfirmed
		j.Detail = ""
	case j.FromVersion != "" && cur == j.FromVersion:
		j.Status = selfUpdateStatusFailed
		j.Detail = "升级未生效：商店仍是升级前版本（平台任务可能失败或已被回收）。可重新发起自更新，或在飞牛应用中心手动安装新版 FPK。"
	default:
		j.Status = selfUpdateStatusUnknown
		j.Detail = "无法确认升级结果：当前版本既不是升级目标也不是升级前版本，请到应用中心核对。"
	}
	s.writeSelfUpdateJournal(j)
	if j.Status != selfUpdateStatusConfirmed && !j.Notified && s.cfg() != nil {
		title := "自更新未生效"
		if j.Status == selfUpdateStatusUnknown {
			title = "自更新结果未知"
		}
		s.notifyEvent("store_update_error", title,
			"v"+j.FromVersion+" → v"+j.ToVersion+"："+j.Detail, false)
		j.Notified = true
		s.writeSelfUpdateJournal(j)
	}
	log.Printf("[self-update] 上次升级 v%s → v%s 对账结果: %s", j.FromVersion, j.ToVersion, j.Status)
	return j, true
}
