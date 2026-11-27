package config

import (
	"os"
	"testing"

	"moo/internal/testenv"
)

// TestMain 2026-11-26（批次 B）：与 internal/api 一致，先过数据目录隔离闸，
// 避免 shell 继承的 TRIM_PKGVAR 指向别的应用时，本包单测（Save 等）写进去。
func TestMain(m *testing.M) {
	cleanup := testenv.Guard()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
