package platform

// 0.6.348：daemon 的已装列表把应用状态分成 running / stopped / start / nostart。
// 其中 `start` 的语义是「已启用（用户没停用）」，**不代表进程真的在跑** ——
// 实测 sub-store 的容器 Exited 20 小时、xunlei/Mahjong 根本没有进程，daemon 仍报 start；
// 反过来 fndepot/magic-qinglong/kerkerker 确实在跑，daemon 也报 start。
// 结果：Moo 列表只能把它们显示成「已安装」，用户看不出到底有没有在运行，
// 而且这类卡既不给「打开」也不给「启动」（0.6.342 遗留死角）。
//
// 本文件补一次「真实运行态探测」，只对 daemon 报 start 的应用执行：
//   - Docker 应用：查 docker.sock 容器列表，按容器名/挂载路径/compose 工作目录匹配。
//   - 其它应用：扫 /proc/*/cmdline，按应用目录（@appcenter/@appdata//var/apps）匹配。
// 探测不出明确结论时**保持原状**（不改写 status），宁可少标也不误标。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	dockerSocketPath = "/var/run/docker.sock"
	runProbeTTL      = 10 * time.Second
)

// runProbeCache 缓存最近一次探测结果，避免每次列表构建都扫 /proc + docker。
var runProbeCache struct {
	mu  sync.Mutex
	at  time.Time
	val map[string]string
}

// InvalidateRunProbe 清空探测缓存（应用启停后调用，避免 10s 内看到旧状态）。
func InvalidateRunProbe() {
	runProbeCache.mu.Lock()
	runProbeCache.val = nil
	runProbeCache.at = time.Time{}
	runProbeCache.mu.Unlock()
}

// dockerContainer 是 /containers/json 里我们关心的最小字段集。
type dockerContainer struct {
	Names  []string `json:"Names"`
	State  string   `json:"State"`
	Mounts []struct {
		Source string `json:"Source"`
	} `json:"Mounts"`
	Labels map[string]string `json:"Labels"`
}

// listDockerContainers 读 docker socket 列举全部容器（含已退出）。
func listDockerContainers(ctx context.Context) ([]dockerContainer, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(c context.Context, _, _ string) (net.Conn, error) {
				return d.DialContext(c, "unix", dockerSocketPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=1", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker api status %d", resp.StatusCode)
	}
	var out []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// appDirMarkers 是第三方应用可能出现的安装/数据目录标记。
// 进程命令行与容器挂载里出现这些前缀即视为「属于该应用」。
var appDirMarkers = []string{"/@appcenter/", "/@appdata/", "/var/apps/"}

// containerMatchesApp 判断容器是否属于给定应用。
func containerMatchesApp(c dockerContainer, app string) bool {
	a := strings.ToLower(app)
	if a == "" {
		return false
	}
	for _, n := range c.Names {
		n = strings.ToLower(strings.TrimPrefix(n, "/"))
		if n == a || strings.HasPrefix(n, a+"-") || strings.HasPrefix(n, a+"_") {
			return true
		}
	}
	for _, m := range c.Mounts {
		s := strings.ToLower(m.Source)
		for _, base := range appDirMarkers {
			if strings.Contains(s, base+a+"/") || strings.HasSuffix(s, base+a) {
				return true
			}
		}
	}
	if wd := strings.ToLower(c.Labels["com.docker.compose.project.working_dir"]); wd != "" {
		for _, base := range appDirMarkers {
			if strings.Contains(wd, base+a+"/") || strings.HasSuffix(wd, base+a) {
				return true
			}
		}
	}
	return false
}

// pathMentionsApp 判断一段文本（进程 cmdline 或挂载路径）是否指向该应用目录。
func pathMentionsApp(text, app string) bool {
	if app == "" {
		return false
	}
	t := strings.ToLower(text)
	a := strings.ToLower(app)
	for _, base := range appDirMarkers {
		if strings.Contains(t, base+a+"/") || strings.HasSuffix(t, base+a) {
			return true
		}
	}
	return false
}

// scanProcesses 扫 /proc 找出命令行里指向应用目录的进程。
func scanProcesses(ctx context.Context, wanted map[string]bool) map[string]bool {
	found := make(map[string]bool, len(wanted))
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return found
	}
	// cmdline 里以 NUL 分隔，整体拼成一个字符串即可用于路径判断。
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		cmdline := strings.ReplaceAll(string(raw), "\x00", " ")
		for app := range wanted {
			if !found[app] && pathMentionsApp(cmdline, app) {
				found[app] = true
			}
		}
	}
	return found
}

// probeRunStates 对给定应用集合做一次真实运行态探测。
// 返回 appname -> "running"/"stopped"；**判定不出明确结论的应用不会出现在结果里**。
func probeRunStates(ctx context.Context, candidates map[string]bool, dockerApps map[string]bool) map[string]string {
	result := make(map[string]string, len(candidates))
	if len(candidates) == 0 {
		return result
	}
	containers, cerr := listDockerContainers(ctx)
	// 先做一次进程扫描（覆盖非 Docker 应用，同时作为 Docker 匹配失败时的兜底）
	procs := scanProcesses(ctx, candidates)

	for app := range candidates {
		if cerr == nil {
			matched := 0
			running := false
			for _, c := range containers {
				if !containerMatchesApp(c, app) {
					continue
				}
				matched++
				if strings.EqualFold(c.State, "running") {
					running = true
				}
			}
			if matched > 0 {
				if running {
					result[app] = "running"
				} else {
					result[app] = "stopped"
				}
				continue
			}
		}
		// Docker 里没有明确对应容器（或 docker 不可用）→ 用进程证据判定。
		if procs[app] {
			result[app] = "running"
			continue
		}
		// 非 Docker 应用没有任何进程 = 确实没在跑；Docker 应用没匹配到容器时
		// 无法确定（容器名可能与应用名无关联），保持原状不误标。
		if !dockerApps[app] {
			result[app] = "stopped"
		}
	}
	return result
}

// ProbeStartStates 是对外入口：输入 daemon 报 start 的应用（appname -> 是否 docker），
// 返回可采信的运行态。结果带 10s 缓存；应用启停后请调用 InvalidateRunProbe。
func ProbeStartStates(ctx context.Context, apps map[string]bool) map[string]string {
	if len(apps) == 0 {
		return nil
	}
	runProbeCache.mu.Lock()
	defer runProbeCache.mu.Unlock()
	if runProbeCache.val != nil && time.Since(runProbeCache.at) < runProbeTTL {
		out := make(map[string]string, len(apps))
		for app := range apps {
			if st, ok := runProbeCache.val[app]; ok {
				out[app] = st
			}
		}
		return out
	}
	candidates := make(map[string]bool, len(apps))
	dockerApps := make(map[string]bool, len(apps))
	for app, isDocker := range apps {
		candidates[app] = true
		if isDocker {
			dockerApps[app] = true
		}
	}
	val := probeRunStates(ctx, candidates, dockerApps)
	runProbeCache.val = val
	runProbeCache.at = time.Now()
	return val
}
