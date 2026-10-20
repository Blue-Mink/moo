package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"moo/internal/config"
	"moo/internal/notify"
)

// MirrorStat 镜像健康状态（前端契约）。
type MirrorStat struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	LatencyMS int64  `json:"latency_ms"`
	// SpeedBPS 吞吐测速（字节/秒；GitHub 文件加速源拉 release 资产、
	// Docker 镜像加速源拉 library/nginx 层；0 = 未测出）。
	// 2026-09-22 实测：延迟相近的镜像吞吐差可达 400 倍（57MB/s ↔ 139KB/s），
	// 故选路以 SpeedBPS 为准、LatencyMS（首字节延迟）仅作参考。
	SpeedBPS int64  `json:"speed_bps,omitempty"`
	// Status 健康状态（0.6.333 F2 扩为三态）：
	//   ok      = 探测通过（参与优选排名）
	//   limited = 429 限流（链路可达但被限流，临时性；不算故障——冻结连败
	//             计数、不计入「全部加速源不可用」判定、不参与优选排名）
	//   fail    = 真实故障（403/5xx/超时/慢/404）
	//   ""      = 未探测
	Status string `json:"status"`
	// Error 原因短语（0.6.321 F6，issue #4「失败原因不可见」缺口闭合）：
	// 429 限流（临时）/ 403 拒绝 / HTTP 5xx / 慢（低于吞吐门槛）/ 连接被拒 /
	// DNS 解析失败 / 探测资产 404。Status=fail 或 limited 时有值；
	// 可选字段，旧前端忽略。
	Error       string `json:"error,omitempty"`
	LastCheck   string `json:"last_check"`
	ConsecFails int    `json:"consec_fails"`
}

// MirrorCheckResult 手动全量测速结果（POST /api/mirrors/check）。
type MirrorCheckResult struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	LatencyMS int64  `json:"latency_ms"`
	SpeedBPS  int64  `json:"speed_bps,omitempty"`
	Status    string `json:"status"` // ok | limited（429 限流）| timeout | error
	Error     string `json:"error,omitempty"`
}

// MirrorSwitch 记录一次自动优选切换（前端「最近一次自动优选」横幅）。
type MirrorSwitch struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Time   string `json:"time"`
	Reason string `json:"reason"`
}

const mirrorCacheTTL = 5 * time.Minute

// mirrorMonitor 镜像健康缓存：GitHub 文件加速 + Docker 镜像加速两组。
type mirrorMonitor struct {
	mu        sync.Mutex
	ghStats   []MirrorStat
	dkStats   []MirrorStat
	lastProbe time.Time
	// 0.6.148：两组各自最近探测时间（间隔独立可配置）
	lastProbeGh time.Time
	lastProbeDk time.Time
	// 自动优选切换跟踪（仅记录最近一次，供前端横幅展示）
	prevActiveGh string
	prevActiveDk string
	lastSwitchGh *MirrorSwitch
	lastSwitchDk *MirrorSwitch
	// 连续失败计数（ok 清零、fail 累加；前端「失败×N」显示）
	ghFails map[string]int
	dkFails map[string]int
	// 0.6.321 F7：「智能选源包含直连」开关（config.MirrorIncludeDirect，默认关）。
	// Server 每探测周期/按需入口前 set；probeGh 据此决定是否追加 direct 候选。
	ghIncludeDirect bool
}

// setGhIncludeDirect 设置「智能选源包含直连」开关（0.6.321 F7）。
func (m *mirrorMonitor) setGhIncludeDirect(v bool) {
	m.mu.Lock()
	m.ghIncludeDirect = v
	m.mu.Unlock()
}

func newMirrorMonitor() *mirrorMonitor {
	return &mirrorMonitor{ghFails: map[string]int{}, dkFails: map[string]int{}}
}

// NewMirrorMonitor 供 main 构造 Server 时注入。
func NewMirrorMonitor() *mirrorMonitor { return newMirrorMonitor() }

// ghProbeAssets 吞吐测速用的固定大文件（pinned release 资产，永不过期）。
// 探测只读前 2MB（10s 上限）——镜像可能忽略 Range 而发整份文件，
// 读完即主动截断，每次探测每镜像浪费不超过几 MB 带宽。
//
// 0.6.333 F3：首位改为 Moo 自有 release 资产（4.98MB，实测直连与各代理均
// 200）——第三方 pinned 资产（ollama 等）一旦被上游下线/移动，全部镜像会
// 同时「探测资产 404」被误判为故障。旧资产保留在后作为后备（404 时自动
// 顺延试下一个，逻辑未变）。
var ghProbeAssets = []string{
	"github.com/Blue-Mink/moo/releases/download/v0.6.325/moo_0.6.325_x86.fpk",
	"github.com/ollama/ollama/releases/download/v0.34.2/ollama-linux-amd64.tar.zst",
	"github.com/ollama/ollama/releases/download/v0.12.6/ollama-linux-amd64.tgz",
}

