package api

// 加速源「深测」——续传能力验证（2026-11-04，PR #14 的纯收益部分）。
//
// 背景：probeGhOne 只读前 1.5MB / 10s，能测出「快不快」，测不出「断了之后
// 能不能续」。FPK 普遍 5–50MB，弱网/中继下断流是常态；镜像若不认 Range
// （返回 200 全量而非 206 分段），aria2/HTTP 的续传都会退化成从头重下。
//
// 深测四步（每源约 2MB 流量）：
//
//	① Range: bytes=0-1048575          下载 1MB
//	② 断开（Close = 模拟断流）
//	③ Range: bytes=1048576-…          全新请求续传，再读 1MB
//	④ 再取第三段两次，内容必须逐字节一致
//	   （防镜像对 Range 返回非确定内容 = 假续传）
//
// 约束：只对吞吐 Top3 做、每源 24h 一次、串行（不并发压测同一镜像）、
// 结果只进进程内缓存（重启后下一个周期重建）。出站同样过 netguard.Public。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
)

const (
	resumeChunkBytes  = 1 << 20          // 每段 1MB
	resumeReadTimeout = 20 * time.Second // 单段读上限（常规测速 10s 的两倍：含 Range 响应）
	resumeMaxAge      = 24 * time.Hour   // 同一源两次深测的最小间隔
	resumeTopN        = 3                // 只深测吞吐前 3 的源
)

// resumeResult 一条源的一次深测结论。
type resumeResult struct {
	OK      bool      `json:"ok"`
	Reason  string    `json:"reason,omitempty"`
	Checked time.Time `json:"checked"`
}

var (
	resumeMu      sync.Mutex
	resumeResults = map[string]resumeResult{}
)

// SetResumeResult 写入深测结论（测试用）。
func SetResumeResult(key string, r resumeResult) {
	resumeMu.Lock()
	resumeResults[key] = r
	resumeMu.Unlock()
}

// ResumeResultOf 读某源的深测结论（未测过第二个返回值=false）。
func ResumeResultOf(key string) (resumeResult, bool) {
	resumeMu.Lock()
	defer resumeMu.Unlock()
	r, ok := resumeResults[key]
	return r, ok
}

// resumeOKOf 仅取布尔结论（优选同速 tie-break 用）。
func resumeOKOf(key string) bool {
	r, ok := ResumeResultOf(key)
	return ok && r.OK
}

// topResumeCandidates 按最近一次常规测速的吞吐取前 n 个可深测镜像。
// 复用 topSpeedKeys 的排序——深测对象就是「当前最有资格被优选的源」；
// auto/direct/custom 无固定前缀，不参与深测。
func topResumeCandidates(stats []MirrorStat, n int) []config.MirrorOption {
	keys := topSpeedKeys(stats)
	if len(keys) > n {
		keys = keys[:n]
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	var out []config.MirrorOption
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL != "" && want[opt.Key] {
			out = append(out, opt)
		}
	}
	return out
}

// resumeGuard 出站校验钩子（默认 netguard.Public）。
// 提成变量是为了单测能直接打 httptest 的本机地址，而不必去改
// netguard 的全局豁免表（那会污染同包其它测试的 SSRF 断言）。
var resumeGuard = netguard.Public

// probeResume 对单个镜像执行续传深测。
func probeResume(ctx context.Context, opt config.MirrorOption, asset string) resumeResult {
	res := resumeResult{Checked: time.Now()}
	base := ghProbeURL(opt, asset)

	// 出站前过 netguard（与常规测速同一条信任线）
	if _, err := resumeGuard(base); err != nil {
		res.Reason = "安全策略拒绝: " + err.Error()
		return res
	}

	b1, err := fetchRange(ctx, base, 0, resumeChunkBytes-1)
	if err != nil {
		res.Reason = "首段失败: " + err.Error()
		return res
	}
	if len(b1) != resumeChunkBytes {
		res.Reason = fmt.Sprintf("首段不完整: %d/%d", len(b1), resumeChunkBytes)
		return res
	}

	// 段 1 已 Close = 模拟断流；段 2 用全新请求从 1MB 处续。
	b2, err := fetchRange(ctx, base, resumeChunkBytes, 2*resumeChunkBytes-1)
	if err != nil {
		res.Reason = "续传段失败: " + err.Error()
		return res
	}
	if len(b2) != resumeChunkBytes {
		res.Reason = fmt.Sprintf("续传段不完整: %d/%d", len(b2), resumeChunkBytes)
		return res
	}

	// 一致性：第三段取两次，内容必须逐字节相同。
	b3a, err := fetchRange(ctx, base, 2*resumeChunkBytes, 2*resumeChunkBytes+31)
	if err != nil {
		res.Reason = "校验段失败: " + err.Error()
		return res
	}
	b3b, err := fetchRange(ctx, base, 2*resumeChunkBytes, 2*resumeChunkBytes+31)
	if err != nil {
		res.Reason = "校验段失败: " + err.Error()
		return res
	}
	if !bytes.Equal(b3a, b3b) {
		res.Reason = "Range 内容不一致（镜像对分段请求返回不同内容）"
		return res
	}

	res.OK = true
	return res
}

// fetchRange 读取 [start,end] 闭区间，最多 resumeChunkBytes 字节。
// 非 206 = 镜像不支持 Range，直接报错（这正是深测要暴露的问题）。
func fetchRange(ctx context.Context, url string, start, end int64) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, resumeReadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d（期望 206 Partial Content——镜像可能不支持 Range 续传）", resp.StatusCode)
	}
	buf := &bytes.Buffer{}
	if _, err := io.Copy(buf, io.LimitReader(resp.Body, resumeChunkBytes)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RunResumeProbes 对给定源集合串行深测（每源约 2MB）。
// 资产 404 时顺延下一个资产（与 probeGhOne 的顺延语义一致）。
func RunResumeProbes(ctx context.Context, opts []config.MirrorOption) map[string]resumeResult {
	out := make(map[string]resumeResult, len(opts))
	for _, opt := range opts {
		assets := ghProbeAssets
		if opt.Key == "conversun" {
			assets = ghProbeAssetsConversun
		}
		var r resumeResult
		for _, a := range assets {
			r = probeResume(ctx, opt, a)
			if r.OK || !strings.Contains(r.Reason, "404") {
				break
			}
		}
		SetResumeResult(opt.Key, r)
		out[opt.Key] = r
	}
	return out
}

// startResumeProbeRound 对吞吐 TopN 触发一轮深测（24h 节流）。
// 独立 goroutine：串行下载 2MB×3，不能拖慢常规测速返回。
func (s *Server) startResumeProbeRound(stats []MirrorStat) {
	cands := topResumeCandidates(stats, resumeTopN)
	now := time.Now()
	due := make([]config.MirrorOption, 0, len(cands))
	for _, opt := range cands {
		if r, ok := ResumeResultOf(opt.Key); !ok || now.Sub(r.Checked) > resumeMaxAge {
			due = append(due, opt)
		}
	}
	if len(due) == 0 {
		return
	}
	go func() {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer rcancel()
		RunResumeProbes(rctx, due)
	}()
}
