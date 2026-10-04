package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"moo/internal/lang"
	"moo/internal/netx"
)

// 商店自更新：官方渠道 = GitHub Releases（Blue-Mink/moo），TLS 直连
//（0.6.207-panel D1：不经镜像候选，防 HTTP 中间人；科学加速代理对
// GitHub 域名仍由 netx 全局代理生效——仅本机代理，信任边界不变）。
//
// 检测：GET /api/store-update → 探测 releases/latest（10min 缓存 + 30min 后台轮询），
// 严格版本比较（只升不降，防恶意源/镜像回灌旧版），仅 amd64（官方只发 x86 FPK）。
//
// 更新：POST /api/store-update（SSE）→ 下载 FPK → 结构校验（gzip/ELF/体积）→
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
// 完整性（0.6.207-panel D1）：自更新全程 TLS 直连 GitHub（不再经 HTTP 镜像
// 候选，消除中间人换包面）。release 附 <资产名>.sha256 时下载后严格验哈希；
// 未附则降级结构校验——解包 FPK 验证其中存在 >5MB 的 ELF 版 moo-server
//（参考 fn-knock 的下载后校验思路，并留日志提示降级）。
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

// selfUpdateSocketPath 飞牛应用中心 daemon 的内部 RPC socket（测试可覆写）。
var selfUpdateSocketPath = "/var/run/com.trim.app.center.sock"

type selfUpdateProbe struct {
	srv *Server

	mu             sync.Mutex
	checkedAt      time.Time
	latestTag      string // 已剥离 v 前缀
	latestAssetURL string
	latestAsset    string
	latestShaURL   string // release 附带的 .sha256 资产 URL（可为空）
	lastErr        error
	lastNotified   string // 已推过 store_update_available 的版本（防重复）
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
func (p *selfUpdateProbe) probe(force bool) (tag, assetURL, asset, shaURL string, err error) {
	p.mu.Lock()
	if !force && p.checkedAt.Add(selfProbeTTL).After(time.Now()) {
		tag, assetURL, asset, shaURL, err = p.latestTag, p.latestAssetURL, p.latestAsset, p.latestShaURL, p.lastErr
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()

	tag, assetURL, asset, shaURL, err = p.srv.doProbe()

	p.mu.Lock()
	p.checkedAt = time.Now()
	p.latestTag, p.latestAssetURL, p.latestAsset, p.latestShaURL, p.lastErr = tag, assetURL, asset, shaURL, err
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

// doProbe 探测 GitHub Releases API（0.6.207-panel D1：TLS 直连 only，
// 不再经 HTTP 镜像候选）。release 附 <资产名>.sha256 时一并返回其 URL
// （下载后验哈希用）；未附则 shaURL 为空，调用方降级结构校验并留日志。
func (s *Server) doProbe() (tag, assetURL, asset, shaURL string, err error) {
	apiURL := "https://api.github.com/repos/" + selfUpdateRepo + "/releases/latest"

	client := netx.NewClient(selfDlTimeout)
	resp, err := client.Get(apiURL)
	if err != nil {
		return "", "", "", "", fmt.Errorf("官方 release 探测失败: %w", err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return "", "", "", "", fmt.Errorf("官方 release 探测失败: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	if rerr != nil {
		return "", "", "", "", fmt.Errorf("官方 release 探测失败: %w", rerr)
	}
	if uerr := json.Unmarshal(data, &rel); uerr != nil {
		return "", "", "", "", fmt.Errorf("官方 release 探测失败: %w", uerr)
	}
	tag = strings.TrimPrefix(rel.TagName, "v")
	if tag == "" {
		return "", "", "", "", errors.New("release 无版本号")
	}
	// 选 x86 FPK 资产（官方只发 x86；arm 资产出现时兜底选任意 fpk）
	fallback := ""
	for _, a := range rel.Assets {
		if !strings.HasSuffix(a.Name, ".fpk") || a.URL == "" {
			continue
		}
		if fallback == "" {
			fallback = a.URL
			assetURL, asset = a.URL, a.Name
		}
		if strings.Contains(a.Name, "_x86") {
			assetURL, asset = a.URL, a.Name
		}
	}
	if assetURL == "" {
		assetURL, asset = fallback, ""
	}
	// D1：release 附 .sha256 资产（资产同名 + .sha256 后缀）→ 下载后验哈希
	for _, a := range rel.Assets {
		if a.URL != "" && a.Name == asset+".sha256" {
			shaURL = a.URL
			break
		}
	}
	return tag, assetURL, asset, shaURL, nil
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
	if runtime.GOARCH != "amd64" {
		writeJSON(w, out) // 官方只发 x86 FPK，非 amd64 不提供自更新
		return
	}
	tag, assetURL, _, _, err := s.selfUpdProbe().probe(false)
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
	tag, assetURL, _, shaURL, err := s.selfUpdProbe().probe(true)
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
	fpkPath, err := s.downloadSelfUpdateFPK(w, f, ctx, assetURL, shaURL, tag)
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
// 不经镜像候选），SSE 上报进度。shaURL 非空（release 附 .sha256）时下载后
// 严格验哈希，不匹配即失败；为空则留日志、由调用方结构校验兜底。
func (s *Server) downloadSelfUpdateFPK(w http.ResponseWriter, f http.Flusher, ctx context.Context, assetURL, shaURL, tag string) (string, error) {
	dst := filepath.Join(os.TempDir(), fmt.Sprintf("moo-selfupdate-%d-%s.fpk", os.Getpid(), tag))
	client := netx.NewClient(8 * time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
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
	var downloaded int64
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
			downloaded += int64(n)
			now := time.Now()
			if now.Sub(lastEmit) > 400*time.Millisecond {
				lastEmit = now
				ev := map[string]any{"step": "downloading", "message": "下载 v" + tag + " 安装包…", "downloaded": downloaded, "total": total}
				if total > 0 {
					pct := int(downloaded * 100 / total)
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
	// D1：release 附 .sha256 → 严格验哈希；未附 → 降级结构校验（留日志）
	if shaURL != "" {
		if err := verifySha256(ctx, client, shaURL, dst); err != nil {
			os.Remove(dst)
			return "", err
		}
		sseSend(w, f, map[string]any{"step": "downloading", "message": "v" + tag + " sha256 校验通过"})
	} else {
		log.Printf("[self-update] release v%s 未附 .sha256，降级结构校验", tag)
	}
	return dst, nil
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