// ghProbeAssetsConversun conversun hub 只代理 conversun 仓库（与 New Store 同结论），
// 用 ollama 资产探测会恒 404 → 误判失败。改用 conversun 自有 release 资产。
// 文件较小（~80KB），读取下限相应放宽（见 ghProbeConversunMinBytes）。
var ghProbeAssetsConversun = []string{
	"github.com/conversun/fnos-apps/releases/download/sub-store/v2.41.0/sub-store_2.41.0_x86.fpk",
	"github.com/conversun/fnos-apps/releases/download/vibenvr/v1.35.8/vibenvr_1.35.8_x86.fpk",
}

const ghProbeConversunMinBytes = 8 * 1024 // 小文件资产：读到 8KB 即可证明代理链路可用

const (
	ghProbeMaxBytes    = 2 * 1024 * 1024 // 读够即止
	ghProbeMinBytes    = 512 * 1024      // 低于此量判失败（慢到不可用/超时）
	ghProbeReadTimeout = 10 * time.Second
)

type mirrorProbeRes struct {
	key string
	ms  MirrorStat
}

// shortNetErr 把错误压成面板可展示短语（0.6.321 F6，#4 可感知性）。
func shortNetErr(err error) string {
	if err == nil {
		return ""
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNS 解析失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "超时"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "超时"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "连接被拒"
	}
	s := err.Error()
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}

// ghProbeURL 把探测资产拼成完整 URL（0.6.333 F1）。
//
// 加速镜像的 URL 是「前缀」形态（如 https://gh-proxy.com/），直接拼
// github.com/owner/repo/... 资产路径即可；而 direct 没有前缀（其 URL 为空），
// 资产本身就是 github.com/... 的裸路径，必须补 https:// 协议头。
//
// 旧实现给 direct 塞了一个 raw.githubusercontent.com 前缀，拼出来是
// https://raw.githubusercontent.com/github.com/ollama/... —— 非法地址，恒 404，
// 导致「直连 GitHub」永远显示失败（0.6.321 F7 引入的回归）。
func ghProbeURL(opt config.MirrorOption, asset string) string {
	// 资产已是绝对 URL（备用资产若写成直链）→ 原样使用
	if strings.HasPrefix(asset, "https://") || strings.HasPrefix(asset, "http://") {
		return asset
	}
	if opt.Key == "direct" {
		return "https://" + asset
	}
	return opt.URL + asset
}

// probeGhOne 探测单个 GitHub 加速源（0.6.333：从 probeGh 内闭包抽出为方法，
// 便于用 httptest 直测 429/404/慢 等分支）。返回该源的 MirrorStat。
func (m *mirrorMonitor) probeGhOne(ctx context.Context, opt config.MirrorOption) MirrorStat {
	// 0.6.321 F6：失败带原因短语（面板状态列展示；旧前端忽略可选字段）
	fail := func(reason string) MirrorStat {
		return MirrorStat{Key: opt.Key, Label: opt.Label, Status: "fail", Error: reason}
	}
	// 0.6.333 F2：429 限流单列「limited」——链路本身可达，属临时性限流，
	// 既不算故障（冻结连败计数、不触发「全部不可用」告警），也不参与优选。
	limited := func(reason string) MirrorStat {
		return MirrorStat{Key: opt.Key, Label: opt.Label, Status: "limited", Error: reason}
	}
	assets, minBytes := ghProbeAssets, int64(ghProbeMinBytes)
	if opt.Key == "conversun" {
		assets, minBytes = ghProbeAssetsConversun, ghProbeConversunMinBytes
	}
	pctx, pcancel := context.WithTimeout(ctx, ghProbeReadTimeout)
	defer pcancel()
	for _, asset := range assets {
		start := time.Now()
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, ghProbeURL(opt, asset), nil)
		if err != nil {
			return fail(shortNetErr(err))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fail(shortNetErr(err))
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			continue // 资产下线 → 试下一个
		}
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			resp.Body.Close()
			return limited("429 限流（临时）")
		case http.StatusForbidden:
			resp.Body.Close()
			return fail("403 拒绝")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 400 {
			resp.Body.Close()
			return fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
		ttfb := time.Now()
		var read int64
		buf := make([]byte, 256*1024)
		for read < ghProbeMaxBytes {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				read += int64(n)
			}
			if rerr != nil {
				break
			}
		}
		resp.Body.Close() // 主动截断（镜像可能发整份文件）
		elapsed := time.Since(start)
		st := MirrorStat{Key: opt.Key, Label: opt.Label, Status: "fail"}
		if read >= minBytes && elapsed > 0 {
			st.Status = "ok"
			st.LatencyMS = ttfb.Sub(start).Milliseconds() // 首字节延迟
			st.SpeedBPS = read * int64(time.Second) / int64(elapsed)
		} else {
			st.Error = fmt.Sprintf("慢（%ds 读 %dKB < %dKB 门槛）", int(ghProbeReadTimeout/time.Second), read>>10, minBytes>>10)
		}
		return st
	}
	return fail("探测资产 404（资产下线或未代理）")
}

