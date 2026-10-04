package api

// Docker 系统镜像源应用（0.6.269，M4 余项「Docker 优选接入」）：
// 把设置里选的 Docker 加速源真正写进 /etc/docker/daemon.json 的
// registry-mirrors 并重启 Docker——此前「Docker 镜像加速」仅展示/测速，
// 实际拉取走系统级镜像源，优选结果落不了地。
//
// 安全设计（系统级变更，必须可回滚）：
//   - 仅管理员（requireAdmin）；
//   - 写前备份现有 daemon.json（保留 5 代，存数据目录）；
//   - 现有文件 JSON 损坏时拒绝写入（不吞掉用户的配置）；
//   - 只改 registry-mirrors 一个键，其余配置原样保留；
//   - 写入走临时文件+原子 rename；
//   - 重启失败或状态异常 → 自动恢复备份并再次重启，回滚后报错；
//   - 「直连」= 移除 registry-mirrors（回到 Docker Hub 直连）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"moo/internal/config"
)

// dockerDaemonJSON 是 Docker 守护进程配置的标准位置。
const dockerDaemonJSON = "/etc/docker/daemon.json"

// dockerMirrorStateFile 记录最近一次应用（状态徽章/回滚提示用）。
func dockerMirrorStateFile() string {
	return filepath.Join(dataDirOf(nil), "docker-mirror", "state.json")
}

type dockerMirrorState struct {
	AppliedAt    string   `json:"applied_at"`
	Mirror       string   `json:"mirror"`
	Mirrors      []string `json:"mirrors"`
	BackupFile   string   `json:"backup_file,omitempty"`
	DockerActive bool     `json:"docker_active"`
}

// dockerRegistryMirrors 读当前 daemon.json 的 registry-mirrors（文件缺失=空）。
// 返回 (mirrors, fileExists, err)；JSON 损坏时 err 非 nil。
func dockerRegistryMirrors() ([]string, bool, error) {
	b, err := os.ReadFile(dockerDaemonJSON)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, true, fmt.Errorf("daemon.json 不是合法 JSON（请先手动修复）: %w", err)
	}
	var out []string
	if v, ok := m["registry-mirrors"]; ok {
		if arr, ok := v.([]any); ok {
			for _, it := range arr {
				if s, ok := it.(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out, true, nil
}

// dockerMirrorStatus GET /api/settings/docker-mirror/status。
func (s *Server) dockerMirrorStatus(w http.ResponseWriter, r *http.Request) {
	mirrors, exists, err := dockerRegistryMirrors()
	out := map[string]any{
		"daemon_json_exists": exists,
		"mirrors":            mirrors,
		"docker_active":      dockerServiceActive(),
	}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, out)
		return
	}
	var st dockerMirrorState
	if b, e := os.ReadFile(dockerMirrorStateFile()); e == nil {
		_ = json.Unmarshal(b, &st)
		out["last_applied"] = st
	}
	// applied = 当前 daemon.json 恰好等于最近一次应用的值（用户手改过会失配）
	out["applied"] = st.AppliedAt != "" && mirrorsEqual(mirrors, st.Mirrors)
	writeJSON(w, out)
}

// dockerMirrorApply POST /api/settings/docker-mirror/apply。
func (s *Server) dockerMirrorApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mirror    string `json:"mirror"`
		CustomURL string `json:"custom_url"`
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	in.Mirror = strings.TrimSpace(in.Mirror)
	in.CustomURL = strings.TrimSpace(in.CustomURL)

	var url string
	switch in.Mirror {
	case "direct":
		url = "" // 直连 = 清空 registry-mirrors
	case "custom":
		if in.CustomURL == "" {
			writeErr(w, http.StatusBadRequest, errors.New("自定义镜像地址不能为空"))
			return
		}
		url = strings.TrimRight(in.CustomURL, "/") + "/"
	default:
		url = config.MirrorURL(config.DockerMirrorOptions(), in.Mirror)
		if url == "" {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("未知镜像: %s", in.Mirror))
			return
		}
	}

	if err := applyDockerMirrors(url); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{
		"ok":       true,
		"mirrors":  mirrorsForState(url),
		"applied":  true,
		"message":  "已应用，Docker 已重启",
		"restarted": dockerServiceActive(),
	})
}

