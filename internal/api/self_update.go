package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aead.dev/minisign"

	"moo/internal/config"
	"moo/internal/lang"
	"moo/internal/netx"
	"moo/internal/release"
)

// 商店自更新：官方渠道 = GitHub Releases（Blue-Mink/moo），TLS 直连
//（0.6.207-panel D1：不经镜像候选，防 HTTP 中间人；科学加速代理对
// GitHub 域名仍由 netx 全局代理生效——仅本机代理，信任边界不变）。
//
// 检测：GET /api/store-update → 探测 releases/latest（10min 缓存 + 30min 后台轮询），
// 严格版本比较（只升不降，防恶意源/镜像回灌旧版），架构取包：amd64 → _x86，
// arm64/arm → _arm（0.6.355 起双架构发布；此前仅 amd64 有自更新）。
//
// 更新：POST /api/store-update（SSE）→ 下载 FPK → sha256 校验（0.6.328 起 fail-closed）→
// 飞牛 appcenter daemon 的 RPC 升级通道就地升级（参考 conversun/fnos-store
// 的自更新设计：daemon 停应用、换包、@appdata 字节级保留、恢复原运行状态，
// 且 daemon 是独立 root 进程——被杀掉的本进程不影响升级完成）。
// 相比「卸载后重装」（fn-knock 早期方案；在部分 fnOS 版本上 CLI 卸载后
// 重装 100% 失败，应用与数据一并丢失，conversun/fnos-apps#265/#189），
// 就地升级通道不会走到卸载路径，数据风险从机制上消除。
//
// 通道（fnOS 1.2.0701 实测；信封 {"code":0,"msg":"","data":{...}}）：
//   unix socket /var/run/com.trim.app.center.sock
//   POST /rpc/v1/download/task   {"packageSourceType":"file","path":fpk} → {"downloadTaskId"}
//   POST /rpc/v1/download/status {"downloadTaskId"} → {status, appName, version, packageType, installed, path}
//   POST /rpc/v1/update/info     {appName, updateVersion, packageType, language} → {wizardInfo:{installedVolumeID,...}}
//   POST /rpc/v1/update/task     {appName, updateVersion, packageType, systemParameters, customParameters, language} → {"taskId"}
//   POST /rpc/v1/common/status   {"taskId"} → {status, message, outputText}
//   状态码：1=运行中 2=成功 5=未知任务
//
// daemon 会在升级过程中杀掉本进程，因此 SSE self_update 事件在提交升级任务
// 之前发出并等待冲刷（前端 pollForRestart 接管后续体验）；若 daemon 拒绝或
// 失败而本进程还活着，则走 SSE error 上报。
//
// 完整性（0.6.207-panel D1；0.6.328 安全审计 W3）：自更新全程 TLS 直连 GitHub
//（不再经 HTTP 镜像候选，消除中间人换包面）。release 必须附 <资产名>.sha256
// 侧车，下载后严格验哈希；缺失即拒绝自更新（fail-closed，不再降级结构校验）。
//
// 运维/测试钩子：MOO_SELFUPDATE_AS_VERSION 环境变量可覆写「当前版本」
// （测试机验证自更新链路用，线上不设）。

const (
	selfUpdateRepo    = "Blue-Mink/moo"
	storeSelfAppName  = "moo"
	selfProbeTTL      = 10 * time.Minute
	selfProbeInterval = 30 * time.Minute
	selfDlTimeout     = 10 * time.Second       // 单候选探测/下载首包超时
	selfFlushDelay    = 750 * time.Millisecond // conversun 实测：足够 SSE 字节到达客户端
	selfStageDeadline = 3 * time.Minute
	selfTaskDeadline  = 15 * time.Minute
	selfStatusPoll    = 2 * time.Second
)

// 0.6.380：下载阶段通道级看护阈值（见 downloadSelfUpdateOne）——连上不吐
// 字节 / 中途掉到 0 都要尽快换通道，而不是等 8 分钟总超时。
// var 不是 const —— 单测压到百毫秒级。
var (
	selfUpdateFirstByteTimeout = 12 * time.Second
	selfUpdateStallWindow      = 20 * time.Second
)

// selfUpdateSocketPath 飞牛应用中心 daemon 的内部 RPC socket（测试可覆写）。
var selfUpdateSocketPath = "/var/run/com.trim.app.center.sock"

// 探测入口（0.6.299：提为变量，httptest 可覆写）。
var (
	selfUpdateAPIBase = "https://api.github.com"
	selfUpdateWebBase = "https://github.com"
	// 第三兜底：FnDepot 公开源的 moo 条目版本号（该仓 2026-07 起公开、
	// 各出口 raw CDN 缓存全热；moo 仓 10-05 才转公开，部分出口的中间层
	// 还负缓存着私有时期的 404，github.com/releases/latest 会持续吐旧 404）
	// 0.6.325-C16：目标从 `moo.json` 改为 `fnpack.json` —— FnDepot 仓根的
	// moo.json 已于 2026-10-10 被删除（该仓现只有 fnpack.json），旧 URL 恒 404，
	// 使这条兜底长期失效（设置页实测「FnDepot 源兜底: HTTP 404」）。
	// 新目标为 V2 结构，moo 节点**无顶层 version**，版本号在 releases 的键上，
	// 解析见 fetchFnDepotVersion（兼容旧的 version 字段写法）。
	selfUpdateFnDepotURL = "https://raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json"
	// 0.6.371 第五兜底：Moo 自索引 moo.json（应用源本体，与 release 同源发布）。
	// 顶层 apps.moo.version 跟版，releases.<ver>.packages 里有 download_url/sha256；
	// FnDepot 的 fnpack.json 只是转发副本，上游索引更直接。
	selfUpdateSelfIndexURL = "https://raw.githubusercontent.com/Blue-Mink/moo/main/moo.json"
)

// 自更新签名公钥（minisign，0.6.329 起）：go:embed 内嵌，验签不联网取公钥
// （否则攻击者可连公钥一起替换）。私钥离线保管，见 AGENTS.md「Moo 自更新签名
// 发布约束」；签名由发布机离线生成 .minisig 侧车。
//
//go:embed self_update_pubkey.txt
var selfUpdatePubkeyText string

var selfUpdatePubkey = func() minisign.PublicKey {
	var pk minisign.PublicKey
	if err := pk.UnmarshalText([]byte(strings.TrimSpace(selfUpdatePubkeyText))); err != nil {
		panic("invalid embedded minisign public key: " + err.Error())
	}
	return pk
}()

// selfUpdateArchSuffix 返回本机架构对应的 FPK 资产后缀（0.6.355）：
// 发布约定 moo_<ver>_x86.fpk / moo_<ver>_arm.fpk。
// 0.6.329~0.6.354 一律只认 x86（当时官方只发 x86 包，非 amd64 直接关闭自更新）；
// 0.6.355 起 Moo 正式发布双架构 FPK，arm 设备按自己的架构取包。
func selfUpdateArchSuffix() string { return selfUpdateArchSuffixFor(runtime.GOARCH) }

// selfUpdateArchSuffixFor 与 GOARCH 解耦，便于单测覆盖 arm 分支。
func selfUpdateArchSuffixFor(goarch string) string {
	// 规则单源：架构映射只在 internal/release 里实现一份。
	return release.ArchSuffix(goarch)
}

// selfUpdateArchSupported 自更新只对有发布资产的双架构开放；
// 其它架构（386 等）没有对应 FPK，保持 0.6.354 前的行为：不提供自更新。
func selfUpdateArchSupported() bool {
	return release.ArchSupported(runtime.GOARCH)
}

