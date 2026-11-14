package api

// autoupdate_deps.go —— 自动更新遇到「依赖占用」时的策略判定与执行（0.6.548）。
//
// 背景：升级被运行中应用依赖的运行时（nodejs_v22/v24 等）时平台会拒绝覆盖并
// 记 16006 `dependent app not stopped`。0.6.547 只在手动更新里做了「停依赖 →
// 升级 → 恢复」，自动更新仍然只能失败/跳过。本文件把同一套能力接进自动更新，
// 并把「要不要停用户的正在运行的应用」交给用户用策略+白名单显式决定。

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"moo/internal/platform"
)

const (
	depPolicySkip   = "skip"
	depPolicyNotify = "notify"
	depPolicyStop   = "stop"

	depWindowDefaultStart = "00:00"
	depWindowDefaultEnd   = "23:59"
	depWindowMinMinutes   = 60
	depStopRetry          = 3
	depStopRetryDelay     = 5 * time.Second
)

// autoUpdateDepPolicyOf 返回生效策略：未知/脏值一律按 skip（绝不放宽权限）。
func autoUpdateDepPolicyOf(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case depPolicyNotify:
		return depPolicyNotify
	case depPolicyStop:
		return depPolicyStop
	default:
		return depPolicySkip
	}
}

// autoUpdateDepWindowOf 返回生效窗口；空/非法值回默认全天（00:00-23:59）。
func autoUpdateDepWindowOf(start, end string) (string, string) {
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if _, ok := parseHHMM(start); !ok {
		start = depWindowDefaultStart
	}
	if _, ok := parseHHMM(end); !ok {
		end = depWindowDefaultEnd
	}
	return start, end
}

