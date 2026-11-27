package config

// datadir_shared_4a_test.go —— 盲点补测 批次 4a · T2（FIXPLAN 盲点矩阵 #6）
//
// 场景：两个 store 实例共用同一 MOO_DATA。守卫在 config 层（cmd/server/main.go
// 启动时先 CheckDataDir 再 EnsureDataDirOwner，冲突即 log.Fatalf 拒绝启动），
// 所以这里直接打这两条边界，等价于"同进程内先后 New 两次"。
//
// 实测结论（写进 _audit_0578/BLINDSPOT_4A.md，**未改产品代码**）：
//   - 同 appname（moo + moo）二次启动：放行，且**零提示**（没有 WARN）→ 缺口；
//   - 不同变体（moo 的目录 + moo-dev 进程）：**硬拒绝启动**，不是 WARN 后继续
//     → FIXPLAN 里"应 WARN 但不拒绝"的设计描述与实现不符（实现更严）。
//
// 断言口径：只锁"当前真实行为 + 不破坏文件"，不把缺口写成期望值
// （例如不断言"必须没有告警"，也不断言"必须有告警"）。

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLog 抓 log 包输出，用于观察"到底有没有 owner 冲突提示"。
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevW, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevW)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

func readOwner(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, DataDirOwnerFile))
	if err != nil {
		t.Fatalf("读取归属标记失败: %v", err)
	}
	return string(b)
}

// T2-a 同 appname 二次启动（同一 MOO_DATA）：必须放行，且不得改写标记。
// 反向验证：把 ownerMatches 改成"自己的标记也不认" → 本例第二个实例被拒 → FAIL。
func TestSharedDataDir_SameAppSecondInstanceAllowed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "moo")
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo")

	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatalf("第一个实例启动失败: %v", err)
	}
	if got := strings.TrimSpace(readOwner(t, dir)); got != "moo" {
		t.Fatalf("首次盖章内容应为 moo，实际 %q", got)
	}
	before := readOwner(t, dir)

	var secondErr error
	secondLog := captureLog(t, func() {
		// 第二个实例启动：main.go 的两步（CheckDataDir → EnsureDataDirOwner）
		secondErr = CheckDataDir(dir)
		if secondErr == nil {
			secondErr = EnsureDataDirOwner(dir)
		}
	})
	if secondErr != nil {
		t.Fatalf("同一 appname 共用同一 MOO_DATA 时第二个实例不得被拒（当前设计放行）：%v", secondErr)
	}
	if after := readOwner(t, dir); after != before {
		t.Fatalf("第二个实例不得改写归属标记（before=%q after=%q）", before, after)
	}
	// 现状记录：这里没有任何 owner 冲突提示（缺口）。只记录、不断言。
	t.Logf("第二次启动的日志（预期缺口：空 = 无 WARN）: %q", strings.TrimSpace(secondLog))
}

// T2-b 不同变体共用目录（moo 盖章 + moo-dev 启动）：当前实现是**硬拒绝**。
// 断言：①返回可读的"已被占用"错误（不是静默放行、不是 panic）；
// ②标记文件原样保留（拒绝时不覆盖、不删除）。
// 反向验证：把 ownerMatches 改成恒 true（不再区分变体）→ 本例 FAIL。
func TestSharedDataDir_CrossVariantRefusesAndKeepsMarker(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "moo")
	t.Setenv("MOO_DATA", dir)
	t.Setenv("TRIM_PKGVAR", "")
	t.Setenv("MOO_APPNAME", "moo")
	if err := EnsureDataDirOwner(dir); err != nil {
		t.Fatalf("moo 首次启动失败: %v", err)
	}
	before := readOwner(t, dir)

	t.Setenv("MOO_APPNAME", "moo-dev") // 换变体：另一个应用来抢同一目录
	var err error
	_ = captureLog(t, func() { err = CheckDataDir(dir) })
	if err == nil {
		t.Fatalf("moo-dev 读 moo 的目录必须拒绝启动（当前实现），实际放行")
	}
	if !strings.Contains(err.Error(), "占用") || !strings.Contains(err.Error(), "moo") {
		t.Fatalf("拒绝原因应说明被谁占用（当前 %q）", err.Error())
	}
	if after := readOwner(t, dir); after != before {
		t.Fatalf("拒绝时不得改动别人的归属标记（before=%q after=%q）", before, after)
	}
	// 拒绝后 Ensure 也不得多写一个自己的标记。
	if e := EnsureDataDirOwner(dir); e == nil {
		t.Fatal("EnsureDataDirOwner 也必须拒绝（否则 main.go 的第二步会绕过守卫）")
	}
	if after := readOwner(t, dir); after != before {
		t.Fatalf("Ensure 拒绝后标记仍须原样（after=%q）", after)
	}
}