// selfUpdateAsset release 里的一个资产（只为把「按架构选包」抽成可单测的纯函数）。
type selfUpdateAsset struct{ Name, URL string }

// pickSelfUpdateAsset 按架构后缀挑 FPK 资产（0.6.355）：
//   - 后缀命中（如 arm64 机器取 moo_x_arm.fpk）优先；
//   - 没有同架构资产时回落到第一个 .fpk（保持 0.6.354 前的可用性行为）。
func pickSelfUpdateAsset(assets []selfUpdateAsset, suffix string) (url, name string) {
	fallbackURL, fallbackName := "", ""
	for _, a := range assets {
		if a.URL == "" || !strings.HasSuffix(a.Name, ".fpk") {
			continue
		}
		if fallbackURL == "" {
			fallbackURL, fallbackName = a.URL, a.Name
		}
		if strings.Contains(a.Name, suffix) {
			return a.URL, a.Name
		}
	}
	return fallbackURL, fallbackName
}

// selfUpdateFallbackAsset 兜底通道（web 302 / FnDepot 源 / 镜像）下构造本次
// 自更新资产：这些通道只拿得到版本号，拿不到 release 资产清单，故按发布约定
// 直拼 FPK URL 与资产名。
//
// 0.6.370 修：兜底分支此前把 .sha256/.minisig URL 返回空串，而 0.6.329 起
// 自更新对两者 fail-closed → 只要 GitHub API 被限流（国内 NAT 共享出口极易
// 耗尽 60/h，实测 core remaining=0）就必然落到兜底，然后 100% 失败，且报错
// 误导为「release 未附签名资产」（实际资产齐全）。发布流程本就要求三件套，
// 侧车 URL 可直接由 FPK URL 拼出。
func selfUpdateFallbackAsset(tag string) (assetURL, assetName string) {
	// 规则单源：三件套拼装只在 internal/release.FallbackAssets 里实现一份。
	a := release.FallbackAssets(selfUpdateWebBase, selfUpdateRepo, tag, selfUpdateArchSuffix())
	if i := strings.LastIndex(a.FPK, "/"); i >= 0 {
		return a.FPK, a.FPK[i+1:]
	}
	return a.FPK, a.FPK
}

type selfUpdateProbe struct {
	srv *Server

	mu               sync.Mutex
	checkedAt        time.Time
	latestTag        string // 已剥离 v 前缀
	latestAssetURL   string
	latestAsset      string
	latestShaURL     string // release 附带的 .sha256 资产 URL（可为空）
	latestMinisigURL string // 0.6.329：release 附带的 .minisig 资产 URL（可为空）
	latestBody       string // 0.6.311：release 正文（更新日志；仅 API 主通道有值）
	lastErr          error
	lastNotified     string    // 已推过 store_update_available 的版本（防重复）
	ratelimitUntil   time.Time // 0.6.299：GitHub 403 限流解除时刻（之前不打 API）
}

func (s *Server) selfUpdProbe() *selfUpdateProbe {
	s.selfUpdOnce.Do(func() {
		p := &selfUpdateProbe{srv: s}
		s.selfUpd = p
		go p.loopForever()
	})
	return s.selfUpd
}

func (p *selfUpdateProbe) loopForever() {
	t := time.NewTicker(selfProbeInterval)
	defer t.Stop()
	for range t.C {
		p.probe(true)
	}
}

