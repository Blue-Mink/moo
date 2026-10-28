package api

// 加速源「深测」——续传能力验证。
//
// 背景：probeGhOne 只读前 2MB / 10s，能测出"快不快"，测不出"断了之后
// 能不能续"。本文件对吞吐 Top N 的镜像做低频深测：
//
//	第 1 步  Range: bytes=0-1048575        下载 1MB
//	第 2 步  断开连接（Close = 模拟断流）
//	第 3 步  Range: bytes=1048576-...      全新请求续传，再读 1MB
//	第 4 步  再取第三段两次，内容必须逐字节一致
//	         （防镜像对 Range 请求返回非确定内容）
//
// 全部通过 = ResumeOK。任一环失败 = ResumeOK=false 带原因。
// 结果进 MirrorStat.ResumeOK，优选同速时 ResumeOK 优先。
//
// 资产与常规测速共用 pinned release（ghProbeAssets），出站过 netguard。

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
)

const (
	resumeChunkBytes  = 1 << 20          // 每段 1MB
	resumeReadTimeout = 20 * time.Second // 单段读上限（常规测速 10s 的两倍：含 Range 响应）
)

// resumeResult 一条源的一次深测结论。
type resumeResult struct {
	OK      bool      `json:"ok"`
	Reason  string    `json:"reason,omitempty"`
	Checked time.Time `json:"checked"`
}

// resumeResults key → 最近一次深测结论（进程内缓存，不持久化——
// 深测是低频验证，进程重启后下一个周期自然重建）。
var (
	resumeMu      sync.Mutex
	resumeResults = map[string]resumeResult{}
)

// SetResumeResult 写入深测结论（api 层与测试用）。
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

// ResumeOKOf 仅取布尔结论（前端徽标 / 优选权重用）。
func ResumeOKOf(key string) bool {
	r, ok := ResumeResultOf(key)
	return ok && r.OK
}

// topResumeCandidates 按最近一次常规测速的吞吐取前 n 个 ok 镜像（深测对象）。
// 复用 topSpeedKeys 的排序——深测对象就是"当前最有资格被优选的源"。
func topResumeCandidates(stats []MirrorStat, n int) []config.MirrorOption {
	keys := topSpeedKeys(stats)
	if len(keys) > n {
		keys = keys[:n]
	}
	var out []config.MirrorOption
	for _, opt := range config.GitHubMirrorOptions() {
		for _, k := range keys {
			if opt.Key == k {
				out = append(out, opt)
				break
			}
		}
	}
	return out
}

// probeResume 对单个镜像执行续传深测。
func probeResume(ctx context.Context, opt config.MirrorOption, asset string) resumeResult {
	res := resumeResult{Checked: time.Now()}
	base := ghProbeURL(opt, asset)

	// 出站前过 netguard（与常规测速同一条信任线）
	if _, err := netguard.Public(base); err != nil {
		res.Reason = "安全策略拒绝: " + err.Error()
		return res
	}

	// 段 1：bytes=0-(chunk-1)
	b1, err := fetchRange(ctx, base, 0, resumeChunkBytes-1)
	if err != nil {
		res.Reason = "首段失败: " + err.Error()
		return res
	}
	if len(b1) != resumeChunkBytes {
		res.Reason = fmt.Sprintf("首段不完整: %d/%d", len(b1), resumeChunkBytes)
		return res
	}

	// 段 1 响应已 Close（fetchRange 内部）= 模拟断流。
	// 段 2：全新请求，bytes=chunk-(2*chunk-1)
	b2, err := fetchRange(ctx, base, resumeChunkBytes, 2*resumeChunkBytes-1)
	if err != nil {
		res.Reason = "续传段失败: " + err.Error()
		return res
	}
	if len(b2) != resumeChunkBytes {
		res.Reason = fmt.Sprintf("续传段不完整: %d/%d", len(b2), resumeChunkBytes)
		return res
	}

	// 一致性：第三段取两次，内容必须逐字节相同——
	// 防止镜像对 Range 请求返回非确定内容（= 不支持真实续传）。
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
// 非 206 = 镜像不支持 Range，直接报错（这是深测要暴露的问题本身）。
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
	_, err = io.Copy(buf, io.LimitReader(resp.Body, resumeChunkBytes))
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RunResumeProbes 对给定源集合执行深测（阻塞，串行——深测低频且每源
// 下载 2MB，串行避免对同一镜像并发压测）。资产 404 时顺延下一个资产
// （与 probeGhOne 的顺延语义一致）。
func RunResumeProbes(ctx context.Context, opts []config.MirrorOption) map[string]resumeResult {
	out := make(map[string]resumeResult, len(opts))
	for _, opt := range opts {
		if opt.URL == "" {
			continue // auto/direct/custom 不可深测
		}
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
			// 404 → 顺延下一个资产
		}
		SetResumeResult(opt.Key, r)
		out[opt.Key] = r
	}
	return out
}

// orderByResume 同速排序辅助：在 topSpeedKeys 的吞吐排序结果上，
// 把「深测通过」的源稳定地排到同速组的前面。
func orderByResume(stats []MirrorStat, keys []string) []string {
	speed := make(map[string]int64, len(stats))
	for _, st := range stats {
		if st.Status == "ok" {
			speed[st.Key] = st.SpeedBPS
		}
	}
	resumeOK := make(map[string]bool, len(keys))
	resumeMu.Lock()
	for k, r := range resumeResults {
		resumeOK[k] = r.OK
	}
	resumeMu.Unlock()
	stable := append([]string(nil), keys...)
	sort.SliceStable(stable, func(i, j int) bool {
		if speed[stable[i]] != speed[stable[j]] {
			return speed[stable[i]] > speed[stable[j]] // 保持吞吐主序
		}
		return resumeOK[stable[i]] && !resumeOK[stable[j]] // 同速：深测通过优先
	})
	return stable
}
