package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// stubVerify 用替身替换真实下载/验签，只记录调用次数（离线可测的接缝）。
func stubVerify(t *testing.T) *int {
	t.Helper()
	old := verifyAssetsFn
	calls := 0
	verifyAssetsFn = func(_ context.Context, _ *http.Client, _ Assets, _, _, _ string) (*Artifact, error) {
		calls++
		return &Artifact{}, nil
	}
	t.Cleanup(func() { verifyAssetsFn = old })
	return &calls
}

// TestVerifyAllDefaultOnlyFirstMarksSkipped：默认只验第一条可用通道，
// 其余必须是「跳过（带原因）」——绝不能落进「契约违规：空消息」分支
// （2026-10-30 假失败：跳过只写了 Extra，没置 VerifySkipped → CLI 误报 + EXIT=1）。
func TestVerifyAllDefaultOnlyFirstMarksSkipped(t *testing.T) {
	calls := stubVerify(t)
	rs := []Result{
		{Channel: "selfidx", OK: true, Assets: Assets{FPK: "https://example.com/a.fpk"}},
		{Channel: "fndepot", OK: true, Assets: Assets{FPK: "https://example.com/b.fpk"}},
		{Channel: "mirror", OK: true, Assets: Assets{FPK: "https://example.com/c.fpk"}},
	}
	out := VerifyAll(context.Background(), Opts{Tag: "0.6.377", Arch: "x86", WorkDir: t.TempDir()}, rs)
	if !out[0].Verified {
		t.Fatalf("第一条应通过验收，实际 verified=%v err=%q", out[0].Verified, out[0].VerifyErr)
	}
	for _, i := range []int{1, 2} {
		if !out[i].VerifySkipped {
			t.Fatalf("第 %d 条必须标记 VerifySkipped（否则 CLI 报契约违规），实际 %+v", i+1, out[i])
		}
		if !strings.Contains(out[i].VerifySkipReason, "只验收第一条") {
			t.Fatalf("第 %d 条跳过原因不对：%q", i+1, out[i].VerifySkipReason)
		}
		if out[i].Verified || out[i].VerifyErr != "" {
			t.Fatalf("跳过既不算失败也不算通过，实际 %+v", out[i])
		}
	}
	if *calls != 1 {
		t.Fatalf("默认应只下载 1 次，实际 %d 次", *calls)
	}
}

// TestVerifyAllSameURLSkipped：同一 FPK URL 只验收一次（四条兜底共用 release URL）。
func TestVerifyAllSameURLSkipped(t *testing.T) {
	calls := stubVerify(t)
	url := "https://example.com/moo_0.6.377_x86.fpk"
	rs := []Result{
		{Channel: "web302", OK: true, Assets: Assets{FPK: url}},
		{Channel: "mirror", OK: true, Assets: Assets{FPK: url}},
	}
	out := VerifyAll(context.Background(), Opts{Tag: "0.6.377", Arch: "x86", WorkDir: t.TempDir(), VerifyAll: true}, rs)
	if !out[0].Verified {
		t.Fatalf("第一条应通过验收，实际 %+v", out[0])
	}
	if !out[1].VerifySkipped || !strings.Contains(out[1].VerifySkipReason, "同一 FPK URL") {
		t.Fatalf("同一 URL 必须跳过并给出原因，实际 %+v", out[1])
	}
	if *calls != 1 {
		t.Fatalf("--verify-all 下同一 URL 仍只下载 1 次，实际 %d 次", *calls)
	}
}

// TestVerifyAllIgnoresProbeFailed：探测失败的通道不参与验收。
func TestVerifyAllIgnoresProbeFailed(t *testing.T) {
	calls := stubVerify(t)
	rs := []Result{
		{Channel: "api", OK: false, Err: "HTTP 403"},
		{Channel: "selfidx", OK: true, Assets: Assets{FPK: "https://example.com/a.fpk"}},
	}
	out := VerifyAll(context.Background(), Opts{Tag: "0.6.377", WorkDir: t.TempDir()}, rs)
	if out[0].Verified || out[0].VerifySkipped {
		t.Fatalf("探测失败不应被验收阶段改写：%+v", out[0])
	}
	if !out[1].Verified || *calls != 1 {
		t.Fatalf("应只验收探测成功的那条，实际 verified=%v calls=%d", out[1].Verified, *calls)
	}
}

