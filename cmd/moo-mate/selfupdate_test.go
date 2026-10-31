package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"context"

	"aead.dev/minisign"
)

// ---- ① 架构选包 ----

func TestSelfUpdatePickAssetX86(t *testing.T) {
	assets := []mateReleaseAsset{
		{Name: "moo-mate_0.2.35_arm.fpk", URL: "http://x/arm.fpk"},
		{Name: "moo-mate_0.2.35_arm.fpk.sha256", URL: "http://x/arm.sha"},
		{Name: "moo-mate_0.2.35_x86.fpk", URL: "http://x/x86.fpk"},
		{Name: "moo-mate_0.2.35_x86.fpk.sha256", URL: "http://x/x86.sha"},
		{Name: "moo-mate_0.2.35_x86.fpk.minisig", URL: "http://x/x86.sig"},
	}
	fpk, sha, sig, err := pickMateSelfUpdateAsset(assets, "amd64")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fpk != "http://x/x86.fpk" || sha != "http://x/x86.sha" || sig != "http://x/x86.sig" {
		t.Fatalf("x86 选包错误：fpk=%q sha=%q sig=%q", fpk, sha, sig)
	}
}

func TestSelfUpdatePickAssetArm(t *testing.T) {
	assets := []mateReleaseAsset{
		{Name: "moo-mate_0.2.35_x86.fpk", URL: "http://x/x86.fpk"},
		{Name: "moo-mate_0.2.35_arm.fpk", URL: "http://x/arm.fpk"},
		{Name: "moo-mate_0.2.35_arm.fpk.sha256", URL: "http://x/arm.sha"},
		{Name: "moo-mate_0.2.35_arm.fpk.minisig", URL: "http://x/arm.sig"},
	}
	fpk, sha, sig, err := pickMateSelfUpdateAsset(assets, "arm64")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fpk != "http://x/arm.fpk" || sha != "http://x/arm.sha" || sig != "http://x/arm.sig" {
		t.Fatalf("arm 选包错误：fpk=%q sha=%q sig=%q", fpk, sha, sig)
	}
}

func TestSelfUpdatePickAssetUnsupportedArch(t *testing.T) {
	assets := []mateReleaseAsset{
		{Name: "moo-mate_0.2.35_x86.fpk", URL: "http://x/x86.fpk"},
	}
	if _, _, _, err := pickMateSelfUpdateAsset(assets, "riscv64"); err == nil {
		t.Fatal("未知架构必须报错")
	}
}

func TestSelfUpdatePickAssetMissingFpk(t *testing.T) {
	assets := []mateReleaseAsset{
		{Name: "moo-mate_0.2.35_arm.fpk", URL: "http://x/arm.fpk"},
	}
	if _, _, _, err := pickMateSelfUpdateAsset(assets, "amd64"); err == nil {
		t.Fatal("缺少本架构包必须报错")
	}
}

// 缺 .minisig 资产时 sigURL 必须为空串（调用方据此 fail-closed 拒绝升级）。
func TestSelfUpdatePickAssetMissingSignature(t *testing.T) {
	assets := []mateReleaseAsset{
		{Name: "moo-mate_0.2.35_x86.fpk", URL: "http://x/x86.fpk"},
		{Name: "moo-mate_0.2.35_x86.fpk.sha256", URL: "http://x/x86.sha"},
	}
	_, sha, sig, err := pickMateSelfUpdateAsset(assets, "amd64")
	if err != nil {
		t.Fatalf("不应报错（缺签名交给调用方判定）：%v", err)
	}
	if sha == "" {
		t.Fatal("sha256 侧车应存在")
	}
	if sig != "" {
		t.Fatalf("缺 .minisig 时 sigURL 必须为空，实际 %q", sig)
	}
}

// ---- 测试脚手架：假 daemon + 假 CDN ----

