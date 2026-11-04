package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 0.2.38：设置页「日志」卡片（对齐 Moo 0.6.249/0.6.500 的日志页能力：
// 查看 / 复制 / 刷新 / 按天清理 / 级别筛选 / 关键词搜索）。
// moo-mate 的日志文件由生命周期脚本决定：单一事实源 ${PKGVAR}/.logpath
// （install_callback 落盘，common 启动时解析并把 daemon 输出重定向到该文件）。
// Go 侧用 MOO_MATE_BOOTSTRAP（= PKGVAR）读同一份 .logpath，保证读的就是进程实际写的。

// resolveMateLogPath 与 fnos-mate/cmd/common 的 LOG_FILE 解析规则逐条一致：
// 绝对路径原样；相对路径相对 PKGVAR；缺失/空 = PKGVAR/moo-mate.log。
func resolveMateLogPath(bootDir string) string {
	dir := strings.TrimSpace(bootDir)
	if dir == "" {
		dir = "."
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".logpath")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			if filepath.IsAbs(s) {
				return s
			}
			return filepath.Join(dir, s)
		}
	}
	return filepath.Join(dir, "moo-mate.log")
}

// mateLogPath 当前生效的日志文件路径。
func (c *companion) mateLogPath() string {
	return resolveMateLogPath(c.bootDir)
}

const (
	mateLogRotateInterval    = 10 * time.Minute
	mateLogRotateMaxArchives = 5 // .1 ~ .5
)

// var：单测可临时调小。
var mateLogRotateMaxSize int64 = 5 << 20

// mateLogFileMu 让「轮转」与「清理」串行：清理会原地重写当前文件，
// 轮转顺移归档时必须互斥（同 Moo 0.6.500 的 logFileMu 口径）。
var mateLogFileMu sync.Mutex

// StartLogRotator 周期性轮转日志（5MB 阈值）。任何失败静默跳过，下次再试。
func (c *companion) StartLogRotator(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(mateLogRotateInterval):
		}
		if ctx.Err() != nil {
			return
		}
		if size, ok := rotateMateLog(c.mateLogPath()); ok {
			log.Printf("[logrotate] 日志轮转: 归档 %d B → %s.1，当前文件清零", size, filepath.Base(c.mateLogPath()))
		}
	}
}

// rotateMateLog 超过阈值时把当前文件拷到 .1（旧归档顺移，最旧丢弃）并截断原文件。
// 不接管进程 stdio 句柄：平台/脚本以 O_APPEND 打开，截断后继续从 0 追加（Moo 已实测）。
func rotateMateLog(p string) (int64, bool) {
	mateLogFileMu.Lock()
	defer mateLogFileMu.Unlock()
	fi, err := os.Stat(p)
	if err != nil || fi.Size() <= mateLogRotateMaxSize {
		return 0, false
	}
	for i := mateLogRotateMaxArchives - 1; i >= 1; i-- {
		_ = os.Rename(p+"."+strconv.Itoa(i), p+"."+strconv.Itoa(i+1))
	}
	src, err := os.Open(p)
	if err != nil {
		return 0, false
	}
	dst, err := os.OpenFile(p+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = src.Close()
		return 0, false
	}
	_, copyErr := io.Copy(dst, src)
	_ = src.Close()
	_ = dst.Close()
	if copyErr != nil {
		return 0, false
	}
	if err := truncateFile(p); err != nil {
		return 0, false
	}
	return fi.Size(), true
}

