package api

// 0.6.500：日志清理（设置页「日志」tab 的删除入口）。
// 保留策略按行首时间戳过滤；无时间戳的行（生命周期脚本输出）一律保留。
// 当前日志原地截断+重写：平台以 O_APPEND fd 持续写同一 inode，rename 换文件会
// 让后续日志写进被删 inode（与 rotateMooLog 的截断方案同理）。归档过滤后为空
// 则删除。与 rotateMooLog 共用 logFileMu，避免轮转与清理互踩。

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var logFileMu sync.Mutex

const (
	logLineTimeLayout = "2006/01/02 15:04:05"
	logCleanupMaxRead = 64 << 20 // 单文件读取上限（正常轮转 ≤5MB，防御异常大文件）
)

type logCleanupResult struct {
	KeepDays     int   `json:"keep_days"`
	RemovedFiles int   `json:"removed_files"` // 被整体删除的归档数
	FreedBytes   int64 `json:"freed_bytes"`   // 释放字节数（含当前日志被截掉的部分）
	KeptBytes    int64 `json:"kept_bytes"`    // 清理后剩余字节数（当前文件 + 归档）
}

// POST /api/logs/cleanup {keep_days: N}
// N=0 全部清空；N>0 删除 N 天前的日志行（保留最近 N 天）。
func (s *Server) cleanupLogs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeepDays int `json:"keep_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("请求格式不正确"))
		return
	}
	if req.KeepDays < 0 || req.KeepDays > 3650 {
		writeErr(w, http.StatusBadRequest, errors.New("保留天数需在 0~3650 之间（0 = 全部清空）"))
		return
	}
	res, err := cleanupLogFiles(resolveMooLogPath(dataDirOf(s)), req.KeepDays, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, res)
}

// cleanupLogFiles 对 logPath（当前日志）+ 同目录 <base>.N 归档执行清理。
// keepDays=0 表示全部清空；时间比较基准 now 由调用方注入（便于单测）。
func cleanupLogFiles(logPath string, keepDays int, now time.Time) (logCleanupResult, error) {
	res := logCleanupResult{KeepDays: keepDays}
	if strings.TrimSpace(logPath) == "" {
		return res, errors.New("日志路径为空")
	}
	dir, base := filepath.Dir(logPath), filepath.Base(logPath)
	cutoff := time.Time{}
	if keepDays > 0 {
		cutoff = now.AddDate(0, 0, -keepDays)
	}

	logFileMu.Lock()
	defer logFileMu.Unlock()

	// 当前日志：只截断/重写，绝不删除文件
	if fi, err := os.Lstat(logPath); err == nil && fi.Mode().IsRegular() {
		freed, kept, err := cleanupLogFile(logPath, cutoff, false)
		if err != nil {
			return res, err
		}
		res.FreedBytes += freed
		res.KeptBytes += kept
	}

	archRe := regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d+)$`)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return res, nil // 当前文件都不在时目录可能不存在：没有可清理的东西
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
		freed, kept, err := cleanupLogFile(p, cutoff, true)
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

// cleanupLogFile 清理单个文件。cutoff 零值 = 清空全部；removeWhenEmpty=true 且
// 过滤后没有任何内容时删除文件（归档）。返回 (释放字节, 剩余字节, error)。
func cleanupLogFile(path string, cutoff time.Time, removeWhenEmpty bool) (int64, int64, error) {
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
	if !cutoff.IsZero() && fi.Size() > logCleanupMaxRead {
		return 0, 0, errors.New("日志文件过大，请稍后重试或选择全部清空")
	}
	var kept []byte
	if !cutoff.IsZero() {
		kept, err = filterLogFile(path, cutoff)
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
	if err := rewriteFileInPlace(path, kept); err != nil {
		return 0, 0, err
	}
	return fi.Size() - int64(len(kept)), int64(len(kept)), nil
}

// filterLogFile 读取并按时间过滤；进程仍在追加时最多重试 3 次（大小必须稳定）。
func filterLogFile(path string, cutoff time.Time) ([]byte, error) {
	var last error = errors.New("日志正在写入，请稍后重试")
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
			return filterLogLines(data, cutoff), nil
		}
		time.Sleep(30 * time.Millisecond)
	}
	return nil, last
}

// filterLogLines 保留时间戳 >= cutoff 的行 + 无时间戳的行（脚本输出的生命周期
// 日志没有时间前缀，保守起见一律保留）。逐行扫描，保留原始换行。
func filterLogLines(data []byte, cutoff time.Time) []byte {
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i+1], data[i+1:]
		} else {
			line, data = data, nil
		}
		if len(line) >= len(logLineTimeLayout) {
			if ts, err := time.ParseInLocation(logLineTimeLayout, string(line[:len(logLineTimeLayout)]), time.Local); err == nil {
				if ts.Before(cutoff) {
					continue
				}
			}
		}
		out = append(out, line...)
	}
	return out
}

// rewriteFileInPlace 原地截断并写回（保留 inode：平台以 O_APPEND fd 持续写该文件）。
func rewriteFileInPlace(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
