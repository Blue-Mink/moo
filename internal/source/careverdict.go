package source

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CareProbeFunc 复核钩子：AutoCarePass 用它在「疑为永久失效」时实测通道。
// nil = 使用默认实现 ProbeCareChannels（真实网络）；测试注入假实现。
type CareProbeFunc func(name, url string) CareVerdict

// probeCareVerdict 汇总通道结论（误判红线 1/2 的判定核心）。
//
//	任一通道 200                → 存活（清零）
//	直连 404/410 且 ≥1 镜像 404/410 → 确定性失效
//	其余（含无镜像可查、403/5xx/超时）→ 不确定（计数不推进）
func probeCareVerdict(groups []probeGroup) CareVerdict {
	return probeCareVerdictWith(careProbeClient, groups)
}

func probeCareVerdictWith(c *http.Client, groups []probeGroup) CareVerdict {
	v, _ := probeCareVerdictDeadline(c, groups, time.Time{})
	return v
}

// probeCareVerdictDeadline 带**总预算**的通道复核（2026-11-27 F10 / 审计 N6）。
//
// 预算用尽时立即返回「不确定」而不是继续把剩下的镜像打完：复核只是判死前的
// 补证，结论不确定的代价只是「本轮不推进计数」，而拖长刷新的代价是整个自动
// 更新顺延数分钟。timedOut=true 表示这次放弃是预算导致（调用方记日志）。
// deadline 为零值 = 不设预算（既有单测口径）。
func probeCareVerdictDeadline(c *http.Client, groups []probeGroup, dl time.Time) (CareVerdict, bool) {
	expired := func() bool { return !dl.IsZero() && !time.Now().Before(dl) }
	var direct *probeGroup
	for i := range groups {
		if groups[i].Name == "direct" {
			direct = &groups[i]
			break
		}
	}
	if direct == nil {
		// 没有直连通道（非 GitHub 仓库形态、或候选全被过滤）：无从双通道确认。
		return CareUncertain, false
	}
	mirrors := make([]probeGroup, 0, len(groups))
	for i := range groups {
		if groups[i].Name != "direct" {
			mirrors = append(mirrors, groups[i])
		}
	}
	// C1（2026-11-27）：判定路径统一走「瞬时失败重试 1 次」的通道探测；
	// 404/410 不重试、预算不足不重试（见 careProbeGetRetry）。
	dr := probeGroupStatusRetry(c, *direct, dl)
	if dr == probeOK {
		return CareAlive, false
	}
	if expired() {
		return CareUncertain, true
	}
	// 逐镜像探测：任一 200 → 源活着（不清零也会立即返回，省掉剩余请求）；
	// 任一 404/410 → 确定性判死（更快，不等 5 轮）；其余记「不可达」。
	mirrorBad := len(mirrors)
	for _, g := range mirrors {
		if expired() {
			// F10：预算用尽即放弃本轮（结论不确定，绝不因此判死）。
			return CareUncertain, true
		}
		switch probeGroupStatusRetry(c, g, dl) {
		case probeOK:
			// 直连 404/5xx 而镜像 200：源仍在，按健康处理（误判红线 1/2）。
			return CareAlive, false
		case probeGone:
			if dr == probeGone {
				log.Printf("[auto-care] 复核：直连 404/410 且镜像 %s 同样 404/410 → 确定性失效", g.Name)
				return CarePermanentlyDead, false
			}
			// 直连只是暂态不可达，镜像却 404：不构成双通道确认，继续看别的镜像。
			mirrorBad--
		default:
			mirrorBad--
		}
	}
	if expired() {
		return CareUncertain, true
	}
	if dr != probeGone {
		// 直连不确定（超时/5xx/403/429）：本轮放弃，计数不推进。
		log.Printf("[auto-care] 复核：直连不可达、镜像无一可达（%s）→ 不确定，本轮不推进计数", groupNames(mirrors))
		return CareUncertain, false
	}
	if mirrorBad == 0 && len(mirrors) > 0 {
		// PATCH_0575 A：直连确定性 404/410，镜像组全部「不可达」（超时/5xx/
		// 403/429，非 404）。仓库在直连侧已被确认移除，镜像是够不着而非
		// 「存在」——按「直连确认失效」累计失败轮，靠 5 轮 + 24h 双门槛兜住
		// 网络抖动（单轮绝不判死）。
		log.Printf("[auto-care] 复核：直连 404/410 确认失效、镜像组全部不可达（%s）→ 计入失败轮", groupNames(mirrors))
		return CareDirectGoneMirrorsUnreachable, false
	}
	log.Printf("[auto-care] 复核：直连不可达或镜像信号不明（%s）→ 不确定，本轮不推进计数", groupNames(mirrors))
	return CareUncertain, false
}

