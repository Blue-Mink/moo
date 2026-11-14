package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// 2026-11-14 用户令：Mate 只支持 GitHub 仓库，非 GitHub 来源必须彻底移除。
// 本文件是「反向验证」的常驻守卫：任何一处把 Gitee 支持加回来都会让用例变红。
func TestGiteeSupportRemoved(t *testing.T) {
	// ① 仓库发现：Gitee 地址必须直接拒绝，且文案明确只支持 GitHub。
	_, err := discoverFromRepo(context.Background(), "https://gitee.com/o/r", "", &http.Client{})
	if err == nil {
		t.Fatal("Gitee 仓库地址应当被拒绝")
	}
	if !strings.Contains(err.Error(), "仅支持 GitHub") {
		t.Fatalf("错误文案应说明仅支持 GitHub，得到 %v", err)
	}

	// ② 图标候选：Gitee 来源不再产生任何候选（避免拼出 gitee.com/raw 死链）。
	if got := mateIconCandidates("demo", "https://gitee.com/o/r"); len(got) != 0 {
		t.Fatalf("Gitee 来源不应产生图标候选，得到 %v", got)
	}

	// ③ 解析器：Gitee 仓库不再被识别为仓库，owner 提取为空。
	if _, _, ok := parseGHRepo("https://gitee.com/o/r"); ok {
		t.Fatal("parseGHRepo 不应命中 Gitee 地址")
	}
	if got := repoOwner("https://gitee.com/o/r"); got != "" {
		t.Fatalf("repoOwner 对 Gitee 应返回空，得到 %q", got)
	}
}
