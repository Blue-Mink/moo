// Package release 提供发布产物的「五通道」探测能力。
//
// 背景（2026-10-29 事故）：Moo 自更新的兜底通道只拼 FPK URL、把 .sha256 /
// .minisig 传空串，导致 fail-closed 校验让所有兜底通道 100% 失败。本包把
// 「每条通道都必须同时产出 FPK + .sha256 + .minisig 三个 URL」固化为类型
// 约束：Result.Assets 里缺任一侧车即判该通道失败（见 assetsComplete）。
//
// 通道顺序（探测优先级）：
//  1. api      —— GitHub Releases API（权威，能列出真实资产并给出 digest）
//  2. web302   —— github.com/releases/download 302 跳转（免限流）
//  3. selfidx  —— moo.json 自索引（源分发的索引文件）
//  4. fndepot  —— FnDepot fnpack.json（V2 协议）
//  5. mirror   —— 加速镜像前缀（gh-proxy / jsDelivr / Fastly 等）
//
// 本包只做「探测 + 校验」，不下载落盘、不改任何状态；CLI 见
// cmd/moo-release。HTTP 客户端复用 internal/netx（GitHub 域名走科学加速
// 代理、其余直连），与运行时自更新保持一致。
package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"moo/internal/netx"
)

// 默认发布坐标。与 internal/api/self_update.go 的运行时探测保持一致。
const (
	DefaultRepo        = "Blue-Mink/moo"
	DefaultOwner       = "Blue-Mink"
	DefaultIndexOwner  = "Blue-Mink"
	DefaultIndexRepo   = "FnDepot"
	DefaultIndexFile   = "fnpack.json"
	DefaultAppName     = "moo"
	DefaultSelfIdxRepo = "Blue-Mink/moo"
	DefaultSelfIdxFile = "moo.json"
)

// 通道基址。生产环境为 GitHub 官方地址；测试通过 apiBase 变量注入
// httptest 服务器（见 api_test.go 的 setAPIBase）。
const (
	webBaseURL     = "https://github.com"
	selfIdxBaseURL = "https://raw.githubusercontent.com"
)

// apiBase 是 GitHub API 基址，声明为变量以便测试注入（生产恒为默认值）。
var (
	apiBase         = originalAPIBase
	originalAPIBase = "https://api.github.com"
	webBase         = webBaseURL
	rawBase         = selfIdxBaseURL
)

// 通道标识（CLI 输出与 Result.Channel 使用，保持稳定可被脚本消费）。
const (
	ChannelAPI     = "api"
	ChannelWeb302  = "web302"
	ChannelSelfIdx = "selfidx"
	ChannelFnDepot = "fndepot"
	ChannelMirror  = "mirror"
)

// AllChannels 是固定探测顺序，供 CLI 汇总与测试遍历。
var AllChannels = []string{ChannelAPI, ChannelWeb302, ChannelSelfIdx, ChannelFnDepot, ChannelMirror}

// Assets 是一条通道给出的「三件套」URL。
//
// 三件套缺一即 fail-closed：FPK 无签名时拒绝自更新（0.6.329+ 语义）。
type Assets struct {
	FPK     string `json:"fpk"`
	SHA256  string `json:"sha256"`
	Minisig string `json:"minisig"`
	// Size 是 FPK 字节数（0 表示该通道未提供；API 通道通常能给出）。
	Size int64 `json:"size,omitempty"`
	// SHA256Digest 是通道侧声明的摘要（GitHub API 的 asset.digest），
	// 用于交叉核对而**不是**信任来源——真正的信任锚是 minisign 签名。
	SHA256Digest string `json:"sha256_digest,omitempty"`
}

// Complete 判断三件套是否齐全。
func (a Assets) Complete() bool {
	return strings.TrimSpace(a.FPK) != "" &&
		strings.TrimSpace(a.SHA256) != "" &&
		strings.TrimSpace(a.Minisig) != ""
}

