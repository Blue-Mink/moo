// Package logagg 提供 host 级日志聚合（0.6.321 F1）。
//
// 背景（issue #3 日志刷爆，14min 32MB 实锤）：DNS 抖动窗口内，每个候选
// 主机的「跳过/失败」日志零节流，1 次图标请求 = 3~4 行，峰值 41,836
// 行/min。聚合策略：同「动作+主机」键首条完整记录；60s 窗口内同键只计数；
// 窗口末出一条聚合行（含次数+首次错误原文）。成功日志与「候选 N 个」行
// 不聚合（保诊断价值）。
package logagg

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// DefaultWindow 聚合窗口（60s）。
const DefaultWindow = 60 * time.Second

type entry struct {
	action   string
	host     string
	count    int
	firstMsg string
}

// Aggregator 按 (action, host) 聚合日志。线程安全。
type Aggregator struct {
	window time.Duration
	mu     sync.Mutex
	items  map[string]*entry
	stop   chan struct{}
	done   chan struct{}
}

// New 创建聚合器并启动 flush 协程（窗口末出聚合行）。
func New(window time.Duration) *Aggregator {
	a := &Aggregator{
		window: window,
		items:  map[string]*entry{},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go a.loop()
	return a
}

// Default 进程级聚合器（60s 窗口）。
var Default = New(DefaultWindow)

// maxItems 322R4：聚合键数量上限（对齐 netguard negCache 的 4096）——窗口内
// 超限时整表清零重计（聚合窗口仅 60s，清零只丢一次计数，影响极小），防恶意/
// 异常源在窗口内灌入海量唯一主机致 items 无界增长。
const maxItems = 4096

// Record 记录一次失败/跳过：首条打完整日志，窗口内同键只计数。
// action 形如 "icon"/"race"/"dl"，host 为候选主机名。
func (a *Aggregator) Record(action, host, format string, args ...any) {
	key := action + "\x00" + strings.ToLower(host)
	a.mu.Lock()
	e, ok := a.items[key]
	if !ok {
		if len(a.items) >= maxItems {
			a.items = map[string]*entry{} // 兜底防无界增长（对齐 netguard）
		}
		e = &entry{action: action, host: host}
		a.items[key] = e
	}
	e.count++
	first := e.count == 1
	if first {
		e.firstMsg = fmt.Sprintf(format, args...)
	}
	a.mu.Unlock()
	if first {
		log.Printf("[%s] %s", action, e.firstMsg)
	}
}

func (a *Aggregator) loop() {
	defer close(a.done)
	tk := time.NewTicker(a.window)
	defer tk.Stop()
	for {
		select {
		case <-a.stop:
			a.Flush() // 退出前把未出窗的聚合行落掉
			return
		case <-tk.C:
			a.Flush()
		}
	}
}

// Flush 立即出窗：count>1 的键出聚合行，所有键计数清零（窗口重开）。
// 导出供单测与 Close 使用。
func (a *Aggregator) Flush() {
	a.mu.Lock()
	batch := a.items
	a.items = map[string]*entry{}
	a.mu.Unlock()
	for _, e := range batch {
		if e.count > 1 {
			log.Printf("[%s] 聚合 %d 次（主机 %s；首次=%s）", e.action, e.count, e.host, e.firstMsg)
		}
	}
}

// Close 停止 flush 协程（进程退出时调用；不 Close 亦无害——协程随进程生命周期）。
func (a *Aggregator) Close() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
	<-a.done
}