// bumpGhFail 单源连败计数（0.6.333 F2 三态语义，调用方须持有 m.mu）：
// ok → 清零；limited（429 临时限流）→ 冻结（不清零也不累加）；其余 → 累加。
func (m *mirrorMonitor) bumpGhFail(st *MirrorStat) {
	switch st.Status {
	case "ok":
		m.ghFails[st.Key] = 0
	case "limited":
		// 冻结：保持原计数不变
	default:
		m.ghFails[st.Key]++
	}
	st.ConsecFails = m.ghFails[st.Key]
}

// probeGh GitHub 加速组测速（0.6.148 从 ensureFresh 拆出）：并发探测全部 gh
// 镜像（读前 2MB 测 MB/s），更新 ghStats/ghFails/lastProbeGh。
// 后台间隔由 mirrorProbeLoopGh 按配置（gh_probe_hours/minutes）驱动。
func (m *mirrorMonitor) probeGh() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gh := make(chan mirrorProbeRes, 32)

	ghProbe := func(ch chan mirrorProbeRes, opt config.MirrorOption) {
		ch <- mirrorProbeRes{opt.Key, m.probeGhOne(ctx, opt)}
	}

	var wg sync.WaitGroup
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL == "" {
			continue // auto/direct/custom 不可探测
		}
		wg.Add(1)
		go func(o config.MirrorOption) {
			defer wg.Done()
			ghProbe(gh, o)
		}(opt)
	}
	// 0.6.321 F7：「智能选源包含直连」开启（默认关=零行为变化）时追加合成
	// 候选 direct——探同一 pinned 资产（raw 直链），bestKey 可返回 direct
	// = 直连参与排名而非仅兜底（#4 建议 #1 承接；知情开启，大陆无代理场景
	// 直连常态慢，开关默认关）。
	// 0.6.333 F1：URL 留空——地址由 ghProbeURL 按 key=direct 拼 https://+资产。
	m.mu.Lock()
	includeDirect := m.ghIncludeDirect
	m.mu.Unlock()
	if includeDirect {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ghProbe(gh, config.MirrorOption{Key: "direct", Label: "直连 GitHub"})
		}()
	}
	wg.Wait()
	close(gh)

	// 按声明顺序收集（保持前端列表顺序）
	ghMap := map[string]MirrorStat{}
	for r := range gh {
		ghMap[r.key] = r.ms
	}
	ghOut := make([]MirrorStat, 0, len(ghMap))
	for _, opt := range config.GitHubMirrorOptions() {
		if st, ok := ghMap[opt.Key]; ok {
			ghOut = append(ghOut, st)
		}
	}

	now := time.Now()
	m.mu.Lock()
	for i := range ghOut {
		// 0.6.333 F2：三态连败计数见 bumpGhFail（ok 清零 / limited 冻结 / fail 累加）
		m.bumpGhFail(&ghOut[i])
	}
	m.ghStats = ghOut
	m.lastProbeGh = now
	m.lastProbe = now
	m.mu.Unlock()
}

