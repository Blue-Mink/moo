package main

// moo-mate 应用内自更新：minisign 验签 + sha256 双校验（均 fail-closed）
// + 飞牛 appcenter daemon 就地升级。
//
// 0.2.35 起与 Moo 主程序同规则：release 必须同时提供
// `<fpk>` + `<fpk>.sha256` + `<fpk>.minisig` 三件套。
//   - 验签验的是**内嵌在二进制里的公钥**（go:embed，私钥离线保管），
//     换仓库/换 release 资产都动不了它 —— 这是唯一的信任锚；
//   - sha256 只证明「与索引登记的一致」，可被同时篡改（sha 与包同仓），
//     所以单独用它挡不住恶意替换，只能防传输损坏，必须与验签并用；
//   - 缺任一件一律拒绝升级（fail-closed），不做「老客户端放行」的兼容开关。
//
// 流程：探测最新 mate-v* 版本 → 下载架构匹配的 FPK → 比对 .sha256 →
// 用内嵌公钥验 .minisig → 交给 /var/run/com.trim.app.center.sock 的
// appcenter daemon 就地升级
// （download/task → download/status → update/info → update/task → common/status）。
// daemon 是独立 root 进程，升级过程中会杀掉 mate 自身进程，前端靠重连兜底。

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aead.dev/minisign"
)

// mateSelfUpdatePubkeyText 自更新签名公钥（minisign）：go:embed 内嵌，
// 验签不联网取公钥（否则攻击者可连公钥一起替换）。
// 私钥离线保管，`.minisig` 由发布机离线生成，见 AGENTS.md「Moo 自更新签名
// 发布约束」。
//
// 当前复用 Moo 本体同一把密钥（同一发布者 / 同一发布机）。若日后要拆成
// mate 专属密钥：替换本文件内容 + 用新私钥重签发布包即可，代码无需改动。
//
//go:embed selfupdate_pubkey.txt
var mateSelfUpdatePubkeyText string

var mateSelfUpdatePubkey = func() minisign.PublicKey {
	var pk minisign.PublicKey
	if err := pk.UnmarshalText([]byte(strings.TrimSpace(mateSelfUpdatePubkeyText))); err != nil {
		panic("invalid embedded minisign public key: " + err.Error())
	}
	return pk
}()

// matePubkeyOverride 仅供单测注入临时密钥（生产恒 nil）。
var matePubkeyOverride *minisign.PublicKey

// matePubkeyForVerify 返回验签用公钥（单测可覆写）。
func matePubkeyForVerify() minisign.PublicKey {
	if matePubkeyOverride != nil {
		return *matePubkeyOverride
	}
	return mateSelfUpdatePubkey
}

// verifyMateSignature 用内嵌公钥校验 path 的 minisign 分离签名（fail-closed）。
// 任何异常（空签名 / 验签不过）都返回错误，调用方必须据此拒绝升级。
func verifyMateSignature(pubkey minisign.PublicKey, path string, sig []byte) error {
	if len(sig) == 0 {
		return errors.New(".minisig 内容为空，为安全起见拒绝自更新")
	}
	// minisign 验签需整个文件内容（BLAKE2b 单遍哈希）；FPK ~4MB，内存可接受。
	msg, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取安装包失败：%v", err)
	}
	if !minisign.Verify(pubkey, msg, sig) {
		return errors.New("自更新签名校验失败（签名无效或与安装包不匹配），已拒绝升级")
	}
	return nil
}

// mateSelfUpdateSocket appcenter daemon 的 unix socket。
// var 不是 const —— 单测要覆写成临时 socket。
var mateSelfUpdateSocket = "/var/run/com.trim.app.center.sock"

// mateAppName 本应用在 appcenter 里的 appName（安装目录 /vol1/@appcenter/moo-mate）。
const mateAppName = "moo-mate"

