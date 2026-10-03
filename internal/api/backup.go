package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/notify"
)

// 设置备份：config.json 全量快照（源列表/加速/面板账号/下载目录/本组备份设置
// 全在内）+ 商店版本，写到备份目录 moo-backup-YYYYMMDD-HHMMSS.json。
// 目录可指到 /volN 下任意共享目录，供外部手机/电脑同步备份走。
//
// 自动备份：StartBackupLoop 每 10 分钟检查一次，到期即写（沿用「读全量 cfg
// 原子写盘」模式）；同目录保留最近 20 份，更早自动淘汰。

const backupRetention = 20

var backupNameRe = regexp.MustCompile(`^moo-backup-\d{8}-\d{6}\.json$`)

// BackupEntry 是一个备份文件条目（列表视图）。
type BackupEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModAt   string `json:"mod_at"`
	Created string `json:"created,omitempty"` // 快照内的生成时间（可读）
}

// BackupsResponse 是 GET /api/backups 的响应。
type BackupsResponse struct {
	Dir                 string        `json:"dir"`
	Auto                bool          `json:"auto"`
	IntervalDays        int           `json:"interval_days"`
	LastBackupAt        string        `json:"last_backup_at,omitempty"`
	Files               []BackupEntry `json:"files"`
	AppCache            AppCacheStats `json:"app_cache"`
	CacheCleanDays      int           `json:"cache_clean_days"`
	CacheCleanEveryDays int           `json:"cache_clean_every_days"`
}

