//go:build linux

package platform

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultVolume 解析默认安装存储卷：
// 1) appcenter-cli default-volume（权威来源）
// 2) 唯一已挂载卷（df 探测 /volN）
// 3) 兜底 1
func DefaultVolume() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "appcenter-cli", "default-volume").Output()
	if err == nil {
		s := strings.TrimSpace(string(out))
		if v, perr := strconv.Atoi(s); perr == nil && v >= 1 {
			return v, nil
		}
	}
	// 唯一已挂载卷
	mounted := mountedVolumes()
	if len(mounted) == 1 {
		return mounted[0], nil
	}
	if len(mounted) > 1 {
		return 0, fmt.Errorf("系统有 %d 个存储卷，无法确定默认安装卷（appcenter-cli default-volume 不可用）", len(mounted))
	}
	return 1, nil
}

// mountedVolumes 从 df 输出探测已挂载的 /volN 卷号。
func mountedVolumes() []int {
	out, err := exec.Command("df", "-l").Output()
	if err != nil {
		return nil
	}
	var vols []int
	seen := map[int]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, "/vol")
		if idx < 0 {
			continue
		}
		rest := line[idx+4:]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i == 0 {
			continue
		}
		v, _ := strconv.Atoi(rest[:i])
		if !seen[v] {
			seen[v] = true
			vols = append(vols, v)
		}
	}
	return vols
}