// probe 探测官方最新版本。fresh 且 !force 直接返回缓存。
func (p *selfUpdateProbe) probe(force bool) (tag, assetURL, asset, shaURL, minisigURL string, err error) {
	p.mu.Lock()
	if !force && p.checkedAt.Add(selfProbeTTL).After(time.Now()) {
		tag, assetURL, asset, shaURL, minisigURL, err = p.latestTag, p.latestAssetURL, p.latestAsset, p.latestShaURL, p.latestMinisigURL, p.lastErr
		p.mu.Unlock()
		return
	}
	// 0.6.299：GitHub 403 限流窗口内（x-ratelimit-reset 未到）不再空打 API，
	// 直接回缓存——匿名配额 60/h 按出口 IP 计，国内 NAT 共享出口常被耗光。
	if !force && p.ratelimitUntil.After(time.Now()) {
		tag, assetURL, asset, shaURL, minisigURL, err = p.latestTag, p.latestAssetURL, p.latestAsset, p.latestShaURL, p.latestMinisigURL, p.lastErr
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	var rlUntil time.Time
	var body string
	tag, assetURL, asset, shaURL, minisigURL, body, rlUntil, err = p.srv.doProbe()

	p.mu.Lock()
	p.checkedAt = time.Now()
	p.latestTag, p.latestAssetURL, p.latestAsset, p.latestShaURL, p.latestMinisigURL, p.lastErr = tag, assetURL, asset, shaURL, minisigURL, err
	p.latestBody = body
	if !rlUntil.IsZero() {
		p.ratelimitUntil = rlUntil
	}
	notified := p.lastNotified
	p.mu.Unlock()

	// C1：探测到比当前版本新且未推过的版本 → store_update_available（每版一次）
	if err == nil && tag != "" && tag != notified &&
		cmpVersions(tag, p.srv.selfUpdateCurrentVersion()) > 0 {
		p.mu.Lock()
		p.lastNotified = tag
		p.mu.Unlock()
		// 0.6.143 用户纠正：更新入口 = 设置页右上角版本号 chip（有更新=红「有更新 vX」点确认；
		// 无更新=灰「vX」点提示），不在「关于」tab
		p.srv.notifyEvent("store_update_available", "有新版本",
			fmt.Sprintf("Moo v%s 已发布，点设置页右上角版本号更新", tag), true)
	}
	return
}

// doProbe 探测 GitHub 最新版本。主通道 = Releases API（0.6.207-panel D1：
// TLS 直连 only，不经 HTTP 镜像候选）；release 附 <资产名>.sha256 时一并
// 返回其 URL（下载后验哈希用），未附则 shaURL 为空（0.6.328 W3 起调用方
// 据此 fail-closed 拒绝自更新）。
//
// 0.6.299/300 兜底链（均不消耗 API 匿名配额）：
//  1. github.com web 通道（/releases/latest 的 302 Location 取 tag，
//     官方域直连 TLS，D1 信任边界不变）；
//  2. FnDepot 公开源 moo.json 的 apps.moo.version（raw CDN——moo 仓
//     10-05 才转公开，部分出口中间层负缓存着私有时期的 404，web 通道
//     会持续吐旧 404；FnDepot 仓 7 月起公开、全国客户端天天在拉）；
//     3.（0.6.300）FnDepot 源经用户配置的 GitHub 加速镜像（gh-proxy 系与
//     GitHub 非同网，直连断流时通常仍可达；镜像不代传 302，只用于 raw）。
//
// 兜底通道只能拿到 tag → 资产 URL 按官方命名约定（moo_{ver}_x86.fpk）
// 构造，无 .sha256 侧车 → 因 fail-closed（0.6.328 W3）自更新将拒绝。
// 返回值 rateLimitUntil：403 且响应带 x-ratelimit-reset 时为其时刻（+1min
// 余量），调用方在此之前不再打 API。
// body = release 正文（更新日志，0.6.311：设置页更新确认弹窗展示）；
// 仅 API 主通道有值，web/FnDepot 兜底通道为空（调用方优雅降级）。
// selfUpdateTokenHost 允许携带 token 的 URL 前缀。生产恒为官方 API；单测可覆写，
// 用来验证「只发官方域」与「401 自动退回匿名」两条安全/健壮性契约。
var selfUpdateTokenHost = "https://api.github.com/"

// githubAPIRequest 构造 GitHub API 请求，并**只在官方 api.github.com 域**带上
// 用户配置的 token（匿名配额 60/h/出口 IP → 带 token 5000/h）。
//
// 共用一个出口 IP 时（Moo 每 30 分钟探一次 + Moo Mate 按需探），匿名配额极易
// 打满；403 之后客户端被迫走兜底链——2026-11-08 事故正是「403 → web 兜底 →
// 拿到 mate 的 tag」。带上 token 让主通道稳定，是对共用面的根本缓解。
//
// 安全边界：自定义 base（含测试 httptest、镜像）一律不带——凭据只发官方域。
func (s *Server) githubAPIRequest(url string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if s != nil && s.Cfg != nil {
		if tok := strings.TrimSpace(s.Cfg.GithubToken); tok != "" &&
			strings.HasPrefix(url, selfUpdateTokenHost) {
			req.Header.Set("Authorization", "token "+tok)
		}
	}
	return req, nil
}

// doGitHubGET 发一次 GitHub API GET；带 token 却收到 401（PAT 过期/被撤销）时
// **自动退回匿名再试一次**——一份过期凭据不该把本来可用的匿名读打掉。
func (s *Server) doGitHubGET(url string) (*http.Response, error) {
	client := netx.NewClient(selfDlTimeout)
	req, err := s.githubAPIRequest(url)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && req.Header.Get("Authorization") != "" {
		resp.Body.Close()
		anon, aerr := http.NewRequest(http.MethodGet, url, nil)
		if aerr != nil {
			return nil, aerr
		}
		anon.Header.Set("Accept", "application/vnd.github+json")
		return client.Do(anon)
	}
	return resp, nil
}

func (s *Server) doProbe() (tag, assetURL, asset, shaURL, minisigURL, body string, rateLimitUntil time.Time, err error) {
	apiURL := selfUpdateAPIBase + "/repos/" + selfUpdateRepo + "/releases/latest"

	resp, gerr := s.doGitHubGET(apiURL)
	var apiErr error
	var tagInvalid bool
	if gerr != nil {
		apiErr = fmt.Errorf("官方 release 探测失败: %w", gerr)
	} else if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden {
			if rs := resp.Header.Get("x-ratelimit-reset"); rs != "" {
				if u, perr := strconv.ParseInt(rs, 10, 64); perr == nil {
					if until := time.Unix(u, 0); until.After(time.Now()) {
						rateLimitUntil = until.Add(time.Minute)
					}
				}
			}
		}
		resp.Body.Close()
		apiErr = fmt.Errorf("官方 release 探测失败: HTTP %d", resp.StatusCode)
	} else {
		var rel struct {
			TagName string `json:"tag_name"`
			Body    string `json:"body"`
			Assets  []struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			} `json:"assets"`
		}
		data, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if rerr != nil {
			return "", "", "", "", "", "", rateLimitUntil, fmt.Errorf("官方 release 探测失败: %w", rerr)
		}
		if uerr := json.Unmarshal(data, &rel); uerr != nil {
			return "", "", "", "", "", "", rateLimitUntil, fmt.Errorf("官方 release 探测失败: %w", uerr)
		}
		tag = strings.TrimPrefix(rel.TagName, "v")
		if tag == "" {
			return "", "", "", "", "", "", rateLimitUntil, errors.New("release 无版本号")
		}
		if !isMooVersionTag(tag) {
			// 同仓 Moo Mate 的 release 抢走了 releases/latest（2026-11-08 事故）：
			// 不能把它当成本仓最新版本 —— cmpVersions 会把 mate-v0.2.55 拆成
			// [0,2,55] 判「无更新」，且 API 返回 200 无 error 会短路整条兜底链。
			tagInvalid = true
			apiErr = fmt.Errorf("官方 release 探测失败: releases/latest 指向非 Moo 版本 tag %q（疑似同仓其它应用发布抢占 latest）", strings.TrimSpace(rel.TagName))
		} else {
			// 选本机架构对应的 FPK 资产（0.6.355：x86 与 arm 都发布；
			// 找不到同架构资产时兜底选任意 fpk，保持旧行为的可用性）
			assets := make([]selfUpdateAsset, 0, len(rel.Assets))
			for _, a := range rel.Assets {
				assets = append(assets, selfUpdateAsset{Name: a.Name, URL: a.URL})
			}
			assetURL, asset = pickSelfUpdateAsset(assets, selfUpdateArchSuffix())
			// D1：release 附 .sha256 资产（资产同名 + .sha256 后缀）→ 下载后验哈希
			for _, a := range rel.Assets {
				if a.URL != "" && a.Name == asset+".sha256" {
					shaURL = a.URL
					break
				}
			}
			// 0.6.329：release 附 .minisig 资产（minisign 分离签名）→ 下载后验签
			for _, a := range rel.Assets {
				if a.URL != "" && a.Name == asset+".minisig" {
					minisigURL = a.URL
					break
				}
			}
			return tag, assetURL, asset, shaURL, minisigURL, rel.Body, rateLimitUntil, nil
		}
	}

	// 兜底 0（本轮新增）：latest 被同仓其它应用的 release 抢占时，改用 release
	// 列表挑最高的 Moo 版本。只在「API 200 但 tag 非法」时调用——此时本来就要
	// 走兜底，不会额外多打一次 API（403 场景由后面的 web 通道接手）。
	if tagInvalid {
		if ltag, lerr := s.probeReleaseList(); lerr == nil {
			log.Printf("[selfupdate] %v，改用 release 列表兜底命中 v%s", apiErr, ltag)
			assetURL, asset := selfUpdateFallbackAsset(ltag)
			return ltag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
		} else {
			apiErr = fmt.Errorf("%v（release 列表兜底: %v）", apiErr, lerr)
		}
	}

	// 兜底 1：web 通道 302
	wtag, werr := s.probeWebLatest()
	if werr == nil {
		log.Printf("[selfupdate] API 通道失败（%v），web 兜底命中 v%s", apiErr, wtag)
		assetURL, asset := selfUpdateFallbackAsset(wtag)
		return wtag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
	}
	// 兜底 2（0.6.371）：Moo 自索引 moo.json（raw CDN）
	itag, ierr := s.probeSelfIndexLatest()
	if ierr == nil {
		log.Printf("[selfupdate] API/web 通道失败（%v / %v），moo.json 索引兜底命中 v%s", apiErr, werr, itag)
		assetURL, asset := selfUpdateFallbackAsset(itag)
		return itag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
	}
	// 兜底 3：Moo 自索引经用户配置的 GitHub 加速镜像（国内直连 raw 常断流）
	imtag, imerr := s.probeSelfIndexMirrorLatest()
	if imerr == nil {
		log.Printf("[selfupdate] API/web/moo.json 直连失败（%v / %v / %v），索引镜像兜底命中 v%s", apiErr, werr, ierr, imtag)
		assetURL, asset := selfUpdateFallbackAsset(imtag)
		return imtag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
	}
	// 兜底 4：FnDepot 公开源版本号（raw CDN，不受 moo 仓负缓存 404 影响）
	jtag, jerr := s.probeFnDepotLatest()
	if jerr == nil {
		log.Printf("[selfupdate] API/web 通道失败（%v / %v），FnDepot 源兜底命中 v%s", apiErr, werr, jtag)
		assetURL, asset := selfUpdateFallbackAsset(jtag)
		return jtag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
	}
	// 兜底 5（0.6.300）：FnDepot 源经用户 GitHub 加速镜像。国内 NAS 直连
	// GitHub 常整体断流（API 403 + web/raw 超时，0.6.299 实测），而 gh-proxy
	// 系镜像与 GitHub 非同网、通常可达。镜像不代传 releases/latest 的 302
	// （内部跟随返回 200 HTML），故只用于 raw 文件（FnDepot moo.json）。
	mtag, merr := s.probeFnDepotMirrorLatest()
	if merr == nil {
		log.Printf("[selfupdate] 前四条通道全失败，FnDepot 镜像兜底命中 v%s", mtag)
		assetURL, asset := selfUpdateFallbackAsset(mtag)
		return mtag, assetURL, asset, assetURL + ".sha256", assetURL + ".minisig", "", rateLimitUntil, nil
	}
	return "", "", "", "", "", "", rateLimitUntil,
		fmt.Errorf("%w（web 兜底: %v；moo.json 索引: %v；索引镜像: %v；FnDepot 源: %v；FnDepot 镜像: %v）",
			apiErr, werr, ierr, imerr, jerr, merr)
}

