//go:build linux

// Package platform 封装 fnOS app-center daemon 的 unix socket RPC 通道。
// 移植自 New Store（conversun/fnos-store）internal/platform/rpc.go 的实测语义，
// 按 Moo 的 clean-room 原则裁剪：只保留 daemon 通道（无 CLI 回退），
// 保留全部任务结果三态语义（成功/确定失败/结果未知）。
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// daemonSocket 是 app-center daemon 的 unix socket。
// 用 var 便于测试注入假 socket；生产代码不得重新赋值。
var daemonSocket = "/var/run/com.trim.app.center.sock"

// SetDaemonSocketForTest 0.6.314：供 api 包单测隔离真实 daemon（指向不存在
// 的路径，装有 daemon 的机器上也不会把真实已装应用并入目录缓存）。
// 仅限 _test 代码调用；生产代码不得调用。
func SetDaemonSocketForTest(path string) {
	daemonSocket = path
}

// Daemon 路由（1.2.05xx 实测）。
const (
	routeDownloadTask   = "/rpc/v1/download/task"
	routeDownloadStatus = "/rpc/v1/download/status"
	routeInstallInfo    = "/rpc/v1/install/info"
	routeInstallTask    = "/rpc/v1/install/task"
	routeUpdateInfo     = "/rpc/v1/update/info"
	routeUpdateTask     = "/rpc/v1/update/task"
	routeUninstallTask  = "/rpc/v1/uninstall/task"
	routeCommonStatus   = "/rpc/v1/common/status"
	routeAppInstalled   = "/rpc/v1/app/installed"
	routeStartCheck     = "/rpc/v1/start/check"
	routeStartTask      = "/rpc/v1/start/task"
	routeStopCheck      = "/rpc/v1/stop/check"
	routeStopTask       = "/rpc/v1/stop/task"
)

const (
	daemonStatusRunning        = 1
	daemonStatusSuccess        = 2
	daemonStatusUnknownTask    = 5
	daemonCodeValidation       = 10030
	daemonCodeTransientTimeout = 10050
	daemonCodePackageMissing   = 10100
	// daemonCodeStatusUnsupported 平台「当前状态不支持该操作」：
	// 常见于重复安装/任务进行中（用户看到的「错误代码 10500」就是它）。
	daemonCodeStatusUnsupported = 10500
	daemonCodeWizardRequired    = 19000
	// daemonCodeDependentRunning 平台「依赖方未停用」：升级 nodejs 这类被
	// 运行中应用依赖的运行时会被拒绝（error.log: dependent app not stopped）。
	daemonCodeDependentRunning = 16006
)

// isDeterministicReject 判断该错误码是否属于「平台已明确拒绝」。
// 这类拒绝之后平台会立刻回收任务记录，继续轮询只会把真实原因等成
// 「任务不再持有」，所以同一 code 连续出现就必须提前定论。
func isDeterministicReject(code int) bool {
	switch code {
	case daemonCodeValidation, daemonCodePackageMissing,
		daemonCodeStatusUnsupported, daemonCodeDependentRunning, daemonCodeWizardRequired:
		return true
	}
	return false
}

// daemonCodeHint 把平台错误码翻成用户能看懂的一句话（0.6.398）。
// 此前 10500 会以「app center 返回错误 10500 (…)」原样弹出，
// 用户看到「错误代码10500」但不知道发生了什么。
func daemonCodeHint(code int) string {
	switch code {
	case daemonCodeStatusUnsupported:
		return "应用中心当前状态不支持该操作（应用可能已安装或正在安装）"
	case daemonCodePackageMissing:
		return "安装包不存在（可能已被清理）"
	case daemonCodeWizardRequired:
		return "缺少安装向导必填参数"
	case daemonCodeValidation:
		return "参数校验失败"
	case daemonCodeDependentRunning:
		return "依赖该应用的其他应用正在运行，请先停用它们"
	}
	return ""
}

// 错误三态：
//   - ErrDaemonUnreachable：字节根本没发出，调用方可安全重试/换路
//   - ErrDaemonAmbiguous：已连接但结果未知，禁止重试（可能已执行）
//   - ErrTaskOutcomeUnknown：任务结果未观察到，禁止自动重试
var (
	ErrDaemonUnreachable  = errors.New("app center daemon 不可达")
	ErrDaemonAmbiguous    = errors.New("app center 请求结果未知")
	ErrTaskOutcomeUnknown = errors.New("无法确认操作结果")
)

// DaemonError 携带 daemon 的业务错误码（如 19000=向导必填项缺失）。
type DaemonError struct {
	Path string
	Code int
	Msg  string
}

func (e *DaemonError) Error() string {
	msg := e.Msg
	if msg == "" {
		msg = daemonCodeHint(e.Code)
	}
	if msg != "" {
		return fmt.Sprintf("app center 返回错误 %d: %s", e.Code, msg)
	}
	return fmt.Sprintf("app center 返回错误 %d (%s)", e.Code, e.Path)
}

// WizardRequiredError 表示升级向导存在必填项，而调用方没有提供（也无默认值）。
// 平台会以 19000「wizard required field ... not found」拒绝，且**拒绝后立刻
// 回收任务记录**——若只靠轮询 common/status，错误会被吞成「任务不再持有」。
// 上层用 errors.As 取出缺项与向导定义，交给用户确认后带 customParameters 重试。
type WizardRequiredError struct {
	App     string
	Version string
	Missing []string
	Content json.RawMessage
}

func (e *WizardRequiredError) Error() string {
	return fmt.Sprintf("%s v%s 需要先完成升级确认（必填：%s）",
		e.App, e.Version, strings.Join(e.Missing, "、"))
}

// ErrorDetail 把缺项与完整向导定义带到操作终态（operation.Detailer）：
// 前端据此复用安装向导弹窗收集参数，再带 customParameters 重试升级——
// 不需要新增 `/update-wizard` 端点，也不必为了「探测向导」先下载目标包。
func (e *WizardRequiredError) ErrorDetail() map[string]any {
	wizard := map[string]any{
		"appname":    e.App,
		"version":    e.Version,
		"has_wizard": true,
	}
	if len(e.Content) > 0 {
		var content any
		if err := json.Unmarshal(e.Content, &content); err == nil {
			wizard["content"] = content
		}
	}
	return map[string]any{
		"kind":    "wizard_required",
		"appname": e.App,
		"version": e.Version,
		"missing": e.Missing,
		"wizard":  wizard,
	}
}

// DepBlocker 是一个正在运行、阻塞升级的依赖方（平台 16006 的成因）。
type DepBlocker struct {
	AppName     string `json:"appName"`
	DisplayName string `json:"displayName"`
	Status      string `json:"status"`
}

