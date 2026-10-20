package api

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// race_stats.go — 0.6.332 F2：候选竞速日志聚合。
//
// 背景（全量测试 P2-1）：raceFetch 每轮打一行「候选 N 个」+ 每个候选打一行
// 「[race] 状态码 URL (t=…)」，线上最近 2000 行里 1954 行（97.7%）是它，
// 真实错误被淹（此前 118 条 ERROR 就藏在其中）。
//
// 改法：进程内按 60s 窗口聚合，一行给全窗口统计；窗口无活动则整窗静默。
//
//	[race] 60s: 42 次竞速 · 命中 39 · 全败 3 · 均 320ms · 最慢 3.2s（最慢 gh-proxy…/x.png）
//
// 保留：单次耗时 ≥3s 的**慢命中**仍立即单独打一行（定位卡顿）；
// 失败/非 200/安全跳过继续走既有 logagg（host 级聚合，含 URL 与错误原文）。

const (
	// 聚合窗口：每 60s 最多 1 行。
	raceFlushEvery = time.Minute
	// 慢命中阈值：单候选耗时 ≥ 此值立即单行告警。
	raceSlowHit = 3 * time.Second
	// 聚合行前缀里的窗口标签（与历史 [race] 前缀保持一致，便于 grep）。
	raceWindowLabel = "60s"
)

// raceWindow 进程内竞速统计（生产用单例 raceAgg）。
type raceWindow struct {
	emit func(string) // 日志出口（测试注入；生产=log.Printf）

	mu         sync.Mutex
	races      int           // begin 次数（一轮竞速算 1 次）
	hits       int           // 整轮拿到 200 的次数
	fails      int           // 整轮全败的次数
	cands      int           // 收到响应的候选数（均/最慢的分母）
	total      time.Duration // 候选响应耗时合计
	slowest    time.Duration
	slowestURL string
	active     bool // 本窗口是否有活动（无活动不打印）

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
}

func newRaceWindow() *raceWindow {
	return &raceWindow{
		emit:   func(s string) { log.Printf("%s", s) },
		stopCh: make(chan struct{}),
	}
}

// raceAgg 进程级单例（所有 README/图片/预览竞速共用）。
var raceAgg = newRaceWindow()

// start 首次使用时启动 60s 聚合循环（进程生命周期常驻）。
func (w *raceWindow) start() {
	w.startOnce.Do(func() {
		go func() {
			t := time.NewTicker(raceFlushEvery)
			defer t.Stop()
			for {
				select {
				case <-w.stopCh:
					return
				case <-t.C:
					w.tick()
				}
			}
		}()
	})
}

// stop 停止聚合循环（测试收尾用；生产不调用）。
func (w *raceWindow) stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// begin 记录一轮竞速开始。
func (w *raceWindow) begin() {
	w.start()
	w.mu.Lock()
	w.races++
	w.active = true
	w.mu.Unlock()
}

// cand 记录一个候选的响应耗时与 URL（d<=0 = 未计时，跳过）。
// 只有刷新「最慢」时才覆盖 slowestURL，聚合行尾部因此永远指向真最慢那条。
func (w *raceWindow) cand(d time.Duration, u string) {
	if d <= 0 {
		return
	}
	w.mu.Lock()
	w.cands++
	w.total += d
	if d > w.slowest {
		w.slowest = d
		w.slowestURL = u
	}
	w.active = true
	w.mu.Unlock()
}

// hit 记录一轮竞速成功（拿到 200）。
func (w *raceWindow) hit() {
	w.mu.Lock()
	w.hits++
	w.active = true
	w.mu.Unlock()
}

// fail 记录一轮竞速全败。
func (w *raceWindow) fail() {
	w.mu.Lock()
	w.fails++
	w.active = true
	w.mu.Unlock()
}

// slowHit 慢命中单行告警（≥raceSlowHit 才打；不参与聚合统计）。
func (w *raceWindow) slowHit(d time.Duration, u string) {
	if d < raceSlowHit {
		return
	}
	w.emit(fmt.Sprintf("[race] 慢命中 200 %s (t=%s)", u, raceDur(d)))
}

// tick 结算当前窗口：有活动则打一行并清零，否则整窗静默。
func (w *raceWindow) tick() {
	w.mu.Lock()
	if !w.active {
		w.mu.Unlock()
		return
	}
	races, hits, fails, cands := w.races, w.hits, w.fails, w.cands
	total, slowest, slowURL := w.total, w.slowest, w.slowestURL
	w.races, w.hits, w.fails, w.cands = 0, 0, 0, 0
	w.total, w.slowest, w.slowestURL = 0, 0, ""
	w.active = false
	w.mu.Unlock()

	var avg time.Duration
	if cands > 0 {
		avg = total / time.Duration(cands)
	}
	line := fmt.Sprintf("[race] %s: %d 次竞速 · 命中 %d · 全败 %d · 均 %s · 最慢 %s",
		raceWindowLabel, races, hits, fails, raceDur(avg), raceDur(slowest))
	if slowURL != "" && slowest > 0 {
		line += fmt.Sprintf("（最慢 %s）", slowURL)
	}
	w.emit(line)
}

// raceDur 人工可读耗时：≥1s 保留一位小数（3.2s），否则毫秒（320ms）。
func raceDur(d time.Duration) string {
	if d <= 0 {
		return "0ms"
	}
	if d >= time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	return d.Round(time.Millisecond).String()
}
