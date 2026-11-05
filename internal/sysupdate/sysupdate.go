package sysupdate

// sysupdate 采集 fnOS 系统更新状态（只读、只提示，不安装）。
//
// 数据源（2026-11-04 实测，全部无需鉴权）：
//   - 当前版本：/usr/trim/etc/version（如 1.2.0800，644 root:root）
//   - 可用版本：GET https://apiv2-liveupdate.fygonas.com/
//     返回 {branchId, kernel{version,size,description}, packages[{packageName:"trim",version,...}]}
//     该接口给的是**稳定分支**最新包；面板「系统更新」里偶尔出现的更高版本属灰度/内测通道
//     （走 WS liveupdate.check + 会话密钥签名），公开接口尚未发布时这里不会误报。
//   - 当前内核：/proc/sys/kernel/osrelease（如 6.18.18.c1126-trim）
//
// 设计：失败静默降级（返回 ok=false），不阻塞、不影响 Moo 主流程；12h 缓存。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"moo/internal/source"
)

// DefaultAPI 是 fnOS 更新服务的公开入口（面板前端同源，无鉴权）。
const DefaultAPI = "https://apiv2-liveupdate.fygonas.com/"

// DefaultVersionFile 是 fnOS 当前系统版本文件。
const DefaultVersionFile = "/usr/trim/etc/version"

// DefaultKernelFile 是当前内核版本（/proc 虚拟文件）。
const DefaultKernelFile = "/proc/sys/kernel/osrelease"

// Status 是一次采集结果。
type Status struct {
	Current       string    `json:"current"`
	Latest        string    `json:"latest"`
	Available     bool      `json:"available"`
	KernelCurrent string    `json:"kernel_current"`
	KernelLatest  string    `json:"kernel_latest"`
	Notes         []string  `json:"notes"`
	Branch        int       `json:"branch"`
	CheckedAt     time.Time `json:"checked_at"`
	OK            bool      `json:"ok"`
	Err           string    `json:"err,omitempty"`
}

type apiResp struct {
	BranchID int `json:"branchId"`
	Kernel   struct {
		Version     string `json:"version"`
		Description string `json:"description"`
		Size        string `json:"size"`
	} `json:"kernel"`
	Packages []struct {
		PackageName string `json:"packageName"`
		Version     string `json:"version"`
		Size        string `json:"size"`
		Description string `json:"description"`
	} `json:"packages"`
}

// Collector 带 12h 缓存与单飞，供 API / 定时器共用。
type Collector struct {
	API         string
	VersionFile string
	KernelFile  string
	TTL         time.Duration
	HTTP        *http.Client

	mu     sync.Mutex
	cached Status
	at     time.Time
}

// New 创建采集器；空字段回落默认值。
func New() *Collector {
	c := &Collector{
		API:         DefaultAPI,
		VersionFile: DefaultVersionFile,
		KernelFile:  DefaultKernelFile,
		TTL:         12 * time.Hour,
		HTTP:        &http.Client{Timeout: 20 * time.Second},
	}
	// 便于独立实例验收与镜像切换（生产默认值不变）。
	if v := strings.TrimSpace(os.Getenv("MOO_SYSUPDATE_API")); v != "" {
		c.API = v
	}
	if v := strings.TrimSpace(os.Getenv("MOO_SYSUPDATE_VERSION_FILE")); v != "" {
		c.VersionFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MOO_SYSUPDATE_KERNEL_FILE")); v != "" {
		c.KernelFile = v
	}
	return c
}

// Get 返回缓存（未过期）或重新采集。force=true 忽略缓存。
func (c *Collector) Get(ctx context.Context, force bool) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && !c.at.IsZero() && time.Since(c.at) < c.TTL {
		return c.cached
	}
	st := c.fetch(ctx)
	c.cached, c.at = st, time.Now()
	return st
}

func (c *Collector) fetch(ctx context.Context) Status {
	st := Status{CheckedAt: time.Now()}
	cur, err := readTrimmed(c.VersionFile)
	if err != nil {
		st.Err = "读取当前版本失败: " + err.Error()
		return st
	}
	st.Current = cur
	if k, err := readTrimmed(c.KernelFile); err == nil {
		st.KernelCurrent = normalizeKernel(k)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API, nil)
	if err != nil {
		st.Err = err.Error()
		return st
	}
	req.Header.Set("User-Agent", "moo-sysupdate")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		st.Err = "请求更新服务失败: " + err.Error()
		return st
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		st.Err = fmt.Sprintf("更新服务 HTTP %d", resp.StatusCode)
		return st
	}
	var ar apiResp
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&ar); err != nil {
		st.Err = "解析更新服务响应失败: " + err.Error()
		return st
	}
	st.Branch = ar.BranchID
	st.KernelLatest = normalizeKernel(ar.Kernel.Version)
	for _, p := range ar.Packages {
		if strings.EqualFold(p.PackageName, "trim") && p.Version != "" {
			st.Latest = p.Version
			if d := firstLines(p.Description, 6); len(d) > 0 {
				st.Notes = d
			}
			break
		}
	}
	if st.Latest == "" {
		st.Err = "更新服务未返回 trim 包版本"
		return st
	}
	// 只提示「稳定分支确实比当前新」的情况；灰度版本不在该接口，不会误报。
	st.Available = source.IsNewer(st.Latest, st.Current)
	st.OK = true
	return st
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// firstLines 取说明的前 n 个非空段落（去 markdown 噪音），用于通知摘要。
func firstLines(s string, n int) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(strings.TrimLeft(line, "-*• "))
		if t == "" {
			continue
		}
		if len(t) > 120 {
			t = t[:120] + "…"
		}
		out = append(out, t)
		if len(out) >= n {
			break
		}
	}
	return out
}

// normalizeKernel 去掉包版本后缀（`6.18.18.c1126-trim#1126` → `6.18.18.c1126-trim`），
// 让 /proc 与更新服务的两种写法可比，避免把「同内核不同构建号」误报成内核更新。
func normalizeKernel(v string) string {
	if i := strings.IndexByte(v, '#'); i > 0 {
		return strings.TrimSpace(v[:i])
	}
	return strings.TrimSpace(v)
}
