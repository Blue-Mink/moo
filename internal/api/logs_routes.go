package api

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// getLogs 0.6.249：设置页「日志」在线查看——返回 moo.log 末尾 N 行
//（默认 200、上限 2000）+ 文件大小 + 轮转归档清单。moo.log 由
// StartLogRotator 控制在 ~5MB 内，全量读取无压力；日志落盘时已过
// MaskSecretInErr 脱敏（0.6.144 P1-4），此处不再二次处理。
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if x, err := strconv.Atoi(v); err == nil && x > 0 {
			n = x
		}
	}
	if n > 2000 {
		n = 2000
	}
	dir := dataDirOf(s)
	logPath := resolveMooLogPath(dir) // 0.6.267：安装向导可改日志路径（.logpath）
	fi, err := os.Stat(logPath)
	if err != nil {
		writeJSON(w, map[string]any{
			"file":     logPath,
			"size":     0,
			"total":    0,
			"returned": 0,
			"lines":    []string{},
			"archives": []string{},
			"note":     "日志文件不存在（应用可能尚未产生日志）",
		})
		return
	}
	f, err := os.Open(logPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n*2 {
			// 文件超限时只保留尾部（轮转上限 5MB，一般走不到）
			lines = lines[len(lines)-n*2:]
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// 归档清单 = 日志文件同目录下的 <文件名>.N（轮转产物）
	archives := []string{}
	base := filepath.Base(logPath)
	if ents, err := os.ReadDir(filepath.Dir(logPath)); err == nil {
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, base) && len(name) > len(base) && name[len(base)] == '.' {
				archives = append(archives, name)
			}
		}
	}
	writeJSON(w, map[string]any{
		"file":     logPath,
		"size":     fi.Size(),
		"total":    len(lines),
		"returned": len(lines),
		"lines":    lines,
		"archives": archives,
	})
}
