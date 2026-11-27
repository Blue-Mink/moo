// Package testenv 给测试提供「数据目录隔离」闸门。
//
// 2026-11-26（批次 B）：起因是 `go test ./...` 在 shell 继承了
// TRIM_PKGVAR=/appdata/other-app（别的应用的数据目录）时，Moo 的 config.DataDir() 会
// 直接采用它（优先级 MOO_DATA > TRIM_PKGVAR > ./moo-data），于是单测把
// config.json / operations.json / blacklist_last.json 写进了别的应用的
// 数据目录（实测发生过）。
//
// Guard 在任何测试跑起来之前调用：只要继承来的 TRIM_PKGVAR 不是 Moo 自己
// 的目录（basename ≠ moo），就把 MOO_DATA 指向一个临时目录，让所有走
// config.DataDir() 的写点落进沙箱；测试结束时复原环境变量并删掉临时目录。
//
// 注意：Guard 只用于测试。生产/调试下 MOO_DATA 显式设置的语义不变。
package testenv

import (
	"log"
	"os"
	"path/filepath"
)

// Guard 检查继承来的数据目录环境，必要时把 MOO_DATA 指向临时目录。
// 返回值必须被调用（defer 或 TestMain 里显式调用）以复原环境并清理目录；
// 不需要隔离时返回 noop。
func Guard() (cleanup func()) {
	noop := func() {}

	// 1) 显式指定 MOO_DATA 优先——调用方已明确要写哪里，测试不去覆盖它。
	if os.Getenv("MOO_DATA") != "" {
		return noop
	}
	// 2) 没有 TRIM_PKGVAR：DataDir() 会落到 ./moo-data，本来就与外部无关。
	pkgvar := os.Getenv("TRIM_PKGVAR")
	if pkgvar == "" {
		return noop
	}
	// 3) 继承的确实是 Moo 自己的数据目录：可以照原样跑（隔离更有意义的是
	//    避免污染生产数据，但那属于部署机，不在测试机器上出现）。
	if filepath.Base(filepath.Clean(pkgvar)) == "moo" {
		return noop
	}

	dir, err := os.MkdirTemp("", "moo-test-data-")
	if err != nil {
		log.Printf("[testenv] 无法创建测试数据目录，测试将沿用继承的 TRIM_PKGVAR=%s: %v", pkgvar, err)
		return noop
	}
	if err := os.Setenv("MOO_DATA", dir); err != nil {
		log.Printf("[testenv] 设置 MOO_DATA 失败，测试将沿用继承的 TRIM_PKGVAR=%s: %v", pkgvar, err)
		_ = os.RemoveAll(dir)
		return noop
	}
	log.Printf("[testenv] 继承的 TRIM_PKGVAR=%s 不是 Moo 的数据目录，测试改用临时目录 %s（结束后删除）", pkgvar, dir)

	return func() {
		_ = os.Unsetenv("MOO_DATA")
		_ = os.RemoveAll(dir)
	}
}
