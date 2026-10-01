package task

import (
	"os"
	"testing"

	"moo/internal/netguard"
)

// TestMain：下载引擎/大小探测测试使用 127.0.0.1 本地 httptest 服务器，
// 加入 netguard 豁免（SSRF 防护不挡测试回环地址）。
func TestMain(m *testing.M) {
	netguard.SetRebuilder(func() []string {
		return []string{"127.0.0.1", "localhost"}
	})
	os.Exit(m.Run())
}