// 下载上限：FPK 约 3.5MB，留足余量防被灌爆磁盘。
const (
	mateSelfUpdateMaxFpk  = 200 << 20 // 200MB
	mateSelfUpdateMaxSide = 4 << 10   // 4KB
	mateSelfUpdateDLTime  = 5 * time.Minute
)

// 0.2.35（用户令：复用 Moo 的 GitHub 加速）：下载阶段通道级看护——
// 国内直连 github.com / objects CDN 常是「连上不吐字节」，旧实现只有
// 5 分钟总超时 → 进度条一直不动。首字节 12s / 连续 20s 无进展即换通道。
// var 不是 const —— 单测压到百毫秒级。
var (
	mateFirstByteTimeout = 12 * time.Second
	mateStallWindow      = 20 * time.Second
)

// mateMirrorProvider 由 main 注入：返回 Moo 侧健康 GitHub 加速前缀（含尾斜杠，
// 按吞吐降序）。Moo 不可达时返回 nil，调用方回落 githubMirrorPrefixes。
// var 不是 const —— 单测覆写成 httptest 前缀。
var mateMirrorProvider = func() []string { return nil }

// daemon 状态码（与 Moo 一致）
const (
	mateDaemonOK      = 2
	mateDaemonRunning = 1
	mateDaemonUnknown = 5
)