// parseHHMM 解析 HH:MM 为当日分钟数（0..1439）。
func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	for i := 0; i < 5; i++ {
		if i == 2 {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	if h > 23 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// inAutoUpdateWindow 判断 now 是否落在 [start,end]；start>end 表示跨零点。
// start==end 视为全天（脏配置不至于把功能锁死）；解析失败也按全天（不阻断）。
func inAutoUpdateWindow(now time.Time, start, end string) bool {
	s, ok1 := parseHHMM(start)
	e, ok2 := parseHHMM(end)
	if !ok1 || !ok2 || s == e {
		return true
	}
	n := now.Hour()*60 + now.Minute()
	if s < e {
		return n >= s && n <= e
	}
	return n >= s || n <= e
}

// validateAutoUpdateWindow 设置保存时校验：HH:MM、开始≠结束、窗口≥1 小时。
func validateAutoUpdateWindow(start, end string) (string, string, error) {
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if start == "" && end == "" {
		return depWindowDefaultStart, depWindowDefaultEnd, nil
	}
	s, ok1 := parseHHMM(start)
	e, ok2 := parseHHMM(end)
	if !ok1 || !ok2 {
		return "", "", fmt.Errorf("自动停用时段格式应为 HH:MM（00:00–23:59）")
	}
	if s == e {
		return "", "", fmt.Errorf("自动停用时段的开始与结束时间不能相同")
	}
	if (e-s+1440)%1440 < depWindowMinMinutes {
		return "", "", fmt.Errorf("自动停用时段不得短于 1 小时")
	}
	return start, end, nil
}

// depAllAllowed 判断依赖方是否全部在允许名单内（大小写不敏感）。
// 返回未允许的 appName 清单；不在名单的应用一个都不许停（停一半仍会 16006）。
func depAllAllowed(deps []platform.DepBlocker, allow []string) (bool, []string) {
	set := make(map[string]bool, len(allow))
	for _, n := range allow {
		if n = strings.TrimSpace(n); n != "" {
			set[strings.ToLower(n)] = true
		}
	}
	missing := make([]string, 0)
	for _, d := range deps {
		if d.AppName == "" {
			continue
		}
		if !set[strings.ToLower(d.AppName)] {
			missing = append(missing, d.AppName)
		}
	}
	return len(missing) == 0, missing
}

// depBlockerLabels 依赖方的可读名（DisplayName 优先）。
func depBlockerLabels(deps []platform.DepBlocker) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		n := d.DisplayName
		if n == "" {
			n = d.AppName
		}
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// shouldCacheDepBlocked 依赖占用结果是否写入 blocked 负缓存（0.6.548）。
//
// 审计发现：stop 档在自动停用时段外被跳过时若也写 2h 缓存，而缓存过滤发生在
// 轮次开始（不再重评时段），就会出现「窗口一到仍被缓存挡住、整窗一次不动手」。
// 所以只有「本来就不打算动手」（非 stop 档）或「时段内仍被占用」才写缓存。
func shouldCacheDepBlocked(policy string, canStopDeps bool) bool {
	return policy != depPolicyStop || canStopDeps
}

// stringSliceEqualFold 判断两个应用名列表是否等价（大小写不敏感、忽略空值、顺序无关）。
func stringSliceEqualFold(a, b []string) bool {
	set := func(in []string) map[string]bool {
		m := make(map[string]bool, len(in))
		for _, n := range in {
			if n = strings.TrimSpace(n); n != "" {
				m[strings.ToLower(n)] = true
			}
		}
		return m
	}
	ma, mb := set(a), set(b)
	if len(ma) != len(mb) {
		return false
	}
	for k := range ma {
		if !mb[k] {
			return false
		}
	}
	return true
}

// DepNotAllowedError 依赖方里有未被允许自动停用的应用（自动更新用）。
type DepNotAllowedError struct {
	App      string
	Blockers []platform.DepBlocker
	Missing  []string
}

func (e *DepNotAllowedError) Error() string {
	return fmt.Sprintf("%s 被 %s 占用，且未全部允许自动停用",
		e.App, strings.Join(depBlockerLabels(e.Blockers), "、"))
}

// ErrorDetail 与手动路径同款 detail，前端可复用「停用并重试」弹窗。
func (e *DepNotAllowedError) ErrorDetail() map[string]any {
	return map[string]any{
		"kind": "dependency_blocked",
		"app":  e.App,
		"blockers": func() []map[string]any {
			out := make([]map[string]any, 0, len(e.Blockers))
			for _, b := range e.Blockers {
				out = append(out, map[string]any{
					"appname": b.AppName, "display_name": b.DisplayName, "status": b.Status,
				})
			}
			return out
		}(),
	}
}

// withUpgradeDepsChecked 自动更新专用：全部依赖方都在 allow 内才停用，
// 停用 → 升级 → 恢复仍放在同一个操作里（关浏览器/进程被杀由 sweep 兜底）。
// 预检失败一律返回错误（自动路径不冒险：预检失败时贸然升级必然再撞 16006）。
func (s *Server) withUpgradeDepsChecked(ctx context.Context, appName string, allow []string,
	progress func(string, float64), onStopped func([]string), fn func(context.Context, func(string, float64)) error) error {
	if progress == nil {
		progress = func(string, float64) {}
	}
	// 0.6.548：用 RunningDependentsForInstalled 而不是裸 UpgradeDeps——
	// 源 feed 的 appName 与 daemon 名可能大小写/别名不同（0.6.208 同款坑），
	// 直接拿 feed 名调 stop/check 会误判「预检失败」并每小时报一次失败。
	deps, checked := platform.RunningDependentsForInstalled(ctx, appName)
	if !checked {
		return fmt.Errorf("依赖预检失败（本轮跳过，下轮重试）")
	}
	if len(deps) == 0 {
		return fn(ctx, progress)
	}
	if ok, missing := depAllAllowed(deps, allow); !ok {
		return &DepNotAllowedError{App: appName, Blockers: deps, Missing: missing}
	}
	stopped := make([]string, 0, len(deps))
	for _, d := range deps {
		if d.AppName == "" {
			continue
		}
		if err := platform.StopApp(ctx, d.AppName); err != nil {
			s.startDeps(stopped) // 停不掉就回滚：不半途升级
			return fmt.Errorf("停用依赖方 %s 失败: %w", d.AppName, err)
		}
		stopped = append(stopped, d.AppName)
	}
	if len(stopped) > 0 {
		progress("已停用依赖应用，开始升级…", 8)
		s.addDepStop(appName, stopped)
		if onStopped != nil {
			onStopped(stopped)
		}
	}
	defer func() {
		s.startDeps(stopped)
		s.removeDepStop(appName)
	}()
	return fn(ctx, progress)
}

// 依赖映射缓存（10 分钟）——弹窗打开时调用，避免每次几十次 stop/check。
var (
	depMapMu   sync.Mutex
	depMapAt   time.Time
	depMapData map[string]any
)

// autoUpdateDepMap GET /api/auto-update/dep-map（0.6.549）
//
// 返回 {"runtimes":{"nodejs_v24":["a1","a2"]},"apps":{"a1":["nodejs_v24"]}}。
// 平台没有「应用依赖谁」的正向接口，这里对已装应用逐个 stop/check 反查（并发 6），
// 结果缓存 10 分钟；单个应用查询失败直接忽略（不影响其余项）。
func (s *Server) autoUpdateDepMap(w http.ResponseWriter, r *http.Request) {
	depMapMu.Lock()
	defer depMapMu.Unlock()
	if depMapData != nil && time.Since(depMapAt) < 10*time.Minute {
		writeJSON(w, depMapData)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	list, err := platform.ListInstalled(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("读取已装应用失败: %w", err))
		return
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sem      = make(chan struct{}, 6)
		runtimes = make(map[string][]string, 8)
	)
	for _, a := range list {
		if a.AppName == "" {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			deps, isDep, derr := platform.DependentApps(ctx, name)
			if derr != nil || !isDep || len(deps) == 0 {
				return
			}
			names := make([]string, 0, len(deps))
			for _, d := range deps {
				if d.AppName != "" {
					names = append(names, d.AppName)
				}
			}
			if len(names) == 0 {
				return
			}
			mu.Lock()
			runtimes[name] = names
			mu.Unlock()
		}(a.AppName)
	}
	wg.Wait()
	apps := make(map[string][]string, len(runtimes)*3)
	for rt, deps := range runtimes {
		for _, d := range deps {
			apps[d] = append(apps[d], rt)
		}
	}
	out := map[string]any{
		"runtimes":   runtimes,
		"apps":       apps,
		"checked_at": time.Now().Format(time.RFC3339),
	}
	depMapData, depMapAt = out, time.Now()
	writeJSON(w, out)
}
