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

// Queue 同一时刻只允许一个活动操作。
type Queue struct {
	mu      sync.Mutex
	current *Op
	history []View
}

func NewQueue() *Queue { return &Queue{} }

// RunFn 是操作体，可通过 progress 回调更新进度与消息。
type RunFn func(ctx context.Context, progress func(msg string, pct float64)) error

// Start 提交一个操作；已有活动操作时拒绝。
func (q *Queue) Start(kind, target string, fn RunFn) (View, error) {
	q.mu.Lock()
	if q.current != nil {
		cur := q.current.View()
		if cur.State == StateRunning {
			q.mu.Unlock()
			return View{}, fmt.Errorf("已有操作正在进行: %s %s", cur.Kind, cur.Target)
		}
	}
	op := newOp(kind, target)
	q.current = op
	q.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer cancel()
		err := fn(ctx, func(msg string, pct float64) { op.update(msg, pct) })
		if err != nil {
			op.setErr(err.Error())
		} else {
			op.setDone()
		}
		q.mu.Lock()
		if q.current == op {
			q.current = nil
		}
		q.history = append([]View{op.View()}, q.history...)
		if len(q.history) > 20 {
			q.history = q.history[:20]
		}
		q.mu.Unlock()
	}()
	return op.View(), nil
}

// StartSync 是 Start 的同步版（0.6.312 B1/F1）：操作体在调用方 goroutine
// 内阻塞执行，供后台批次（自动更新）逐条执行并统计结果。互斥语义与 Start
// 相同——已有活动操作（手动或另一自动项）时拒绝；每操作 25 分钟超时与
// Start 一致。完成后同样入 history（前端任务条可见「更新 X」进行中）。
func (q *Queue) StartSync(ctx context.Context, kind, target string, fn RunFn) (View, error) {
	q.mu.Lock()
	if q.current != nil {
		cur := q.current.View()
		if cur.State == StateRunning {
			q.mu.Unlock()
			return View{}, fmt.Errorf("已有操作正在进行: %s %s", cur.Kind, cur.Target)
		}
	}
	op := newOp(kind, target)
	q.current = op
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		if q.current == op {
			q.current = nil
		}
		q.history = append([]View{op.View()}, q.history...)
		if len(q.history) > 20 {
			q.history = q.history[:20]
		}
		q.mu.Unlock()
	}()
	cctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()
	err := fn(cctx, func(msg string, pct float64) { op.update(msg, pct) })
	if err == nil && cctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("操作超时（25 分钟）")
	}
	if err != nil {
		op.setErr(err.Error())
	} else {
		op.setDone()
	}
	// 同步契约：操作体错误直接返回（视图/history 同步记录终态，供任务条
	// 事后查看）；ctx 取消同样以错误返回（批次循环按 ctx.Err() 退出）。
	return op.View(), err
}

// Current 返回当前活动操作视图（无则 nil）。
func (q *Queue) Current() *View {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current != nil {
		v := q.current.View()
		return &v
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
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current != nil && q.current.id == id {
		return false, fmt.Errorf("操作正在进行，无法删除: %s", id)
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