// truncateFile 原地清零（保留 inode，写端 O_APPEND fd 不受影响）。
func truncateFile(p string) error {
	f, err := os.OpenFile(p, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(0)
}

// mateLogLineTS 解析行首时间戳 `YYYY/MM/DD HH:MM:SS`（Go log 包默认格式）。
// 无时间戳的行（生命周期脚本输出，如 "Stopping moo-mate ..."）返回 zero + false。
func mateLogLineTS(line string) (time.Time, bool) {
	if len(line) < 19 {
		return time.Time{}, false
	}
	ts, err := time.ParseInLocation("2006/01/02 15:04:05", line[:19], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	// 第 19 位必须是分隔符或行尾（防止误判普通数字开头的文本）
	if len(line) > 19 {
		c := line[19]
		if c != ' ' && c != '\t' && c != '\r' {
			return time.Time{}, false
		}
	}
	return ts, true
}

// fmtLogBytes 人类可读体积（前端也会格式化，这里用于 note 文案）。
func fmtLogBytes(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// mateLogData GET /api/logs 的响应（与 Moo 0.6.500 同字段名，前端可复用同一套解析）。
type mateLogData struct {
	File         string   `json:"file"`
	Size         int64    `json:"size"`
	Total        int      `json:"total"`
	Returned     int      `json:"returned"`
	Lines        []string `json:"lines"`
	Archives     []string `json:"archives"`
	ArchiveBytes int64    `json:"archive_bytes"`
	Note         string   `json:"note,omitempty"`
}

// handleLogs GET /api/logs?lines=N：返回日志末尾 N 行（默认 200、上限 2000）
// + 文件体积 + 归档清单/占用。日志由生命周期脚本/进程 stdout 写入，不做二次脱敏。
func (c *companion) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	if n > 2000 {
		n = 2000
	}
	logPath := c.mateLogPath()
	fi, err := os.Stat(logPath)
	if err != nil {
		writeJSON(w, http.StatusOK, mateLogData{
			File: logPath, Lines: []string{}, Archives: []string{},
			Note: "日志文件不存在（应用可能尚未产生日志）",
		})
		return
	}
	f, err := os.Open(logPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "日志读取失败："+err.Error())
		return
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n*2 { // 轮转上限 5MB，一般走不到；防御异常大文件
			lines = lines[len(lines)-n*2:]
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	archives, archiveBytes := mateLogArchives(logPath)
	writeJSON(w, http.StatusOK, mateLogData{
		File: logPath, Size: fi.Size(), Total: len(lines), Returned: len(lines),
		Lines: lines, Archives: archives, ArchiveBytes: archiveBytes,
	})
}

// mateLogArchives 列出同目录下 <base>.N 归档（轮转产物）与总占用。
func mateLogArchives(logPath string) ([]string, int64) {
	base := filepath.Base(logPath)
	ents, err := os.ReadDir(filepath.Dir(logPath))
	if err != nil {
		return []string{}, 0
	}
	archRe := mateLogArchiveRe(base)
	out := []string{}
	var total int64
	for _, e := range ents {
		if !archRe.MatchString(e.Name()) {
			continue
		}
		out = append(out, e.Name())
		if fi, err := e.Info(); err == nil {
			total += fi.Size()
		}
	}
	sort.Strings(out)
	return out, total
}

// mateLogArchiveRe 归档名匹配器：<base>.N（N 为纯数字）。
func mateLogArchiveRe(base string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.\d+$`)
}

// mateLogCleanupResult POST /api/logs/cleanup 的响应（与 Moo 同字段名）。
type mateLogCleanupResult struct {
	KeepDays     int   `json:"keep_days"`
	RemovedFiles int   `json:"removed_files"`
	FreedBytes   int64 `json:"freed_bytes"`
	KeptBytes    int64 `json:"kept_bytes"`
}

// handleLogsCleanup POST /api/logs/cleanup {keep_days}：
// 0 = 全部清空；N>0 = 删除 N 天前的日志行（保留最近 N 天）。
// 只动本机日志文件，不影响索引/配置/备份。
func (c *companion) handleLogsCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepDays int `json:"keep_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	if req.KeepDays < 0 || req.KeepDays > 3650 {
		writeErr(w, http.StatusBadRequest, "保留天数需在 0~3650 之间（0 = 全部清空）")
		return
	}
	res, err := cleanupMateLogFiles(c.mateLogPath(), req.KeepDays, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// cleanupMateLogFiles 清理「当前日志 + 同目录归档」。keepDays=0 全清（无时间戳行仍保留，
// 与轮转/脚本输出的语义保持一致）。当前文件原地重写保 inode，归档清空则删除。
func cleanupMateLogFiles(logPath string, keepDays int, now time.Time) (mateLogCleanupResult, error) {
	res := mateLogCleanupResult{KeepDays: keepDays}
	if strings.TrimSpace(logPath) == "" {
		return res, fmt.Errorf("日志路径为空")
	}
	dir, base := filepath.Dir(logPath), filepath.Base(logPath)
	cutoff := time.Time{}
	if keepDays > 0 {
		cutoff = now.AddDate(0, 0, -keepDays)
	}

	mateLogFileMu.Lock()
	defer mateLogFileMu.Unlock()

	// 当前日志：只截断/重写，绝不删除文件
	if fi, err := os.Lstat(logPath); err == nil && fi.Mode().IsRegular() {
		freed, kept, err := cleanupMateLogFile(logPath, cutoff, false)
		if err != nil {
			return res, err
		}
		res.FreedBytes += freed
		res.KeptBytes += kept
	}

	archRe := mateLogArchiveRe(base)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return res, nil // 目录都不在：没有可清理的东西
	}
	for _, e := range ents {
		name := e.Name()
		if name == base || !archRe.MatchString(name) {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		freed, kept, err := cleanupMateLogFile(p, cutoff, true)
		if err != nil {
			continue // 单个归档失败不影响其它文件
		}
		res.FreedBytes += freed
		res.KeptBytes += kept
		if kept == 0 {
			res.RemovedFiles++
		}
	}
	return res, nil
}

// cleanupMateLogFile 清理单个文件。cutoff 零值 = 清空全部；removeWhenEmpty=true 且
// 过滤后没有任何内容时删除文件（归档）。返回 (释放字节, 剩余字节, error)。
func cleanupMateLogFile(path string, cutoff time.Time, removeWhenEmpty bool) (int64, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	if fi.Size() == 0 {
		if removeWhenEmpty {
			_ = os.Remove(path)
		}
		return 0, 0, nil
	}
	var kept []byte
	if !cutoff.IsZero() {
		kept, err = filterMateLogFile(path, cutoff)
		if err != nil {
			return 0, 0, err
		}
		if int64(len(kept)) == fi.Size() {
			return 0, int64(len(kept)), nil // 没有可删的行
		}
	}
	if len(kept) == 0 && removeWhenEmpty {
		if err := os.Remove(path); err != nil {
			return 0, 0, err
		}
		return fi.Size(), 0, nil
	}
	if err := rewriteMateFileInPlace(path, kept); err != nil {
		return 0, 0, err
	}
	return fi.Size() - int64(len(kept)), int64(len(kept)), nil
}

// filterMateLogFile 读取并按时间过滤；进程仍在追加时最多重试 3 次（大小必须稳定）。
func filterMateLogFile(path string, cutoff time.Time) ([]byte, error) {
	last := fmt.Errorf("日志正在写入，请稍后重试")
	for i := 0; i < 3; i++ {
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if before.Size() == after.Size() && int64(len(data)) == after.Size() {
			return filterMateLogLines(data, cutoff), nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return nil, last
}

// filterMateLogLines 保留时间戳 >= cutoff 的行 + 无时间戳的行（生命周期脚本输出没有
// 时间前缀，保守起见一律保留）。逐行扫描，保留原始换行。
func filterMateLogLines(data []byte, cutoff time.Time) []byte {
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i+1], data[i+1:]
		} else {
			line, data = data, nil
		}
		if ts, ok := mateLogLineTS(string(line)); ok && ts.Before(cutoff) {
			continue
		}
		out = append(out, line...)
	}
	return out
}

// rewriteMateFileInPlace 原地截断并写回（保留 inode：进程以 O_APPEND fd 持续写该文件）。
func rewriteMateFileInPlace(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
