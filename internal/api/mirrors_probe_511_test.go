package api

import (
	"strings"
	"testing"
)

// 0.6.511 F1：测速探针不得使用 .fpk 安装包。
//
// GitHub 会给每次 release asset 下载记一次 download_count；Moo 的镜像测速
// 每 5 分钟对各镜像各下一次探针（读前 2MB 即断），若探针是 FPK，就会把该
// 应用的下载量灌到几十万（实测 0.6.325 x86 FPK 被灌 41 万，占 moo 95%）。
// 这条护栏保证以后谁把探针改回 FPK，CI 立刻红。
func TestGhProbeAssetsAreNotFpk(t *testing.T) {
	for _, a := range ghProbeAssets {
		if strings.HasSuffix(strings.ToLower(a), ".fpk") {
			t.Fatalf("测速探针不得是 .fpk（会污染应用下载量）：%s", a)
		}
	}
	if len(ghProbeAssets) == 0 {
		t.Fatal("ghProbeAssets 不能为空")
	}
}

// 主探针必须钉在 Moo 自有 release 的非安装包资产上——第三方资产（ollama 等）
// 一旦被上游下线，全部镜像会同时「探测资产 404」被误判为故障（0.6.333 F3）。
func TestGhProbePrimaryAssetIsMooBinary(t *testing.T) {
	const want = "/Blue-Mink/moo/releases/download/"
	if !strings.Contains(ghProbeAssets[0], want) {
		t.Fatalf("首位探针应指向 Blue-Mink/moo 的 release 资产，实际 %q", ghProbeAssets[0])
	}
	if !strings.HasSuffix(ghProbeAssets[0], "/moo-server-linux-amd64") {
		t.Fatalf("首位探针应为 moo-server-linux-amd64（非 .fpk），实际 %q", ghProbeAssets[0])
	}
}
