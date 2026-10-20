package operation

import (
	"context"
	"sync"
)

// Gate 是容量 1 的先进先出闸门。
//
// 0.6.347（用户反馈）：此前 Queue 把所有操作（含「下载+安装」整条链）串行，
// 前面有任务时后面的应用连安装包都下不来。现在下载阶段并行执行，只有真正
// 触碰平台通道的步骤（暂存 / 安装 / 升级 / 卸载 / 启停）要过这道闸门——
// 平台 daemon 的任务表同一时刻只认一个任务，必须严格串行。
type Gate struct {
	sem chan struct{}

	mu      sync.Mutex
	waiting int // 已提交但还没拿到闸门的操作数（含自己）
}

// NewGate 创建闸门。
func NewGate() *Gate { return &Gate{sem: make(chan struct{}, 1)} }

// Acquire 取闸门：拿到返回 release（幂等，调用方负责在离开平台通道时调用）；
// 需要等待时先回调一次 wait(前面还有几个任务)，再阻塞到轮到自己。
// ctx 取消且尚未拿到闸门时返回 ctx.Err()。
func (g *Gate) Acquire(ctx context.Context, wait func(pos int)) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	g.waiting++ // 计入「已持有 + 排队中」
	pos := g.waiting
	g.mu.Unlock()
	if pos > 1 && wait != nil {
		wait(pos - 1)
	}
	select {
	case g.sem <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-g.sem
				g.mu.Lock()
				g.waiting-- // 离开闸门：不再计入
				g.mu.Unlock()
			})
		}, nil
	case <-ctx.Done():
		g.mu.Lock()
		g.waiting--
		g.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Waiting 返回当前「已持有 + 仍在排队」的操作数，供测试/诊断用。
func (g *Gate) Waiting() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiting
}

// ── 操作体上下文句柄（让出并发额度）────────────────────────────────

type handleKey struct{}

// Handle 是队列为每个操作体准备的运行时句柄（经 ctx 传递，避免改动
// RunFn 的签名与所有既有调用点）。
type Handle struct {
	Progress func(msg string, pct float64)
	detach   func()
}

// WithHandle 把句柄挂到 ctx 上（Queue 内部使用）。
func WithHandle(ctx context.Context, h *Handle) context.Context {
	return context.WithValue(ctx, handleKey{}, h)
}

// Detach 让当前操作让出队列并发额度：操作已进入「纯等待」阶段（下载完成、
// 正在等安装闸门），后续排队的操作可以立刻开始各自的下载。幂等；不在队列
// 上下文里调用是安全的空操作。
func Detach(ctx context.Context) {
	if h, ok := ctx.Value(handleKey{}).(*Handle); ok && h.detach != nil {
		h.detach()
	}
}
