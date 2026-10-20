// Package operation 提供单一活动操作队列（安装/升级/卸载/启动/停用互斥）。
package operation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// 状态。
const (
	StateQueued  = "queued" // 0.6.346：已入队、等待前一个操作结束
	StateRunning = "running"
	StateDone    = "done"
	StateError   = "error"
)

// View 是操作的可序列化视图（不含锁）。
type View struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // install / upgrade / uninstall / start / stop
	Target     string    `json:"target"`
	State      string    `json:"state"`
	Msg        string    `json:"msg"`
	Progress   float64   `json:"progress"` // 0-100
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Err        string    `json:"error,omitempty"`
}

// Op 是一次长操作（内部持有锁）。
type Op struct {
	mu         sync.Mutex
	id         string
	kind       string
	target     string
	state      string
	msg        string
	progress   float64
	startedAt  time.Time
	finishedAt time.Time
	errMsg     string
}

func newOp(kind, target string) *Op {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &Op{
		id:        hex.EncodeToString(b),
		kind:      kind,
		target:    target,
		state:     StateRunning,
		startedAt: time.Now(),
	}
}

func (o *Op) update(msg string, progress float64) {
	o.mu.Lock()
	o.msg = msg
	o.progress = progress
	o.mu.Unlock()
}

func (o *Op) setErr(err string) {
	o.mu.Lock()
	o.state = StateError
	o.errMsg = err
	o.finishedAt = time.Now()
	o.mu.Unlock()
}

func (o *Op) setDone() {
	o.mu.Lock()
	o.state = StateDone
	o.finishedAt = time.Now()
	o.mu.Unlock()
}

// View 返回当前快照。
func (o *Op) View() View {
	o.mu.Lock()
	defer o.mu.Unlock()
	return View{
		ID: o.id, Kind: o.kind, Target: o.target,
		State: o.state, Msg: o.msg, Progress: o.progress,
		StartedAt: o.startedAt, FinishedAt: o.finishedAt, Err: o.errMsg,
	}
}

// pendingOp 是排队中的操作（尚未开始执行）。
type pendingOp struct {
	op *Op
	fn RunFn
}

// Queue 执行长操作，最多 limit 个并行（默认 1=严格串行），其余按提交顺序排队。
//
// 0.6.346（用户反馈「同时装几个应用只会报错、不排队」）：此前第二个操作
// 直接被拒（「已有操作正在进行」），用户必须等前一个跑完再点一次。现在
// 提交即入队并返回 queued 视图，SSE 会持续回报「排队中（前面还有 N 个）」，
// 轮到自己时自动执行——多选安装/更新不再报错。
//
// 0.6.347（用户反馈「后面的应用直接不能下载」）：安装操作里最慢的是下载，
// 而平台 daemon 任务表只容得下一个安装任务。现在 limit 放宽到 3，让多个
// 应用并行下载；真正触碰平台通道的步骤（暂存/安装/升级/卸载/启停）另外
// 走 operation.Gate（容量 1）严格串行。操作体进入「等安装闸门」时调用
// Detach(ctx) 让出额度，后续排队的应用即可立刻开始自己的下载。
type Queue struct {
	mu       sync.Mutex
	limit    int
	running  []*Op
	detached map[string]bool // 已让出并发额度的运行中操作（等闸门中）
	pending  []pendingOp
	history  []View
}

func NewQueue() *Queue {
	return &Queue{limit: 1, detached: make(map[string]bool)}
}

// SetLimit 调整并行额度（<1 视为 1）。调大后立即从队列里补位开跑。
func (q *Queue) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	q.mu.Lock()
	q.limit = n
	q.mu.Unlock()
	q.pump()
}

// Limit 返回当前并行额度。
func (q *Queue) Limit() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.limit
}

// RunFn 是操作体，可通过 progress 回调更新进度与消息。
type RunFn func(ctx context.Context, progress func(msg string, pct float64)) error

