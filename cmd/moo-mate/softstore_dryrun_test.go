package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestDryRunSoftstore 拉真实软仓官方源做转换统计（只读，不断言，人工核对）。
// 用 -run TestDryRunSoftstore 单独跑；软仓数据会变，故不断言具体数量。
func TestDryRunSoftstore(t *testing.T) {
	if testing.Short() {
		t.Skip("dry-run 跳过（-short）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hc := &http.Client{Timeout: 30 * time.Second}
	apps, err := discoverFromSoftstore(ctx, "http://nas.hhxs2025.top:5660", hc)
	if err != nil {
		t.Fatalf("拉软仓失败: %v", err)
	}
	var withIcon, withSha, multiArch, withURLs int
	for _, ia := range apps {
		if ia.IconURL != "" {
			withIcon++
		}
		if ia.Sha256 != "" {
			withSha++
		}
		if len(ia.Packages) > 1 {
			multiArch++
		}
		if len(ia.DownloadURLs) > 0 {
			withURLs++
		}
	}
	fmt.Printf("=== 软仓转换统计 ===\n")
	fmt.Printf("唯一应用: %d\n", len(apps))
	fmt.Printf("有 icon_url: %d\n", withIcon)
	fmt.Printf("有 sha256: %d\n", withSha)
	fmt.Printf("双架构(packages>1): %d\n", multiArch)
	fmt.Printf("有候选下载链: %d\n", withURLs)
	// 抽样打印 3 个应用验证字段映射
	n := 0
	for name, ia := range apps {
		if n >= 3 {
			break
		}
		fmt.Printf("--- %s ---\n", name)
		fmt.Printf("  display_name=%q version=%q type=%q\n", ia.DisplayName, ia.Version, ia.AppType)
		fmt.Printf("  icon=%q\n", ia.IconURL)
		fmt.Printf("  download=%q\n", ia.DownloadURL)
		fmt.Printf("  packages=%d archs=%v\n", len(ia.Packages), ia.Platform)
		fmt.Printf("  urls=%d\n", len(ia.DownloadURLs))
		n++
	}
}