// DependencyBlockedError 表示目标应用被运行中的依赖方占用：
// 平台拒绝覆盖正在被使用的运行时目录。只能由用户先停用依赖方。
type DependencyBlockedError struct {
	App      string
	Blockers []DepBlocker
}

func (e *DependencyBlockedError) Error() string {
	names := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		n := b.DisplayName
		if n == "" {
			n = b.AppName
		}
		names = append(names, n)
	}
	return fmt.Sprintf("%s 正被 %s 使用，请先停用后再升级", e.App, strings.Join(names, "、"))
}

// ErrorDetail 把「谁在占用」带到前端（operation.Detailer）：弹窗逐条列出，
// 用户确认后可一键停用这些依赖方并重试升级，不必自己去找应用。
func (e *DependencyBlockedError) ErrorDetail() map[string]any {
	blockers := make([]map[string]any, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		blockers = append(blockers, map[string]any{
			"appname":      b.AppName,
			"display_name": b.DisplayName,
			"status":       b.Status,
		})
	}
	return map[string]any{
		"kind":     "dependency_blocked",
		"appname":  e.App,
		"blockers": blockers,
	}
}

// LicenseRequiredError 表示该版本要求接受许可协议（wizardInfo.hasLicense）。
// Moo 不代用户同意许可，需引导到应用中心完成。
type LicenseRequiredError struct {
	App     string
	Version string
}

func (e *LicenseRequiredError) Error() string {
	return fmt.Sprintf("%s v%s 需要先接受许可协议，请在应用中心完成后再更新", e.App, e.Version)
}

// ErrorDetail：许可协议只能在应用中心交互确认（daemon 已带 agreedToProtocol=true
// 仍被拒），因此前端给「去应用中心」指引，不做自动重试。
func (e *LicenseRequiredError) ErrorDetail() map[string]any {
	return map[string]any{
		"kind":    "license_required",
		"appname": e.App,
		"version": e.Version,
	}
}

type rpcEnvelope struct {
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
				return d.DialContext(ctx, "unix", daemonSocket)
			},
		},
	}
}

func daemonCallMethod(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if method == http.MethodPost {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reader)
	if err != nil {
		return err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	var connected bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { connected = true },
	}))
	resp, err := newDaemonClient(90 * time.Second).Do(req)
	if err != nil {
		if !connected {
			return fmt.Errorf("%w (%s): %v", ErrDaemonUnreachable, path, err)
		}
		return fmt.Errorf("%w (%s): %v", ErrDaemonAmbiguous, path, err)
	}
	defer resp.Body.Close()
	var env rpcEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if env.Code != 0 {
		// 平台在「任务已接受但预检失败」时会用非 0 code 返回，同时 Data 里
		// 仍带着 Status 对象（实测 common/status：code=19000 + Data{status:1}，
		// 每秒一条）。这里 best-effort 解析一次 Data，让轮询方能看到任务态；
		// 调用方仍以返回的 *DaemonError 为准（所有调用点都先判 err）。
		if out != nil && len(env.Data) > 0 {
			_ = json.Unmarshal(env.Data, out)
		}
		return &DaemonError{Path: path, Code: env.Code, Msg: env.Msg}
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decode %s data: %w", path, err)
		}
	}
	return nil
}

func daemonCall(ctx context.Context, path string, body any, out any) error {
	return daemonCallMethod(ctx, http.MethodPost, path, body, out)
}

func daemonCallGet(ctx context.Context, path string, out any) error {
	return daemonCallMethod(ctx, http.MethodGet, path, nil, out)
}

// Available 探测 daemon socket 是否可用。
func Available() bool {
	if _, err := net.DialTimeout("unix", daemonSocket, 2*time.Second); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := daemonCall(ctx, routeInstallInfo, map[string]any{}, nil)
	var de *DaemonError
	return errors.As(err, &de) && de.Code == daemonCodeValidation
}

// ---------------------------------------------------------------------------
// 暂存（stage）
// ---------------------------------------------------------------------------

