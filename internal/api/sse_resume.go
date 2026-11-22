package api

// SSE_RESUME（2026-11-21，B 方案）：可续传 SSE。
//
// 用户报障：iOS WebKit 点「更新」，SSE 在首块事件之后断流（读流阶段抛
// `TypeError: Load failed`）→ 前端只看到「更新请求失败 + Load failed」；
// 而服务端 sseRunOp 见 r.Context().Done() 即 return，操作在队列里照旧跑完
// → 「提示失败、实际成功」。
//
// 修法：每条事件带 "id": <opID>；新增 GET /api/operations/{id}/events 按 id
// 跟随既有操作（只读，不需要 admin）；前端断流后凭 id 续传（无 id 时按
// kind+target + 5 分钟内开始 兜底匹配）。终态副作用按 opID 幂等——断线后
// 可能有第二个连接跟随同一操作，目录失效/计数不能漏也不能重复。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"moo/internal/operation"
)

// opEvent 给事件体补上 "id": <opID>——客户端凭它断线续传（SSE_RESUME）。
func opEvent(id string, ev map[string]any) map[string]any {
	if ev == nil {
		ev = make(map[string]any, 1)
	}
	if id != "" {
		ev["id"] = id
	}
	return ev
}

// finalizeOp 执行操作终态副作用（目录失效 + 本机安装计数），按 opID 幂等。
// 原实现只在「原连接看到 done」时执行 → 客户端一断线就漏掉目录失效，
// 用户重连后仍看到旧的「有更新」徽章；反向（多连接各执行一次）又会让
// 本机计数重复累加。sync.Map 记已结算的 opID，二者都不会发生。
// 返回 true 表示本次调用真正执行了副作用（首次结算该 opID）。
func (s *Server) finalizeOp(v operation.View) bool {
	if v.ID == "" {
		return false
	}
	if _, loaded := s.opFinalized.LoadOrStore(v.ID, struct{}{}); loaded {
		return false
	}
	s.invalidateCatalog() // 安装/更新/卸载/启停都改变目录内容
	// 安装/更新成功累计本机次数（第三方源应用无全局下载量，列表回退展示）
	if v.Kind == "install" || v.Kind == "update" {
		s.bumpLocalInstalls(v.Target)
	}
	return true
}

// getOperationJSON 是 GET /api/operations/{id}：返回单个操作的当前状态（JSON）。
//
// 用途（P0-1 终态轮询兜底）：移动端断流后，SSE 续传可能连续失败（网关持续
// 掐连接）；客户端退化为按 opID 定时查询本端点，直到 done/error。轮询没有
// 长连接，弱网/网关下比 SSE 可靠。
//
// 终态时顺带执行 finalizeOp：客户端断开后服务端可能没有第二个 SSE 跟随者，
// 目录失效/本机计数就会漏掉（用户重连仍见旧「有更新」徽章）。该副作用按
// opID 幂等，轮询与续传同时命中也不会重复执行。
func (s *Server) getOperationJSON(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Ops == nil {
		writeErr(w, http.StatusNotFound, errors.New("操作不存在或已过期"))
		return
	}
	st := s.Ops.Status(id)
	if st == nil {
		writeErr(w, http.StatusNotFound, errors.New("操作不存在或已过期"))
		return
	}
	if st.State == operation.StateDone || st.State == operation.StateError {
		s.finalizeOp(*st)
	}
	writeJSON(w, st)
}

// followOperationSSE 是 GET /api/operations/{id}/events：跟随一个已有操作
// （断线续传用）。id 不存在或已过期（history 只留最近 20 条）→ 404 JSON，
// 客户端据此退回 kind+target 兜底匹配。
func (s *Server) followOperationSSE(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Ops == nil || s.Ops.Status(id) == nil {
		writeErr(w, http.StatusNotFound, errors.New("操作不存在或已过期"))
		return
	}
	f := sseStart(w)
	// 立刻补发一条当前状态，续传客户端不必干等首个 700ms 轮询。
	s.sseFollowOp(r.Context(), w, f, id, true)
}

// sseFollowOp 按 id 轮询操作状态并以 SSE 回报，到终态（done/error）即返回。
// ctx 用于「客户端断开即 return」——操作本身在队列里继续跑，不受影响。
// announce=true 表示进入循环先补发一条当前状态（续传端点用；sseRunOp 自己已
// 发过首条「启动…」事件，传 false 避免重复）。
//
// 事件格式与 sseRunOp 历史行为完全一致（step=queued / kind / done / error，
// error 带 detail），新增的只有每条事件上的 "id"。
func (s *Server) sseFollowOp(ctx context.Context, w http.ResponseWriter, f http.Flusher, id string, announce bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Ops == nil {
		return
	}
	lastMsg, lastPct := "", -1
	first := announce
	// 审计 B2：长时间静默（排队/大包下载）会被面板网关或 NAT 当空闲连接掐断。
	// 每 15s 写一条 SSE 注释行（客户端按规范忽略），保活且不打扰事件解析。
	lastPing := time.Now()
	for {
		st := s.Ops.Status(id)
		if st == nil {
			return // 已被清理（history 只保留最近 20 条），无可跟随的状态
		}
		switch st.State {
		case operation.StateQueued:
			msg := "排队中…"
			if pos := s.Ops.Position(id); pos > 0 {
				msg = fmt.Sprintf("排队中（前面还有 %d 个任务）", pos)
			}
			if first || msg != lastMsg {
				sseSend(w, f, opEvent(id, map[string]any{"step": "queued", "progress": 0, "message": msg}))
				lastMsg = msg
			}
		case operation.StateRunning:
			// running：进度/消息变化才推送（避免刷屏）。
			pct := int(st.Progress / 2)
			if first || st.Msg != lastMsg || pct != lastPct {
				sseSend(w, f, opEvent(id, map[string]any{"step": st.Kind, "progress": st.Progress, "message": st.Msg}))
				lastMsg, lastPct = st.Msg, pct
			}
		case operation.StateDone:
			s.finalizeOp(*st)
			sseSend(w, f, opEvent(id, map[string]any{"step": "done", "progress": 100, "message": st.Msg}))
			return
		case operation.StateError:
			msg := st.Err
			if msg == "" {
				msg = "操作失败"
			}
			ev := map[string]any{"step": "error", "error": msg}
			// 0.6.5xx：失败原因的结构化补充（升级向导缺项 / 阻塞依赖）。
			// 前端凭 detail.kind 决定是弹向导、还是只提示，无需二次探测。
			if len(st.Detail) > 0 {
				ev["detail"] = st.Detail
			}
			sseSend(w, f, opEvent(id, ev))
			return
		}
		first = false
		if time.Since(lastPing) >= 15*time.Second {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err == nil {
				f.Flush()
			}
			lastPing = time.Now()
		}
		select {
		case <-ctx.Done():
			return // 客户端断开，操作在后台继续
		case <-time.After(700 * time.Millisecond):
		}
	}
}