// probeWebLatest 经 github.com web 通道的 302 重定向取最新 release tag：
// GET https://github.com/{repo}/releases/latest → /releases/tag/vX.Y.Z。
// 国内 NAS 出口偶发分钟级断流（0.6.299 主 NAS 实测），网络类错误重试一次。
func (s *Server) probeWebLatest() (string, error) {
	c := netx.NewClient(selfDlTimeout)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(selfUpdateWebBase + "/" + selfUpdateRepo + "/releases/latest")
	if err != nil {
		time.Sleep(1500 * time.Millisecond)
		if resp2, err2 := c.Get(selfUpdateWebBase + "/" + selfUpdateRepo + "/releases/latest"); err2 == nil {
			resp = resp2
		} else {
			return "", fmt.Errorf("web 探测失败: %w", err2)
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		return "", fmt.Errorf("web 探测失败: HTTP %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	segs := strings.Split(strings.TrimRight(loc, "/"), "/")
	if len(segs) < 2 {
		return "", errors.New("web 探测: 重定向目标无效")
	}
	tag := strings.TrimPrefix(segs[len(segs)-1], "v")
	if tag == "" {
		return "", errors.New("web 探测: 重定向目标无版本号")
	}
	if !isMooVersionTag(tag) {
		return "", fmt.Errorf("web 探测: 重定向目标不是 Moo 版本 tag（%q，疑似同仓其它应用抢占 latest）", segs[len(segs)-1])
	}
	return tag, nil
}

// probeSelfIndexLatest 兜底 2（0.6.371）：读 Moo 自索引 moo.json 的
// apps.moo.version。这是应用源本体，跟版比 FnDepot 的转发副本更直接。
func (s *Server) probeSelfIndexLatest() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), selfDlTimeout)
	defer cancel()
	return fetchMooIndexVersion(ctx, selfUpdateSelfIndexURL)
}

// probeSelfIndexMirrorLatest 兜底 3（0.6.371）：Moo 自索引经用户配置的
// GitHub 加速镜像。取吞吐前 4 的健康镜像并发竞速，任一命中即返回。
func (s *Server) probeSelfIndexMirrorLatest() (string, error) {
	prefixes := selfUpdateMirrorPrefixes(s)
	if len(prefixes) > 4 {
		prefixes = prefixes[:4]
	}
	if len(prefixes) == 0 {
		return "", errors.New("无可用加速镜像")
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done bool
		tag  string
		errs []string
	)
	ctx, cancel := context.WithTimeout(context.Background(), selfDlTimeout)
	defer cancel()
	for _, p := range prefixes {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			t, err := fetchMooIndexVersion(ctx, p+selfUpdateSelfIndexURL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, p+" → "+err.Error())
				return
			}
			if !done {
				done, tag = true, t
			}
		}(p)
	}
	wg.Wait()
	if done {
		return tag, nil
	}
	return "", errors.New(strings.Join(errs, " | "))
}