// mockDaemon 记录调用次数，并按预期逐步返回。
type mockDaemon struct {
	calls        int32 // update/task 调用次数
	srv          *httptest.Server
	socket       string
	failAt       string // 非空时该路径返回 code!=0
	codeMsg      string
	noVolume     bool // true 时 update/info 不返回存储卷（测「无卷则中止」）
	notInstalled bool // true 时 download/status 回报 installed=false
}

func startMockMateDaemon(t *testing.T) *mockDaemon {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")
	m := &mockDaemon{socket: sock}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, msg string, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg, "data": data})
	}
	mux.HandleFunc("/rpc/v1/download/task", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.calls, 1)
		if m.failAt == "/rpc/v1/download/task" {
			reply(w, 7, m.codeMsg, nil)
			return
		}
		reply(w, 0, "", map[string]any{"downloadTaskId": "dt-1"})
	})
	mux.HandleFunc("/rpc/v1/download/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 0, "", map[string]any{
			"status": 2, "appName": mateAppName, "version": "0.2.35",
			"packageType": "native", "installed": !m.notInstalled, "path": "/tmp/x.fpk",
		})
	})
	mux.HandleFunc("/rpc/v1/update/info", func(w http.ResponseWriter, r *http.Request) {
		if m.failAt == "/rpc/v1/update/info" {
			reply(w, 9, m.codeMsg, nil)
			return
		}
		if m.noVolume {
			reply(w, 0, "", map[string]any{"wizardInfo": map[string]any{}})
			return
		}
		reply(w, 0, "", map[string]any{"wizardInfo": map[string]any{"installedVolumeID": 1}})
	})
	mux.HandleFunc("/rpc/v1/update/task", func(w http.ResponseWriter, r *http.Request) {
		if m.failAt == "/rpc/v1/update/task" {
			reply(w, 11, m.codeMsg, nil)
			return
		}
		reply(w, 0, "", map[string]any{"taskId": "tk-1"})
	})
	mux.HandleFunc("/rpc/v1/common/status", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 0, "", map[string]any{"status": 2, "message": "", "outputText": ""})
	})

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	m.srv = &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: mux},
	}
	m.srv.Start()
	t.Cleanup(func() { m.srv.Close() })
	return m
}

// testSigner 生成一次性密钥对并把它注入为验签公钥（生产用内嵌公钥）。
// 返回的 sign 用于给测试载荷生成合法 minisig。
type testSigner struct{ priv minisign.PrivateKey }

func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败：%v", err)
	}
	old := matePubkeyOverride
	matePubkeyOverride = &pub
	t.Cleanup(func() { matePubkeyOverride = old })
	return &testSigner{priv: priv}
}

func (s *testSigner) sign(body []byte) []byte { return minisign.Sign(s.priv, body) }

