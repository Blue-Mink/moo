package source

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/netx"
)

// P1-1（2026-11-26 CARE）：失败通道——连续 N 轮「确定性永久失败」的源沉底 +
// 自动停用。
//
// 为什么需要单独的探测：刷新失败的错误串是「所有候选地址均失败: <最后一个
// 错误>」，只保留了最后一条候选的报错（可能是镜像 404，也可能 raw 超时），
// 拿它做永久性判定必然误判。误判红线 1 要求「raw 直连 + ≥1 个独立镜像同时
// 404/410」才算确定性失效，因此判定必须回到通道级别实测。

// CareVerdict 一次通道复核的结论。
type CareVerdict int

const (
	// CareUncertain 不确定（超时/5xx/429/403/单通道 404…）——本轮放弃、计数不推进。
	CareUncertain CareVerdict = iota
	// CareAlive 任一通道可达（含「镜像 404 但直连 200」）——计数清零。
	CareAlive
	// CarePermanentlyDead 直连与 ≥1 个独立镜像同时 404/410——确定性失效。
	CarePermanentlyDead
	// CareDirectGoneMirrorsUnreachable 直连组全部候选 404/410，但镜像组全部
	// 「不可达」（超时 / 5xx / 403 / 429，非 404）。仓库在直连侧已被确认移除，
	// 镜像只是暂时够不着——不足以「立刻」判死（红线 1 要求双通道确定性），
	// 但也不该像纯网络抖动那样无限等下去：按「直连确认失效」累计失败轮，
	// 连续 AutoCareFailThreshold 轮且跨 AutoCareFailMinWindow 才停用
	// （PATCH_0575 A 段，2026-11-27）。
	CareDirectGoneMirrorsUnreachable
)

const (
	// AutoCareFailThreshold 连续「确定性永久失败」轮数门槛（误判红线 3）。
	AutoCareFailThreshold = 5
	// AutoCareFailMinWindow 距首次失败的墙钟门槛：两个门槛都满足才动手
	//（防重启后短时间连判、防 hourly 轮次过快误杀）。
	AutoCareFailMinWindow = 24 * time.Hour
)

// careVerdictLabel 把复核结论映射成 /api/sources 的稳定字符串（空 = 未知）。
// 只读信号：不参与任何判定。CareDirectGoneMirrorsUnreachable（直连确定性
// 404/410、镜像组全不可达）在双通道意义上**没有**确认失效，故仍标 uncertain；
// 它按「直连确认失效」累计失败轮的事实由 fail_streak 体现。
func careVerdictLabel(v CareVerdict) string {
	switch v {
	case CareAlive:
		return "alive"
	case CarePermanentlyDead:
		return "dead"
	default:
		return "uncertain"
	}
}

// careProbeClient 复核专用短超时客户端（带科学加速代理，与刷新同一条出网
// 路径）；探测属于补救动作，单请求超过 8s 就当作不确定，不拖慢刷新轮。
var careProbeClient = netx.NewClient(8 * time.Second)

// probeGroup 一个独立通道（raw 直连 / 某个镜像前缀）及其候选地址。
type probeGroup struct {
	Name string
	URLs []string
}

// probeOutcome 单通道结论。
type probeOutcome int

const (
	probeBad  probeOutcome = iota // 不确定（5xx/429/403/TLS/超时/其它）
	probeOK                       // 200/2xx：通道可用
	probeGone                     // 组内全部候选 404/410：确定性不存在
)

// mirrorPrefixes 当前配置里带 URL 的 GitHub 镜像前缀（含尾部斜杠）。
func mirrorPrefixes() []string {
	opts := config.GitHubMirrorOptionsHealthy()
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		if o.URL == "" { // auto = 非直连项
			continue
		}
		out = append(out, strings.TrimRight(o.URL, "/")+"/")
	}
	return out
}

// careProbeGroups 把源地址展开成通道分组：直连组 + 各镜像组。
// jsDelivr 一律剔除——它是边缘缓存，陈旧副本不能作为「源还在」的依据
// （与 withRawOnly 同口径）。
func careProbeGroups(u string) []probeGroup {
	cands := buildCandidatesLimited(strings.TrimRight(u, "/"), -1)
	prefixes := mirrorPrefixes()
	var direct []string
	byPrefix := map[string][]string{}
	var order []string
	for _, c := range cands {
		if strings.Contains(c, "cdn.jsdelivr.net") {
			continue
		}
		if p, ok := matchMirrorPrefix(c, prefixes); ok {
			if _, seen := byPrefix[p]; !seen {
				order = append(order, p)
			}
			byPrefix[p] = append(byPrefix[p], c)
			continue
		}
		direct = append(direct, c)
	}
	out := make([]probeGroup, 0, len(order)+1)
	if len(direct) > 0 {
		out = append(out, probeGroup{Name: "direct", URLs: direct})
	}
	for _, p := range order {
		out = append(out, probeGroup{Name: "mirror:" + p, URLs: byPrefix[p]})
	}
	return out
}

// matchMirrorPrefix 判断候选地址是否被某个镜像前缀包裹。
func matchMirrorPrefix(u string, prefixes []string) (string, bool) {
	low := strings.ToLower(u)
	for _, p := range prefixes {
		if strings.HasPrefix(low, strings.ToLower(p)) {
			return p, true
		}
	}
	return "", false
}

// probeGroupStatus 逐候选探测一个通道：命中 2xx 即可用；组内全部 404/410 才算
// 「确定性不存在」；出现任何 5xx/429/403/网络错误立即返回「不确定」（不再
// 继续浪费请求，也绝不把混合信号当成永久失效）。
func probeGroupStatus(c *http.Client, g probeGroup) probeOutcome {
	return probeGroupStatusWithin(c, g, time.Time{})
}

