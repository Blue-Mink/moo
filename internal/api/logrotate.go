package api

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// 0.6.216 P2（N3）：moo.log 大小轮转。
// 平台把进程 stdout/stderr 捕获到 <dataDir>/moo.log（O_APPEND——已实测：
// 外部截断文件后，下一条写入落在 offset 0，无稀疏空洞）。因此轮转策略 =
// 超过阈值：整份拷贝到 moo.log.1（旧 .1 顺移到 .2）+ 原文件截断为 0。
// 不接管日志输出、不持文件句柄——平台日志展示与现有功能零影响。
// 保留：当前文件（≤ 阈值+单条行）+ 2 份归档（≤ 阈值/份）。
const (
	logRotateInterval    = 10 * time.Minute
	logRotateMaxSize     = 5 << 20 // 超过 5MB 触发轮转
	logRotateMaxArchives = 2       // moo.log.1 / moo.log.2
)

func (s *Server) StartLogRotator(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(logRotateInterval):
		}
		if ctx.Err() != nil {
			return
		}
		rotateMooLog(dataDirOf(s))
	}
}

// rotateMooLog 归档 moo.log 到 moo.log.1（旧 .1 → .2）并截断原文件。
// 任何一步失败都静默放弃本次轮转（10 分钟后重试），绝不影响主流程。
func rotateMooLog(dataDir string) {
	p := filepath.Join(dataDir, "moo.log")
	fi, err := os.Stat(p)
	if err != nil || fi.Size() <= logRotateMaxSize {
		return
	}
	// 归档顺移：.1 → .2（POSIX rename 覆盖同名目标，旧 .2 丢弃）
	_ = os.Rename(p+".1", p+".2")

	src, err := os.Open(p)
	if err != nil {
		return
	}
	dst, err := os.OpenFile(p+".1", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		src.Close()
		return
	}
	_, copyErr := io.Copy(dst, src)
	src.Close()
	dst.Close()
	if copyErr != nil {
		log.Printf("[logrotate] 归档复制失败，本轮跳过: %v", copyErr)
		return
	}
	// 截断为 0（平台 O_APPEND fd 继续从 offset 0 追加，已实测）
	if f, err := os.OpenFile(p, os.O_WRONLY, 0); err == nil {
		err = f.Truncate(0)
		f.Close()
	}
	if err != nil {
		log.Printf("[logrotate] 截断失败，本轮跳过: %v", err)
		return
	}
	log.Printf("[logrotate] moo.log 轮转: 归档 %d B → moo.log.1，当前文件清零", fi.Size())
}
