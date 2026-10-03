// probesupport — 真机验证 ProbeUISupport（一次性诊断，不进发布包）。
// 用法: go run ./tools/probesupport
package main

import (
	"context"
	"fmt"
	"time"

	"moo/internal/official"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 用 RFC5737 文档保留段（192.0.2.0/24）占位，勿写真机内网 IP（安全红线）
	for _, base := range []string{"http://127.0.0.1:5666", "http://192.0.2.10:5666"} {
		fmt.Printf("%s -> ui_supported=%v\n", base, official.ProbeUISupport(ctx, base))
	}
	debugProbe("http://192.0.2.10:5666")
}