// groupNames 只给日志用的通道名列表（避免把 URL 写进日志）。
func groupNames(gs []probeGroup) string {
	if len(gs) == 0 {
		return "无镜像"
	}
	names := make([]string, 0, len(gs))
	for _, g := range gs {
		names = append(names, g.Name)
	}
	return strings.Join(names, ",")
}

// careProbeBudget 单源复核的总预算（2026-11-27 F10 / 审计 N6）。
//
// 最坏路径 = 直连 + 每个镜像组各一次请求（11 组 × 8s ≈ 96s；线上实测一轮
// 复核花了 ~77s）。复核是「判死前的补证」，不能把刷新轮拖成分钟级：到预算
// 就放弃本轮，结论按**不确定**处理（计数不推进，绝不因此判死 —— 用户口径
// 「宁可漏停，不可错停」）。用 var 以便单测压到毫秒级验证该分支。
var careProbeBudget = 30 * time.Second

// careProbeConcurrency 全站同时进行的复核定并发上限（F10）。
// 去掉「全局串行」是为了让多个死源不互相排队，但并发必须压得很低：复核打的
// 是同一批镜像，并发越高越容易撞限流（429）→ 假「不可达」。
const careProbeConcurrency = 2

// probeSourceLocks 按源串行：同一源同一时刻只允许一个复核/自愈探测在跑。
// 并发打同一仓库既容易撞限流，也让两条通道的结论互相矛盾。
// key = probeKey(name,url) → *sync.Mutex；条目数上界 = 源数（不回收）。
var probeSourceLocks sync.Map

// probeSem 全站复核并发闸门（容量见 careProbeConcurrency）。
var probeSem = make(chan struct{}, careProbeConcurrency)

func probeKey(name, url string) string { return name + "\x00" + url }

func probeSourceLock(key string) *sync.Mutex {
	if v, ok := probeSourceLocks.Load(key); ok {
		return v.(*sync.Mutex)
	}
	actual, _ := probeSourceLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// ProbeCareChannels 默认复核实现：展开通道并实测（无缓存、不看索引名记忆、
// 放开全部镜像——误判红线 4 的「强制全量复核」）。单源总预算见 careProbeBudget。
func ProbeCareChannels(name, url string) CareVerdict {
	if strings.TrimSpace(url) == "" {
		return CareUncertain
	}
	budget := careProbeBudget
	v, timedOut := probeCareVerdictDeadline(careProbeClient, careProbeGroups(url), time.Now().Add(budget))
	if timedOut {
		log.Printf("[auto-care] 复核：源「%s」用完单源预算 %s（剩余通道不再探测）→ 不确定，本轮不推进计数", name, budget)
	}
	return v
}

// probeCareSafe 带 panic 保护 + 按源串行 + 全局限并发的复核入口
// （探测不应让策略轮崩掉，也不该让一个慢源把其它源全部堵住）。
func (m *Manager) probeCareSafe(name, url string) CareVerdict {
	mu := probeSourceLock(probeKey(name, url))
	mu.Lock()
	defer mu.Unlock()
	probeSem <- struct{}{}
	defer func() { <-probeSem }()
	fn := m.CareProbe
	if fn == nil {
		fn = ProbeCareChannels
	}
	defer func() {
		if r := recover(); r != nil {
			_ = r
		}
	}()
	return fn(name, url)
}