// probeDk Docker 镜像组探测（0.6.148 从 ensureFresh 拆出）：registry 可达性
// + 吞吐测速（拉 library/nginx 首层读前 1MB），更新 dkStats/dkFails/lastProbeDk。
// 后台间隔由 mirrorProbeLoopDk 按配置（dk_probe_hours/minutes）驱动。
func (m *mirrorMonitor) probeDk() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dk := make(chan mirrorProbeRes, 32)

	dkProbe := func(ch chan mirrorProbeRes, opt config.MirrorOption) {
		base, client := opt.URL, http.DefaultClient
		if opt.Key == "kspeeder" {
			base = "https://" + config.KSpeederLocalAddr + "/"
			client = dkLocalClient
		}
		start := time.Now()
		resp, err := dkGet(ctx, base+"v2/", nil, client)
		lat := time.Since(start).Milliseconds()
		st := MirrorStat{Key: opt.Key, Label: opt.Label, Status: "fail"}
		if err != nil {
			st.Error = shortNetErr(err) // 0.6.321 F6
			ch <- mirrorProbeRes{opt.Key, st}
			return
		}
		ok := resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized
		_ = resp.Body.Close()
		if ok {
			st.Status = "ok"
			st.LatencyMS = lat
			// 测速失败（403 / 不代理 Docker Hub / 太慢）只影响速度列，不改可达判定
			st.SpeedBPS = dkSpeedProbe(ctx, base, client)
		}
		ch <- mirrorProbeRes{opt.Key, st}
	}

	var wg sync.WaitGroup
	for _, opt := range config.DockerMirrorOptions() {
		if opt.URL == "" && opt.Key != "kspeeder" {
			continue // kspeeder 无 URL（本地回环探测），其余空 URL（auto/direct/custom）不可探测
		}
		wg.Add(1)
		go func(o config.MirrorOption) {
			defer wg.Done()
			dkProbe(dk, o)
		}(opt)
	}
	wg.Wait()
	close(dk)

	// 按声明顺序收集（保持前端列表顺序）
	dkMap := map[string]MirrorStat{}
	for r := range dk {
		dkMap[r.key] = r.ms
	}
	dkOut := make([]MirrorStat, 0, len(dkMap))
	for _, opt := range config.DockerMirrorOptions() {
		if st, ok := dkMap[opt.Key]; ok {
			dkOut = append(dkOut, st)
		}
	}

	now := time.Now()
	m.mu.Lock()
	for i := range dkOut {
		st := &dkOut[i]
		if st.Status == "ok" {
			m.dkFails[st.Key] = 0
		} else {
			m.dkFails[st.Key]++
		}
		st.ConsecFails = m.dkFails[st.Key]
	}
	m.dkStats = dkOut
	m.lastProbeDk = now
	m.lastProbe = now
	m.mu.Unlock()
}

// ensureFresh 按需入口（设置面板「立即测速」/ ?refresh=1）：过期（或 force）
// 的组各探一次。0.6.148 起后台循环保留给 probeGh/probeDk 独立间隔驱动
// （mirrorProbeLoopGh/Dk，间隔可配置），此处只防按需调用时的重复全量探测。
func (m *mirrorMonitor) ensureFresh(force bool) {
	m.mu.Lock()
	ghFresh := time.Since(m.lastProbeGh) < mirrorCacheTTL
	dkFresh := time.Since(m.lastProbeDk) < mirrorCacheTTL
	m.mu.Unlock()
	if force || !ghFresh {
		m.probeGh()
	}
	if force || !dkFresh {
		m.probeDk()
	}
}

// dkWWWAuthRe WWW-Authenticate Bearer 参数解析（预编译）。
var (
	dkRealmRe = regexp.MustCompile(`realm="([^"]+)"`)
	dkSvcRe   = regexp.MustCompile(`service="([^"]+)"`)
	dkScopeRe = regexp.MustCompile(`scope="([^"]+)"`)
)

// dkLocalClient 本地 KSpeeder 引擎探测专用：自签名证书跳过校验。
// 目标是 127.0.0.1 回环（仅本机进程可达，无外部 MITM 面），与 docker daemon
// 对 127.0.0.1:port insecure-registry 的处理等效（同 New Store 做法）。
var dkLocalClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	},
}

// dkGet 带 Docker 客户端 User-Agent 的 GET（多镜像站对 python/curl 默认 UA 返回 403，
// 2026-09-24 测试机实测需 docker 风格 UA 才能过鉴权链路）。
func dkGet(ctx context.Context, rawURL string, extra map[string]string, client *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Docker/24.0.7 go/go1.21.3 git/79b8d9b os/linux arch/amd64")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(req)
}