// startMockCDN 返回一个提供 fpkBody / .sha256 / .minisig 的测试服务器。
// sidecar 为空串表示不提供 .sha256（返回 404）；sig 为 nil 表示不提供 .minisig。
func startMockCDN(t *testing.T, fpkBody []byte, sidecar string, sig []byte) (*httptest.Server, string, string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/moo-mate_0.2.35_x86.fpk", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(fpkBody)
	})
	mux.HandleFunc("/moo-mate_0.2.35_x86.fpk.sha256", func(w http.ResponseWriter, r *http.Request) {
		if sidecar == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sidecar))
	})
	mux.HandleFunc("/moo-mate_0.2.35_x86.fpk.minisig", func(w http.ResponseWriter, r *http.Request) {
		if len(sig) == 0 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(sig)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv,
		srv.URL + "/moo-mate_0.2.35_x86.fpk",
		srv.URL + "/moo-mate_0.2.35_x86.fpk.sha256",
		srv.URL + "/moo-mate_0.2.35_x86.fpk.minisig"
}

// setupSelfUpdateTest 让 daemon socket 指向假 daemon，并加速轮询。
func setupSelfUpdateTest(t *testing.T, m *mockDaemon) {
	t.Helper()
	oldSock, oldHook := mateSelfUpdateSocket, mateSelfUpdateHook
	mateSelfUpdateSocket = m.socket
	mateSelfUpdateHook = func() {}
	t.Cleanup(func() {
		mateSelfUpdateSocket = oldSock
		mateSelfUpdateHook = oldHook
	})
}

// 让 mateUpgrade 的默认参数变快（不改默认值本身，改为直接传参）
func fastParams() mateUpgradeParams {
	p := defaultMateUpgradeParams(mateAppName, "0.2.35", "native")
	p.PollInterval = 5 * time.Millisecond
	p.DownloadWait = 2 * time.Second
	p.TaskTimeout = 2 * time.Second
	return p
}

func shaOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ---- ② sha256 不符 → 拒绝，且 daemon 零调用 ----

func TestSelfUpdateSha256MismatchRejected(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	body := []byte("this is not the real fpk")
	bad := shaOf([]byte("something else")) + "  moo-mate_0.2.35_x86.fpk\n"
	sg := newTestSigner(t)
	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, bad, sg.sign(body))

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("sha256 不符必须报错")
	}
	if !strings.Contains(err.Error(), "校验失败") {
		t.Fatalf("错误文案应包含「校验失败」，实际：%v", err)
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("sha256 不符时 daemon 不得被调用，实际调用 %d 次", n)
	}
}

// ---- ③ 侧车缺失 → 拒绝 ----

func TestSelfUpdateMissingSidecarRejected(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	body := []byte("payload")
	sg := newTestSigner(t)
	srv, fpkURL, _, sigURL := startMockCDN(t, body, "", sg.sign(body)) // 侧车 404
	_ = srv
	shaURL := srv.URL + "/moo-mate_0.2.35_x86.fpk.sha256"

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("侧车缺失必须拒绝升级")
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("侧车缺失时 daemon 不得被调用，实际 %d 次", n)
	}

	// shaURL 为空（资产里压根没有侧车）也必须拒绝
	if err := runMateSelfUpdate(fpkURL, "", sigURL, "0.2.35", "native"); err == nil {
		t.Fatal("shaURL 为空必须拒绝升级")
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("shaURL 为空时 daemon 不得被调用，实际 %d 次", n)
	}
}

// ---- ③b 签名缺失 / 签名不符 / 载荷被换 → 一律拒绝，daemon 零调用 ----

func TestSelfUpdateMissingSignatureRejected(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	body := []byte("payload-no-sig")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	_, fpkURL, shaURL, _ := startMockCDN(t, body, good, nil) // .minisig 404

	// sigURL 为空（release 没附签名）必须拒绝
	if err := runMateSelfUpdate(fpkURL, shaURL, "", "0.2.35", "native"); err == nil {
		t.Fatal("sigURL 为空必须拒绝升级")
	}
	// 给了 URL 但服务器 404 也必须拒绝
	sigURL := strings.TrimSuffix(fpkURL, ".fpk") + ".fpk.minisig"
	if err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native"); err == nil {
		t.Fatal(".minisig 下载失败必须拒绝升级")
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("缺签名时 daemon 不得被调用，实际 %d 次", n)
	}
}

func TestSelfUpdateSignatureMismatchRejected(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	body := []byte("payload-signed-by-wrong-key")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	newTestSigner(t) // 验签公钥 = 密钥 A（本测试不用它签名）
	_, otherPriv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成第二把密钥失败：%v", err)
	}
	forged := minisign.Sign(otherPriv, body) // 用未登记的密钥 B 签名

	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, good, forged)
	err = runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("签名与验签公钥不匹配必须拒绝升级")
	}
	if !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("错误文案应含「签名校验失败」，实际：%v", err)
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("验签失败时 daemon 不得被调用，实际 %d 次", n)
	}
}