// AppCacheStats 是 Moo 应用缓存统计（图标/README/下载量统计等，
// 位于 <DataDir>/cache；已下载 FPK 不在其列，永不被自动清理）。
type AppCacheStats struct {
	Dir        string `json:"dir"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
	OldCount   int    `json:"old_count"` // 超过保留天数的文件数（cleanDays<=0 时为 0）
	OldBytes   int64  `json:"old_bytes"`
}

// BackupSnapshot 是备份文件的结构（config 全量 + 元数据）。
type BackupSnapshot struct {
	BackupTime string          `json:"backup_time"`
	Version    string          `json:"version"`
	Config     json.RawMessage `json:"config"`
}

// backupDirOf 生效的备份目录（未配置 = 应用数据目录下 backups/）。
func (s *Server) backupDirOf() string {
	d := strings.TrimSpace(s.Cfg.BackupDir)
	if d == "" {
		return filepath.Join(dataDirOf(s), "backups")
	}
	return d
}

// backupIntervalDaysOf 生效的自动备份周期（天；未配置 = 7 天）。
func (s *Server) backupIntervalDaysOf() int {
	d := s.Cfg.BackupIntervalDays
	if d <= 0 {
		d = 7
	}
	if d > 30 {
		d = 30
	}
	return d
}

// writeBackup 写一份新快照；返回文件名。原子写（临时文件 + rename）。
// maskedConfigForBackup 0.6.144 安全审计 P1-1：备份快照剔除凭据——
// 面板口令置空、通知渠道敏感参数（webhook key 等）脱敏。
// 恢复此类备份后渠道 Webhook 需重新填写（面板账号同样需在设置页重填）。
func (s *Server) maskedConfigForBackup() config.Config {
	snap := *s.Cfg
	snap.PanelPassword = ""
	// 0.6.215 修复（潜伏自 0.6.144 的别名 bug）：snap := *s.Cfg 的 NotifyChannels
	// 与 live 共享底层数组，直接在 snap 上写掩码值会把 live 内存中的渠道凭据
	// 就地改成 **** 掩码——之后任何一次 config 落盘都会把掩码写进磁盘，
	// 企微/钉钉等推送渠道随即静默失效（0.6.215 测试机实锤复现）。
	// 先整体拷贝切片再掩码，切断别名。
	snap.NotifyChannels = make([]config.NotifyChannel, len(s.Cfg.NotifyChannels))
	copy(snap.NotifyChannels, s.Cfg.NotifyChannels)
	for i := range snap.NotifyChannels {
		snap.NotifyChannels[i] = notify.Masked(snap.NotifyChannels[i])
	}
	return snap
}

func (s *Server) writeBackup() (string, error) {
	dir := s.backupDirOf()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("备份目录不可写: %w", err)
	}
	cfgBytes, err := json.MarshalIndent(s.maskedConfigForBackup(), "", "  ")
	if err != nil {
		return "", err
	}
	now := time.Now()
	snap := BackupSnapshot{
		BackupTime: now.Format(time.RFC3339),
		Version:    s.Version,
		Config:     cfgBytes,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	name := "moo-backup-" + now.Format("20060102-150405") + ".json"
	dst := filepath.Join(dir, name)
	tmp := dst + ".tmp"
	// 0.6.144 安全审计 P1-1：备份文件 0600（原 0644）——备份含全量配置，是
	// @appdata 内最容易被「文件吐出类」缺陷带走的落点
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", err
	}
	s.pruneBackups(dir)
	log.Printf("[backup] 已写入 %s (%d B)", name, len(data))
	return name, nil
}

// pruneBackups 保留最近 backupRetention 份快照，其余删除；
// .tmp 写入残留（中断遗留）一律清除。
func (s *Server) pruneBackups(dir string) {
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(e.Name(), ".tmp"):
			os.Remove(filepath.Join(dir, e.Name()))
		case backupNameRe.MatchString(e.Name()):
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 文件名含时间戳，字典序 = 时间序
	for i, n := range names {
		if i < len(names)-backupRetention {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

// listBackups 备份目录列表（新→旧）。
func (s *Server) listBackups() []BackupEntry {
	dir := s.backupDirOf()
	entries, _ := os.ReadDir(dir)
	out := make([]BackupEntry, 0, len(entries))
	var lastMod time.Time
	for _, e := range entries {
		if e.IsDir() || !backupNameRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		be := BackupEntry{Name: e.Name(), Size: fi.Size(), ModAt: fi.ModTime().Format(time.RFC3339)}
		if fi.ModTime().After(lastMod) {
			lastMod = fi.ModTime()
		}
		// 快照内时间（文件被同步工具拷贝时 mtime 会漂移，以内容为准）
		if data, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
			var snap BackupSnapshot
			if json.Unmarshal(data, &snap) == nil && snap.BackupTime != "" {
				be.Created = snap.BackupTime
			}
		}
		out = append(out, be)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// lastBackupAtOf 最近一份备份的时间（无 = 空）。
func (s *Server) lastBackupAtOf() string {
	files := s.listBackups()
	if len(files) == 0 {
		return ""
	}
	if files[0].Created != "" {
		return files[0].Created
	}
	return files[0].ModAt
}

// cacheCleanEveryDaysOf 清理周期（天；未配置 = 每天）。
func (s *Server) cacheCleanEveryDaysOf() int {
	d := s.Cfg.CacheCleanEveryDays
	if d <= 0 {
		d = 1
	}
	if d > 30 {
		d = 30
	}
	return d
}

// appCacheDir Moo 应用缓存目录（图标/README/下载量统计等；
// 已下载 FPK 在 <DataDir>/downloads，绝不在清理范围内）。
func (s *Server) appCacheDir() string {
	return filepath.Join(dataDirOf(s), "cache")
}

// appCacheStatsOf 应用缓存统计（递归）。
func (s *Server) appCacheStatsOf() AppCacheStats {
	dir := s.appCacheDir()
	st := AppCacheStats{Dir: dir}
	days := s.Cfg.CacheCleanDays
	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		st.FileCount++
		st.TotalBytes += fi.Size()
		if days > 0 && !fi.ModTime().After(cutoff) {
			st.OldCount++
			st.OldBytes += fi.Size()
		}
		return nil
	})
	return st
}

// cleanAppCache 删除应用缓存中超过配置保留天数的文件（自动清理用）。
// 只动 <DataDir>/cache；已下载 FPK、已装应用、配置均不受影响。
func (s *Server) cleanAppCache() (int, int64, error) {
	days := s.Cfg.CacheCleanDays
	if days <= 0 {
		return 0, 0, errors.New("未启用自动清理")
	}
	return s.cleanAppCacheWithDays(days)
}

// cleanAppCacheWithDays 按指定天数阈值清理（手动清理与自动清理共用）。
// days=0 表示强制清空：不设年龄阈值，删掉整个缓存目录里的文件（仍只动
// <DataDir>/cache，已下载 FPK 不受影响）。
func (s *Server) cleanAppCacheWithDays(days int) (int, int64, error) {
	if days < 0 {
		return 0, 0, errors.New("保留天数无效")
	}
	var cutoff time.Time
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}
	dir := s.appCacheDir()
	removed, bytes := 0, int64(0)
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		if days > 0 && fi.ModTime().After(cutoff) {
			return nil
		}
		if os.Remove(p) == nil {
			removed++
			bytes += fi.Size()
		}
		return nil
	})
	// 回收空目录
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || !e.IsDir() || p == dir {
			return nil
		}
		if entries, err := os.ReadDir(p); err == nil && len(entries) == 0 {
			os.Remove(p)
		}
		return nil
	})
	if removed > 0 {
		if days > 0 {
			log.Printf("[backup] 应用缓存清理: 删除 %d 个 >%dd 的文件, 释放 %d B", removed, days, bytes)
		} else {
			log.Printf("[backup] 应用缓存强制清空: 删除全部 %d 个文件, 释放 %d B", removed, bytes)
		}
	}
	return removed, bytes, nil
}

// ---- HTTP handlers ----

func (s *Server) getBackups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, BackupsResponse{
		Dir:                 s.backupDirOf(),
		Auto:                s.Cfg.BackupAuto,
		IntervalDays:        s.backupIntervalDaysOf(),
		LastBackupAt:        s.lastBackupAtOf(),
		Files:               s.listBackups(),
		AppCache:            s.appCacheStatsOf(),
		CacheCleanDays:      s.Cfg.CacheCleanDays,
		CacheCleanEveryDays: s.cacheCleanEveryDaysOf(),
	})
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	name, err := s.writeBackup()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if !backupNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, errors.New("非法备份文件名"))
		return
	}
	if err := os.Remove(filepath.Join(s.backupDirOf(), name)); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// cleanCacheHandler 手动清理应用缓存。body 可带 {"days":N} 指定本次阈值
// （手动清理不依赖自动清理开关）；缺省用配置的保留天数。
// {"force":true} 强制清空全部缓存文件（不设年龄阈值；仍只动 cache 目录）。
func (s *Server) cleanCacheHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Days  int  `json:"days"`
		Force bool `json:"force"`
	}
	_ = jsonDecode(r, &in)
	if in.Force {
		removed, bytes, err := s.cleanAppCacheWithDays(0)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "force": true, "removed": removed, "freed_bytes": bytes})
		return
	}
	days := in.Days
	if days <= 0 {
		days = s.Cfg.CacheCleanDays
	}
	if days <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("请先填写保留天数"))
		return
	}
	if days > 30 {
		days = 30
	}
	removed, bytes, err := s.cleanAppCacheWithDays(days)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "removed": removed, "freed_bytes": bytes})
}

// downloadBackup 让浏览器把备份文件下载到手机/电脑（外部存储设备通道）。
func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if !backupNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, errors.New("非法备份文件名"))
		return
	}
	f, err := os.Open(filepath.Join(s.backupDirOf(), name))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = io.Copy(w, f)
}

// restoreBackup 用指定备份覆盖当前配置，随后重启服务使全部子模块
// （源管理器/面板账号/下载目录/加速引擎）吃新配置。
// 重启走 appcenter-cli（与 FPK 生命周期一致）；appcenter 不在环境里时
// （本地开发）仅落盘配置并返回提示。
// reinstateSecrets 恢复前把当前 live 凭据回填进掩码备份快照（0.6.215 折中 P0）：
//   - 面板口令：备份恒为置空（掩码产物）→ 恢复后保持 live 值不变。
//     面板口令 = 主机 root 口令，官方源同步依赖它，一旦恢复抹空即断；
//     备份本就不应承载口令的「回滚/抹除」语义，故口令永远取 live。
//   - 渠道敏感参数：备份中 **** 掩码的，按渠道 ID 匹配 live，用 live 真值替换
//     （与 notifyChannelsPost 的「**** 回填」语义一致，0.6.144）。
// 只改掩码位，不触碰备份里的其他配置值。
func (s *Server) reinstateSecrets(restored *config.Config) {
	if restored.PanelPassword == "" && s.Cfg.PanelPassword != "" {
		restored.PanelPassword = s.Cfg.PanelPassword
	}
	if len(restored.NotifyChannels) == 0 {
		return
	}
	liveByID := make(map[string]config.NotifyChannel, len(s.Cfg.NotifyChannels))
	for _, c := range s.Cfg.NotifyChannels {
		liveByID[c.ID] = c
	}
	for i := range restored.NotifyChannels {
		lc, ok := liveByID[restored.NotifyChannels[i].ID]
		if !ok {
			continue
		}
		for k, v := range restored.NotifyChannels[i].Params {
			if strings.HasPrefix(v, "****") {
				if sv, ok2 := lc.Params[k]; ok2 && sv != "" {
					restored.NotifyChannels[i].Params[k] = sv
				}
			}
		}
	}
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("name"))
	if !backupNameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, errors.New("非法备份文件名"))
		return
	}
	data, err := os.ReadFile(filepath.Join(s.backupDirOf(), name))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var snap BackupSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("备份文件损坏: %v", err))
		return
	}
	var restored config.Config
	if err := json.Unmarshal(snap.Config, &restored); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("备份内容无效: %v", err))
		return
	}
	// 0.6.215 折中 P0：恢复前把 live 凭据回填进掩码备份——备份快照是脱敏的
	// （0.6.144 P1-1：面板口令置空、渠道敏感参数 **** 掩码）。若直接整份覆盖，
	// 一次「备份→恢复」循环就会抹掉 live 面板口令（官方源同步断）与渠道
	// Webhook（通知断）。回填后口令/密钥恒等于当前值，恢复不破坏现有功能。
	s.reinstateSecrets(&restored)
	// 目录存在性兜底：下载目录/备份目录不存在则补建（恢复跨机场景）。
	for _, d := range []string{restored.DownloadDir, restored.BackupDir} {
		if d == "" {
			continue
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("目录不可用: %s", d))
				return
			}
		}
	}
	// 原子落盘 + 热替换内存配置
	newCfg := restored
	newCfg.WebPort = s.Cfg.WebPort // 端口跟随当前部署，不被备份覆盖
	if err := newCfg.Save(dataDirOf(s)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	*s.Cfg = newCfg
	s.invalidateCatalog()
	log.Printf("[backup] 已用 %s 恢复配置", name)

	restarting := s.scheduleRestart()
	writeJSON(w, map[string]any{"ok": true, "restarting": restarting})
}

// scheduleRestart 脱离本进程异步重启服务（appcenter-cli restart moo），
// 先等 600ms 让 HTTP 响应刷出。返回是否真的会重启。
func (s *Server) scheduleRestart() bool {
	bin := ""
	for _, c := range []string{"appcenter-cli", "/usr/bin/appcenter-cli", "/usr/trim/bin/appcenter-cli"} {
		if p, err := exec.LookPath(c); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		log.Printf("[backup] 未找到 appcenter-cli，配置已恢复但需手动重启服务生效")
		return false
	}
	go func() {
		time.Sleep(600 * time.Millisecond)
		cmd := exec.Command("setsid", bin, "restart", "moo")
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Start() // 完全脱离：本进程随后被 restart 杀掉也不影响
		log.Printf("[backup] 已触发服务重启")
	}()
	return true
}

// StartBackupLoop 自动备份 + 应用缓存自动清理：
// 每 10 分钟检查一次——自动备份开启且距上次快照超过周期（天）即写；
// 缓存清理开启且距上次清理超过 24h 即清（每天至多一次，只动 app cache）。
func (s *Server) StartBackupLoop(ctx context.Context) {
	lastClean := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
		if ctx.Err() != nil {
			return
		}
		if s.Cfg.BackupAuto {
			last := s.lastBackupAtOf()
			due := true
			if last != "" {
				if t, err := time.Parse(time.RFC3339, last); err == nil {
					due = time.Since(t) >= time.Duration(s.backupIntervalDaysOf())*24*time.Hour
				}
			}
			if due {
				if name, err := s.writeBackup(); err != nil {
					log.Printf("[backup] 自动备份失败: %v", err)
					s.logNotify("backup_error", "自动备份失败："+err.Error(), false)
				} else {
					s.logNotify("backup_done", "自动备份完成："+name, true)
				}
			}
		}
		if s.Cfg.CacheCleanDays > 0 && (lastClean.IsZero() ||
			time.Since(lastClean) >= time.Duration(s.cacheCleanEveryDaysOf())*24*time.Hour) {
			lastClean = time.Now()
			if removed, bytes, err := s.cleanAppCache(); err == nil && removed > 0 {
				log.Printf("[backup] 自动清理应用缓存: %d 个, 释放 %d B", removed, bytes)
			}
		}
	}
}