// dkSpeedProbe 测 registry 镜像的拉层吞吐（字节/秒）：
// manifest（401 时走 WWW-Authenticate 匿名 token 流程）→ 首个 layer digest →
// blobs 读前 1MB 计时。任一步失败返回 0（前端显示「—」，不影响可达性判定）。
// 镜像不代理 Docker Hub（如纯 ghcr 源）或限速过慢同样得 0。
func dkSpeedProbe(ctx context.Context, base string, client *http.Client) int64 {
	const (
		maxBytes  = 1 * 1024 * 1024 // 读够即止（慢源 10s 内读不满则超时返回 0）
		minBytes  = 256 * 1024      // 低于此量判未测出
		speedTime = 10 * time.Second
	)
	accept := "application/vnd.docker.distribution.manifest.v2+json, " +
		"application/vnd.oci.image.manifest.v1+json, " +
		"application/vnd.docker.distribution.manifest.list.v2+json, " +
		"application/vnd.oci.image.index.v1+json"

	sctx, cancel := context.WithTimeout(ctx, speedTime)
	defer cancel()

	h := map[string]string{"Accept": accept}
	resp, err := dkGet(sctx, base+"v2/library/nginx/manifests/latest", h, client)
	if err != nil {
		return 0
	}
	if resp.StatusCode == http.StatusUnauthorized {
		wa := resp.Header.Get("WWW-Authenticate")
		_ = resp.Body.Close()
		realmM := dkRealmRe.FindStringSubmatch(wa)
		if realmM == nil {
			return 0
		}
		var svc string
		if m := dkSvcRe.FindStringSubmatch(wa); m != nil {
			svc = m[1]
		}
		scope := "repository:library/nginx:pull"
		if m := dkScopeRe.FindStringSubmatch(wa); m != nil {
			scope = m[1]
		}
		tokURL := realmM[1] + "?" + url.Values{
			"service": {svc},
			"scope":   {scope},
		}.Encode()
		tr, terr := dkGet(sctx, tokURL, nil, client)
		if terr != nil {
			return 0
		}
		var tok struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
		}
		tb, rerr := io.ReadAll(io.LimitReader(tr.Body, 1<<20))
		_ = tr.Body.Close()
		if rerr != nil || json.Unmarshal(tb, &tok) != nil {
			return 0
		}
		if tok.Token == "" {
			tok.Token = tok.AccessToken
		}
		if tok.Token == "" {
			return 0
		}
		h["Authorization"] = "Bearer " + tok.Token
		resp, err = dkGet(sctx, base+"v2/library/nginx/manifests/latest", h, client)
		if err != nil {
			return 0
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return 0
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	if rerr != nil {
		return 0
	}

	digest, ok := dkLayerDigest(sctx, base, h, body, client)
	if !ok {
		return 0
	}

	t0 := time.Now()
	bresp, err := dkGet(sctx, base+"v2/library/nginx/blobs/"+digest, h, client)
	if err != nil {
		return 0
	}
	var read int64
	buf := make([]byte, 256*1024)
	for read < maxBytes {
		n, rerr := bresp.Body.Read(buf)
		if n > 0 {
			read += int64(n)
		}
		if rerr != nil {
			break
		}
	}
	_ = bresp.Body.Close()
	if read < minBytes {
		return 0
	}
	return read * int64(time.Second) / int64(time.Since(t0))
}

// dkManifest manifest 或 index（二选一有 layers / manifests）。
type dkManifest struct {
	Layers []struct {
		Digest string `json:"digest"`
	} `json:"layers"`
	Manifests []struct {
		Digest string `json:"digest"`
	} `json:"manifests"`
}

// dkLayerDigest 从 manifest 体取首个 layer digest；index 时再取一个平台 manifest。
func dkLayerDigest(ctx context.Context, base string, h map[string]string, body []byte, client *http.Client) (string, bool) {
	var m dkManifest
	if json.Unmarshal(body, &m) != nil {
		return "", false
	}
	if len(m.Layers) > 0 {
		return m.Layers[0].Digest, true
	}
	if len(m.Manifests) == 0 {
		return "", false
	}
	r2, err := dkGet(ctx, base+"v2/library/nginx/manifests/"+m.Manifests[0].Digest, h, client)
	if err != nil {
		return "", false
	}
	b2, rerr := io.ReadAll(io.LimitReader(r2.Body, 4<<20))
	_ = r2.Body.Close()
	if rerr != nil {
		return "", false
	}
	var m2 dkManifest
	if json.Unmarshal(b2, &m2) != nil || len(m2.Layers) == 0 {
		return "", false
	}
	return m2.Layers[0].Digest, true
}

// trackSwitch 生效镜像变化时记录一次优选切换（仅最近一次；初始值不记）。
func (m *mirrorMonitor) trackSwitch(group, active, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, sw := &m.prevActiveGh, &m.lastSwitchGh
	if group == "dk" {
		prev, sw = &m.prevActiveDk, &m.lastSwitchDk
	}
	if *prev != "" && *prev != active {
		*sw = &MirrorSwitch{From: *prev, To: active, Time: time.Now().Format(time.RFC3339), Reason: reason}
	}
	*prev = active
}

// peekActive 读当前记录的生效镜像（0.6.133 通知挂点：trackSwitch 前后比对）。
func (m *mirrorMonitor) peekActive(group string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if group == "dk" {
		return m.prevActiveDk
	}
	return m.prevActiveGh
}

var mirrorSwitchNotified = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// mirrorSwitchNotifyOK 切换通知 30 分钟冷却（防 A→B→A 抖动刷屏）。
func (s *Server) mirrorSwitchNotifyOK(group string) bool {
	mirrorSwitchNotified.Lock()
	defer mirrorSwitchNotified.Unlock()
	if t, ok := mirrorSwitchNotified.at[group]; ok && time.Since(t) < 30*time.Minute {
		return false
	}
	mirrorSwitchNotified.at[group] = time.Now()
	return true
}

// mirrorLabel 镜像 key → 渠道通知用的可读标签（0.6.210）：
// direct=直连、custom=自定义源，其余查内置选项表（GH-Proxy HK 等），
// 查不到回退原始 key。
func mirrorLabel(group, key string) string {
	switch key {
	case "direct":
		return "直连"
	case "custom":
		return "自定义源"
	}
	opts := config.GitHubMirrorOptions()
	if group == "dk" {
		opts = config.DockerMirrorOptions()
	}
	for _, o := range opts {
		if o.Key == key {
			return o.Label
		}
	}
	return key
}

func (m *mirrorMonitor) lastSwitchOf(group string) *MirrorSwitch {
	m.mu.Lock()
	defer m.mu.Unlock()
	if group == "gh" {
		return m.lastSwitchGh
	}
	return m.lastSwitchDk
}

// StartMirrorProbeLoop 后台加速源测速循环：启动 10s 后先全量探一次
// （首批 FPK 下载 / 列表图标竞速就有新鲜测速数据），之后 GitHub / Docker
// 两组各自按配置间隔独立探测（0.6.148：设置页双齿轮可调，默认 5 分钟；
// 此前固定 mirrorCacheTTL 5 分钟，不再依赖用户打开设置面板）。
func (s *Server) StartMirrorProbeLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	s.applyMirrorProbeFlags()
	s.Mirrors.ensureFresh(true) // 首批：全量探一次
	// 0.6.187：首轮也评估一次——建立 prevActive 基线（首调不推），
	// 之后后台循环每齿轮周期自动评估，UI 不开也推。
	s.evaluateMirror(orAuto(cfgMirror(s)), "gh", s.Mirrors.gh())
	s.evaluateMirror(orAuto(cfgDockerMirror(s)), "dk", s.Mirrors.dk())
	go s.mirrorProbeLoopGh(ctx)
	go s.mirrorProbeLoopDk(ctx)
}

