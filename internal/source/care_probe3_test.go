package source

import (
	"testing"
	"time"
)

// 红线 1 的另一半：直连 404 但镜像 200 → 源仍然活着，绝不能判死
// （「只认确定性永久错误」要求双通道同向失败）。
func TestProbeVerdictDirectGoneMirrorOK(t *testing.T) {
	direct := probeSrv(t, 404, 0)
	mirror := probeSrv(t, 200, 0)
	groups := []probeGroup{
		{Name: "direct", URLs: []string{direct.URL + "/moo.json"}},
		{Name: "mirror:m1", URLs: []string{mirror.URL + "/moo.json"}},
	}
	if got := probeCareVerdictWith(testProbeClient(2*time.Second), groups); got != CareAlive {
		t.Fatalf("直连 404 但镜像 200 应判存活, got %v", got)
	}
}

// 红线 4「动手前强制全量复核」：复核必须放开**全部**镜像、**全部**索引名，
// 不能被索引名记忆或镜像分级缩减（否则复核只是把记忆里的死路径再走一遍）。
func TestCareProbeUsesAllMirrorsAndNames(t *testing.T) {
	url := "https://github.com/owner/repo"
	gs := careProbeGroups(url)
	prefixes := mirrorPrefixes()
	if len(prefixes) == 0 {
		t.Skip("当前配置无带 URL 的镜像，无法断言全量")
	}
	if len(gs) != len(prefixes)+1 {
		t.Fatalf("镜像组应覆盖全部 %d 个前缀（+1 直连）, got %d 组", len(prefixes), len(gs))
	}
	// 分支 × 索引名全量：直连组至少 2 个分支 × 2 个名字
	if n := len(gs[0].URLs); n < 4 {
		t.Fatalf("直连组应是全量索引名（main/master × 多个名字），got %d", n)
	}
	for _, g := range gs[1:] {
		if len(g.URLs) < 2 {
			t.Fatalf("镜像组 %s 应含 main/master 两个分支, got %d", g.Name, len(g.URLs))
		}
	}
}