// probeGroupStatusWithin 同 probeGroupStatus，但受总预算 deadline 约束
// （2026-11-27 F10）：预算用尽时把该通道判为「不确定」（probeBad）并立即返回，
// 不再发后续请求。dl 零值 = 不设预算。**不重试**（旧口径 / 纯探测口径）。
func probeGroupStatusWithin(c *http.Client, g probeGroup, dl time.Time) probeOutcome {
	return probeGroupStatusVia(c, g, dl, careProbeGetWithin)
}

// probeGroupStatusRetry 同 probeGroupStatusWithin，但瞬时失败重试 1 次
// （2026-11-27 FIXPLAN 盲点 #3 · C1 消抖）。复核判定路径用它。
func probeGroupStatusRetry(c *http.Client, g probeGroup, dl time.Time) probeOutcome {
	return probeGroupStatusVia(c, g, dl, careProbeGetRetry)
}

// careGetFunc 一次候选探测的实现（带 / 不带重试两种）。
type careGetFunc func(c *http.Client, u string, dl time.Time) (int, error)

// probeGroupStatusVia 通道探测主体：候选逐个试，命中 2xx 立即可用；组内全部
// 404/410 才算「确定性不存在」；任何 5xx/429/403/网络错误立即「不确定」。
func probeGroupStatusVia(c *http.Client, g probeGroup, dl time.Time, get careGetFunc) probeOutcome {
	sawGone := false
	for _, u := range g.URLs {
		if !dl.IsZero() && !time.Now().Before(dl) {
			return probeBad
		}
		code, err := get(c, u, dl)
		if err != nil {
			return probeBad
		}
		switch {
		case code >= 200 && code < 300:
			return probeOK
		case code == http.StatusNotFound || code == http.StatusGone:
			sawGone = true
			continue
		default:
			return probeBad
		}
	}
	if sawGone {
		return probeGone
	}
	return probeBad
}

// careProbeGet 取状态码（不读body，立即关闭连接）。
func careProbeGet(c *http.Client, u string) (int, error) {
	return careProbeGetWithin(c, u, time.Time{})
}

// careProbeGetWithin 同 careProbeGet，但请求受 deadline 约束：预算一到，
// 在途请求立刻被 context 取消（不必等客户端自身的 8s 超时）。
func careProbeGetWithin(c *http.Client, u string, dl time.Time) (int, error) {
	ctx := context.Background()
	if !dl.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	// 只关心可达性：Range 让服务端尽量只回一个字节（不支持也无害）。
	req.Header.Set("Range", "bytes=0-0")
	req.Header.Set("User-Agent", "moo-source-care")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// ── 瞬时失败重试一次（2026-11-27 FIXPLAN 盲点 #3 · C1 抖动消抖）──
//
// 为什么：单次超时 / 网络错误 / 5xx / 429 会把一轮复核判成「不确定」。这个方向
// 本身是安全的（不推进计数、绝不停源），但会让「连续 5 轮」在抖动网络上永远凑
// 不齐，也让判定可观测字段（C2 的 last_verdict）在 alive/uncertain 之间来回跳。
// 消抖只做一件事：**同一个候选地址**的瞬时失败重试 1 次，仍失败才按首次结论走。
//
// 红线（一条都不放松）：
//
//	· 404/410 是确定性结论，**绝不重试**（CarePermanentlyDead 路径不受影响）；
//	· 重试与首次同受单源预算 deadline 约束，剩余预算不够就放弃重试、按首次结论走；
//	· 重试在复核既有的并发闸门（probeSem = careProbeConcurrency ≤2）内同步进行，
//	  不新开 goroutine，不突破并发上限；
//	· 任何判定阈值（AutoCareThreshold / 曾自愈源 10 轮 / 5 轮 + 24h / 6h 节流）
//	  一律不动 —— 重试只影响「这一轮看到什么」，不改变「多少轮才动手」。
var (
	// careProbeRetryDelay 重试间隔：只需躲开瞬时连接抖动/限流，不做长等待。
	careProbeRetryDelay = 300 * time.Millisecond
	// careProbeRetryMinLeft 允许重试所需的最小剩余预算：不够就按首次结论走，
	// 绝不为了一次重试把复核拖过单源预算（F10 的 30s 上限优先）。
	careProbeRetryMinLeft = 1 * time.Second
)

// careProbeTransient 是否为「瞬时失败」（值得重试一次）。
// 2xx / 404 / 410 都是确定性结论；其余（超时、网络错误、5xx、429、403…）算瞬时。
func careProbeTransient(code int, err error) bool {
	if err != nil {
		return true
	}
	if code >= 200 && code < 300 {
		return false
	}
	return code != http.StatusNotFound && code != http.StatusGone
}

// careProbeRetryAllowed 剩余预算是否够重试（dl 零值 = 不设预算，允许）。
func careProbeRetryAllowed(dl time.Time) bool {
	if dl.IsZero() {
		return true
	}
	return time.Until(dl) >= careProbeRetryMinLeft+careProbeRetryDelay
}

// careProbeGetRetry 单候选探测 + 瞬时失败重试 1 次（仅此一处重试，无循环）。
func careProbeGetRetry(c *http.Client, u string, dl time.Time) (int, error) {
	code, err := careProbeGetWithin(c, u, dl)
	if !careProbeTransient(code, err) || !careProbeRetryAllowed(dl) {
		return code, err
	}
	time.Sleep(careProbeRetryDelay)
	return careProbeGetWithin(c, u, dl)
}