// mirrorProbeLoopGh GitHub 加速组后台测速循环（0.6.148：间隔可配置，
// 每周期现读配置——改设置后下一周期即生效，无需重启）。
func (s *Server) mirrorProbeLoopGh(ctx context.Context) {
	for {
		mins := config.DefaultProbeIntervalMinutes
		if s.Cfg != nil {
			mins = s.Cfg.GhProbeIntervalMinutes()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(mins) * time.Minute):
		}
		// 0.6.312 B3/F6（活性门控）：不活跃（>30min 无 UI）时探针 5→15min——
		// tick 按配置间隔不变，仅跳过「距上次探测不足 15min」的不活跃 tick。
		if s.uiInactive() && time.Since(s.probeGhLast()) < mirrorProbeIdleGap {
			continue
		}
		s.applyMirrorProbeFlags()
		s.Mirrors.probeGh()
		s.lastProbeGh.Store(time.Now().Unix())
		// 0.6.187：每齿轮周期评估优选切换（生效源变化 → 通知，30min 冷却防抖）
		s.evaluateMirror(orAuto(cfgMirror(s)), "gh", s.Mirrors.gh())
		s.mirrorsAllFailedCheck() // D3：全部镜像+直连失败 = 网络层异常（30min 冷却）
	}
}

// mirrorProbeLoopDk Docker 镜像组后台探测循环（同上，独立间隔）。
func (s *Server) mirrorProbeLoopDk(ctx context.Context) {
	for {
		mins := config.DefaultProbeIntervalMinutes
		if s.Cfg != nil {
			mins = s.Cfg.DkProbeIntervalMinutes()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(mins) * time.Minute):
		}
		// 0.6.312 B3/F6（活性门控）：同 gh 组，dk 组独立计时
		if s.uiInactive() && time.Since(s.probeDkLast()) < mirrorProbeIdleGap {
			continue
		}
		s.Mirrors.probeDk()
		s.lastProbeDk.Store(time.Now().Unix())
		// 0.6.187：每齿轮周期评估优选切换（同 gh，独立组）
		s.evaluateMirror(orAuto(cfgDockerMirror(s)), "dk", s.Mirrors.dk())
		// 0.6.188：dk 组全挂/恢复监测（与 gh 组对偶）
		s.mirrorsAllFailedCheckOf("dk")
	}
}

// cfgMirror/cfgDockerMirror 读当前选中的加速源（nil 配置安全）。
func cfgMirror(s *Server) string {
	if s.Cfg == nil {
		return ""
	}
	return s.Cfg.Mirror
}

