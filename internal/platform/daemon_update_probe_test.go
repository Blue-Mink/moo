//go:build linux

package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// 0.6.574（B-UPDATE-CHANNEL-10371）：FPK 升级通道预检。
// 0.6.578 批次 3a（D1）：10371 命中后做二次确认（已装记录有无 sourceID）。
// 反向验证：删掉 ProbeFPKUpgrade 里的 10371 分支 → TestProbeFPKUpgradeNotAllowed FAIL；
// 把默认分支改成返回错误 → TestProbeFPKUpgradeOtherCodesPass FAIL；
// 去掉二次确认（10371 直接返回错误）→ TestProbeFPKUpgradeChannelTransientPasses 与
// TestProbeFPKUpgradeInstalledQueryFailsPasses FAIL。

// probeFixture 描述假 daemon 的行为（installed 用于 10371 的二次确认）。
type probeFixture struct {
	code int // update/info 回的平台 code
	msg  string
	// installedBody 非空时注册 /rpc/v1/app/installed 并回它。
	installedBody string
	// installedStatus 非 0 且非 200 时，installed 路由回该状态码 + 空体
	// （模拟「二次查询失败」）。
	installedStatus int
	// installedHits 记录 installed 路由被调用次数：证明二次确认真的查了，
	// 否则「返回 nil」可能只是根本没查。
	installedHits int32
}