// mateSelfUpdateState 状态机（全局单例，mutex 保护）。
type mateSelfUpdateState struct {
	Stage         string `json:"stage"`
	Message       string `json:"message"`
	Err           string `json:"error,omitempty"`
	Percent       int    `json:"percent"`
	TargetVersion string `json:"target_version"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	Running       bool   `json:"running"`
}

var mateSelfUpdate struct {
	mu  sync.Mutex
	cur mateSelfUpdateState
}

// mateSelfUpdateStatus 返回状态机快照（供 API 读取）。
func mateSelfUpdateStatus() mateSelfUpdateState {
	mateSelfUpdate.mu.Lock()
	defer mateSelfUpdate.mu.Unlock()
	return mateSelfUpdate.cur
}

// setStage 更新状态机字段。
func setStage(stage, message string, percent int, errMsg string, running bool) {
	mateSelfUpdate.mu.Lock()
	defer mateSelfUpdate.mu.Unlock()
	mateSelfUpdate.cur.Stage = stage
	mateSelfUpdate.cur.Message = message
	mateSelfUpdate.cur.Percent = percent
	mateSelfUpdate.cur.Running = running
	if errMsg != "" {
		mateSelfUpdate.cur.Err = errMsg
	}
	if stage == "done" || stage == "failed" {
		mateSelfUpdate.cur.FinishedAt = time.Now().Format(time.RFC3339)
	}
}

// mateArchSuffix 按运行时架构返回 FPK 资产后缀；不支持时 ok=false。
func mateArchSuffix(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "_x86.fpk", true
	case "arm64":
		return "_arm.fpk", true
	default:
		return "", false
	}
}

// mateFallbackAssetURL 按发布约定拼 release 资产直链。
// API 配额耗尽（国内出口常见 remaining=0）时，这条通道仍能下载。
func mateFallbackAssetURL(tag, name string) string {
	return "https://github.com/Blue-Mink/moo/releases/download/mate-v" + tag + "/" + name
}

// mateSelfUpdateAssetURLs 选本架构的三件套：FPK / .sha256 / .minisig。
// 优先用 release 资产清单（更准）；清单取不到、版本对不上或缺本架构时，
// 回落按发布约定拼 URL —— 两条路径都必须同时给出三个 URL。
//
// 版本必须核对：api / 镜像 / atom 三条探测通道会短暂不同步，资产清单里的
// 「最高 release」可能不是本次要装的版本。若拿别的版本的资产 URL 去下载，
// 会被 sha256/验签挡下（不至于装错），但用户看到的是「更新失败」；所以版本
// 不符时直接用 target 的约定 URL，三条通道都按同一个 tag 拼。
func mateSelfUpdateAssetURLs(tag, goarch string) (fpkURL, shaURL, sigURL string, err error) {
	suffix, ok := mateArchSuffix(goarch)
	if !ok {
		return "", "", "", fmt.Errorf("当前平台（%s）不支持应用内自更新，请到 GitHub Release 手动下载", goarch)
	}
	if assets, ver, aerr := mateReleaseAssets(); aerr == nil && ver == tag {
		if u, s, g, perr := pickMateSelfUpdateAsset(assets, goarch); perr == nil {
			return u, s, g, nil
		}
	}
	name := "moo-mate_" + tag + suffix
	return mateFallbackAssetURL(tag, name),
		mateFallbackAssetURL(tag, name+".sha256"),
		mateFallbackAssetURL(tag, name+".minisig"),
		nil
}

// pickMateSelfUpdateAsset 从 release 资产里挑出本架构 FPK 及其
// .sha256 / .minisig 侧车。返回的 shaURL / sigURL 为空表示缺侧车
// （调用方必须拒绝升级）。
func pickMateSelfUpdateAsset(assets []mateReleaseAsset, goarch string) (fpkURL, shaURL, sigURL string, err error) {
	suffix, ok := mateArchSuffix(goarch)
	if !ok {
		return "", "", "", fmt.Errorf("当前平台（%s）不支持应用内自更新，请到 GitHub Release 手动下载", goarch)
	}
	want := ""
	for _, a := range assets {
		if strings.HasSuffix(a.Name, suffix) {
			fpkURL = a.URL
			want = a.Name
			break
		}
	}
	if fpkURL == "" {
		return "", "", "", fmt.Errorf("release 资产里没有 %s 包（当前平台不支持自更新）", suffix)
	}
	for _, a := range assets {
		switch a.Name {
		case want + ".sha256":
			shaURL = a.URL
		case want + ".minisig":
			sigURL = a.URL
		}
	}
	return fpkURL, shaURL, sigURL, nil
}

// parseSha256Sidecar 从 .sha256 侧车里取出前 64 位十六进制摘要。
// 兼容 "sha  filename" / 裸 sha / CRLF，其余一律视为非法。
func parseSha256Sidecar(b []byte) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		cand := strings.ToLower(fields[0])
		if len(cand) != 64 {
			return "", fmt.Errorf("sha256 侧车格式非法（摘要长度 %d）", len(cand))
		}
		if _, err := hex.DecodeString(cand); err != nil {
			return "", fmt.Errorf("sha256 侧车不是合法十六进制：%v", err)
		}
		return cand, nil
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("读取 sha256 侧车失败：%v", err)
	}
	return "", errors.New("sha256 侧车为空")
}

// fetchLimited 下载 url 到 dst，最多 limit 字节。
func fetchLimited(client *http.Client, url, dst string, limit int64) (int64, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "moo-mate-selfupdate")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	var (
		got    atomic.Int64
		lastAt atomic.Int64
		stall  atomic.Value
	)
	lastAt.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			g := got.Load()
			idle := time.Since(time.Unix(0, lastAt.Load()))
			if g == 0 {
				if idle >= mateFirstByteTimeout {
					stall.Store(fmt.Sprintf("首字节超时（%s 无数据）", idle.Round(time.Second)))
					cancel()
					return
				}
				continue
			}
			if idle >= mateStallWindow {
				stall.Store(fmt.Sprintf("下载停滞（%s 无进展，已下 %d KB）", idle.Round(time.Second), g>>10))
				cancel()
				return
			}
		}
	}()
	buf := make([]byte, 256<<10)
	var rerr error
	for rerr == nil {
		var rn int
		rn, rerr = resp.Body.Read(buf)
		if rn > 0 {
			if _, werr := f.Write(buf[:rn]); werr != nil {
				f.Close()
				os.Remove(dst)
				return got.Load(), werr
			}
			// 记账（单写者；看护 goroutine 只读）
			got.Add(int64(rn))
			lastAt.Store(time.Now().UnixNano())
			if got.Load() > limit {
				break
			}
		}
	}
	cerr := f.Close()
	if rerr != nil && rerr != io.EOF {
		os.Remove(dst)
		if v := stall.Load(); v != nil {
			if msg, ok := v.(string); ok && msg != "" {
				return got.Load(), errors.New(msg)
			}
		}
		return got.Load(), rerr
	}
	if cerr != nil {
		return got.Load(), cerr
	}
	if got.Load() > limit {
		os.Remove(dst)
		return got.Load(), fmt.Errorf("下载内容超过上限 %d 字节", limit)
	}
	return got.Load(), nil
}

// mateDownloadCandidates 直链 → 下载候选：**镜像前缀在前、直连最后**。
// 镜像表优先取 Moo 侧健康表（mateMirrorProvider），取不到回落硬编码表。
// 非 GitHub 地址只留原 URL。
func mateDownloadCandidates(raw string) []string {
	// 只改写真正的下载域；API 域（api.github.com/repos/…/assets/123）不加前缀
	// ——那需要鉴权头与 Accept，镜像代理不了。
	if !strings.HasPrefix(raw, "https://github.com/") && !strings.HasPrefix(raw, "https://raw.githubusercontent.com/") {
		return []string{raw}
	}
	prefixes := mateMirrorProvider()
	if len(prefixes) == 0 {
		prefixes = githubMirrorPrefixes
	}
	out := make([]string, 0, len(prefixes)+1)
	seen := map[string]bool{}
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		u := strings.TrimSuffix(p, "/") + "/" + raw
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	out = append(out, raw)
	return out
}

// fetchAnyMirror 依次尝试各候选通道下载，任一成功即返回（校验在调用方做）。
// onTry 用于把「正在试第几个通道」写进进度文案。
func fetchAnyMirror(client *http.Client, raw, dst string, limit int64, onTry func(i, total int)) (int64, error) {
	cands := mateDownloadCandidates(raw)
	var fails []string
	for i, c := range cands {
		if onTry != nil {
			onTry(i+1, len(cands))
		}
		n, err := fetchLimited(client, c, dst, limit)
		if err == nil {
			return n, nil
		}
		fails = append(fails, fmt.Sprintf("%s: %v", hostFromURL(c), err))
		os.Remove(dst)
	}
	return 0, fmt.Errorf("所有下载通道均失败：%s", strings.Join(fails, "；"))
}

// fileSha256 实算本地文件摘要。
func fileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- appcenter daemon RPC ----

// mateDaemonEnvelope daemon 统一信封：code!=0 即错误。
type mateDaemonEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// mateDaemonClient 走 unix socket 的极简 JSON POST 客户端。
type mateDaemonClient struct {
	hc     *http.Client
	socket string
}

func newMateDaemonClient(socket string) *mateDaemonClient {
	return &mateDaemonClient{
		socket: socket,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// call 发一次 RPC，成功时把 data 反序列化进 out。
func (c *mateDaemonClient) call(path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, "http://localhost"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("连接应用中心失败：%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("读取应用中心响应失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("应用中心返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var env mateDaemonEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("应用中心响应解析失败：%v", err)
	}
	if env.Code != 0 {
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = fmt.Sprintf("code=%d", env.Code)
		}
		return fmt.Errorf("应用中心拒绝：%s", msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("应用中心数据解析失败：%v", err)
		}
	}
	return nil
}

// mateDownloadStatus download/status 的返回。
// mateUpdateInfoResp 是 /rpc/v1/update/info 的响应（只需所在存储卷）。
type mateUpdateInfoResp struct {
	WizardInfo struct {
		InstalledVolumeID int `json:"installedVolumeID"`
	} `json:"wizardInfo"`
}

type mateDownloadStatus struct {
	Status      int    `json:"status"`
	AppName     string `json:"appName"`
	Version     string `json:"version"`
	PackageType string `json:"packageType"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path"`
}