// Missing 返回缺失的侧车名（空表示齐全），便于错误信息说清「缺什么」。
func (a Assets) Missing() []string {
	var miss []string
	if strings.TrimSpace(a.FPK) == "" {
		miss = append(miss, "fpk")
	}
	if strings.TrimSpace(a.SHA256) == "" {
		miss = append(miss, "sha256")
	}
	if strings.TrimSpace(a.Minisig) == "" {
		miss = append(miss, "minisig")
	}
	return miss
}

// Result 是单条通道的探测结果。
type Result struct {
	Channel   string        `json:"channel"`
	OK        bool          `json:"ok"`
	Version   string        `json:"version,omitempty"`
	Assets    Assets        `json:"assets,omitempty"`
	Err       string        `json:"error,omitempty"`
	LatencyMS int64         `json:"latency_ms"`
	Extra     []string      `json:"extra,omitempty"`
	Elapsed   time.Duration `json:"-"`

	// 以下字段由 Verify 阶段填充：探测通过 ≠ 用户能装，必须真下载真验签。
	Artifact  *Artifact `json:"artifact,omitempty"`
	Verified  bool      `json:"verified"`
	VerifyErr string    `json:"verify_error,omitempty"`
	// VerifyTransport=传输失败（超时/断流/5xx），不算契约违规；缺件/摘要不符/验签失败才算。
	VerifyTransport bool `json:"verify_transport,omitempty"`
	// VerifySkipped=本轮未做验收（默认只验第一条，或与已验通道同一 URL），
	// 不算失败也不算通过——绝不能落进「契约违规」分支（2026-10-30 假失败修复）。
	VerifySkipped bool `json:"verify_skipped,omitempty"`
	// VerifySkipReason 说明为什么跳过，供 CLI/JSON 直接展示（空=未跳过）。
	VerifySkipReason string `json:"verify_skip_reason,omitempty"`
}

// Opts 是探测参数。
type Opts struct {
	Tag       string   // 必需，如 "0.6.377" 或 "v0.6.377"（内部会归一）
	Arch      string   // "x86" / "arm"，默认 x86
	Repos     []string // GitHub owner/repo，默认 DefaultRepo
	Mirrors   []string // 镜像前缀，默认取 config 的 GitHub 镜像列表
	IndexURL  string   // 自定义自索引 URL（可选，覆盖默认）
	FnURL     string   // 自定义 FnDepot 索引 URL（可选）
	Timeout   time.Duration
	Channels  []string // 只跑指定通道（空=全跑）
	Client    *http.Client
	Owner     string
	IndexRepo string
	AppName   string
	WorkDir   string // 验收临时目录（空=系统临时目录）
	// VerifyTimeout 是「真下载」的超时（远大于探测超时：国内经镜像 ~90KB/s，
	// 5MB 包需 ~60s）。0 表示 180s。
	VerifyTimeout time.Duration
	// VerifyAll 为真时验收所有探测通过的通道；默认只验收第一条（模拟用户实际选择）。
	VerifyAll bool
}

// NormalizedTag 归一化版本号：去 v 前缀、去空白。
func NormalizedTag(tag string) string {
	t := strings.TrimSpace(tag)
	t = strings.TrimPrefix(t, "v")
	t = strings.TrimPrefix(t, "V")
	return t
}

