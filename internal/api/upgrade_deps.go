package api

// upgrade_deps.go —— 升级时「停依赖方 → 升级 → 恢复」的事务化（0.6.547）。
//
// 为什么放后端：停用只是升级这一步的副作用。只要「停用」与「升级」是两个
// 独立请求，恢复就永远依赖浏览器活着——关页面、SSE 中断（切网/锁屏）、
// Moo 重启或断电，都会把用户的应用永久留在停止态。
// 放进同一个操作里：客户端断开不影响恢复（操作队列用后台 ctx 跑）；
// 进程被杀则由启动时的 sweep 兜底。

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"

	"moo/internal/platform"
)

const (
	depStopsFileName = "dep_stops.json"
	// depStopsSweepDelay 启动后等平台 daemon 就绪再兜底恢复。
	depStopsSweepDelay = 20 * time.Second
	// depStopsMaxAge 超过这个时间的标记视为陈旧（只清理，不再重启应用）。
	depStopsMaxAge = 24 * time.Hour
)

type depStopMarker struct {
	App      string   `json:"app"`
	Blockers []string `json:"blockers"`
	At       string   `json:"at"`
}

type depStopState struct {
	SchemaVersion int             `json:"schema_version,omitempty"`
	Items         []depStopMarker `json:"items"`
}

func (s *Server) depStopsPath() string {
	d := dataDirOf(s)
	if d == "" {
		return ""
	}
	return filepath.Join(d, depStopsFileName)
}

func (s *Server) readDepStops() []depStopMarker {
	p := s.depStopsPath()
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var st depStopState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return st.Items
}

func (s *Server) writeDepStops(items []depStopMarker) {
	p := s.depStopsPath()
	if p == "" {
		return
	}
	b, err := json.Marshal(depStopState{SchemaVersion: 1, Items: items})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + "." + time.Now().Format("20060102150405.000000000")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// addDepStop 记一条「为了升级 X 停了这些应用」的标记（幂等：同 app 覆盖）。
func (s *Server) addDepStop(app string, blockers []string) {
	if len(blockers) == 0 {
		return
	}
	items := s.readDepStops()
	out := make([]depStopMarker, 0, len(items)+1)
	for _, it := range items {
		if it.App != app {
			out = append(out, it)
		}
	}
	out = append(out, depStopMarker{App: app, Blockers: blockers, At: time.Now().Format(time.RFC3339)})
	s.writeDepStops(out)
}

// removeDepStop 清掉该应用的标记（正常流程结束或 sweep 处理后）。
func (s *Server) removeDepStop(app string) {
	items := s.readDepStops()
	if len(items) == 0 {
		return
	}
	out := make([]depStopMarker, 0, len(items))
	changed := false
	for _, it := range items {
		if it.App == app {
			changed = true
			continue
		}
		out = append(out, it)
	}
	if changed {
		s.writeDepStops(out)
	}
}

// withUpgradeDeps 在升级操作内部完成「停依赖方 → 执行升级 → 恢复依赖方」。
// stopDeps=false 时是零成本直通（默认路径行为完全不变）。
func (s *Server) withUpgradeDeps(ctx context.Context, appName string, stopDeps bool,
	progress func(string, float64), fn func() error) error {
	if !stopDeps {
		return fn()
	}
	// 0.6.548：改用带 daemon 名映射的预检（源 feed 与 daemon 命名可能不同）；
	// checked=false（未装/预检失败）时按「无依赖」处理，不阻断升级——
	// 真正的失败仍由升级本身报出（保持原错误语义）。
	deps, _ := platform.RunningDependentsForInstalled(ctx, appName)
	stopped := make([]string, 0, len(deps))
	for _, d := range deps {
		if d.AppName == "" {
			continue
		}
		if err := platform.StopApp(ctx, d.AppName); err != nil {
			// 有一个停不掉，升级必然再次 16006：回滚已停的，直接如实报错。
			s.startDeps(stopped)
			return err
		}
		stopped = append(stopped, d.AppName)
	}
	if len(stopped) > 0 {
		progress("已停用依赖应用，开始升级…", 8)
		s.addDepStop(appName, stopped)
	}
	defer func() {
		s.startDeps(stopped)
		s.removeDepStop(appName)
	}()
	return fn()
}

// startDeps 尽力恢复（单独成函数便于测试时替换观察）。
// 0.6.548：单次失败不再静默——自动更新夜里无人值守，启动任务偶发拥挤时
// 需要重试几次；仍失败则留日志 + dep_stops 标记交给启动后 sweep。
func (s *Server) startDeps(names []string) {
	for _, n := range names {
		if n == "" {
			continue
		}
		var err error
		for i := 0; i < depStopRetry; i++ {
			if err = platform.StartApp(context.Background(), n); err == nil {
				break
			}
			if i < depStopRetry-1 {
				time.Sleep(depStopRetryDelay)
			}
		}
		if err != nil {
			log.Printf("[dep-stops] 恢复 %s 失败（已重试 %d 次）: %v", n, depStopRetry, err)
		}
	}
}

// SweepDepStops 启动后兜底：上次进程在升级中被杀/断电，依赖方还停着。
// 只处理 24h 内的标记；更旧的只清理标记，不动应用状态（避免误启用户自己停的服务）。
func (s *Server) SweepDepStops(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(depStopsSweepDelay):
	}
	s.sweepDepStopsOnce(ctx)
}

// sweepDepStopsOnce 实际执行体（单测直接调用，避免等 20s）。
func (s *Server) sweepDepStopsOnce(ctx context.Context) {
	items := s.readDepStops()
	if len(items) == 0 {
		return
	}
	keep := make([]depStopMarker, 0, len(items))
	for _, it := range items {
		at, err := time.Parse(time.RFC3339, it.At)
		if err != nil || time.Since(at) > depStopsMaxAge {
			log.Printf("[dep-stops] 丢弃陈旧标记 %s（%s）", it.App, it.At)
			continue
		}
		for _, n := range it.Blockers {
			if n == "" {
				continue
			}
			if err := platform.StartApp(ctx, n); err != nil {
				// 起不来（已卸载/被用户改过配置）就如实记一条，不再反复重试
				log.Printf("[dep-stops] 恢复 %s 失败（升级 %s 时停用）: %v", n, it.App, err)
				continue
			}
			log.Printf("[dep-stops] 已恢复 %s（升级 %s 时停用，进程曾中断）", n, it.App)
		}
	}
	s.writeDepStops(keep)
}