func serveProbeDaemonWith(t *testing.T, f *probeFixture) {
	t.Helper()
	lis, err := net.Listen("unix", t.TempDir()+"/probe.sock")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/v1/update/info", func(w http.ResponseWriter, r *http.Request) {
		// 断言探针真的按 FPK 渠道、带目标版本发问（写错 packageType 会静默失效）
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// 关键断言：必须按 FPK 渠道发问（写错 packageType 会静默变成 cloud 语义）
		if body["packageType"] != "fpk" {
			t.Errorf("探针 packageType=%v，期望 fpk", body["packageType"])
		}
		if body["updateVersion"] == "" || body["appName"] == "" {
			t.Errorf("探针缺 appName/updateVersion：%v", body)
		}
		if f.code != 0 {
			fmt.Fprintf(w, `{"code":%d,"msg":%q}`, f.code, f.msg)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"wizardInfo":{"installedVolumeID":1}}}`)
	})
	if f.installedBody != "" || f.installedStatus != 0 {
		mux.HandleFunc("/rpc/v1/app/installed", func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&f.installedHits, 1)
			if f.installedStatus != 0 && f.installedStatus != http.StatusOK {
				w.WriteHeader(f.installedStatus)
				return
			}
			fmt.Fprint(w, f.installedBody)
		})
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(lis)
	orig := daemonSocket
	daemonSocket = lis.Addr().String()
	t.Cleanup(func() {
		srv.Close()
		daemonSocket = orig
	})
}

func serveProbeDaemon(t *testing.T, code int, msg string) {
	t.Helper()
	serveProbeDaemonWith(t, &probeFixture{code: code, msg: msg})
}

// probeInstalledBody 造 daemon 的 app/installed 响应（status=stop：
// 不触发 applyRealRunStates 的运行态探测，假 daemon 只服务本用例要问的路由）。
func probeInstalledBody(appName, sourceID string) string {
	return fmt.Sprintf(`{"code":0,"data":{"total":1,"list":[{"appName":%q,`+
		`"name":"演示","version":"1.0.0","status":"stop","sourceID":%q}]}}`, appName, sourceID)
}

func TestProbeFPKUpgradeNotAllowed(t *testing.T) {
	// 批次 3a：10371 的真实语义是「安装记录通道 ≠ 请求的 packageType」，
	// 判别量是已装记录的 sourceID。有 sourceID = 该安装归某个应用源管理，
	// 走 FPK 通道确实不允许 → 维持可读错误。
	f := &probeFixture{code: 10371, installedBody: probeInstalledBody("com.dustinky.qwenpaw", "243")}
	serveProbeDaemonWith(t, f)
	err := ProbeFPKUpgrade(context.Background(), "com.dustinky.qwenpaw", "2.3.0")
	if err == nil {
		t.Fatal("10371 + 安装记录有 sourceID 应当返回可读错误，实际放行")
	}
	for _, want := range []string{"官方应用中心", "应用中心", "10371", "卸载", "sourceID"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误文案缺少 %q：%v", want, err)
		}
	}
	if n := atomic.LoadInt32(&f.installedHits); n != 1 {
		t.Fatalf("二次确认应查一次已装记录，实际 %d 次", n)
	}
}

// TestProbeFPKUpgradeChannelTransientPasses 10371 但已装记录没有 sourceID
// （Moo 用 appcenter-cli 装的，appcenter 不跟踪）→ 判为瞬时态放行。
// 反向验证：去掉二次确认（10371 直接返回错误）→ 本用例 FAIL。
func TestProbeFPKUpgradeChannelTransientPasses(t *testing.T) {
	f := &probeFixture{code: 10371, installedBody: probeInstalledBody("demo", "")}
	serveProbeDaemonWith(t, f)
	if err := ProbeFPKUpgrade(context.Background(), "demo", "2.0.0"); err != nil {
		t.Fatalf("安装记录无 sourceID 应判为瞬时态放行，实际 %v", err)
	}
	if n := atomic.LoadInt32(&f.installedHits); n != 1 {
		t.Fatalf("二次确认未发生：installed 被查 %d 次，用例会假通过", n)
	}
}

// TestProbeFPKUpgradeInstalledQueryFailsPasses 二次查询本身失败（503/断连）→ 放行。
// 探测永远不能挡住正常升级，后续流程会用真实错误报告。
// 反向验证：去掉二次确认 → 本用例 FAIL。
func TestProbeFPKUpgradeInstalledQueryFailsPasses(t *testing.T) {
	f := &probeFixture{code: 10371, installedStatus: http.StatusInternalServerError}
	serveProbeDaemonWith(t, f)
	if err := ProbeFPKUpgrade(context.Background(), "demo", "2.0.0"); err != nil {
		t.Fatalf("安装记录查询失败必须放行，实际 %v", err)
	}
	if n := atomic.LoadInt32(&f.installedHits); n != 1 {
		t.Fatalf("二次确认未发生：installed 被查 %d 次，用例会假通过", n)
	}
}

func TestProbeFPKUpgradeOtherCodesPass(t *testing.T) {
	for _, code := range []int{0, 10100, 10500, 16006} {
		t.Run(fmt.Sprintf("code=%d", code), func(t *testing.T) {
			serveProbeDaemon(t, code, "")
			if err := ProbeFPKUpgrade(context.Background(), "demo", "1.0.1"); err != nil {
				t.Fatalf("code=%d 不属渠道拒绝，应放行，实际 %v", code, err)
			}
		})
	}
}

func TestProbeFPKUpgradeDaemonUnreachable(t *testing.T) {
	orig := daemonSocket
	daemonSocket = t.TempDir() + "/no-such.sock"
	t.Cleanup(func() { daemonSocket = orig })
	if err := ProbeFPKUpgrade(context.Background(), "demo", "1.0.1"); err != nil {
		t.Fatalf("探测失败必须放行（不能挡住正常升级），实际 %v", err)
	}
}

func TestDaemonCodeHintNotAllowUpgrade(t *testing.T) {
	e := &DaemonError{Path: "/rpc/v1/update/info", Code: 10371}
	got := e.Error()
	if !strings.Contains(got, "FPK 渠道") || !strings.Contains(got, "10371") {
		t.Fatalf("提示文案不符合预期：%s", got)
	}
}

// 复核 M2（2026-11-27）：二次确认的两条 fail-open 分支必须有永久用例——
// 否则删掉它们不会有任何用例变红（反向验证只覆盖了「无 sourceID / 查询失败」）。
func TestProbeFPKUpgradeInstNilPasses(t *testing.T) {
	f := &probeFixture{code: 10371, installedBody: `{"code":0,"data":{"total":0,"list":[]}}`}
	serveProbeDaemonWith(t, f)
	if err := ProbeFPKUpgrade(context.Background(), "demo", "2.0.0"); err != nil {
		t.Fatalf("已装列表查不到该应用应判瞬时态放行，实际 %v", err)
	}
	if got := atomic.LoadInt32(&f.installedHits); got == 0 {
		t.Fatal("应至少查询一次已装列表（否则放行可能只是没查）")
	}
}

// 预检预算耗尽（ctx 已过期）时必须放行：探测永远不能挡住正常升级。
func TestConfirmNotAllowUpgradeCtxExpiredPasses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := confirmNotAllowUpgrade(ctx, "demo", 10371); err != nil {
		t.Fatalf("预检预算耗尽时必须放行（fail-open），实际 %v", err)
	}
}