// Start 提交一个操作：空闲则立即开始，否则按提交顺序排队（不再拒绝）。
// 返回值在空闲起步时是 running，排队时是 queued（可用 Position 查队位）。
func (q *Queue) Start(kind, target string, fn RunFn) (View, error) {
	op := newOp(kind, target)
	op.mu.Lock()
	op.state = StateQueued
	op.mu.Unlock()
	q.mu.Lock()
	q.pending = append(q.pending, pendingOp{op: op, fn: fn})
	q.mu.Unlock()
	q.pump()
	return op.View(), nil
}

// pump 在有空闲额度时按顺序取出队首开始执行（调用方不持锁）。
func (q *Queue) pump() {
	q.mu.Lock()
	var starts []pendingOp
	for len(q.pending) > 0 && q.activeLocked() < q.limit {
		item := q.pending[0]
		q.pending = q.pending[1:]
		item.op.mu.Lock()
		item.op.state = StateRunning
		item.op.startedAt = time.Now()
		item.op.mu.Unlock()
		q.running = append(q.running, item.op)
		starts = append(starts, item)
	}
	q.mu.Unlock()
	for _, item := range starts {
		go q.run(item.op, item.fn)
	}
}

// activeLocked 统计占用并发额度的操作数（已 Detach 的等闸门操作不占额度）。
// 调用方必须持 q.mu。
func (q *Queue) activeLocked() int {
	n := 0
	for _, op := range q.running {
		if !q.detached[op.id] {
			n++
		}
	}
	return n
}

// run 执行一个操作体，结束后入 history 并推进队列。
func (q *Queue) run(op *Op, fn RunFn) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	h := &Handle{
		Progress: func(msg string, pct float64) { op.update(msg, pct) },
		detach: func() {
			q.mu.Lock()
			if q.detached[op.id] {
				q.mu.Unlock()
				return
			}
			q.detached[op.id] = true
			q.mu.Unlock()
			q.pump() // 让出额度 → 后续排队的应用立刻开始下载
		},
	}
	err := fn(WithHandle(ctx, h), h.Progress)
	if err == nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("操作超时（25 分钟）")
	}
	if err != nil {
		op.setErr(err.Error())
	} else {
		op.setDone()
	}
	q.mu.Lock()
	for i, r := range q.running {
		if r == op {
			q.running = append(q.running[:i], q.running[i+1:]...)
			break
		}
	}
	delete(q.detached, op.id)
	q.history = append([]View{op.View()}, q.history...)
	if len(q.history) > 20 {
		q.history = q.history[:20]
	}
	q.mu.Unlock()
	q.pump()
}

// StartSync 是 Start 的同步版（0.6.312 B1/F1）：提交并等待本操作到终态，
// 供后台批次（自动更新）逐条执行并统计结果。0.6.346 起与 Start 共用同一
// 队列：前面有操作时不再拒绝，而是排队等轮到自己；ctx 取消且尚未开跑时
// 直接撤出队列（批次循环按 ctx.Err() 退出）。
func (q *Queue) StartSync(ctx context.Context, kind, target string, fn RunFn) (View, error) {
	op := newOp(kind, target)
	op.mu.Lock()
	op.state = StateQueued
	op.mu.Unlock()
	q.mu.Lock()
	q.pending = append(q.pending, pendingOp{op: op, fn: fn})
	q.mu.Unlock()
	q.pump()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		v := op.View()
		switch v.State {
		case StateDone:
			return v, nil
		case StateError:
			return v, fmt.Errorf("%s", v.Err)
		}
		select {
		case <-ctx.Done():
			// 还在队列里就直接撤掉；已在执行则等它自己结束（不打断平台安装）。
			if q.removeQueued(op.id) {
				return op.View(), ctx.Err()
			}
			return op.View(), ctx.Err()
		case <-tick.C:
		}
	}
}