// StagedPackage 是 daemon 已解包识别的 fpk。
type StagedPackage struct {
	AppName     string `json:"appName"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	PackageType string `json:"packageType"`
	Installed   bool   `json:"installed"`
	Path        string `json:"path"`
}

type stageStatus struct {
	Status      int     `json:"status"`
	Message     string  `json:"message"`
	Progress    float64 `json:"progress"`
	PackageType string  `json:"packageType"`
	Path        string  `json:"path"`
	AppName     string  `json:"appName"`
	Version     string  `json:"version"`
	Name        string  `json:"name"`
	Installed   bool    `json:"installed"`
}

// StageFpk 把本地 fpk 交给 daemon 解包识别（安装/升级的前置步骤）。
func StageFpk(ctx context.Context, fpkPath string, onProgress func(float64)) (*StagedPackage, error) {
	var task struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := daemonCall(ctx, routeDownloadTask, map[string]any{
		"packageSourceType": "file",
		"path":              fpkPath,
	}, &task); err != nil {
		return nil, fmt.Errorf("暂存安装包失败: %w", err)
	}
	if task.DownloadTaskID == "" {
		return nil, errors.New("暂存安装包失败: app center 未返回任务 ID")
	}
	deadline := time.Now().Add(3 * time.Minute)
	transient := 0
	for time.Now().Before(deadline) {
		var st stageStatus
		err := daemonCall(ctx, routeDownloadStatus, map[string]any{"downloadTaskId": task.DownloadTaskID}, &st)
		if err != nil {
			if !isTransientPollError(err) {
				return nil, fmt.Errorf("查询暂存状态失败: %w", err)
			}
			if transient >= 5 {
				return nil, fmt.Errorf("查询暂存状态失败: 连续瞬时错误 %w", err)
			}
			transient++
		} else {
			transient = 0
			if st.Status == daemonStatusSuccess {
				if st.AppName == "" || st.Version == "" {
					return nil, errors.New("暂存完成但 app center 未能识别安装包")
				}
				return &StagedPackage{
					AppName: st.AppName, Version: st.Version, Name: st.Name,
					PackageType: st.PackageType, Installed: st.Installed, Path: st.Path,
				}, nil
			}
			if st.Status != daemonStatusRunning {
				return nil, fmt.Errorf("暂存失败: 状态 %d %s", st.Status, st.Message)
			}
			if onProgress != nil {
				onProgress(st.Progress)
			}
		}
		if err := sleepCtx(ctx, 500*time.Millisecond); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("暂存安装包超时")
}

func isTransientPollError(err error) bool {
	var de *DaemonError
	if errors.As(err, &de) && de.Code == daemonCodeTransientTimeout {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "TRPC read timeout") || strings.Contains(msg, "timeout")
}

// RemoveStagedPackage 清理 daemon 解包目录（daemon 不会自动回收）。
// 只允许删 .../appcenter-downloads/*-tpk 绝对路径，其余一律拒绝。
func RemoveStagedPackage(p string) {
	if p == "" {
		return
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) ||
		!strings.HasSuffix(clean, "-tpk") ||
		filepath.Base(filepath.Dir(clean)) != "appcenter-downloads" {
		log.Printf("RemoveStagedPackage: 拒绝删除异常路径 %q", p)
		return
	}
	if err := os.RemoveAll(clean); err != nil {
		log.Printf("RemoveStagedPackage: %s: %v", p, err)
	}
}

// ---------------------------------------------------------------------------
// 向导
// ---------------------------------------------------------------------------

// WizardParam 是安装/升级向导的用户答案。
type WizardParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type wizardInfo struct {
	AppName           string          `json:"appName"`
	Version           string          `json:"version"`
	InstallType       string          `json:"installType"`
	InstalledType     string          `json:"installedType"` // 已装实例的安装类型："root"=系统空间（/var/apps，卷号恒 0）
	InstalledVolumeID int             `json:"installedVolumeID"`
	HasWizard         bool            `json:"hasWizard"`
	WizardContent     json.RawMessage `json:"wizardContent"`
	// HasLicense 平台返回的「需要接受许可协议」标记（0.6.5xx）。
	// 此前未解析：遇到需要许可的版本时平台报错但无法给出可读原因。
	HasLicense bool `json:"hasLicense"`
}

type infoResponse struct {
	WizardInfo wizardInfo `json:"wizardInfo"`
}

// AppWizard 是应用安装向导的摘要视图。
type AppWizard struct {
	AppName         string
	Version         string
	HasWizard       bool
	Content         json.RawMessage
	InstallVolumeID int
}

// FetchWizard 暂存 fpk 并读取向导定义（不安装）。
func FetchWizard(ctx context.Context, staged *StagedPackage) (*AppWizard, error) {
	route := routeInstallInfo
	body := map[string]any{
		"appName":     staged.AppName,
		"version":     staged.Version,
		"packageType": staged.PackageType,
		"language":    "zh-CN",
	}
	if staged.Installed {
		route = routeUpdateInfo
		body = map[string]any{
			"appName":       staged.AppName,
			"updateVersion": staged.Version,
			"packageType":   staged.PackageType,
			"language":      "zh-CN",
		}
	}
	var info infoResponse
	if err := daemonCall(ctx, route, body, &info); err != nil {
		return nil, fmt.Errorf("读取安装信息失败: %w", err)
	}
	w := info.WizardInfo
	return &AppWizard{
		AppName:         staged.AppName,
		Version:         staged.Version,
		HasWizard:       w.HasWizard,
		Content:         w.WizardContent,
		InstallVolumeID: w.InstalledVolumeID,
	}, nil
}

// AutoFillParams 读取暂存包的向导定义并自动填充参数（initValue）。
// 返回填充后的参数与缺失的必填字段清单（无向导时为空参数、无缺失）。
// 供安装路径统一使用：必填无默认 → 调用方明确报错而非 daemon 19000。
func AutoFillParams(ctx context.Context, staged *StagedPackage) ([]WizardParam, []string, error) {
	_, params, missing, err := AutoFillParamsWithWizard(ctx, staged)
	return params, missing, err
}

// AutoFillParamsWithWizard 同 AutoFillParams，但同时把向导定义带回来。
// 升级路径缺必填项时，调用方需要用 Content 构造 WizardRequiredError，
// 让前端直接弹「升级确认」收集参数（社区源升级与官方 cloud 通道同款契约）。
func AutoFillParamsWithWizard(ctx context.Context, staged *StagedPackage) (*AppWizard, []WizardParam, []string, error) {
	w, err := FetchWizard(ctx, staged)
	if err != nil {
		return nil, nil, nil, err
	}
	if !w.HasWizard || len(w.Content) == 0 {
		return w, []WizardParam{}, nil, nil
	}
	params, missing, err := AutoFillWizardParams(w.Content)
	if err != nil {
		return w, nil, nil, err
	}
	return w, params, missing, nil
}

// AutoFillWizardParams 用向导声明的 initValue 自动填充参数；
// 必填且无默认值的字段返回缺失清单（调用方决定报错还是交互）。
func AutoFillWizardParams(content json.RawMessage) (params []WizardParam, missing []string, err error) {
	if len(content) == 0 {
		return nil, nil, nil
	}
	var steps []struct {
		Items []struct {
			Type      string `json:"type"`
			Field     string `json:"field"`
			InitValue string `json:"initValue"`
			Rules     []struct {
				Required bool `json:"required"`
			} `json:"rules"`
		} `json:"items"`
	}
	if err := json.Unmarshal(content, &steps); err != nil {
		return nil, nil, fmt.Errorf("解析向导定义失败: %w", err)
	}
	for _, st := range steps {
		for _, it := range st.Items {
			if it.Type == "tips" || it.Field == "" {
				continue
			}
			required := false
			for _, r := range it.Rules {
				if r.Required {
					required = true
					break
				}
			}
			if it.InitValue == "" {
				if required {
					missing = append(missing, it.Field)
				}
				continue
			}
			params = append(params, WizardParam{Key: it.Field, Value: it.InitValue})
		}
	}
	return params, missing, nil
}

// unsatisfiedRequired 返回「必填、且调用方没有提供值」的字段。
// 有 initValue 的字段由 AutoFillWizardParams 自动填充，不算缺；
// 勾选类字段（同意兼容性/备份说明）没有 initValue，必须由用户提供。
func unsatisfiedRequired(content json.RawMessage, provided []WizardParam) []string {
	if len(content) == 0 {
		return nil
	}
	have := make(map[string]bool, len(provided))
	for _, p := range provided {
		if strings.TrimSpace(p.Value) != "" {
			have[p.Key] = true
		}
	}
	var steps []struct {
		Items []struct {
			Type      string `json:"type"`
			Field     string `json:"field"`
			InitValue string `json:"initValue"`
			Rules     []struct {
				Required bool `json:"required"`
			} `json:"rules"`
		} `json:"items"`
	}
	if err := json.Unmarshal(content, &steps); err != nil {
		return nil
	}
	var missing []string
	for _, st := range steps {
		for _, it := range st.Items {
			if it.Type == "tips" || it.Field == "" || have[it.Field] {
				continue
			}
			if strings.TrimSpace(it.InitValue) != "" {
				continue
			}
			for _, r := range it.Rules {
				if r.Required {
					missing = append(missing, it.Field)
					break
				}
			}
		}
	}
	return missing
}

// ---------------------------------------------------------------------------
// 任务轮询
// ---------------------------------------------------------------------------

type taskStatus struct {
	Status     int     `json:"status"`
	Message    string  `json:"message"`
	Progress   float64 `json:"progress"`
	OutputText string  `json:"outputText"`
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

const (
	taskPollOutageBudget = 90 * time.Second
	taskPollInterval     = 1 * time.Second
	// 官方云升级的分段预算（0.6.5xx）。操作队列有 25 分钟硬超时
	// （operation/queue.go），内部预算之和必须留余量：此前 下载 10min +
	// 等待 15min 正好等于 25min，大包或慢网时外层先超时，错误被降级成
	// 「过程中断开连接」，真实原因（下载慢/平台任务仍在跑）全部丢失。
	upgradeDownloadBudget = 8 * time.Minute
	upgradeWaitBudget     = 12 * time.Minute
)

// UpgradeDeps 返回「依赖目标应用且正在运行」的应用清单。
// 平台升级被依赖的运行时（nodejs_v22/v24）时会拒绝覆盖其目录并记
// `dependent app not stopped`（code 16006）。该检查只用 stop/check，
// 不依赖安装包，可以放在下载之前，避免白下载一整包。
func UpgradeDeps(ctx context.Context, appname string) ([]DepBlocker, error) {
	var chk struct {
		DependentApps struct {
			IsDependency   bool         `json:"isDependency"`
			DependencyApps []DepBlocker `json:"dependencyApps"`
		} `json:"dependentApps"`
	}
	if err := daemonCall(ctx, routeStopCheck, map[string]any{"appName": appname}, &chk); err != nil {
		return nil, err
	}
	out := make([]DepBlocker, 0, len(chk.DependentApps.DependencyApps))
	for _, d := range chk.DependentApps.DependencyApps {
		switch d.Status {
		case "running", "start", "starting":
			out = append(out, d)
		}
	}
	return out, nil
}

// DependentApps 返回应用的全部依赖方（不过滤状态，含 stopped）（0.6.549）。
//
// 「允许自动停用的应用」弹窗要显示「这个应用依赖哪个运行时」，但平台没有
// 正向查询接口：只能对每个已装应用 stop/check 反查，再把 runtime→apps 反转。
// isDep=false 表示该应用不是被依赖方（无依赖方）。
func DependentApps(ctx context.Context, appname string) (deps []DepBlocker, isDep bool, err error) {
	var chk struct {
		DependentApps struct {
			IsDependency   bool         `json:"isDependency"`
			DependencyApps []DepBlocker `json:"dependencyApps"`
		} `json:"dependentApps"`
	}
	if err := daemonCall(ctx, routeStopCheck, map[string]any{"appName": appname}, &chk); err != nil {
		return nil, false, err
	}
	return chk.DependentApps.DependencyApps, chk.DependentApps.IsDependency, nil
}

// RunningDependentsForInstalled 「已装应用」才返回运行中的依赖方（0.6.548）。
//
// 社区 FPK 通道此前没有依赖预检：升级被依赖的运行时（nodejs_v22/v24）时，
// 平台先下载整包再以 16006 拒绝，前端只看到裸错误、弹不出「停用并重试」。
// checked=false 表示「未安装」或「预检失败」——调用方一律忽略，不阻断升级
// （不因 daemon 抖动误伤正常安装）。
func RunningDependentsForInstalled(ctx context.Context, appName string) (deps []DepBlocker, checked bool) {
	list, err := ListInstalled(ctx)
	if err != nil {
		return nil, false
	}
	// daemon appName 与源 feed 的 name 大小写/别名可能不一致（0.6.208 同款问题）。
	daemonName := ""
	for _, a := range list {
		if strings.EqualFold(a.AppName, appName) {
			daemonName = a.AppName
			break
		}
	}
	if daemonName == "" {
		return nil, false
	}
	deps, err = UpgradeDeps(ctx, daemonName)
	if err != nil {
		return nil, false
	}
	return deps, true
}

// WaitTask 轮询 daemon 任务至终态。区分：成功 / 确定失败 / ErrTaskOutcomeUnknown。
func WaitTask(ctx context.Context, taskID, what string, budget time.Duration, onProgress func(float64)) error {
	if taskID == "" {
		return fmt.Errorf("%s失败: app center 未返回任务 ID", what)
	}
	deadline := time.Now().Add(budget)
	var outageStart time.Time
	// lastReject 记住最近一次「确定性拒绝」：平台在预检拒绝后立刻回收任务
	// 记录，不记住它就只能报「已不再持有任务」（实测 motrix 19000、
	// nodejs_v22/v24 16006 都是这条路径）。
	var lastReject *DaemonError
	rejectStreak := 0
	for time.Now().Before(deadline) {
		var st taskStatus
		err := daemonCall(ctx, routeCommonStatus, map[string]any{"taskId": taskID}, &st)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %s过程中断开与 app center 的连接", ErrTaskOutcomeUnknown, what)
			}
			var de *DaemonError
			if errors.As(err, &de) && isDeterministicReject(de.Code) {
				if lastReject != nil && lastReject.Code == de.Code {
					rejectStreak++
				} else {
					lastReject, rejectStreak = de, 1
				}
				// 实测平台每秒回一次同样的拒绝；3 次即可定论，
				// 不必陪跑到任务记录被回收（那还会刷爆平台 error.log）。
				if rejectStreak >= 3 {
					return fmt.Errorf("%s失败: %w", what, de)
				}
			}
			if outageStart.IsZero() {
				outageStart = time.Now()
			}
			if time.Since(outageStart) > taskPollOutageBudget {
				return fmt.Errorf("%w: 连续无法查询%s状态 (%v)", ErrTaskOutcomeUnknown, what, err)
			}
			if err := sleepCtx(ctx, taskPollInterval); err != nil {
				return fmt.Errorf("%w: %s过程中断开连接", ErrTaskOutcomeUnknown, what)
			}
			continue
		}
		outageStart = time.Time{}
		switch st.Status {
		case daemonStatusSuccess:
			return nil
		case daemonStatusRunning:
			if onProgress != nil {
				onProgress(st.Progress)
			}
		case daemonStatusUnknownTask:
			// 任务记录被回收：若此前出现过确定性拒绝，真实原因是那次拒绝。
			// 报成「任务丢失」会把用户与排障者引向完全错误的方向。
			if lastReject != nil {
				return fmt.Errorf("%s失败: %w", what, lastReject)
			}
			return fmt.Errorf("%w: app center 已不再持有该%s任务", ErrTaskOutcomeUnknown, what)
		default:
			detail := st.Message
			if detail == "" {
				detail = st.OutputText
			}
			return fmt.Errorf("%s失败: 状态 %d %s", what, st.Status, detail)
		}
		if err := sleepCtx(ctx, taskPollInterval); err != nil {
			return fmt.Errorf("%w: %s过程中断开连接", ErrTaskOutcomeUnknown, what)
		}
	}
	return fmt.Errorf("%w: %s超过 %s 仍未结束（任务可能仍在进行，稍后在应用中心查看）", ErrTaskOutcomeUnknown, what, budget)
}

// ---------------------------------------------------------------------------
// 安装 / 升级 / 卸载
// ---------------------------------------------------------------------------

// InstallFpk 安装未安装的应用（volume<=0 时用默认卷探测；params 可空）。
// volume>0 = 调用方已解析的安装卷（设置「应用安装位置」/DefaultVolume 兜底），
// 0.6.325：FPK 链路透传用户配置卷，不再硬编码 0 走探测。
func InstallFpk(ctx context.Context, staged *StagedPackage, volume int, params []WizardParam, onProgress func(float64)) error {
	if staged.Installed {
		return fmt.Errorf("%s 已安装，请使用升级功能", staged.AppName)
	}
	if volume <= 0 {
		v, err := defaultVolumeFn()
		if err != nil {
			return err
		}
		volume = v
	}
	if params == nil {
		params = []WizardParam{}
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeInstallTask, map[string]any{
		"appName":     staged.AppName,
		"version":     staged.Version,
		"packageType": staged.PackageType,
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   false,
		},
		"customParameters": params,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("安装提交失败: %w", err)
	}
	return WaitTask(ctx, task.TaskID, "安装", 15*time.Minute, onProgress)
}

// UpgradeFpk 就地升级已安装应用（保留 @appdata；volume 取应用当前所在卷）。
func UpgradeFpk(ctx context.Context, staged *StagedPackage, params []WizardParam, onProgress func(float64)) error {
	if !staged.Installed {
		return fmt.Errorf("%s 尚未安装，无法升级", staged.AppName)
	}
	var info infoResponse
	if err := daemonCall(ctx, routeUpdateInfo, map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"language":      "zh-CN",
	}, &info); err != nil {
		return fmt.Errorf("升级前检查失败: %w", err)
	}
	volume := info.WizardInfo.InstalledVolumeID
	// 0.6.320：系统空间（root）应用装在 /var/apps，平台对它的 installedVolumeID
	// 恒为 0——0 是合法值而非"无法确定"（fnOS 1.2.06xx 实测 trim.preview：
	// 应用中心 UI 携 INSTALL_VOLUME_ID=0 升级成功，平台 APP_UPDATED 事件同值）。
	// 仅当已装实例不是系统空间却取不到卷号时才中止保护。
	if volume <= 0 && info.WizardInfo.InstalledType != "root" {
		return fmt.Errorf("无法确定 %s 当前所在存储卷，已中止升级以保护数据", staged.AppName)
	}
	if params == nil {
		params = []WizardParam{}
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeUpdateTask, map[string]any{
		"appName":       staged.AppName,
		"updateVersion": staged.Version,
		"packageType":   staged.PackageType,
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   false,
		},
		"customParameters": params,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("升级提交失败: %w", err)
	}
	return WaitTask(ctx, task.TaskID, "升级", 15*time.Minute, onProgress)
}

// UpgradeCloud 走官方 cloud 通道就地升级已安装应用（与应用中心 UI「更新」
// 按钮同一路径，保留 @appdata——平台 Operation.Upgrade）：
//
//  1. download/task (cloud, version=升级目标版本) → 轮询 download/status
//  2. update/info（取应用当前所在卷 / 向导定义）
//  3. update/task (packageType=cloud) → 轮询 common/status
//
// upgradeVersion 必须是 daemon upgradeInfo 报告的目标版本（或更高的目录
// 版本）：目录滞后时传目录版本只会拉到目录里的旧包（实测 trim.preview
// 目录 0.1.16，升级目标 0.2.4，下载/升级通道按 upgradeInfo 版本才成功）。
// onProgress 回调进度 0~100。
func UpgradeCloud(ctx context.Context, appName, sourceID, upgradeVersion string, customParams []WizardParam, onProgress func(float64)) error {
	if sourceID == "" {
		return fmt.Errorf("%s 缺少应用中心 sourceID，无法走官方通道升级", appName)
	}
	// 依赖预检（0.6.5xx）：平台拒绝覆盖被运行中应用依赖的运行时目录
	// （error.log: dependent app not stopped / code 16006）。放在下载之前，
	// 省一次整包下载。stop/check 自身失败不阻断升级（不因预检故障误伤）。
	if deps, derr := UpgradeDeps(ctx, appName); derr != nil {
		log.Printf("[upgrade] %s 依赖预检失败（忽略，继续）: %v", appName, derr)
	} else if len(deps) > 0 {
		return &DependencyBlockedError{App: appName, Blockers: deps}
	}
	if customParams == nil {
		customParams = []WizardParam{}
	}
	report := func(pct float64) {
		if onProgress == nil || pct < 0 {
			return
		}
		if pct > 100 {
			pct = 100
		}
		onProgress(pct)
	}
	var dl struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := daemonCall(ctx, routeDownloadTask, map[string]any{
		"packageSourceType": "cloud",
		"appName":           appName,
		"sourceID":          sourceID,
		"version":           upgradeVersion,
		"language":          "zh-CN",
	}, &dl); err != nil {
		return fmt.Errorf("启动官方下载失败: %w", err)
	}
	dlDone := false
	deadline := time.Now().Add(upgradeDownloadBudget)
	transient := 0
	for time.Now().Before(deadline) {
		var st stageStatus
		err := daemonCall(ctx, routeDownloadStatus, map[string]any{"downloadTaskId": dl.DownloadTaskID}, &st)
		if err != nil {
			if !isTransientPollError(err) {
				return fmt.Errorf("查询下载状态失败: %w", err)
			}
			if transient >= 5 {
				return fmt.Errorf("查询下载状态失败: 连续瞬时错误 %w", err)
			}
			transient++
		} else {
			transient = 0
			if st.Status == daemonStatusSuccess {
				dlDone = true
				break
			}
			if st.Status != daemonStatusRunning {
				return fmt.Errorf("官方下载失败: 状态 %d %s", st.Status, st.Message)
			}
			report(st.Progress * 50) // 下载占升级总进度 0~50
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	if !dlDone {
		return errors.New("官方下载超时")
	}
	var info infoResponse
	if err := daemonCall(ctx, routeUpdateInfo, map[string]any{
		"appName":       appName,
		"updateVersion": upgradeVersion,
		"packageType":   "cloud",
		"language":      "zh-CN",
	}, &info); err != nil {
		return fmt.Errorf("升级前检查失败: %w", err)
	}
	volume := info.WizardInfo.InstalledVolumeID
	// 0.6.320：系统空间（root）应用卷号恒 0 属平台正常态（见 UpgradeFpk 同处注释），
	// 照实传 0 给 update/task（与应用中心 UI 行为一致）；仅非 root 且卷号为 0 才中止。
	if volume <= 0 && info.WizardInfo.InstalledType != "root" {
		return fmt.Errorf("无法确定 %s 当前所在存储卷，已中止升级以保护数据", appName)
	}
	// 许可协议（0.6.5xx）：平台用 hasLicense 表示该版本要求接受许可，
	// Moo 不代用户同意，引导到应用中心完成。
	if info.WizardInfo.HasLicense && len(customParams) == 0 {
		return &LicenseRequiredError{App: appName, Version: upgradeVersion}
	}
	// 升级向导预检（0.6.5xx）：平台直到 update/task 阶段才校验必填项并回
	// 19000，随即回收任务记录 —— 轮询端只能报「不再持有任务」。这里用
	// update/info 已返回的 wizardContent 提前判定，缺项交给用户确认。
	if info.WizardInfo.HasWizard && len(info.WizardInfo.WizardContent) > 0 {
		if len(customParams) == 0 {
			if filled, _, ferr := AutoFillWizardParams(info.WizardInfo.WizardContent); ferr == nil {
				customParams = filled // 只填有 initValue 的字段，不替用户同意任何勾选项
			}
		}
		if missing := unsatisfiedRequired(info.WizardInfo.WizardContent, customParams); len(missing) > 0 {
			return &WizardRequiredError{App: appName, Version: upgradeVersion,
				Missing: missing, Content: info.WizardInfo.WizardContent}
		}
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeUpdateTask, map[string]any{
		"appName":       appName,
		"updateVersion": upgradeVersion,
		"packageType":   "cloud",
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   true,
		},
		"customParameters": customParams,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("升级提交失败: %w", err)
	}
	// WaitTask 进度量程 0~100（common/status 实测），映射到总进度 50~100。
	if err := WaitTask(ctx, task.TaskID, "升级", upgradeWaitBudget, func(p float64) {
		report(50 + p/2)
	}); err != nil {
		return err
	}
	return nil
}

// DownloadCloud 仅下载官方 cloud 包（不安装），等价应用中心「下载」动作，
// 走 daemon unix socket 通道（无需面板登录）。返回下载完成后安装包的路径
// （FPK 应用 = .fpk 文件；原生应用 = TPK 解压目录）。onProgress 报告 0~100。
func DownloadCloud(ctx context.Context, appName, sourceID, version string, volume int, onProgress func(float64)) (string, error) {
	if sourceID == "" {
		return "", fmt.Errorf("%s 缺少应用中心 sourceID，无法走官方通道", appName)
	}
	report := func(pct float64) {
		if onProgress == nil || pct < 0 {
			return
		}
		if pct > 100 {
			pct = 100
		}
		onProgress(pct)
	}
	var dl struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := daemonCall(ctx, routeDownloadTask, map[string]any{
		"packageSourceType": "cloud",
		"appName":           appName,
		"sourceID":          sourceID,
		"version":           version,
		"volumeID":          volume,
		"language":          "zh-CN",
	}, &dl); err != nil {
		return "", fmt.Errorf("启动官方下载失败: %w", err)
	}
	deadline := time.Now().Add(10 * time.Minute)
	transient := 0
	for time.Now().Before(deadline) {
		var st stageStatus
		err := daemonCall(ctx, routeDownloadStatus, map[string]any{"downloadTaskId": dl.DownloadTaskID, "language": "zh-CN"}, &st)
		if err != nil {
			if !isTransientPollError(err) {
				return "", fmt.Errorf("查询下载状态失败: %w", err)
			}
			if transient >= 5 {
				return "", fmt.Errorf("查询下载状态失败: 连续瞬时错误 %w", err)
			}
			transient++
		} else {
			transient = 0
			if st.Status == daemonStatusSuccess {
				return st.Path, nil
			}
			if st.Status != daemonStatusRunning {
				return "", fmt.Errorf("官方下载失败: 状态 %d %s", st.Status, st.Message)
			}
			report(st.Progress * 100)
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return "", err
		}
	}
	return "", errors.New("官方下载超时")
}

// InstallCloud 新装官方 cloud 应用（与应用中心 UI「安装」按钮同一路径，
// daemon unix socket 通道，全程无需面板登录）：
//
//  1. download/task (cloud) → 轮询 download/status
//  2. install/task (packageType=cloud, immediateStart=true) → 轮询 common/status
//
// volume 为目标存储卷（调用方已解析默认卷/用户选择）。onProgress 报告 0~100。
func InstallCloud(ctx context.Context, appName, sourceID, version string, volume int, customParams []WizardParam, onProgress func(float64)) error {
	if sourceID == "" {
		return fmt.Errorf("%s 缺少应用中心 sourceID，无法走官方通道", appName)
	}
	if volume <= 0 {
		v, err := DefaultVolume()
		if err != nil {
			return err
		}
		volume = v
	}
	if customParams == nil {
		customParams = []WizardParam{}
	}
	report := func(pct float64) {
		if onProgress == nil || pct < 0 {
			return
		}
		if pct > 100 {
			pct = 100
		}
		onProgress(pct)
	}
	// 1) 下载（占总进度 0~50）
	var dl struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := daemonCall(ctx, routeDownloadTask, map[string]any{
		"packageSourceType": "cloud",
		"appName":           appName,
		"sourceID":          sourceID,
		"version":           version,
		"volumeID":          volume,
		"language":          "zh-CN",
	}, &dl); err != nil {
		return fmt.Errorf("启动官方下载失败: %w", err)
	}
	dlDone := false
	deadline := time.Now().Add(upgradeDownloadBudget)
	transient := 0
	for time.Now().Before(deadline) {
		var st stageStatus
		err := daemonCall(ctx, routeDownloadStatus, map[string]any{"downloadTaskId": dl.DownloadTaskID, "language": "zh-CN"}, &st)
		if err != nil {
			if !isTransientPollError(err) {
				return fmt.Errorf("查询下载状态失败: %w", err)
			}
			if transient >= 5 {
				return fmt.Errorf("查询下载状态失败: 连续瞬时错误 %w", err)
			}
			transient++
		} else {
			transient = 0
			if st.Status == daemonStatusSuccess {
				dlDone = true
				break
			}
			if st.Status != daemonStatusRunning {
				return fmt.Errorf("官方下载失败: 状态 %d %s", st.Status, st.Message)
			}
			report(st.Progress * 50)
		}
		if err := sleepCtx(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	if !dlDone {
		return errors.New("官方下载超时")
	}
	// 2) 安装（占总进度 50~100）
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, routeInstallTask, map[string]any{
		"appName":     appName,
		"version":     version,
		"packageType": "cloud",
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volume,
			"dataVolumeId":     volume,
			"immediateStart":   true,
			"apiScope":         map[string]any{},
		},
		"customParameters": customParams,
		"language":         "zh-CN",
	}, &task); err != nil {
		return fmt.Errorf("提交安装失败: %w", err)
	}
	return WaitTask(ctx, task.TaskID, "安装", 15*time.Minute, func(p float64) {
		report(50 + p/2)
	})
}

// FetchCloudWizard 取官方 cloud 应用的安装向导定义（包须已下载）。
func FetchCloudWizard(ctx context.Context, appName, version string) (*AppWizard, error) {
	var info infoResponse
	if err := daemonCall(ctx, routeInstallInfo, map[string]any{
		"appName":  appName,
		"version":  version,
		"language": "zh-CN",
	}, &info); err != nil {
		return nil, fmt.Errorf("获取安装向导失败: %w", err)
	}
	return &AppWizard{
		AppName:         appName,
		Version:         version,
		HasWizard:       info.WizardInfo.HasWizard,
		Content:         info.WizardInfo.WizardContent,
		InstallVolumeID: info.WizardInfo.InstalledVolumeID,
	}, nil
}

// SingleInstalled 返回 daemon 记录中的单个已装应用；未安装返回 (nil, nil)。
func SingleInstalled(ctx context.Context, appName string) (*InstalledApp, error) {
	apps, err := ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	for i := range apps {
		if apps[i].AppName == appName {
			return &apps[i], nil
		}
	}
	return nil, nil
}

// Uninstall 卸载应用。deleteData=true 时删除 @appdata（默认 false 保留数据）。
func Uninstall(ctx context.Context, appname string, deleteData bool, onProgress func(float64)) error {
	del := "false"
	if deleteData {
		del = "true"
	}
	var task struct {
		TaskID string `json:"taskId"`
	}
	err := daemonCall(ctx, routeUninstallTask, map[string]any{
		"appname":            appname,
		"wizard_delete_data": del,
	}, &task)
	if err != nil {
		return fmt.Errorf("卸载提交失败: %w", err)
	}
	return WaitTask(ctx, task.TaskID, "卸载", 10*time.Minute, onProgress)
}

// ---------------------------------------------------------------------------
// 启动 / 停用 / 列表
// ---------------------------------------------------------------------------

// StartApp start/check → start/task → 轮询。
func StartApp(ctx context.Context, appname string) error {
	// 0.6.348：启停后真实运行态必然变化，清掉探测缓存，下一次列表即刷新。
	defer InvalidateRunProbe()
	var chk struct {
		IsSystemVersionMatch bool `json:"isSystemVersionMatch"`
	}
	if err := daemonCall(ctx, routeStartCheck, map[string]any{"appName": appname}, &chk); err != nil {
		return fmt.Errorf("启动前校验失败: %w", err)
	}
	if !chk.IsSystemVersionMatch {
		return fmt.Errorf("系统版本不满足 %s 的运行要求", appname)
	}
	return submitControl(ctx, routeStartTask, appname, "启动")
}

// StopApp stop/check → stop/task → 轮询（含依赖检查）。
func StopApp(ctx context.Context, appname string) error {
	defer InvalidateRunProbe()
	var chk struct {
		IsSystemVersionMatch bool `json:"isSystemVersionMatch"`
		DependentApps        struct {
			IsDependency bool `json:"isDependency"`
		} `json:"dependentApps"`
	}
	if err := daemonCall(ctx, routeStopCheck, map[string]any{"appName": appname}, &chk); err != nil {
		return fmt.Errorf("停用前校验失败: %w", err)
	}
	if !chk.IsSystemVersionMatch {
		return fmt.Errorf("系统版本不满足 %s 的停用要求", appname)
	}
	if chk.DependentApps.IsDependency {
		return fmt.Errorf("其他应用依赖 %s，应用中心拒绝停用", appname)
	}
	return submitControl(ctx, routeStopTask, appname, "停用")
}

func submitControl(ctx context.Context, route, appname, what string) error {
	var task struct {
		TaskID string `json:"taskId"`
	}
	if err := daemonCall(ctx, route, map[string]any{"appName": appname}, &task); err != nil {
		return fmt.Errorf("%s失败: %w", what, err)
	}
	err := WaitTask(ctx, task.TaskID, what, 5*time.Minute, nil)
	if err == nil {
		return nil
	}
	// 0.6.342：daemon 会很快回收「秒完成」的启停任务记录，轮询拿到 status=5
	//（unknown task）→ WaitTask 返回 ErrTaskOutcomeUnknown（"无法确认操作结果"），
	// 但应用实际已启动/停用（实测 dog-dev 启动成功却报错）。对「结果未知」
	// 用权威已装列表复核一次：达到目标状态即视为成功；确定失败保留原错误。
	if errors.Is(err, ErrTaskOutcomeUnknown) {
		wantRunning := what == "启动"
		if st, known := installedRunStatus(ctx, appname); known {
			switch st {
			case "running", "start", "starting":
				if wantRunning {
					return nil
				}
			case "stopped", "stopping":
				if !wantRunning {
					return nil
				}
			}
		}
	}
	return err
}

// installedRunStatus 查询 daemon 权威已装列表里该应用的运行状态。
// known=false 表示列表取不到或应用不在列表（无法用于判定）。
func installedRunStatus(ctx context.Context, appname string) (status string, known bool) {
	list, err := ListInstalled(ctx)
	if err != nil {
		return "", false
	}
	for _, a := range list {
		if strings.EqualFold(a.AppName, appname) {
			return a.Status, true
		}
	}
	return "", false
}

// UpgradeInfo 是平台权威的「有更新」信息（与应用中心 UI 的「更新」角标
// 同一数据源）。面板目录 app/list 的 version 字段可能滞后于平台实际可升级
// 版本（实测 trim.preview：目录 0.1.16 vs 升级目标 0.2.4），更新判定与
// 升级目标版本必须用它，而不是目录版本比较。
type UpgradeInfo struct {
	Version          string `json:"version"`
	VersionID        int64  `json:"versionID"`
	PublishAt        int64  `json:"publishAt"`
	ChangeLog        string `json:"changeLog"`
	AutoUpdateFailed bool   `json:"autoUpdateFailed"`
}

// InstalledApp 是 daemon 报告的已安装应用记录。
type InstalledApp struct {
	AppName  string `json:"appName"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	SourceID string `json:"sourceID,omitempty"`
	Status   string `json:"status"`
	Icon     string `json:"icon"`
	// Docker 标记该应用是 Docker 类（0.6.348：真实运行态探测用它选判定通道）。
	Docker      bool `json:"docker"`
	IsOpen      bool `json:"isOpen"`
	IsStartStop bool `json:"isStartStop"`
	IsUninstall bool `json:"isUninstall"`
	Upgrade     bool `json:"upgrade"`
	// UpgradeInfo 非 nil 表示平台认为该应用有可升级版本（版本即目标版本）。
	UpgradeInfo *UpgradeInfo `json:"upgradeInfo,omitempty"`
	// Web 是应用可打开的 Web 入口（iframe/url 类型才有）。
	Web Protocol `json:"web,omitempty"`
	// ServiceName 是 fnOS Web UI 壳的应用服务名（如 "moo.Application"），
	// 壳内打开（openCustomApp）用；daemon 对每个已装应用都会下发。
	ServiceName string `json:"serviceName,omitempty"`
}

