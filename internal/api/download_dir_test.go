package api

import (
	"os"
	"testing"
)

// 回归（0.6.74）：FPK 下载目录选择器的路径白名单——只允许 /volN 卷根
// 与卷下顶层可见目录（共享目录），防止把下载缓存写进系统目录/应用数据
// 深处/共享目录子层。
func TestIsAccessibleDownloadDir(t *testing.T) {
	s := &Server{}
	// 拒绝（不依赖机器环境，任何环境都成立）
	bad := []string{
		"rel/path",               // 相对路径
		"/",                      // 虚拟根不可选
		"/tmp/fpk",               // 非 /volN
		"/usr/share/fpk",         // 系统目录
		"/vol1/.hidden",          // 顶层隐藏目录
		"/vol1/@appdata",         // 平台应用数据
		"/vol1/share/.cache",     // 子层隐藏目录（任意一层 . 开头即拒）
		"/vol1/share/@appcenter", // 子层平台目录
		"/vol9/whatever",         // 不存在的卷
	}
	for _, p := range bad {
		if s.isAccessibleDownloadDir(p) {
			t.Errorf("%s 应被拒绝", p)
		}
	}
	// 默认目录（应用数据目录）任何环境都必须允许（随时可切回默认）
	if !s.isAccessibleDownloadDir(defaultDownloadDir()) {
		t.Errorf("默认目录 %s 应被允许", defaultDownloadDir())
	}
	// 允许（需要真实卷环境；测试机/主 NAS 有 /vol1，纯 CI 环境跳过）
	if _, err := os.Stat("/vol1"); err != nil {
		t.Skip("测试环境无 /vol1，跳过允许分支")
	}
	good := []string{
		"/vol1",                    // 卷根本身
		"/vol1/some-share",         // 卷下顶层可见目录（尚不存在也允许，MkdirAll 创建）
		"/vol1/downloads",          // 常见共享目录名
		"/vol1/downloads/fpk",      // 共享目录子层（0.6.75 起允许任意深度）
		"/vol1/a/b/c",              // 任意深度可见路径
	}
	for _, p := range good {
		if !s.isAccessibleDownloadDir(p) {
			t.Errorf("%s 应被允许", p)
		}
	}
}