// mateUpgradeParams 升级参数。
type mateUpgradeParams struct {
	AppName      string
	Version      string
	PackageType  string
	WizardInfo   json.RawMessage
	Language     string
	StageTimeout time.Duration
	TaskTimeout  time.Duration
	PollInterval time.Duration
	DownloadWait time.Duration
}

// mateUpgrade 执行 daemon 4 步就地升级。logf 用于记录非致命信息。
func mateUpgrade(c *mateDaemonClient, fpk string, p mateUpgradeParams, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	// 1) download/task：把本地 FPK 交给 daemon
	var dl struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := c.call("/rpc/v1/download/task", map[string]any{
		"packageSourceType": "file",
		"path":              fpk,
	}, &dl); err != nil {
		return fmt.Errorf("提交换包任务失败：%w", err)
	}
	if dl.DownloadTaskID == "" {
		return errors.New("应用中心未返回下载任务 ID")
	}

	// 2) download/status：轮询到解析完成
	deadline := time.Now().Add(p.DownloadWait)
	var st mateDownloadStatus
	for {
		st = mateDownloadStatus{}
		if err := c.call("/rpc/v1/download/status", map[string]any{
			"downloadTaskId": dl.DownloadTaskID,
		}, &st); err != nil {
			return fmt.Errorf("查询换包状态失败：%w", err)
		}
		if st.Status == mateDaemonOK {
			break
		}
		if st.Status != mateDaemonRunning && st.Status != 0 {
			// status 其它取值（如 5=未知任务）视为失败
			return fmt.Errorf("应用中心未能解析安装包（status=%d）", st.Status)
		}
		if time.Now().After(deadline) {
			return errors.New("等待应用中心解析安装包超时")
		}
		time.Sleep(p.PollInterval)
	}

	// 身份/版本核对（对齐 Moo）：防换错包、防降级、防「装成新装」
	if st.AppName != "" && st.AppName != p.AppName {
		return fmt.Errorf("应用中心解析出的应用是 %s，不是 %s，已中止", st.AppName, p.AppName)
	}
	if !st.Installed {
		return errors.New("应用中心未把该包识别为对已安装应用的就地升级，为保护数据已中止")
	}
	if st.Version != "" && p.Version != "" && st.Version != p.Version {
		return fmt.Errorf("安装包内版本 %s 与目标版本 %s 不一致，已中止（防降级）", st.Version, p.Version)
	}
	if st.PackageType != "" {
		// packageType 以 daemon 解析结果为准（native/docker 等由包本身决定）
		p.PackageType = st.PackageType
	}

	// 3) update/info：必须拿到所在存储卷，否则拒绝升级（对齐 Moo：数据保护）
	var info mateUpdateInfoResp
	if err := c.call("/rpc/v1/update/info", map[string]any{
		"appName":       p.AppName,
		"updateVersion": p.Version,
		"packageType":   p.PackageType,
		"language":      p.Language,
	}, &info); err != nil {
		return fmt.Errorf("升级前检查失败：%w", err)
	}
	volume := info.WizardInfo.InstalledVolumeID
	if volume <= 0 {
		return errors.New("无法确定应用所在存储卷，为保护现有数据已中止升级")
	}
	logf("就地升级：vol=%d pkgType=%s ver=%s", volume, p.PackageType, p.Version)

	// 4) update/task：真正升级（本进程随后会被 daemon 杀掉）
	sys := map[string]any{
		"agreedToProtocol": true,
		"installVolumeID":  volume,
		"dataVolumeId":     volume,
		"immediateStart":   false, // daemon 升级后自动恢复原运行状态
	}
	cus := []any{}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := c.call("/rpc/v1/update/task", map[string]any{
		"appName":          p.AppName,
		"updateVersion":    p.Version,
		"packageType":      p.PackageType,
		"systemParameters": sys,
		"customParameters": cus,
		"language":         p.Language,
	}, &task); err != nil {
		return fmt.Errorf("提交升级任务失败：%w", err)
	}
	if task.TaskID == "" {
		return errors.New("应用中心未返回升级任务 ID")
	}

	// 5) common/status：等到成功（进程通常在此之前已被杀，前端接管）
	deadline = time.Now().Add(p.TaskTimeout)
	for {
		var s struct {
			Status     int    `json:"status"`
			Message    string `json:"message"`
			OutputText string `json:"outputText"`
		}
		if err := c.call("/rpc/v1/common/status", map[string]any{
			"taskId": task.TaskID,
		}, &s); err != nil {
			return fmt.Errorf("查询升级进度失败：%w", err)
		}
		switch s.Status {
		case mateDaemonOK:
			return nil
		case mateDaemonRunning, 0:
			// 继续轮询
		case mateDaemonUnknown:
			return errors.New("应用中心任务已丢失（可能已完成安装）")
		default:
			msg := strings.TrimSpace(s.Message)
			if msg == "" {
				msg = strings.TrimSpace(s.OutputText)
			}
			if msg == "" {
				msg = fmt.Sprintf("status=%d", s.Status)
			}
			return fmt.Errorf("升级失败：%s", msg)
		}
		if time.Now().After(deadline) {
			return errors.New("等待应用中心完成安装超时")
		}
		time.Sleep(p.PollInterval)
	}
}