// removeQueued 撤销一个尚未开跑的操作：出队 → 标记取消 → 入 history。
// 返回是否命中（未在排队中返回 false）。history 落一条 error 终态，等待中的
// SSE 才能按 ID 取到「已取消」而不是无限轮询。
func (q *Queue) removeQueued(id string) bool {
	q.mu.Lock()
	idx := -1
	for i := range q.pending {
		if q.pending[i].op.id == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		q.mu.Unlock()
		return false
	}
	op := q.pending[idx].op
	q.pending = append(q.pending[:idx], q.pending[idx+1:]...)
	q.mu.Unlock()

	op.setErr("已取消")
	q.mu.Lock()
	q.history = append([]View{op.View()}, q.history...)
	if len(q.history) > 20 {
		q.history = q.history[:20]
	}
	q.mu.Unlock()
	return true
}

// Current 返回当前活动操作视图（最早开始的那个；无则 nil）。
// 多操作并行时用 Running() 取全部。
func (q *Queue) Current() *View {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.running) > 0 {
		v := q.running[0].View()
		return &v
	}
	return nil
}

// Running 返回所有正在执行的操作视图（按开始先后）。
func (q *Queue) Running() []View {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]View, 0, len(q.running))
	for _, op := range q.running {
		out = append(out, op.View())
	}
	return out
}

// Pending 返回排队中的操作视图（按提交顺序）。0.6.346：任务条与
// /api/tasks 需要把「排队中」也展示出来，否则用户点了安装看不到任何反馈。
func (q *Queue) Pending() []View {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]View, 0, len(q.pending))
	for _, p := range q.pending {
		out = append(out, p.op.View())
	}
	return out
}

// Position 返回该操作在队列中的位次（1 = 下一个执行；0 = 不在排队）。
func (q *Queue) Position(id string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.pending {
		if q.pending[i].op.id == id {
			return i + 1
		}
	}
	return 0
}

// Status 按 ID 返回操作视图（当前 → 排队 → 历史；未找到 nil）。
// SSE 轮询用它区分「排队中 / 执行中 / 已终态」，不会把排队误判为完成。
func (q *Queue) Status(id string) *View {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, op := range q.running {
		if op.id == id {
			v := op.View()
			return &v
		}
	}
	for _, p := range q.pending {
		if p.op.id == id {
			v := p.op.View()
			return &v
		}
	}
	for i := range q.history {
		if q.history[i].ID == id {
			v := q.history[i]
			return &v
		}
	}
	return nil
}

// History 返回历史操作视图（最新在前）。
func (q *Queue) History() []View {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]View, len(q.history))
	copy(out, q.history)
	return out
}

// Remove 删除一条终态历史操作（0.6.261：设置页「已下载 FPK」失败行删除入口——
// 官方 cloud 下载走本队列而非下载管理器，失败行此前无删除入口）。
// 语义：只删 history 条目，不中断任何后台执行（终态操作已无副作用）；
// current 操作或 running 条目拒绝删除（返回 error）；未找到返回 (false, nil)。
func (q *Queue) Remove(id string) (bool, error) {
	// 0.6.346：排队中的操作可直接撤出（删除 = 取消；未执行故无副作用）。
	if q.removeQueued(id) {
		return true, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, op := range q.running {
		if op.id == id {
			return false, fmt.Errorf("操作正在进行，无法删除: %s", id)
		}
	}
	for i := range q.history {
		if q.history[i].ID == id {
			if q.history[i].State == StateRunning {
				return false, fmt.Errorf("操作正在进行，无法删除: %s", id)
			}
			q.history = append(q.history[:i], q.history[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

// Find 按 ID 查历史操作视图（最近 20 条内；未找到返回 nil）。
// SSE 轮询必须用它取终态：操作结束时「清空 current + 入 history」是同一
// 把锁内的原子动作，慢轮询可能恰好错过 running→error 的窗口——只看
// Current()==nil 会把失败误报成「完成」。
func (q *Queue) Find(id string) *View {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.history {
		if q.history[i].ID == id {
			v := q.history[i]
			return &v
		}
	}
	return nil
}
