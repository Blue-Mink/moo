package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moo/internal/release"
)

// capture 重定向 stdout 收集 emitText 输出。
func capture(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExitCodeSkippedIsNotFailure：跳过验收不算失败（2026-10-30 假失败回归）。
func TestExitCodeSkippedIsNotFailure(t *testing.T) {
	rs := []release.Result{
		{Channel: "selfidx", OK: true, Verified: true},
		{Channel: "fndepot", OK: true, VerifySkipped: true, VerifySkipReason: "默认只验收第一条可用通道"},
		{Channel: "mirror", OK: true, VerifySkipped: true, VerifySkipReason: "同一 FPK URL 已验收过"},
	}
	if got := exitCode(rs, true, false); got != 0 {
		t.Fatalf("存在跳过但已有通道通过验收，期望 exit=0，实际 %d", got)
	}
}

// TestExitCodeHardFailReturns1：可达但校验失败必须报失败。
func TestExitCodeHardFailReturns1(t *testing.T) {
	rs := []release.Result{
		{Channel: "selfidx", OK: true, Verified: true},
		{Channel: "mirror", OK: true, VerifyErr: "sha256 不符"},
	}
	if got := exitCode(rs, true, false); got != 1 {
		t.Fatalf("有契约违规应 exit=1，实际 %d", got)
	}
}

// TestExitCodeNoVerifiedReturns1：全部跳过、无一条真验证 → 不能算通过。
func TestExitCodeNoVerifiedReturns1(t *testing.T) {
	rs := []release.Result{
		{Channel: "selfidx", OK: true, VerifySkipped: true},
		{Channel: "mirror", OK: true, VerifySkipped: true},
	}
	if got := exitCode(rs, true, false); got != 1 {
		t.Fatalf("无任何通道通过验收应 exit=1，实际 %d", got)
	}
}

// TestExitCodeTransportNotFailure：网络不可达（403/超时）按设计不算契约违规。
func TestExitCodeTransportNotFailure(t *testing.T) {
	rs := []release.Result{
		{Channel: "api", OK: true, VerifyTransport: true, VerifyErr: "context deadline exceeded"},
		{Channel: "selfidx", OK: true, Verified: true},
	}
	if got := exitCode(rs, true, false); got != 0 {
		t.Fatalf("传输失败不算契约违规，期望 exit=0，实际 %d", got)
	}
}

// TestEmitTextSkipNotReportedAsViolation：跳过必须打成 ⏭ + 原因，不能出现「契约违规」。
func TestEmitTextSkipNotReportedAsViolation(t *testing.T) {
	rs := []release.Result{
		{Channel: "selfidx", OK: true, Verified: true, Artifact: &release.Artifact{
			Size: 5176019, SHA256: strings.Repeat("a", 64),
			FPK: &release.FPKInfo{Version: "0.6.377", Platform: "x86", ELFMachine: "x86_64", FrontendBundle: "index-x.js"},
		}},
		{Channel: "fndepot", OK: true, VerifySkipped: true, VerifySkipReason: "默认只验收第一条可用通道（--verify-all 可全验）"},
	}
	out := capture(t, func() { emitText("0.6.377", "x86", 2*time.Second, rs, false, true) })
	if strings.Contains(out, "契约违规") {
		t.Fatalf("跳过不应显示为契约违规：\n%s", out)
	}
	if !strings.Contains(out, "⏭") || !strings.Contains(out, "只验收第一条") {
		t.Fatalf("跳过行应显示原因：\n%s", out)
	}
	if !strings.Contains(out, "1 条通过客户端同款验收") {
		t.Fatalf("汇总行应给出通过条数：\n%s", out)
	}
}

// TestEmitTextEmptyVerifyErrNeverEmpty：分类兜底——绝不能再打印空的「契约违规：」。
func TestEmitTextEmptyVerifyErrNeverEmpty(t *testing.T) {
	rs := []release.Result{{Channel: "mirror", OK: true}}
	out := capture(t, func() { emitText("0.6.377", "x86", time.Second, rs, false, true) })
	if strings.Contains(out, "契约违规：\n") || strings.HasSuffix(strings.TrimSpace(out), "契约违规：") {
		t.Fatalf("空错误详情必须给出提示：\n%s", out)
	}
	if !strings.Contains(out, "无错误详情") {
		t.Fatalf("应提示分类缺陷：\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// --local-dir：以子进程方式跑真 CLI，验证退出码与输出（runLocal 会 os.Exit，
// 进程内调用无法取退出码，故用测试二进制自举）。
// ---------------------------------------------------------------------------

// TestMain 允许子进程模式下直接跑 CLI 主流程。
func TestMain(m *testing.M) {
	if os.Getenv("MOO_RELEASE_TEST_CLI") == "1" {
		os.Args = append([]string{"moo-release"}, strings.Split(os.Getenv("MOO_RELEASE_TEST_ARGS"), "\x1f")...)
		main()
		return
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "MOO_RELEASE_TEST_CLI=1",
		"MOO_RELEASE_TEST_ARGS="+strings.Join(args, "\x1f"))
	out, err := cmd.Output()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// 本地验收通过：用仓库根的真包（moo_0.6.377_x86.fpk + 两个侧车）。
func TestCLILocalDirPass(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "moo_0.6.377_x86.fpk.minisig")); err != nil {
		t.Skip("仓库根缺 0.6.377 三件套，跳过真机用例")
	}
	out, code := runCLI(t, "--tag", "0.6.377", "--arch", "x86", "--local-dir", repo)
	if code != 0 {
		t.Fatalf("期望 exit=0，实际 %d：%s", code, out)
	}
	if !strings.Contains(out, "✅ 本地验收通过") || !strings.Contains(out, "manifest=0.6.377/x86") {
		t.Fatalf("输出不符：%s", out)
	}
}

// 本地验收失败：目录里没有对应包 → exit=1，JSON 字段完整。
func TestCLILocalDirFail(t *testing.T) {
	dir := t.TempDir()
	out, code := runCLI(t, "--tag", "9.9.9", "--arch", "x86", "--local-dir", dir)
	if code != 1 {
		t.Fatalf("期望 exit=1，实际 %d：%s", code, out)
	}
	if !strings.Contains(out, "❌ 本地验收失败") || !strings.Contains(out, "本地缺少应用包") {
		t.Fatalf("输出不符：%s", out)
	}

	out, code = runCLI(t, "--tag", "9.9.9", "--arch", "x86", "--local-dir", dir, "--json")
	if code != 1 {
		t.Fatalf("JSON 模式期望 exit=1，实际 %d", code)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("JSON 不可解析: %v（%s）", err, out)
	}
	if payload["verified"] != false || payload["exit_code"] != float64(1) || payload["artifact"] != nil {
		t.Fatalf("字段不符：%v", payload)
	}
	if payload["tag"] != "v0.6.377" && payload["tag"] != "9.9.9" && payload["tag"] != "v9.9.9" {
		t.Fatalf("tag 字段异常：%v", payload["tag"])
	}
}