// mateSelfUpdateHook 允许测试注入 daemon 参数/客户端（生产为 nil 即默认）。
var mateSelfUpdateHook func()

func defaultMateUpgradeParams(appName, ver, pkgType string) mateUpgradeParams {
	if pkgType == "" {
		pkgType = "native"
	}
	return mateUpgradeParams{
		AppName:      appName,
		Version:      ver,
		PackageType:  pkgType,
		Language:     "zh-CN",
		StageTimeout: 5 * time.Minute,
		TaskTimeout:  10 * time.Minute,
		PollInterval: 2 * time.Second,
		DownloadWait: 3 * time.Minute,
	}
}

// runMateSelfUpdate 下载三件套、双校验并把 FPK 交给 daemon。
// fpkURL/shaURL/sigURL 由调用方按架构挑好；任一侧车 URL 为空 → 拒绝升级。
func runMateSelfUpdate(fpkURL, shaURL, sigURL, targetVersion, pkgType string) error {
	tmp, err := os.MkdirTemp("", "moo-mate-update-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败：%v", err)
	}
	// 默认清理；成功时把 success 置真，交 /tmp 回收（daemon 可能还在读）
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tmp)
		}
	}()

	if shaURL == "" {
		return errors.New("release 未附 .sha256 校验文件，为安全起见拒绝自更新（请到 GitHub Release 手动下载）")
	}
	if sigURL == "" {
		return errors.New("release 未附 .minisig 签名文件，为安全起见拒绝自更新（请到 GitHub Release 手动下载）")
	}

	client := mateExternalClient(mateSelfUpdateDLTime)
	fpkPath := filepath.Join(tmp, "moo-mate.fpk")

	setStage("downloading", "正在下载安装包 0%", 0, "", true)
	if _, err := fetchAnyMirror(client, fpkURL, fpkPath, mateSelfUpdateMaxFpk, func(i, n int) {
		if n > 1 {
			setStage("downloading", fmt.Sprintf("正在下载安装包（通道 %d/%d）", i, n), 0, "", true)
		}
	}); err != nil {
		return err
	}

	setStage("downloading", "正在下载校验文件", 90, "", true)
	shaPath := filepath.Join(tmp, "moo-mate.fpk.sha256")
	if _, err := fetchAnyMirror(client, shaURL, shaPath, mateSelfUpdateMaxSide, nil); err != nil {
		return fmt.Errorf("下载 sha256 校验文件失败：%w", err)
	}
	sigPath := filepath.Join(tmp, "moo-mate.fpk.minisig")
	if _, err := fetchAnyMirror(client, sigURL, sigPath, mateSelfUpdateMaxSide, nil); err != nil {
		return fmt.Errorf("下载签名文件失败：%w", err)
	}

	// ---- sha256 fail-closed ----
	setStage("verifying", "正在校验安装包完整性", 95, "", true)
	wantRaw, err := os.ReadFile(shaPath)
	if err != nil {
		return fmt.Errorf("读取 sha256 校验文件失败：%v", err)
	}
	want, err := parseSha256Sidecar(wantRaw)
	if err != nil {
		return fmt.Errorf("校验文件不可用，拒绝升级：%v", err)
	}
	got, err := fileSha256(fpkPath)
	if err != nil {
		return fmt.Errorf("计算安装包摘要失败：%v", err)
	}
	if got != want {
		return fmt.Errorf("安装包校验失败（期望 %s… 实际 %s…），已拒绝升级", want[:12], got[:12])
	}

	// ---- minisign 验签 fail-closed ----
	// 这是唯一能证明「包是本发布者签的」的一环：sha256 侧车与包同仓，
	// 两者一起被替换时 sha 校验看不出来，只有内嵌公钥验签能挡住。
	setStage("verifying", "正在校验安装包签名", 96, "", true)
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("读取签名文件失败，拒绝升级：%v", err)
	}
	if err := verifyMateSignature(matePubkeyForVerify(), fpkPath, sig); err != nil {
		return err
	}

	// ---- daemon 就地升级 ----
	setStage("staging", "安装包校验通过，准备安装", 100, "", true)
	params := defaultMateUpgradeParams(mateAppName, targetVersion, pkgType)
	if mateSelfUpdateHook != nil {
		mateSelfUpdateHook()
	}
	c := newMateDaemonClient(mateSelfUpdateSocket)
	setStage("upgrading", "应用中心正在安装…", 100, "", true)
	if err := mateUpgrade(c, fpkPath, params, func(f string, a ...any) {
		logf(f, a...)
	}); err != nil {
		return err
	}
	success = true
	return nil
}