func TestSelfUpdateSignatureDoesNotCoverSwappedPayload(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	// 正经签名对应 original，但 CDN 实际发的是 tampered
	original := []byte("original fpk bytes")
	tampered := []byte("tampered fpk bytes")
	sg := newTestSigner(t)
	good := shaOf(tampered) + "  moo-mate_0.2.35_x86.fpk\n" // sha 侧车与包同时被换（攻击者能做到）
	_, fpkURL, shaURL, sigURL := startMockCDN(t, tampered, good, sg.sign(original))

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("载荷与签名不匹配必须拒绝升级（这正是 sha256 单独挡不住的情形）")
	}
	if n := atomic.LoadInt32(&m.calls); n != 0 {
		t.Fatalf("验签失败时 daemon 不得被调用，实际 %d 次", n)
	}
}

// 内嵌公钥必须可解析，且就是 Moo 那把（密钥 ID 51B18BC7E27A3069）。
func TestMateEmbeddedPubkeyValid(t *testing.T) {
	if !strings.Contains(mateSelfUpdatePubkeyText, "51B18BC7E27A3069") {
		t.Fatalf("内嵌公钥不是预期的 Moo 密钥：%q", mateSelfUpdatePubkeyText)
	}
	if err := verifyMateSignature(mateSelfUpdatePubkey, "", nil); err == nil {
		t.Fatal("空签名必须报错")
	}
}

// ---- ④ 假 daemon 全 4 步成功 ----

func TestSelfUpdateDaemonSuccess(t *testing.T) {
	m := startMockMateDaemon(t)
	setupSelfUpdateTest(t, m)

	body := []byte("fake fpk payload for success case")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	sg := newTestSigner(t)
	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, good, sg.sign(body))

	if err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native"); err != nil {
		t.Fatalf("应成功，实际：%v", err)
	}
	if n := atomic.LoadInt32(&m.calls); n != 1 {
		t.Fatalf("daemon download/task 应被调用 1 次，实际 %d", n)
	}
}

// ---- ⑤ daemon 返回 code!=0 → 失败且含 daemon 文案 ----

func TestSelfUpdateDaemonErrorSurfaced(t *testing.T) {
	m := startMockMateDaemon(t)
	m.failAt = "/rpc/v1/download/task"
	m.codeMsg = "包已损坏，无法解析"
	setupSelfUpdateTest(t, m)

	body := []byte("payload-2")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	sg := newTestSigner(t)
	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, good, sg.sign(body))

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("daemon 报错时必须失败")
	}
	if !strings.Contains(err.Error(), m.codeMsg) {
		t.Fatalf("错误应含 daemon 文案 %q，实际：%v", m.codeMsg, err)
	}
}

// ---- 附带：侧车解析 ----

func TestParseSha256Sidecar(t *testing.T) {
	good := strings.Repeat("a", 64)
	cases := []struct {
		in      string
		wantOK  bool
		wantSha string
	}{
		{good + "  file.fpk\n", true, good},
		{good + "\r\n", true, good},
		{good, true, good},
		{"abc  file\n", false, ""},
		{"", false, ""},
		{strings.Repeat("z", 64) + "  f\n", false, ""},
	}
	for i, c := range cases {
		got, err := parseSha256Sidecar([]byte(c.in))
		if c.wantOK {
			if err != nil {
				t.Fatalf("case %d 应成功，实际 %v", i, err)
			}
			if got != c.wantSha {
				t.Fatalf("case %d 摘要不符：%s", i, got)
			}
		} else if err == nil {
			t.Fatalf("case %d 应失败", i)
		}
	}
}

var _ = context.Background
var _ = os.Getpid

// ---- ⑥ 拿不到存储卷 → 中止（数据保护，对齐 Moo） ----

func TestSelfUpdateAbortWithoutVolume(t *testing.T) {
	m := startMockMateDaemon(t)
	m.noVolume = true
	setupSelfUpdateTest(t, m)

	body := []byte("payload-3")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	sg := newTestSigner(t)
	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, good, sg.sign(body))

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("拿不到存储卷必须中止升级")
	}
	if !strings.Contains(err.Error(), "存储卷") {
		t.Fatalf("错误文案应提到存储卷，实际：%v", err)
	}
}

