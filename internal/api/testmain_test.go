package api

import (
	"os"
	"testing"

	"moo/internal/netguard"
	"moo/internal/testenv"
)

// TestMain：测试服务器的 httptest 地址均为 127.0.0.1 本地回环，
// 加入 netguard 豁免，避免 raceFetch/图标等测试被 SSRF 防护误挡。
//
// 2026-11-26（批次 B）：先过 testenv.Guard —— shell 继承的 TRIM_PKGVAR 若
// 指向别的应用目录，config.DataDir() 会把 config/operations/blacklist_last
// 写进去（实测发生过），这里把数据目录改指临时目录。
func TestMain(m *testing.M) {
	cleanup := testenv.Guard()
	netguard.SetRebuilder(func() []string {
		return []string{"127.0.0.1", "localhost"}
	})
	code := m.Run()
	cleanup()
	os.Exit(code)
}