// mateProbeFn 版本探测（单测可覆写，避免真实网络）。
var mateProbeFn = probeMateUpdateCached

// startMateSelfUpdate 单飞启动后台自更新 goroutine。
func startMateSelfUpdate() (string, error) {
	mateSelfUpdate.mu.Lock()
	if mateSelfUpdate.cur.Running {
		v := mateSelfUpdate.cur.TargetVersion
		mateSelfUpdate.mu.Unlock()
		return v, errors.New("已有更新任务在进行中，请稍候")
	}
	// 立即占位：检查与置位必须同一把锁内完成，否则并发点击都能通过检查
	//（中间隔着 6~8s 的网络探测）→ 会起两个升级任务。
	mateSelfUpdate.cur = mateSelfUpdateState{
		Stage:     "checking",
		Message:   "正在检查新版本…",
		StartedAt: time.Now().Format(time.RFC3339),
		Running:   true,
	}
	mateSelfUpdate.mu.Unlock()

	fail := func(err error) (string, error) {
		setStage("failed", "更新失败", 0, err.Error(), false)
		return "", err
	}

	info, err := mateProbeFn()
	if err != nil {
		return fail(fmt.Errorf("检查新版本失败：%w", err))
	}
	if !info.HasUpdate || strings.TrimSpace(info.AvailableVersion) == "" {
		return fail(errors.New("当前已是最新版本"))
	}
	target := strings.TrimSpace(info.AvailableVersion)

	fpkURL, shaURL, sigURL, err := mateSelfUpdateAssetURLs(target, runtime.GOARCH)
	if err != nil {
		return fail(err)
	}

	setStage("downloading", "正在准备下载…", 0, "", true)
	mateSelfUpdate.mu.Lock()
	mateSelfUpdate.cur.TargetVersion = target
	mateSelfUpdate.mu.Unlock()

	go func() {
		err := runMateSelfUpdate(fpkURL, shaURL, sigURL, target, "")
		if err != nil {
			setStage("failed", "更新失败", 0, err.Error(), false)
			logf("应用内自更新失败：%v", err)
			return
		}
		setStage("done", "已更新到 v"+target, 100, "", false)
		logf("应用内自更新完成：v%s", target)
	}()
	return target, nil
}