// Protocol 是 Web 入口协议描述。
// daemon 的 urls.host 字段恒为空（语义=当前访问主机），因此这里
// 只保存协议/端口/path 结构化字段，不拼完整 URL——完整 URL 由前端
// 按当前访问上下文（http 直连 / https 反代 / fnOS 桌面壳）重建。
// 此前后端硬编码 "${host}" 占位拼 URL，浏览器按字面量解析导致
// 打开按钮 ERR_NAME_NOT_RESOLVED（deepseek-harness 实测）。
type Protocol struct {
	Type     string `json:"type"`
	URL      string `json:"url,omitempty"`
	OpenType string `json:"openType"`
	Proto    string `json:"proto,omitempty"`
	Port     int    `json:"port,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ListInstalled 拉取 daemon 的权威已安装列表。
func ListInstalled(ctx context.Context) ([]InstalledApp, error) {
	var resp struct {
		Total int `json:"total"`
		List  []struct {
			AppName     string       `json:"appName"`
			Name        string       `json:"name"`
			Version     string       `json:"version"`
			Source      string       `json:"source"`
			SourceID    string       `json:"sourceID"`
			Status      string       `json:"status"`
			Icon        string       `json:"icon"`
			Docker      bool         `json:"docker"`
			UpgradeInfo *UpgradeInfo `json:"upgradeInfo"`
			Control     struct {
				IsOpen      bool `json:"isOpen"`
				IsStartStop bool `json:"isStartStop"`
				IsUninstall bool `json:"isUninstall"`
				Upgrade     bool `json:"upgrade"`
			} `json:"control"`
			AppServiceInfo struct {
				Type string `json:"type"`
				URLs struct {
					Protocol string `json:"protocol"`
					Host     string `json:"host"`
					Port     string `json:"port"`
					Path     string `json:"path"`
				} `json:"urls"`
				OpenType    string `json:"openType"`
				ServiceName string `json:"serviceName"`
			} `json:"appServiceInfo"`
		} `json:"list"`
	}
	if err := daemonCallGet(ctx, routeAppInstalled, &resp); err != nil {
		return nil, err
	}
	apps := make([]InstalledApp, 0, len(resp.List))
	for _, d := range resp.List {
		a := InstalledApp{
			AppName: d.AppName, Name: d.Name, Version: d.Version, Source: d.Source,
			SourceID: d.SourceID,
			Status:   d.Status, Icon: d.Icon, Docker: d.Docker,
			IsOpen: d.Control.IsOpen, IsStartStop: d.Control.IsStartStop,
			IsUninstall: d.Control.IsUninstall, Upgrade: d.Control.Upgrade,
		}
		// upgradeInfo 恒在（无更新时 version 为空）；只有平台给出真实目标
		// 版本时才算「有更新」。
		if d.UpgradeInfo != nil && d.UpgradeInfo.Version != "" {
			ui := *d.UpgradeInfo
			a.UpgradeInfo = &ui
		}
		a.ServiceName = d.AppServiceInfo.ServiceName
		if d.AppServiceInfo.Type == "iframe" || d.AppServiceInfo.Type == "url" {
			proto := d.AppServiceInfo.URLs.Protocol
			if proto == "" {
				proto = "http"
			}
			path := d.AppServiceInfo.URLs.Path
			if path == "" {
				path = "/"
			}
			port := 0
			if d.AppServiceInfo.URLs.Port != "" {
				if p, err := strconv.Atoi(d.AppServiceInfo.URLs.Port); err == nil {
					port = p
				}
			}
			web := Protocol{
				Type:     d.AppServiceInfo.Type,
				OpenType: d.AppServiceInfo.OpenType,
				Proto:    proto,
				Port:     port,
				Path:     path,
			}
			// daemon 的 host 字段实测恒为空（=当前访问主机）；若某版本
			// 下发了真实主机（非占位符）则拼完整 URL，占位符/模板一律
			// 不下发（交由前端按当前上下文重建）。
			if h := d.AppServiceInfo.URLs.Host; h != "" && !strings.Contains(h, "${") {
				hostport := h
				if port > 0 {
					hostport = fmt.Sprintf("%s:%d", h, port)
				}
				web.URL = fmt.Sprintf("%s://%s%s", proto, hostport, path)
			}
			a.Web = web
		}
		apps = append(apps, a)
	}
	applyRealRunStates(ctx, apps)
	return apps, nil
}

// applyRealRunStates 校正 daemon 报 "start" 的应用：
// 该值只表示「用户没停用它」，不保证进程/容器真的在跑（实测 sub-store 容器
// 已退出 20h 仍报 start，而 magic-qinglong 正常运行时也报 start）。
// 探测出明确结论才改写为 running/stopped；判定不出保持原样，绝不误标。
func applyRealRunStates(ctx context.Context, apps []InstalledApp) {
	cand := make(map[string]bool, 8)
	for _, a := range apps {
		if a.Status == "start" {
			cand[a.AppName] = a.Docker
		}
	}
	if len(cand) == 0 {
		return
	}
	states := ProbeStartStates(ctx, cand)
	if len(states) == 0 {
		return
	}
	for i := range apps {
		if apps[i].Status != "start" {
			continue
		}
		if st, ok := states[apps[i].AppName]; ok && st != "" {
			apps[i].Status = st
		}
	}
}

// FindInstalled 查单个应用的已安装记录（未安装返回 nil, nil）。
func FindInstalled(ctx context.Context, appName string) (*InstalledApp, error) {
	list, err := ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].AppName == appName {
			return &list[i], nil
		}
	}
	return nil, nil
}