// ---- ⑦ daemon 说包不是就地升级（installed=false）→ 中止 ----

func TestSelfUpdateAbortWhenNotInPlace(t *testing.T) {
	m := startMockMateDaemon(t)
	m.notInstalled = true
	setupSelfUpdateTest(t, m)

	body := []byte("payload-4")
	good := shaOf(body) + "  moo-mate_0.2.35_x86.fpk\n"
	sg := newTestSigner(t)
	_, fpkURL, shaURL, sigURL := startMockCDN(t, body, good, sg.sign(body))

	err := runMateSelfUpdate(fpkURL, shaURL, sigURL, "0.2.35", "native")
	if err == nil {
		t.Fatal("daemon 未识别为就地升级时必须中止")
	}
}

// ---- ⑧ 兜底通道必须同时给出 FPK / .sha256 / .minisig（防 0.6.370 事故重演） ----

func TestFallbackAssetURLCarriesSidecar(t *testing.T) {
	fpk := mateFallbackAssetURL("0.2.35", "moo-mate_0.2.35_x86.fpk")
	sha := mateFallbackAssetURL("0.2.35", "moo-mate_0.2.35_x86.fpk.sha256")
	sig := mateFallbackAssetURL("0.2.35", "moo-mate_0.2.35_x86.fpk.minisig")
	want := "/releases/download/mate-v0.2.35/"
	if !strings.Contains(fpk, want) || !strings.HasSuffix(fpk, "_x86.fpk") {
		t.Fatalf("FPK 兜底 URL 形状不对：%s", fpk)
	}
	if !strings.HasSuffix(sha, "_x86.fpk.sha256") {
		t.Fatalf("侧车兜底 URL 形状不对：%s", sha)
	}
	if !strings.HasSuffix(sig, "_x86.fpk.minisig") {
		t.Fatalf("签名兜底 URL 形状不对：%s", sig)
	}
}

// ---- ⑨ 并发点击只允许起一个升级任务（检查与置位必须同一把锁） ----

func TestStartSelfUpdateSingleFlight(t *testing.T) {
	oldProbe, oldSock, oldHook := mateProbeFn, mateSelfUpdateSocket, mateSelfUpdateHook
	mateSelfUpdateHook = func() {}
	mateSelfUpdateSocket = filepath.Join(t.TempDir(), "absent.sock")
	mateProbeFn = func() (mateUpdateInfo, error) {
		time.Sleep(150 * time.Millisecond) // 模拟慢探测（真实网络 6~8s）
		return mateUpdateInfo{}, errors.New("探测失败（测试）")
	}
	t.Cleanup(func() {
		mateProbeFn, mateSelfUpdateSocket, mateSelfUpdateHook = oldProbe, oldSock, oldHook
		mateSelfUpdate.mu.Lock()
		mateSelfUpdate.cur = mateSelfUpdateState{}
		mateSelfUpdate.mu.Unlock()
	})
	mateSelfUpdate.mu.Lock()
	mateSelfUpdate.cur = mateSelfUpdateState{}
	mateSelfUpdate.mu.Unlock()

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, errs[0] = startMateSelfUpdate() }()
	time.Sleep(30 * time.Millisecond) // 确保第一个已占位
	go func() { defer wg.Done(); _, errs[1] = startMateSelfUpdate() }()
	wg.Wait()

	if errs[0] == nil || errs[1] == nil {
		t.Fatalf("两次都应失败（探测恒失败），实际：%v / %v", errs[0], errs[1])
	}
	if !strings.Contains(errs[1].Error(), "已有更新任务") {
		t.Fatalf("第二个请求必须被单飞拦住，实际：%v", errs[1])
	}
}