// TestSkipReasonPrecedence 直接覆盖判定优先级：先「默认只验第一条」，再「已验过」，再「试满次数」。
func TestSkipReasonPrecedence(t *testing.T) {
	verified := map[string]bool{"u1": true}
	attempts := map[string]int{"u1": 2, "u2": maxSameURLAttempts}
	if r := skipReason("u3", true, false, verified, attempts); !strings.Contains(r, "只验收第一条") {
		t.Fatalf("done 且非 verify-all 时优先报默认跳过，实际 %q", r)
	}
	if r := skipReason("u1", true, true, verified, attempts); !strings.Contains(r, "同一 FPK URL") {
		t.Fatalf("已验过的 URL 应报重复跳过，实际 %q", r)
	}
	if r := skipReason("u2", false, false, verified, attempts); !strings.Contains(r, "停止重试") {
		t.Fatalf("试满次数应停止重试，实际 %q", r)
	}
	if r := skipReason("u9", false, false, verified, attempts); r != "" {
		t.Fatalf("首次遇到的新 URL 不应跳过，实际 %q", r)
	}
}

// TestVerifyAllFailedURLDoesNotPoisonOthers：验收失败的 URL 不能被登记为「已验收」，
// 否则四条共用同一 release URL 的兜底通道全被跳过 → 0 条通过验收（2026-10-30 真实误判）。
func TestVerifyAllFailedURLDoesNotPoisonOthers(t *testing.T) {
	old := verifyAssetsFn
	calls := 0
	verifyAssetsFn = func(_ context.Context, _ *http.Client, _ Assets, _, _, _ string) (*Artifact, error) {
		calls++
		return nil, fmt.Errorf("%w: 下载中断: i/o timeout", ErrTransport)
	}
	t.Cleanup(func() { verifyAssetsFn = old })

	url := "https://example.com/moo_0.6.377_x86.fpk"
	rs := []Result{
		{Channel: "api", OK: true, Assets: Assets{FPK: url}},
		{Channel: "web302", OK: true, Assets: Assets{FPK: url}},
		{Channel: "selfidx", OK: true, Assets: Assets{FPK: url}},
	}
	out := VerifyAll(context.Background(), Opts{Tag: "0.6.377", Arch: "x86", WorkDir: t.TempDir()}, rs)
	if out[0].VerifySkipped || out[1].VerifySkipped {
		t.Fatalf("前两次应真实尝试（失败也算尝试），实际 skipped=%v/%v", out[0].VerifySkipped, out[1].VerifySkipped)
	}
	for _, i := range []int{0, 1} {
		if !out[i].VerifyTransport || out[i].Verified {
			t.Fatalf("超时必须是传输失败而非契约违规，实际 %+v", out[i])
		}
	}
	if !out[2].VerifySkipped || !strings.Contains(out[2].VerifySkipReason, "停止重试") {
		t.Fatalf("第三次应因试满次数跳过，实际 %+v", out[2])
	}
	if calls != maxSameURLAttempts {
		t.Fatalf("同一 URL 应只尝试 %d 次，实际 %d 次", maxSameURLAttempts, calls)
	}
}

// TestIsTransportErr：超时/断流归类为传输失败，契约类错误不能误判。
func TestIsTransportErr(t *testing.T) {
	yes := []error{
		fmt.Errorf("%w: i/o timeout", ErrTransport),
		context.DeadlineExceeded,
		io.ErrUnexpectedEOF,
		// 用 RFC 5737 文档段（198.51.100.0/24）而不是内网段：脱敏门会命中 10.x/192.168.x，
		// 测试夹具没必要踩线（2026-10-31）。
		errors.New("read tcp 198.51.100.7:443: connection reset by peer"),
		errors.New("net/http: request canceled (Client.Timeout or context cancellation while reading body)"),
	}
	for _, e := range yes {
		if !isTransportErr(e) {
			t.Fatalf("应判为传输失败：%v", e)
		}
	}
	no := []error{nil, errors.New("sha256 不符"), errors.New("验签失败")}
	for _, e := range no {
		if isTransportErr(e) {
			t.Fatalf("不应判为传输失败：%v", e)
		}
	}
}

// timeoutErr 模拟读响应体时的 i/o 超时（net.Error）。
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "read tcp: i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// bodyErrRT always 返回「读体超时」的响应体，用于确定性地打中下载读体失败路径。
type bodyErrRT struct{}

func (bodyErrRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(io.MultiReader(strings.NewReader("x"), errReader{})),
	}, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, timeoutErr{} }

// TestVerifyAssetsBodyTimeoutIsTransport：响应体读到一半超时 → 必须包成 ErrTransport，
// 否则调用方会当成「契约违规」把好发布判成坏发布（2026-10-30 真实误判）。
func TestVerifyAssetsBodyTimeoutIsTransport(t *testing.T) {
	c := &http.Client{Transport: bodyErrRT{}}
	base := "https://example.com/moo_0.6.377_x86.fpk"
	_, err := VerifyAssets(context.Background(), c,
		Assets{FPK: base, SHA256: base + ".sha256", Minisig: base + ".minisig"},
		"0.6.377", "x86", t.TempDir())
	if err == nil {
		t.Fatal("期望读体超时失败，实际成功")
	}
	if !errors.Is(err, ErrTransport) {
		t.Fatalf("读体超时必须包成 ErrTransport（否则误判契约违规），实际 err=%v", err)
	}
}