// fetchMooIndexVersion 解析 moo.json 的 apps.moo 版本号。顶层 version 优先；
// 缺失时取 releases 里最大的版本（JSON 对象键顺序不保证，不能取第一个）。
func fetchMooIndexVersion(ctx context.Context, fullURL string) (string, error) {
	c := netx.NewClient(selfDlTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return "", fmt.Errorf("moo.json 索引探测失败: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("moo.json 索引探测失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("moo.json 索引探测失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("moo.json 索引探测失败: %w", err)
	}
	var doc struct {
		Apps map[string]struct {
			Version  string                     `json:"version"`
			Releases map[string]json.RawMessage `json:"releases"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", fmt.Errorf("moo.json 索引探测失败: %w", err)
	}
	moo, ok := doc.Apps["moo"]
	if !ok {
		return "", errors.New("moo.json 索引内无 moo 条目")
	}
	v := strings.TrimPrefix(strings.TrimSpace(moo.Version), "v")
	for k := range moo.Releases {
		kk := strings.TrimPrefix(strings.TrimSpace(k), "v")
		if v == "" || cmpVersions(kk, v) > 0 {
			v = kk
		}
	}
	if v == "" {
		return "", errors.New("moo.json 索引内无版本号")
	}
	return v, nil
}

// probeFnDepotLatest 兜底 4：读 FnDepot 公开源 moo.json（V2 结构）的
// apps.moo.version 字段。发版时与 GitHub release 同步更新（gen-repo-moo-json）。
func (s *Server) probeFnDepotLatest() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), selfDlTimeout)
	defer cancel()
	return s.fetchFnDepotVersion(ctx, selfUpdateFnDepotURL)
}

// fetchFnDepotVersion 从给定完整 URL（直连 raw 或 镜像前缀+raw）拉取 moo.json
// 并解析 apps.moo.version。探测链各层共用。
func (s *Server) fetchFnDepotVersion(ctx context.Context, fullURL string) (string, error) {
	c := netx.NewClient(selfDlTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return "", fmt.Errorf("FnDepot 源探测失败: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("FnDepot 源探测失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("FnDepot 源探测失败: HTTP %d", resp.StatusCode)
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if rerr != nil {
		return "", fmt.Errorf("FnDepot 源探测失败: %w", rerr)
	}
	var idx struct {
		Apps map[string]struct {
			Version string `json:"version"`
			// 0.6.325-C16：V2 源（FnDepot fnpack.json）把版本号放在 releases 键上，
			// 节点本身没有 version 字段；两种写法都接受，取最高版本。
			Releases map[string]json.RawMessage `json:"releases"`
		} `json:"apps"`
	}
	if uerr := json.Unmarshal(data, &idx); uerr != nil {
		return "", fmt.Errorf("FnDepot 源探测失败: %w", uerr)
	}
	entry := idx.Apps["moo"]
	tag := strings.TrimPrefix(strings.TrimSpace(entry.Version), "v")
	if tag == "" {
		for k := range entry.Releases {
			v := strings.TrimPrefix(strings.TrimSpace(k), "v")
			if v == "" {
				continue
			}
			if tag == "" || compareVersions(v, tag) > 0 {
				tag = v
			}
		}
	}
	if tag == "" {
		return "", errors.New("FnDepot 源无 moo 版本号")
	}
	return tag, nil
}

// 0.6.300：镜像兜底层用的 GitHub 加速前缀来源（httptest 可覆写）。
var selfUpdateMirrorPrefixes = defaultMirrorPrefixes

// defaultMirrorPrefixes 返回 GitHub 加速镜像 URL 前缀（含尾斜杠）：
// 有健康数据 = ok 镜像按吞吐降序；无数据 = 声明顺序静态回退（config 注释约定）。
// conversun 仅代理 conversun 仓库，拉 Blue-Mink 源恒 404，排除。
func defaultMirrorPrefixes(s *Server) []string {
	if s == nil || s.Mirrors == nil {
		return nil
	}
	stats := s.Mirrors.gh()
	var keys []string
	hasHealth := false
	for _, st := range stats {
		if st.Status != "" {
			hasHealth = true
			break
		}
	}
	if hasHealth {
		keys = topSpeedKeys(stats)
	} else {
		for _, opt := range config.GitHubMirrorOptions() {
			if opt.URL != "" {
				keys = append(keys, opt.Key)
			}
		}
	}
	var out []string
	for _, k := range keys {
		if k == "conversun" {
			continue
		}
		if p := mirrorURLByKey(k); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// probeFnDepotMirrorLatest 第四兜底：FnDepot moo.json 经用户配置的 GitHub
// 加速镜像拉取。取吞吐前 4 的健康镜像并发竞速，任一命中即返回。
func (s *Server) probeFnDepotMirrorLatest() (string, error) {
	prefixes := selfUpdateMirrorPrefixes(s)
	if len(prefixes) > 4 {
		prefixes = prefixes[:4]
	}
	if len(prefixes) == 0 {
		return "", errors.New("无可用加速镜像")
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done bool
		tag  string
		errs []string
	)
	ctx, cancel := context.WithTimeout(context.Background(), selfDlTimeout)
	defer cancel()
	for _, p := range prefixes {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			t, err := s.fetchFnDepotVersion(ctx, p+selfUpdateFnDepotURL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, p+" → "+err.Error())
				return
			}
			if !done {
				done = true
				tag = t
			}
		}(p)
	}
	wg.Wait()
	if done {
		return tag, nil
	}
	return "", errors.New(strings.Join(errs, " | "))
}

// isMooVersionTag 判断一个 tag（或去前缀后的版本串）是否是 Moo 自己的版本号：
// 去掉可选 `v` 前缀后必须是**纯数字点分**（0.6.539）。
//
// 为什么必须有（2026-11-08 事故）：Moo 与 Moo Mate 的 release 同放
// Blue-Mink/moo 一个仓，`/releases/latest` 是全仓唯一指针。mate 发版会把
// `mate-v0.2.55` 顶成 latest，旧逻辑只 TrimPrefix("v") 就采信，`splitVersion`
// 拆成 [0,2,55] 判「无更新」——且 HTTP 200 无 error，短路整条兜底链。
func isMooVersionTag(s string) bool {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return false
	}
	dots := 0
	for _, c := range s {
		switch {
		case c == '.':
			dots++
		case c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return dots > 0
}

// probeReleaseList 兜底 0：latest 指针被同仓其它应用的 release 抢占时，
// 直接列 releases 挑最高的 Moo 版本（只认 v<数字点分>、非 draft、非 prerelease）。
//
// 只在「API 200 但 tag 形态非法」时调用：既不会在 403 限流时多打一次 API，
// 也不依赖 latest 指针——即便 mate 永远占着 latest 也能拿到正确的 Moo 版本。
func (s *Server) probeReleaseList() (string, error) {
	u := selfUpdateAPIBase + "/repos/" + selfUpdateRepo + "/releases?per_page=10"
	resp, err := s.doGitHubGET(u)
	if err != nil {
		return "", fmt.Errorf("release 列表探测失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release 列表探测失败: HTTP %d", resp.StatusCode)
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if rerr != nil {
		return "", fmt.Errorf("release 列表探测失败: %w", rerr)
	}
	var rels []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if uerr := json.Unmarshal(data, &rels); uerr != nil {
		return "", fmt.Errorf("release 列表探测失败: %w", uerr)
	}
	best := ""
	for _, r := range rels {
		if r.Draft || r.Prerelease {
			continue
		}
		t := strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
		if !isMooVersionTag(t) {
			continue
		}
		if best == "" || cmpVersions(t, best) > 0 {
			best = t
		}
	}
	if best == "" {
		return "", errors.New("release 列表里没有可用的 Moo 版本 tag")
	}
	return best, nil
}

// cmpVersions 点分数字版本比较（返回 -1/0/1）。非数字段按 0 处理。
func cmpVersions(a, b string) int {
	na := splitVersion(strings.TrimPrefix(strings.TrimSpace(a), "v"))
	nb := splitVersion(strings.TrimPrefix(strings.TrimSpace(b), "v"))
	for i := 0; i < len(na) || i < len(nb); i++ {
		var x, y int
		if i < len(na) {
			x = na[i]
		}
		if i < len(nb) {
			y = nb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func splitVersion(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out
}

// selfUpdateCurrentVersion 当前版本（测试钩子可覆写）。
func (s *Server) selfUpdateCurrentVersion() string {
	if v := strings.TrimSpace(os.Getenv("MOO_SELFUPDATE_AS_VERSION")); v != "" {
		return v
	}
	return s.Version
}

// storeUpdateInfo GET /api/store-update：官方渠道版本探测。
// last_check / last_error 让「无新版本」与「探测失败」可区分（0.6.121）。
func (s *Server) storeUpdateInfo(w http.ResponseWriter, r *http.Request) {
	cur := s.selfUpdateCurrentVersion()
	out := map[string]any{"current_version": cur, "has_update": false}
	if !selfUpdateArchSupported() {
		writeJSON(w, out) // 0.6.355：amd64/arm64/arm 都提供自更新；其它架构无对应 FPK 资产
		return
	}
	// 0.6.299：?force=1 强制重探（设置页版本 chip 告警态的点击重试入口）
	force := r.URL.Query().Get("force") == "1"
	tag, assetURL, _, _, _, err := s.selfUpdProbe().probe(force)
	p := s.selfUpdProbe()
	p.mu.Lock()
	lastCheck := p.checkedAt
	lastErr := p.lastErr
	p.mu.Unlock()
	if !lastCheck.IsZero() {
		out["last_check"] = lastCheck.Unix()
	}
	if lastErr != nil {
		out["last_error"] = lastErr.Error()
	}
	if err != nil || tag == "" {
		writeJSON(w, out)
		return
	}
	if cmpVersions(tag, cur) > 0 {
		out["has_update"] = true
		out["available_version"] = tag
		_ = assetURL
		// 0.6.311：更新确认弹窗展示最新版本更新日志（release 正文；
		// 兜底通道无正文时字段缺省，前端不渲染该区块）。
		p.mu.Lock()
		latestBody := p.latestBody
		p.mu.Unlock()
		if latestBody != "" {
			out["latest_changelog"] = latestBody
		}
	}
	writeJSON(w, out)
}

// selfUpdateBusy 单飞保护。
var selfUpdateBusy int32

// storeUpdateSSE POST /api/store-update：应用内自更新（下载→校验→daemon 就地升级）。
func (s *Server) storeUpdateSSE(w http.ResponseWriter, r *http.Request) {
	f := sseStart(w)
	if !trySelfUpdateLock() {
		sseSend(w, f, map[string]any{"step": "error", "error": "已有自更新任务在进行中"})
		return
	}
	defer releaseSelfUpdateLock()

	// C2：失败事件（0.6.121）——每个错误出口都留痕到通知记录/推送渠道
	failNotify := func(step, msg string) {
		s.notifyEvent("store_update_error", "自更新失败",
			fmt.Sprintf("【%s】%s", step, msg), false)
	}

	ctx := r.Context()
	tag, assetURL, _, shaURL, minisigURL, err := s.selfUpdProbe().probe(true)
	if err != nil || tag == "" {
		failNotify("版本探测", "官方版本探测失败: "+err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": "官方版本探测失败，请稍后重试"})
		return
	}
	cur := s.selfUpdateCurrentVersion()
	if cmpVersions(tag, cur) <= 0 {
		sseSend(w, f, map[string]any{"step": "error", "error": "当前已是最新版本 v" + cur})
		return
	}
	if assetURL == "" {
		failNotify("版本探测", "官方 release 缺少 FPK 资产")
		sseSend(w, f, map[string]any{"step": "error", "error": "官方 release 缺少 FPK 资产"})
		return
	}

	sseSend(w, f, map[string]any{"step": "downloading", "message": "下载 v" + tag + " 安装包…", "progress": 0})
	fpkPath, err := s.downloadSelfUpdateFPK(w, f, ctx, assetURL, shaURL, minisigURL, tag)
	if err != nil {
		failNotify("下载", "下载 v"+tag+" 安装包失败: "+err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": "下载失败: " + err.Error()})
		return
	}
	defer os.Remove(fpkPath)

	sseSend(w, f, map[string]any{"step": "verifying", "message": "校验安装包…"})
	if _, tmpDir, err := extractSelfUpdateBinary(fpkPath); err != nil {
		os.RemoveAll(tmpDir)
		failNotify("校验", "安装包校验失败: "+err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": "安装包校验失败: " + err.Error()})
		return
	} else {
		os.RemoveAll(tmpDir)
	}

	if !daemonReachable() {
		failNotify("daemon 连接", "应用中心 daemon RPC 不可达")
		sseSend(w, f, map[string]any{
			"step":  "error",
			"error": "无法连接飞牛应用中心的升级服务，暂不能应用内更新。请改用飞牛应用中心「手动安装」上传 FPK 完成更新（切勿先卸载原应用）。",
		})
		return
	}

	sseSend(w, f, map[string]any{"step": "installing", "message": "提交飞牛应用中心就地升级…"})
	staged, err := stageSelfUpdateFpk(ctx, fpkPath)
	if err != nil {
		failNotify("暂存", err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	if staged.AppName != storeSelfAppName || !staged.Installed {
		failNotify("暂存", "应用中心未能确认就地升级身份（暂存 "+staged.AppName+"）")
		sseSend(w, f, map[string]any{"step": "error", "error": "应用中心未能确认对当前商店的就地升级（暂存身份 " + staged.AppName + "），为保护现有数据已中止"})
		return
	}
	if staged.Version != tag {
		failNotify("校验", fmt.Sprintf("安装包版本 %s 与 release %s 不一致", staged.Version, tag))
		sseSend(w, f, map[string]any{"step": "error", "error": fmt.Sprintf("安装包内版本（%s）与 release 版本（%s）不一致，为防降级已中止", staged.Version, tag)})
		return
	}

	var info selfUpdateInfo
	if err := daemonCall(ctx, "/rpc/v1/update/info", map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"language":      lang.From(ctx),
	}, &info); err != nil {
		failNotify("升级前检查", err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": "升级前检查失败: " + err.Error()})
		return
	}
	volume := info.WizardInfo.InstalledVolumeID
	if volume <= 0 {
		failNotify("升级前检查", "无法确定商店所在存储卷")
		sseSend(w, f, map[string]any{"step": "error", "error": "无法确定商店当前所在存储卷，为保护现有数据已中止升级"})
		return
	}

	// daemon 会在升级过程中杀掉本进程：先发事件并等 SSE 字节到达客户端
	// （conversun 实测 750ms 足够；之后由前端 pollForRestart 接管）。
	sseSend(w, f, map[string]any{"step": "self_update", "message": "商店正在重启…"})
	time.Sleep(selfFlushDelay)

	var task struct {
		TaskID string `json:"taskId"`
	}
	err = daemonCall(ctx, "/rpc/v1/update/task", map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   false, // daemon 升级后自动恢复原运行状态
		},
		"customParameters": []any{},
		"language":         lang.From(ctx),
	}, &task)
	if err != nil {
		failNotify("提交升级", err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": "提交升级失败: " + err.Error()})
		return
	}
	if task.TaskID == "" {
		failNotify("提交升级", "应用中心未返回任务 ID")
		sseSend(w, f, map[string]any{"step": "error", "error": "升级失败: 应用中心未返回任务 ID"})
		return
	}

	// C2：任务已被应用中心接受 = 提交点（常见路径 daemon 随后杀掉本进程，
	// 此后成功与否以重启后版本号为准；此处报「已提交」不报「已完成」）。
	s.notifyEvent("store_update_success", "自更新已提交",
		fmt.Sprintf("v%s 就地升级任务已提交应用中心（任务 %s），商店重启后生效", staged.Version, task.TaskID), true)

	// 若 daemon 没有杀掉本进程就走到了这里（少见），继续跟踪任务直到完成。
	if err := waitSelfUpdateTask(ctx, task.TaskID); err != nil {
		failNotify("等待升级", err.Error())
		sseSend(w, f, map[string]any{"step": "error", "error": err.Error()})
		return
	}
	sseSend(w, f, map[string]any{"step": "done", "message": "更新完成", "new_version": staged.Version})
}

// downloadSelfUpdateFPK 直连下载 FPK（0.6.207-panel D1：TLS 直连 only，
// 不经镜像候选），SSE 上报进度。minisigURL 非空（0.6.329 起 release 附
// .minisig）时下载后验签；shaURL 非空（release 附 .sha256）时下载后验哈希。
// 任一缺失或校验失败均 fail-closed 拒绝自更新。
// selfUpdateDownloadCandidate 一条下载通道：FPK 与两份侧车取自同一前缀，
// 保证镜像通道下「包 / 签名 / 哈希」不被拆到不同源（拆开也无用：私钥离线）。
type selfUpdateDownloadCandidate struct{ asset, sha, minisig string }

// selfUpdateMirrorRewrite 把 GitHub 域 URL 改写为加速镜像前缀；非 GitHub 域
// 原样返回，避免把无关地址拼到第三方前缀上。单测可覆写（httptest 不是 GitHub 域）。
var selfUpdateMirrorRewrite = func(prefix, u string) string {
	if prefix == "" || u == "" {
		return u
	}
	if strings.HasPrefix(u, "https://github.com/") || strings.HasPrefix(u, "https://raw.githubusercontent.com/") {
		return prefix + u
	}
	return u
}

// selfUpdateDownloadCandidates 构造下载候选：**加速镜像在前、直连最后**。
//
// 0.6.380（用户令「点更新用不了 GitHub 加速、进度很慢」）：旧顺序是直连在
// 首、失败才换镜像，而国内出口直连 objects.githubusercontent.com 常是
// 0 字节（实测 25s 一字节不下）→ 界面一直卡在「下载中」。现在镜像先试，
// 直连只作兜底。安全性不变：0.6.329 起强制 minisign 验签 + sha256（私钥
// 离线），镜像最多改动传输内容、换不了包。
func selfUpdateDownloadCandidates(s *Server, assetURL, shaURL, minisigURL string) []selfUpdateDownloadCandidate {
	out := make([]selfUpdateDownloadCandidate, 0, 4)
	for _, p := range selfUpdateMirrorPrefixes(s) {
		if len(out) >= 3 {
			break
		}
		a := selfUpdateMirrorRewrite(p, assetURL)
		if a == assetURL {
			continue
		}
		out = append(out, selfUpdateDownloadCandidate{
			asset:   a,
			sha:     selfUpdateMirrorRewrite(p, shaURL),
			minisig: selfUpdateMirrorRewrite(p, minisigURL),
		})
	}
	out = append(out, selfUpdateDownloadCandidate{asset: assetURL, sha: shaURL, minisig: minisigURL})
	return out
}

func (s *Server) downloadSelfUpdateFPK(w http.ResponseWriter, f http.Flusher, ctx context.Context, assetURL, shaURL, minisigURL, tag string) (string, error) {
	// 0.6.329 安全审计（自更新签名）：release 必须附 <资产名>.minisig 分离签名，
	// 缺失即拒绝（fail-closed）。.sha256 与 FPK 同源（无需私钥即可重算），防不了
	// release 本身投毒；只有 minisign 签名（私钥离线保管）能证明发布者身份。
	if minisigURL == "" {
		return "", fmt.Errorf("release v%s 未附 .minisig 签名资产，为安全起见拒绝自更新（请报源维护者）", tag)
	}
	if shaURL == "" {
		return "", fmt.Errorf("release v%s 未附 .sha256 校验资产，为安全起见拒绝自更新（请报源维护者）", tag)
	}
	client := netx.NewClient(8 * time.Minute)
	cands := selfUpdateDownloadCandidates(s, assetURL, shaURL, minisigURL)
	if len(cands) > 1 {
		sseSend(w, f, map[string]any{"step": "downloading", "message": "正在通过 GitHub 加速通道下载…"})
	}
	var fails []string
	for i, c := range cands {
		if i > 0 {
			sseSend(w, f, map[string]any{"step": "downloading",
				"message": fmt.Sprintf("当前通道未成功，切换备用通道重试（%d/%d）…", i+1, len(cands))})
		}
		dst, err := downloadSelfUpdateOne(w, f, ctx, client, c.asset, tag)
		if err != nil {
			fails = append(fails, err.Error())
			continue
		}
		if err := verifyMinisig(ctx, client, c.minisig, dst); err != nil {
			os.Remove(dst)
			fails = append(fails, err.Error())
			continue
		}
		if err := verifySha256(ctx, client, c.sha, dst); err != nil {
			os.Remove(dst)
			fails = append(fails, err.Error())
			continue
		}
		sseSend(w, f, map[string]any{"step": "downloading", "message": "v" + tag + " 校验通过（签名 + 哈希）"})
		return dst, nil
	}
	return "", errors.New("下载或校验失败：" + strings.Join(fails, "；"))
}

// downloadSelfUpdateOne 单通道下载 FPK 到临时文件（校验收在调用方做，便于
// 校验失败后换下一个通道）。返回可直接交给 verifyMinisig/verifySha256 的路径。
func downloadSelfUpdateOne(w http.ResponseWriter, f http.Flusher, ctx context.Context, client *http.Client, assetURL, tag string) (string, error) {
	dst := filepath.Join(os.TempDir(), fmt.Sprintf("moo-selfupdate-%d-%s.fpk", os.Getpid(), tag))
	// 0.6.380：首字节/停滞看护——直连常出现「TCP 连上但一个字节不吐」或
	// 「吐几百 KB 后掉到 0」，旧实现只有 8 分钟总超时，界面就一直卡在下载中。
	// 这里按「首字节 12s / 连续 20s 无进展」主动取消，交给上层切下一个通道。
	dctx, dcancel := context.WithCancel(ctx)
	defer dcancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(dst)
	if err != nil {
		resp.Body.Close()
		return "", err
	}
	total := resp.ContentLength
	var downloaded atomic.Int64
	var lastProg atomic.Int64
	lastProg.Store(time.Now().UnixNano())
	var stallMsg atomic.Value
	stopWatch := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopWatch:
				return
			case <-t.C:
			}
			got := downloaded.Load()
			idle := time.Since(time.Unix(0, lastProg.Load()))
			if got == 0 {
				if idle >= selfUpdateFirstByteTimeout {
					stallMsg.Store(fmt.Sprintf("首字节超时（%s 无数据）", idle.Round(time.Second)))
					dcancel()
					return
				}
				continue
			}
			if idle >= selfUpdateStallWindow {
				stallMsg.Store(fmt.Sprintf("下载停滞（%s 无进展，已下 %d KB）", idle.Round(time.Second), got>>10))
				dcancel()
				return
			}
		}
	}()
	defer close(stopWatch)
	lastEmit := time.Now()
	lastPct := -1
	buf := make([]byte, 256*1024)
	// 读循环：io.EOF 为正常结束（基线旧实现把 EOF 当错误 → 成功下载也
	// 落到「候选失败」，属潜在 bug，D1 重写时一并修正）。
	var rerr error
	for rerr == nil {
		n, err := resp.Body.Read(buf)
		rerr = err
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				resp.Body.Close()
				os.Remove(dst)
				return "", werr
			}
			dl := downloaded.Add(int64(n))
			lastProg.Store(time.Now().UnixNano())
			now := time.Now()
			if now.Sub(lastEmit) > 400*time.Millisecond {
				lastEmit = now
				ev := map[string]any{"step": "downloading", "message": "下载 v" + tag + " 安装包…", "downloaded": dl, "total": total}
				if total > 0 {
					pct := int(dl * 100 / total)
					ev["progress"] = pct
					lastPct = pct
				}
				sseSend(w, f, ev)
			}
		}
	}
	out.Close()
	resp.Body.Close()
	if rerr != nil && rerr != io.EOF {
		os.Remove(dst)
		if v := stallMsg.Load(); v != nil {
			if msg, ok := v.(string); ok && msg != "" {
				return "", errors.New(msg)
			}
		}
		return "", rerr
	}
	if total > 0 && lastPct < 100 {
		sseSend(w, f, map[string]any{"step": "downloading", "progress": 100, "downloaded": total, "total": total})
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() < 1<<20 {
		os.Remove(dst)
		return "", errors.New("安装包过小，可能损坏")
	}
	return dst, nil
}

// verifyMinisig 下载 .minisig 资产并用内嵌公钥（selfUpdatePubkey）验签 FPK。
// 0.6.329 起自更新的信任锚：签名由发布机离线私钥生成，内嵌公钥验签不联网取钥。
func verifyMinisig(ctx context.Context, client *http.Client, minisigURL, fpkPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, minisigURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("拉取 .minisig 失败: HTTP %d", resp.StatusCode)
	}
	sig, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(sig) == 0 {
		return errors.New(".minisig 内容为空")
	}
	// minisign 验签需整个文件内容（BLAKE2b 单遍哈希）；FPK ~5MB，内存可接受。
	msg, err := os.ReadFile(fpkPath)
	if err != nil {
		return err
	}
	if !minisign.Verify(selfUpdatePubkey, msg, sig) {
		return errors.New("自更新签名校验失败（签名无效或不匹配，已拒绝）")
	}
	return nil
}

// verifySha256 下载 .sha256 资产并与本地文件哈希比对。
// 资产内容格式：`<64 位 hex>[ 文件名]`（GitHub 惯例，取首字段）。
func verifySha256(ctx context.Context, client *http.Client, shaURL, fpkPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shaURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("拉取 .sha256 失败: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return err
	}
	fields := strings.Fields(strings.ToLower(string(data)))
	if len(fields) == 0 {
		return errors.New(".sha256 内容为空")
	}
	expected := fields[0]
	f, err := os.Open(fpkPath)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, cerr := io.Copy(h, f)
	f.Close()
	if cerr != nil {
		return cerr
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		if len(expected) > 12 {
			expected = expected[:12]
		}
		return fmt.Errorf("sha256 不匹配（期望 %s…，实际 %s…）", expected, actual[:12])
	}
	return nil
}

// extractSelfUpdateBinary 双层解包 FPK（gzip tar → app.tgz → gzip tar →
// moo-server），校验 gzip/ELF/体积。返回二进制路径与临时目录（调用方清理）。
func extractSelfUpdateBinary(fpkPath string) (string, string, error) {
	head, err := readHead(fpkPath, 2)
	if err != nil {
		return "", "", err
	}
	if head[0] != 0x1f || head[1] != 0x8b {
		return "", "", errors.New("不是 gzip 压缩包")
	}
	tmpDir, err := os.MkdirTemp("", "moo-selfupd-")
	if err != nil {
		return "", "", err
	}
	appTgz, err := readTarMember(fpkPath, "app.tgz")
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", "", fmt.Errorf("FPK 中缺少 app.tgz: %w", err)
	}
	outPath := filepath.Join(tmpDir, "moo-server")
	if err := extractTarMemberTo(appTgz, "moo-server", outPath); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", err
	}
	fi, err := os.Stat(outPath)
	if err != nil || fi.Size() < 5<<20 {
		os.RemoveAll(tmpDir)
		return "", "", errors.New("moo-server 二进制缺失或过小")
	}
	elf, err := readHead(outPath, 4)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", "", err
	}
	if elf[0] != 0x7f || elf[1] != 'E' || elf[2] != 'L' || elf[3] != 'F' {
		os.RemoveAll(tmpDir)
		return "", "", errors.New("moo-server 不是 ELF 可执行文件")
	}
	return outPath, tmpDir, nil
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readTarMember 从 gzip tar 中读出名为 base 的成员（按文件名匹配，忽略路径前缀）。
func readTarMember(fpkPath, base string) ([]byte, error) {
	f, err := os.Open(fpkPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == base {
			return io.ReadAll(tr)
		}
	}
	return nil, errors.New("成员不存在")
}

func extractTarMemberTo(tgz []byte, base, outPath string) error {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == base {
			out, err := os.Create(outPath)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			return err
		}
	}
	return errors.New("成员不存在")
}

// ---------- 飞牛 appcenter daemon RPC（就地升级通道） ----------
//
// 设计移植自 conversun/fnos-store 的自更新实现：不走「卸载后重装」
// （fn-knock 早期脚本方案；在部分 fnOS 版本上该路径会丢失应用与数据），
// 而是驱动 appcenter daemon 自身的升级操作——停应用、就地换包、
// @appdata 保留、恢复原运行状态。daemon 是独立 root 进程，本进程被
// 杀掉后任务照常完成。

const (
	daemonStatusRunning = 1
	daemonStatusSuccess = 2
	daemonStatusUnknown = 5
)

type daemonEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func newDaemonClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", selfUpdateSocketPath)
			},
		},
	}
}

// daemonCall POST JSON 到 daemon unix socket，code!=0 为错误，data 解入 out。
func daemonCall(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := newDaemonClient(60 * time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("应用中心服务不可用: %v", err)
	}
	defer resp.Body.Close()
	var env daemonEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("应用中心响应解析失败: %v", err)
	}
	if env.Code != 0 {
		return fmt.Errorf("应用中心错误 %d: %s", env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("应用中心数据解析失败: %v", err)
		}
	}
	return nil
}

// daemonReachable 运行时探测升级通道（conversun 的做法：探通道而非查版本
// 号——能连上 daemon 升级通道，就地升级在任何 fnOS 版本上都是安全的）。
func daemonReachable() bool {
	resp, err := newDaemonClient(2*time.Second).Post(
		"http://localhost/rpc/v1/common/status", "application/json",
		strings.NewReader(`{"taskId":"moo-selfupdate-probe"}`))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

type stagedSelfPackage struct {
	AppName     string `json:"appName"`
	Version     string `json:"version"`
	PackageType string `json:"packageType"`
	Installed   bool   `json:"installed"`
}

// stageSelfUpdateFpk 把 FPK 交给 daemon 解包识别（register 升级素材）。
func stageSelfUpdateFpk(ctx context.Context, fpkPath string) (*stagedSelfPackage, error) {
	var task struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := daemonCall(ctx, "/rpc/v1/download/task", map[string]any{
		"packageSourceType": "file",
		"path":              fpkPath,
	}, &task); err != nil {
		return nil, fmt.Errorf("暂存安装包失败: %v", err)
	}
	if task.DownloadTaskID == "" {
		return nil, errors.New("暂存安装包失败: 应用中心未返回任务 ID")
	}
	deadline := time.Now().Add(selfStageDeadline)
	for time.Now().Before(deadline) {
		var st struct {
			Status      int    `json:"status"`
			Message     string `json:"message"`
			AppName     string `json:"appName"`
			Version     string `json:"version"`
			PackageType string `json:"packageType"`
			Installed   bool   `json:"installed"`
		}
		if err := daemonCall(ctx, "/rpc/v1/download/status", map[string]any{"downloadTaskId": task.DownloadTaskID}, &st); err != nil {
			return nil, fmt.Errorf("查询暂存状态失败: %v", err)
		}
		switch st.Status {
		case daemonStatusSuccess:
			if st.AppName == "" || st.Version == "" {
				return nil, errors.New("暂存完成，但应用中心未能识别安装包内容")
			}
			return &stagedSelfPackage{AppName: st.AppName, Version: st.Version, PackageType: st.PackageType, Installed: st.Installed}, nil
		case daemonStatusRunning:
			// 继续轮询
		default:
			return nil, fmt.Errorf("暂存安装包失败: 状态 %d %s", st.Status, st.Message)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, errors.New("暂存安装包超时")
}

type selfUpdateInfo struct {
	WizardInfo struct {
		InstalledVolumeID int `json:"installedVolumeID"`
	} `json:"wizardInfo"`
}

// waitSelfUpdateTask 轮询升级任务直到终态。读状态失败只说明读不到
// （daemon 在高负载下会瞬时失败），不能当作任务失败——继续轮询。
func waitSelfUpdateTask(ctx context.Context, taskID string) error {
	deadline := time.Now().Add(selfTaskDeadline)
	for time.Now().Before(deadline) {
		var st struct {
			Status     int    `json:"status"`
			Message    string `json:"message"`
			OutputText string `json:"outputText"`
		}
		if err := daemonCall(ctx, "/rpc/v1/common/status", map[string]any{"taskId": taskID}, &st); err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(selfStatusPoll):
			}
			continue
		}
		switch st.Status {
		case daemonStatusSuccess:
			return nil
		case daemonStatusRunning:
			// 继续轮询
		case daemonStatusUnknown:
			// daemon 不再持有该任务：通常意味着升级已走到杀进程阶段、
			// 前端正在轮询重启；若本进程还活着，按「已移交」处理而非失败
			//（conversun 同口径：结果未知时绝不自动重试、绝不误报失败）。
			return nil
		default:
			detail := st.Message
			if detail == "" {
				detail = st.OutputText
			}
			return fmt.Errorf("升级失败: 状态 %d %s", st.Status, detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(selfStatusPoll):
		}
	}
	return errors.New("等待升级任务超时")
}

func trySelfUpdateLock() bool { return atomic.CompareAndSwapInt32(&selfUpdateBusy, 0, 1) }
func releaseSelfUpdateLock()  { atomic.StoreInt32(&selfUpdateBusy, 0) }
