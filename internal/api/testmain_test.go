package api

import (
	"os"
	"testing"

	"moo/internal/netguard"
)

// TestMain：测试服务器的 httptest 地址均为 127.0.0.1 本地回环，
// 加入 netguard 豁免，避免 raceFetch/图标等测试被 SSRF 防护误挡。
func TestMain(m *testing.M) {
	netguard.SetRebuilder(func() []string {
		return []string{"127.0.0.1", "localhost"}
	})
	os.Exit(m.Run())
}
