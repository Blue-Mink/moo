package config

import (
	"sync"
	"testing"
	"time"
)

// 2026-10-28 审查补丁：hook 返回前若有可变共享状态，并发读会 data race。
// 当前实现 hook 只读 cfg.CustomMirrors（切片头拷贝），理论安全；
// 本测试用 -race 跑出证据。
func TestMergeConcurrentReads(t *testing.T) {
	SetCustomMirrorProvider(func() []MirrorOption {
		time.Sleep(time.Millisecond) // 放大窗口
		return []MirrorOption{{Key: "custom-1", Label: "x", URL: "https://r.example.com/"}}
	})
	t.Cleanup(func() { SetCustomMirrorProvider(nil) })
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = mergeCustomMirrors(GitHubMirrorOptions())
			}
		}()
	}
	wg.Wait()
}