// ---- ⑩ 资产 URL 选择：兜底通道必须给全三件套（Moo 0.6.369 事故同型） ----

// stubProviders 临时替换探测通道，返回 restore。
func stubProviders(t *testing.T, urls ...string) {
	t.Helper()
	old := mateUpdateProviders
	ps := make([]mateUpdateProvider, 0, len(urls))
	for i, u := range urls {
		ps = append(ps, mateUpdateProvider{name: "p" + string(rune('a'+i)), url: u})
	}
	mateUpdateProviders = ps
	t.Cleanup(func() { mateUpdateProviders = old })
}

func TestSelfUpdateAssetURLsFallbackCarriesAllThree(t *testing.T) {
	// 所有探测通道都挂 → 走约定 URL 兜底
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(bad.Close)
	stubProviders(t, bad.URL)

	fpk, sha, sig, err := mateSelfUpdateAssetURLs("0.2.36", "amd64")
	if err != nil {
		t.Fatalf("兜底不应报错：%v", err)
	}
	for what, u := range map[string]string{"fpk": fpk, "sha": sha, "sig": sig} {
		if u == "" {
			t.Fatalf("兜底通道缺 %s URL（0.6.369 事故同型：缺侧车会让 fail-closed 100%% 失败）", what)
		}
		if !strings.Contains(u, "/mate-v0.2.36/") {
			t.Fatalf("%s URL 的 tag 不对：%s", what, u)
		}
	}
	if !strings.HasSuffix(fpk, "moo-mate_0.2.36_x86.fpk") ||
		!strings.HasSuffix(sha, "moo-mate_0.2.36_x86.fpk.sha256") ||
		!strings.HasSuffix(sig, "moo-mate_0.2.36_x86.fpk.minisig") {
		t.Fatalf("兜底三件套命名不符：%s / %s / %s", fpk, sha, sig)
	}
}

// 资产清单属于「另一版」时不得使用（否则会装错包），必须回落 target 的约定 URL。
func TestSelfUpdateAssetURLsIgnoresMismatchedVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "mate-v0.2.34",
			"assets": []map[string]string{{
				"name":                 "moo-mate_0.2.34_x86.fpk",
				"browser_download_url": "http://cdn/0.2.34/x86.fpk",
			}},
		}})
	}))
	t.Cleanup(srv.Close)
	stubProviders(t, srv.URL)

	fpk, _, sig, err := mateSelfUpdateAssetURLs("0.2.36", "amd64")
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if strings.Contains(fpk, "0.2.34") || !strings.Contains(fpk, "0.2.36") {
		t.Fatalf("版本不符时仍用了旧资产 URL：%s", fpk)
	}
	if !strings.HasSuffix(sig, "moo-mate_0.2.36_x86.fpk.minisig") {
		t.Fatalf("版本不符时签名 URL 应指向 target：%s", sig)
	}
}

// 版本一致且资产齐全时用资产清单里的真实 URL（含镜像/签名）。
func TestSelfUpdateAssetURLsUsesMatchingAssets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "mate-v0.2.36",
			"assets": []map[string]string{
				{"name": "moo-mate_0.2.36_x86.fpk", "browser_download_url": "http://cdn/36/x86.fpk"},
				{"name": "moo-mate_0.2.36_x86.fpk.sha256", "browser_download_url": "http://cdn/36/x86.sha256"},
				{"name": "moo-mate_0.2.36_x86.fpk.minisig", "browser_download_url": "http://cdn/36/x86.minisig"},
			},
		}})
	}))
	t.Cleanup(srv.Close)
	stubProviders(t, srv.URL)

	fpk, sha, sig, err := mateSelfUpdateAssetURLs("0.2.36", "amd64")
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if fpk != "http://cdn/36/x86.fpk" || sha != "http://cdn/36/x86.sha256" || sig != "http://cdn/36/x86.minisig" {
		t.Fatalf("应使用资产清单 URL：%s / %s / %s", fpk, sha, sig)
	}
}