// normalize 填充默认值并返回副本，避免调用方结构被改写。
func (o Opts) normalize() Opts {
	if strings.TrimSpace(o.Arch) == "" {
		o.Arch = "x86"
	}
	o.Arch = strings.ToLower(strings.TrimSpace(o.Arch))
	if len(o.Repos) == 0 {
		o.Repos = []string{DefaultRepo}
	}
	if strings.TrimSpace(o.Owner) == "" {
		o.Owner = DefaultOwner
	}
	if strings.TrimSpace(o.IndexRepo) == "" {
		o.IndexRepo = DefaultIndexRepo
	}
	if strings.TrimSpace(o.AppName) == "" {
		o.AppName = DefaultAppName
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.Client == nil {
		o.Client = netx.NewClient(o.Timeout)
	}
	o.Tag = NormalizedTag(o.Tag)
	return o
}

// FPKName 返回发布约定下的 FPK 文件名：moo_<tag>_<arch>.fpk。
func FPKName(tag, arch string) string {
	return fmt.Sprintf("%s_%s_%s.fpk", DefaultAppName, NormalizedTag(tag), strings.ToLower(arch))
}

// AssetsFor 按发布约定拼出某 tag 的三件套 URL（给定 release 下载前缀）。
// 这是兜底通道的唯一拼装点——保证三条兜底永远不会漏掉侧车。
func AssetsFor(prefix, tag, arch string) Assets {
	name := FPKName(tag, arch)
	base := strings.TrimSuffix(prefix, "/")
	return Assets{
		FPK:     base + "/" + name,
		SHA256:  base + "/" + name + ".sha256",
		Minisig: base + "/" + name + ".minisig",
	}
}

// Probe 依次探测各通道，返回全部结果（含失败原因）。
//
// 不做短路：即使 api 通道成功也会继续跑兜底通道，因为「每一通道都必须
// 自带三件套」本身就是要验证的契约（2026-10-29 事故的直接教训）。
func Probe(ctx context.Context, opts Opts) []Result {
	o := opts.normalize()
	want := map[string]bool{}
	for _, c := range o.Channels {
		want[strings.ToLower(strings.TrimSpace(c))] = true
	}
	picked := func(c string) bool { return len(want) == 0 || want[c] }

	var out []Result
	if picked(ChannelAPI) {
		out = append(out, run(ctx, ChannelAPI, func(ctx context.Context) (Result, error) {
			return probeAPI(ctx, o)
		}))
	}
	if picked(ChannelWeb302) {
		out = append(out, run(ctx, ChannelWeb302, func(ctx context.Context) (Result, error) {
			return probeWeb302(ctx, o)
		}))
	}
	if picked(ChannelSelfIdx) {
		out = append(out, run(ctx, ChannelSelfIdx, func(ctx context.Context) (Result, error) {
			return probeSelfIndex(ctx, o)
		}))
	}
	if picked(ChannelFnDepot) {
		out = append(out, run(ctx, ChannelFnDepot, func(ctx context.Context) (Result, error) {
			return probeFnDepot(ctx, o)
		}))
	}
	if picked(ChannelMirror) {
		out = append(out, run(ctx, ChannelMirror, func(ctx context.Context) (Result, error) {
			return probeMirror(ctx, o)
		}))
	}
	return out
}

// run 计时并统一错误包装，保证任何通道 panic/超时都只影响自身。
func run(ctx context.Context, name string, fn func(context.Context) (Result, error)) Result {
	start := time.Now()
	res, err := fn(ctx)
	res.Channel = name
	res.Elapsed = time.Since(start)
	res.LatencyMS = res.Elapsed.Milliseconds()
	if err != nil {
		res.OK = false
		res.Err = err.Error()
	}
	return res
}

// assetsComplete 校验三件套齐全，作为各通道共同的收尾判据。
//
// 缺任一侧车即返回错误——这正是 0.6.370 修复要固化的行为：兜底通道
// 不再产出 shaURL="" / minisigURL="" 的半成品。
func assetsComplete(a Assets) error {
	if miss := a.Missing(); len(miss) > 0 {
		return fmt.Errorf("三件套不完整，缺 %s", strings.Join(miss, "+"))
	}
	return nil
}

// httpGet 是一次带 UA 的 GET，返回 body（调用方负责 Close）。
func httpGet(ctx context.Context, c *http.Client, url string) (io.ReadCloser, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	// User-Agent 必须给，否则 GitHub API 直接 403。
	req.Header.Set("User-Agent", "moo-release-probe")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}
	return resp.Body, resp.StatusCode, nil
}

// joinTags 给 CLI 汇总用：把成功的通道按探测顺序列出。
func Succeeded(rs []Result) []string {
	var ok []string
	for _, r := range rs {
		if r.OK {
			ok = append(ok, r.Channel)
		}
	}
	sort.SliceStable(ok, func(i, j int) bool {
		return indexOf(AllChannels, ok[i]) < indexOf(AllChannels, ok[j])
	})
	return ok
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return len(list)
}

// ErrNotFound 表示 tag 在索引/发布里不存在（区别于网络错误）。
var ErrNotFound = errors.New("版本未在索引中找到")

// Short 返回摘要前 12 位（报告用，避免刷屏）。
func Short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
