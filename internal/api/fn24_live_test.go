//go:build fn24live

package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/task"
)

// 真实网络 E2E（仅 -tags fn24live 时编译运行）：
// 用 issue #24 报障用户库里固化的「镜像壳地址」，走真实下载器下落并校验 sha256。
// 反向验证：把 buildFPKCandidates 还原成旧实现（host != github.com 直接单条），
// 本用例必 FAIL。
func TestFn24Live_DirtyURLFallsBackToDirect(t *testing.T) {
	const inner = "https://raw.githubusercontent.com/Wyf841015/FnDepot/master/fnclearup/fnclearup.fpk"
	const dirty = "https://cdn.gh-proxy.org/" + inner // 用户报障时的形态
	const wantSHA = "128d5a243b217328df495eee36d3eaf905e6061b509e141cebd96c60a8186565"

	var prefixes []string
	for _, o := range config.GitHubMirrorOptions() {
		if o.URL != "" {
			prefixes = append(prefixes, o.URL)
		}
	}
	cands := buildFPKCandidates(dirty, prefixes)
	t.Logf("candidates (%d): %v", len(cands), cands)
	if len(cands) < 2 {
		t.Fatalf("镜像壳地址应展开多条候选，got %v", cands)
	}
	hasInner := false
	for _, c := range cands {
		if c == inner {
			hasInner = true
		}
	}
	if !hasInner {
		t.Fatalf("候选缺少脱壳直连 %s", inner)
	}
	// 脱壳后直连必须保留（排最后一条直连候选）。注意：脏地址若与清单里的
	// 某个镜像前缀相同，会被去重合并到前面的镜像位——这同样安全（该镜像
	// 一旦失败仍会继续走到 inner 直连）。
	last := cands[len(cands)-1]
	if last != inner && last != dirty {
		t.Fatalf("最后一条应为直连或原脏地址，got %v", cands)
	}

	// 死镜像排最前（hk 已实测全球 500），要求：跳过它、直连成功、失败原因归到加速源。
	cands2 := append([]string{"https://hk.gh-proxy.org/" + inner}, cands...)
	dir := t.TempDir()
	dest := filepath.Join(dir, "fnclearup.fpk")
	var fails []string
	err := task.NewEngine("").Download(context.Background(), task.DownloadOpts{
		DestPath:   dest,
		Candidates: cands2,
		SizeBytes:  619944,
		SHA256:     wantSHA,
		OnFail: func(c, stage string, e error) {
			fails = append(fails, e.Error())
		},
	})
	if err != nil {
		t.Fatalf("应从后续候选成功下载，实际失败：%v", err)
	}
	if len(fails) == 0 {
		t.Fatalf("死镜像应留下失败记录（否则兜底路径没被走到）")
	}
	if !strings.Contains(fails[0], "加速源故障") {
		t.Errorf("死镜像 500 应归因为加速源故障，实际 %q", fails[0])
	}
	if strings.Contains(fails[0], "源站故障") {
		t.Errorf("不应误报源站故障，实际 %q", fails[0])
	}
	fi, serr := os.Stat(dest)
	if serr != nil || fi.Size() != 619944 {
		t.Fatalf("成品异常：%v size=%d", serr, fi.Size())
	}
}