// applyDockerMirrors 备份 → 合并写 daemon.json → 重启 → 验证；失败自动回滚。
func applyDockerMirrors(url string) error {
	// 1. 读现状（损坏 = 拒绝，不动用户文件）
	oldBytes, err := os.ReadFile(dockerDaemonJSON)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var cfg map[string]any
	if len(oldBytes) > 0 {
		if err := json.Unmarshal(oldBytes, &cfg); err != nil {
			return fmt.Errorf("现有 %s 不是合法 JSON，为保护已有配置已放弃写入（请先手动修复）", dockerDaemonJSON)
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	// 2. 备份（保留 5 代）
	backupFile, err := backupDockerDaemonJSON(oldBytes)
	if err != nil {
		return fmt.Errorf("备份失败: %w", err)
	}

	// 3. 合并写（临时文件 + 原子 rename）
	if url == "" {
		delete(cfg, "registry-mirrors")
	} else {
		cfg["registry-mirrors"] = []any{url}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dockerDaemonJSON), 0o755); err != nil {
		return err
	}
	tmp := dockerDaemonJSON + ".moo-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dockerDaemonJSON); err != nil {
		os.Remove(tmp)
		// rename 失败 → 回滚（文件本未变，清 tmp 即可）
		return err
	}

	// 4. 重启 Docker
	if err := restartDockerService(); err != nil {
		_ = restoreDockerDaemonJSON(backupFile, oldBytes)
		_ = restartDockerService()
		return fmt.Errorf("Docker 重启失败（已回滚）: %w", err)
	}

	// 5. 验证：daemon 起来了 + 配置可读回
	active := false
	for i := 0; i < 6; i++ {
		time.Sleep(1500 * time.Millisecond)
		if dockerServiceActive() {
			active = true
			break
		}
	}
	if !active {
		_ = restoreDockerDaemonJSON(backupFile, oldBytes)
		_ = restartDockerService()
		return errors.New("Docker 重启后未恢复运行（已回滚原配置）")
	}
	got, _, rerr := dockerRegistryMirrors()
	if rerr != nil {
		_ = restoreDockerDaemonJSON(backupFile, oldBytes)
		_ = restartDockerService()
		return fmt.Errorf("写入后读回失败（已回滚）: %w", rerr)
	}
	if !mirrorsEqual(got, mirrorsForState(url)) {
		_ = restoreDockerDaemonJSON(backupFile, oldBytes)
		_ = restartDockerService()
		return fmt.Errorf("写入未生效（已回滚）: 期望 %v 实际 %v", url, got)
	}

	// 6. 记状态
	st := dockerMirrorState{
		AppliedAt:    time.Now().Format(time.RFC3339),
		Mirror:       "mirrors=" + strings.Join(mirrorsForState(url), ","),
		Mirrors:      mirrorsForState(url),
		BackupFile:   backupFile,
		DockerActive: true,
	}
	if b, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.MkdirAll(filepath.Dir(dockerMirrorStateFile()), 0o755)
		_ = os.WriteFile(dockerMirrorStateFile(), b, 0o600)
	}
	log.Printf("[docker-mirror] 已应用系统镜像源: %v（备份 %s）", st.Mirrors, backupFile)
	return nil
}

// backupDockerDaemonJSON 备份 daemon.json 到数据目录（保留 5 代），返回最新备份路径。
func backupDockerDaemonJSON(oldBytes []byte) (string, error) {
	dir := filepath.Join(dataDirOf(nil), "docker-mirror")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("daemon.json.bak-%s", time.Now().Format("20060102-150405"))
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, oldBytes, 0o600); err != nil {
		return "", err
	}
	// 清理旧备份，只留 5 代
	entries, _ := os.ReadDir(dir)
	var baks []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "daemon.json.bak-") {
			baks = append(baks, e.Name())
		}
	}
	for i := 0; i < len(baks)-5; i++ {
		os.Remove(filepath.Join(dir, baks[i]))
	}
	return p, nil
}

// restoreDockerDaemonJSON 回滚：有备份写备份，否则写回原字节（都没有=删除文件还原"不存在"）。
func restoreDockerDaemonJSON(backupFile string, oldBytes []byte) error {
	var b []byte
	switch {
	case backupFile != "":
		b, _ = os.ReadFile(backupFile)
	case oldBytes != nil:
		b = oldBytes
	default:
		// 原本不存在 → 删除
		if err := os.Remove(dockerDaemonJSON); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(dockerDaemonJSON, b, 0o644)
}

func mirrorsForState(url string) []string {
	if url == "" {
		return nil
	}
	return []string{url}
}

func mirrorsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dockerServiceActive systemctl is-active docker（systemctl 缺失时回退 service）。
func dockerServiceActive() bool {
	if out, err := exec.Command("systemctl", "is-active", "docker").Output(); err == nil {
		return strings.TrimSpace(string(out)) == "active"
	}
	cmd := exec.Command("service", "docker", "status")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return err == nil
}

// restartDockerService 重启 Docker（systemctl 优先，回退 service）。
func restartDockerService() error {
	if err := exec.Command("systemctl", "restart", "docker").Run(); err == nil {
		return nil
	}
	return exec.Command("service", "docker", "restart").Run()
}