func cfgDockerMirror(s *Server) string {
	if s.Cfg == nil {
		return ""
	}
	return s.Cfg.DockerMirror
}

// applyMirrorProbeFlags 应用本周期探测开关（0.6.321 F7；Cfg 为 nil 时 no-op）。
func (s *Server) applyMirrorProbeFlags() {
	if s.Cfg != nil {
		s.Mirrors.setGhIncludeDirect(s.Cfg.MirrorIncludeDirect)
	}
}

func (m *mirrorMonitor) gh() []MirrorStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ghStats
}

func (m *mirrorMonitor) dk() []MirrorStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dkStats
}

func (m *mirrorMonitor) lastProbeTime() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastProbe
}

// lastProbeOf 指定组（"gh"/"dk"）的最近探测时间（0.6.148 两组独立）。
func (m *mirrorMonitor) lastProbeOf(group string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if group == "dk" {
		return m.lastProbeDk
	}
	return m.lastProbeGh
}

// statusOf 返回指定 key 的健康状态（"" = 无数据）。
func statusOf(stats []MirrorStat, key string) string {
	for _, s := range stats {
		if s.Key == key {
			return s.Status
		}
	}
	return ""
}

// bestKey 返回健康数据里吞吐最高的 ok 镜像 key（无速度数据时退回首字节延迟最低）。
func bestKey(stats []MirrorStat) string {
	keys := topSpeedKeys(stats)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// topSpeedKeys 按吞吐降序返回全部 ok 镜像 key（无速度数据时按首字节延迟升序兜底）。
// 0.6.333 F2：只收 ok——limited（限流）与 fail 均不参与优选排名（限流源没有
// 吞吐数据，纳入排名会把它顶到「最快」位置，与实际可用性相反）。
func topSpeedKeys(stats []MirrorStat) []string {
	type e struct {
		key   string
		speed int64
		lat   int64
	}
	var ok []e
	for _, s := range stats {
		if s.Status != "ok" {
			continue
		}
		ok = append(ok, e{s.Key, s.SpeedBPS, s.LatencyMS})
	}
	// 速度降序；同速（或都无速度数据）时延迟升序
	sort.SliceStable(ok, func(i, j int) bool {
		if ok[i].speed != ok[j].speed {
			return ok[i].speed > ok[j].speed
		}
		return ok[i].lat < ok[j].lat
	})
	keys := make([]string, 0, len(ok))
	for _, x := range ok {
		keys = append(keys, x.key)
	}
	return keys
}

// evaluateMirror 计算本组生效镜像（手选优先规则）并做优选切换跟踪/通知。
// 生效规则（2026-09-24 用户定案「手选优先」）：
//   - auto/空     = 智能优选（吞吐/健康最快）
//   - 手动指定源  = 未失效时优先用它；最近测速 fail 时自动切到智能优选，
//     并记录可见的切换原因（手测恢复后自动回到手选源首位）
//   - direct/custom = 按所选生效
//
// 0.6.187：从 healthOf 抽出——切换判定 + 通知（0.6.133）+ trackSwitch 由
// 后台探测循环（mirrorProbeLoopGh/Dk，按时/分齿轮间隔）与 healthOf 视图
// 共同调用：UI 不开也按齿轮周期自动推（用户定案：齿轮选多久就多久推一次）。
// 30min 冷却 + prevActive 去重保证多调用点幂等（重复调用不重复推）。
func (s *Server) evaluateMirror(selected, group string, stats []MirrorStat) (active, reason string) {
	autoReason := "Docker 源按健康度自动优选"
	if group == "gh" {
		autoReason = "按吞吐测速自动优选"
	}
	active, reason = "", autoReason
	switch {
	case selected == "" || selected == "auto":
		active = bestKey(stats)
		if active == "" {
			active = "direct"
		}
	case selected == "direct" || selected == "custom":
		active = selected
	default:
		// 0.6.333 F2：只有真实故障（fail）才判手选源失效并自动切换；
		// limited（429 临时限流）视为可用 → 保持手选，不弹「已失效，自动切换」。
		if statusOf(stats, selected) == "fail" {
			active = bestKey(stats)
			if active == "" {
				active = selected
			}
			reason = "手选源已失效，自动切换智能优选"
		} else {
			active = selected
		}
	}
	// 加速源自动切换优选通知：生效源真实变化才推（30min 冷却防抖动）。
	// 0.6.210 排版统一：key 换可读标签（gh-proxy-hk → GH-Proxy HK），
	// 此前渠道里直接显示原始 key 不好读。
	if before := s.Mirrors.peekActive(group); before != "" && before != active {
		if s.mirrorSwitchNotifyOK(group) && s.IsNotifyEventOn("mirror_switched") {
			grp := "GitHub"
			if group == "dk" {
				grp = "Docker"
			}
			label := mirrorLabel(group, before)
			labelNew := mirrorLabel(group, active)
			// 0.6.212 排版统一（对齐其余通知的美观排版）：简洁/友好/完整
			// 变体 + 卡片两列行（加速组/原生效源/新生效源/切换原因）+
			// markdown_v2 表格。
			reasonText := reason
			if reasonText == "" {
				reasonText = "自动优选"
			}
			body := fmt.Sprintf("%s 加速源：%s → %s\n原因：%s", grp, label, labelNew, reasonText)
			var tb strings.Builder
			fmt.Fprintf(&tb, "| 项目 | 值 |\n| :-- | :-- |\n")
			fmt.Fprintf(&tb, "| 加速组 | %s |\n", grp)
			fmt.Fprintf(&tb, "| 原生效源 | %s |\n", mdCell(label))
			fmt.Fprintf(&tb, "| 新生效源 | %s |\n", mdCell(labelNew))
			fmt.Fprintf(&tb, "| 切换原因 | %s |\n", mdCell(reasonText))
			rowsFull := []notify.CardRow{
				{Key: "加速组", Value: grp},
				{Key: "原生效源", Value: label},
				{Key: "新生效源", Value: labelNew},
				{Key: "切换原因", Value: reasonText},
			}
			rowsConcise := []notify.CardRow{
				{Key: "原生效源", Value: label},
				{Key: "新生效源", Value: labelNew},
			}
			s.notifyEventRichV("mirror_switched", "加速源自动切换优选",
				body, tb.String(), rowsFull, "", "", true,
				&notify.Variants{
					ContentConcise: fmt.Sprintf("%s：%s → %s", grp, label, labelNew),
					ContentFull:    body,
					TableFull:      tb.String(),
					RowsConcise:    rowsConcise,
					ConciseSub:     fmt.Sprintf("%s → %s", label, labelNew),
					RowsFull:       rowsFull,
				})
		}
	}
	s.Mirrors.trackSwitch(group, active, reason)
	return active, reason
}

// healthOf 生成前端 MirrorHealth 视图（group: gh/dk，用于优选切换跟踪）。
func (s *Server) healthOf(selected, group string, stats []MirrorStat) MirrorHealth {
	active, _ := s.evaluateMirror(selected, group, stats)
	last := ""
	if t := s.Mirrors.lastProbeOf(group); !t.IsZero() {
		last = t.Format(time.RFC3339)
	}
	// 0.6.148：上报本组配置的有效测速间隔（齿轮选择框改后实时跟随）
	intervalS := config.DefaultProbeIntervalMinutes * 60
	if s.Cfg != nil {
		if group == "gh" {
			intervalS = s.Cfg.GhProbeIntervalMinutes() * 60
		} else {
			intervalS = s.Cfg.DkProbeIntervalMinutes() * 60
		}
	}
	out := make([]MirrorStat, len(stats))
	copy(out, stats)
	return MirrorHealth{
		Mirrors:    out,
		Selected:   selected,
		Active:     active,
		LastProbe:  last,
		IntervalS:  intervalS,
		LastSwitch: s.Mirrors.lastSwitchOf(group),
	}
}

// ghOrderedKeys 返回 GitHub 加速源的优先顺序（手选优先）：
// 手动指定源（未失效）排首位，其后按吞吐序排健康源；auto = 纯吞吐序；
// custom 排首位（其后接智能优选）；direct = 空（仅直连）。
// 手选源最近测速 fail 时从序列剔除 —— 选路自动切到智能优选，
// 测速恢复 ok 后自动回到首位（无需改设置）。
func (s *Server) ghOrderedKeys() []string {
	tops := topSpeedKeys(s.Mirrors.gh())
	mirror, custom := "auto", ""
	if s.Cfg != nil {
		mirror, custom = s.Cfg.Mirror, s.Cfg.CustomGitHubMirror
	}
	switch mirror {
	case "", "auto":
		return tops
	case "direct":
		return nil
	case "custom":
		if custom == "" {
			return tops
		}
		out := make([]string, 0, len(tops)+1)
		out = append(out, "custom")
		return append(out, tops...)
	default:
		// 0.6.333 F2：limited（限流）不剔除手选源，与 evaluateMirror 同口径。
		if statusOf(s.Mirrors.gh(), mirror) == "fail" {
			return tops
		}
		out := make([]string, 0, len(tops)+1)
		out = append(out, mirror)
		for _, k := range tops {
			if k != mirror {
				out = append(out, k)
			}
		}
		return out
	}
}
