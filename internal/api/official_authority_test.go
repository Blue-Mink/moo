package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"moo/internal/official"
	"moo/internal/platform"
)

// 0.6.257 回归：官方/平台应用（appcenter 跟踪：sourceID 非空 或 source=official）
// 在官方源（OAuth）断连时，不得因社区同名条目的更高版本产生假「有更新」——
// 更新判定完全交给 appcenter daemon 权威。手动 FPK（无 sourceID）保留社区源更新。

// ① 平台应用（daemon source=thirdparty 但 sourceID 非空，即从 appcenter 登记源
//
//	安装的官方应用，如 nodejs_v22/1Panel/python312/git）断连 + 无 daemon 升级：
//	社区同名更高版本 → 假更新被清除。这是用户 2026-10-03 报的 6 条假更新场景。
func TestApplyPlatformUpdateAuthority_TrackedThirdpartyClearsCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"nodejs_v22": {AppName: "nodejs_v22", Version: "22.18.0-1", Source: "thirdparty", SourceID: "98"},
	}
	out := []AppInfo{
		{
			Key:              "nodejs_v22@shuangji66",
			AppName:          "nodejs_v22",
			Source:           "shuangji66",
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.23.2",
			HasUpdate:        true,
			AvailableVersion: "22.23.2",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate {
		t.Error("appcenter 跟踪的官方应用（sourceID 非空）无 daemon 升级时不应 HasUpdate")
	}
	if a.AvailableVersion != "" {
		t.Errorf("AvailableVersion 应清空，实际 %q", a.AvailableVersion)
	}
	if a.LatestVersion != "22.18.0-1" {
		t.Errorf("bogus 高 LatestVersion 应回落已装版本 22.18.0-1，实际 %q", a.LatestVersion)
	}
}

// ② 官方应用（source=official）断连 + 无 daemon 升级：社区同名更高版本 → 清除。
func TestApplyPlatformUpdateAuthority_OfficialClearsCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {AppName: "trim.preview", Version: "0.2.4", Source: "official", SourceID: "388"},
	}
	out := []AppInfo{
		{
			Key:              "trim.preview",
			AppName:          "trim.preview",
			Source:           "someCommunity",
			Installed:        true,
			InstalledVersion: "0.2.4",
			LatestVersion:    "0.9.9",
			HasUpdate:        true,
			AvailableVersion: "0.9.9",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate || a.AvailableVersion != "" || a.LatestVersion != "0.2.4" {
		t.Errorf("官方应用无 daemon 升级应清除假更新: HasUpdate=%v avail=%q latest=%q",
			a.HasUpdate, a.AvailableVersion, a.LatestVersion)
	}
}

// ③ 平台应用断连 + 有 daemon 升级目标：真平台更新保留，目标=daemon 版本
//
//	（即使社区同名版本更高也不顶替）。
func TestApplyPlatformUpdateAuthority_DaemonUpgradeForcedTarget(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"trim.preview": {
			AppName:     "trim.preview",
			Version:     "0.1.16",
			Source:      "official",
			SourceID:    "388",
			Upgrade:     true,
			UpgradeInfo: &platform.UpgradeInfo{Version: "0.2.4"},
		},
	}
	out := []AppInfo{
		{
			Key:              "trim.preview",
			AppName:          "trim.preview",
			Source:           "someCommunity",
			Installed:        true,
			InstalledVersion: "0.1.16",
			LatestVersion:    "0.3.0",
			HasUpdate:        true,
			AvailableVersion: "0.3.0",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if !a.HasUpdate {
		t.Fatal("有 daemon 升级目标时应保留 HasUpdate")
	}
	if a.AvailableVersion != "0.2.4" {
		t.Errorf("目标应为 daemon 权威版本 0.2.4（非社区 0.3.0），实际 %q", a.AvailableVersion)
	}
}

// ④ 手动 FPK 应用（sourceID 空、source=thirdparty）：appcenter 不跟踪，
//
//	社区同名更高版本是合法更新，保留（零回归）。
func TestApplyPlatformUpdateAuthority_ManualFpkPreservesCommunity(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"wb2api": {AppName: "wb2api", Version: "1.11.1", Source: "thirdparty"}, // 无 sourceID
	}
	out := []AppInfo{
		{
			Key:              "wb2api@wabisabi926",
			AppName:          "wb2api",
			Source:           "wabisabi926",
			Installed:        true,
			InstalledVersion: "1.11.1",
			LatestVersion:    "1.11.6",
			HasUpdate:        true,
			AvailableVersion: "1.11.6",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if !a.HasUpdate || a.AvailableVersion != "1.11.6" {
		t.Errorf("手动 FPK 应用的社区同名更高版本应保留为更新，实际 HasUpdate=%v avail=%q",
			a.HasUpdate, a.AvailableVersion)
	}
}

// ⑤ 官方卡（Source==OfficialSourceID）：官方目录已收敛，本函数跳过不碰。
func TestApplyPlatformUpdateAuthority_OfficialCardSkipped(t *testing.T) {
	byName := map[string]platform.InstalledApp{
		"nodejs_v22": {AppName: "nodejs_v22", Version: "22.18.0-1", Source: "official", SourceID: "98"},
	}
	out := []AppInfo{
		{
			Key:              "nodejs_v22@fnos-official",
			AppName:          "nodejs_v22",
			Source:           OfficialSourceID,
			Installed:        true,
			InstalledVersion: "22.18.0-1",
			LatestVersion:    "22.18.0-1",
		},
	}
	applyPlatformUpdateAuthority(out, byName)

	a := &out[0]
	if a.HasUpdate || a.AvailableVersion != "" || a.LatestVersion != "22.18.0-1" {
		t.Errorf("官方卡不应被本函数改动: HasUpdate=%v avail=%q latest=%q",
			a.HasUpdate, a.AvailableVersion, a.LatestVersion)
	}
}

// ===== 2026-11-26 加固 C：GET /api/official/status 三态身份裁剪 =====
//
// 口径（用户令）：status **永远 200**（前端 apiFetch GET / Mate / 脚本都靠它判断
// 连接状态，改成 403 会把这些调用方全打死），但 panel_base / panel_base_source /
// last_error 只给管理员。last_error 里含面板基址与面板侧错误原文，panel_base 本身
// 就是内网拓扑 → 非管理员一律看不到，改用 can_admin=false 表达「你没有管理权限」。

// newStatusProbeHandler 造最小 Server（只含 /api/official/* 路由）并制造一条
// last_error：用不可达的面板基址跑一次换 token，失败信息里带面板地址——正是
// 非管理员必须被裁掉的内容（这样「删键」断言才有意义，不是空断言）。
func newStatusProbeHandler(t *testing.T) http.Handler {
	t.Helper()
	// 固定面板基址解析：env 命中即返回，不触发 5666/5667 回环探测。
	t.Setenv("MOO_PANEL_BASE", "http://127.0.0.1:5666")
	m := official.NewManagerWithBase(t.TempDir(), "http://127.0.0.1:1")
	if _, err := m.BeginAuthorizationAt(""); err != nil {
		t.Fatalf("BeginAuthorizationAt: %v", err)
	}
	if err := m.CompleteAuthorization(context.Background(), "MOCKCODE"); err == nil {
		t.Fatal("不可达面板应换 token 失败（用于制造 last_error）")
	}
	if _, ok := m.Status(time.Now())["last_error"]; !ok {
		t.Fatal("前置条件失败：Status 未记录 last_error")
	}
	srv := &Server{Official: m, Panel: &Panel{}}
	mux := http.NewServeMux()
	srv.officialRoutes(mux, srv.officialStoreV())
	return mux
}

// statusAs 以指定身份取一次 status（nil = 匿名/TCP 调试通道）。
func statusAs(t *testing.T, h http.Handler, u *User) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/official/status", nil)
	if u != nil {
		req = req.WithContext(withUserCtx(req.Context(), *u))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status 必须恒为 200（不因身份改 403），实际 %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON: %v body=%s", err, w.Body.String())
	}
	return got
}

// assertTrimmed 断言「非管理员视角」：无面板地址、无错误详情，can_admin=false，
// 但公开字段 authorized 仍在（脚本/前端靠它判断连接状态）。
func assertTrimmed(t *testing.T, got map[string]any) {
	t.Helper()
	for _, k := range []string{"panel_base", "panel_base_source", "last_error"} {
		if v, ok := got[k]; ok {
			t.Errorf("非管理员不得拿到 %q（实际值 %v）", k, v)
		}
	}
	if got["can_admin"] != false {
		t.Errorf("can_admin 应为 false，实际 %v", got["can_admin"])
	}
	if _, ok := got["authorized"]; !ok {
		t.Error("公开字段 authorized 必须保留（调用方据此判断连接状态）")
	}
}

// ① 匿名（TCP 调试通道 / 未登录 / 无身份上下文）：200 + 裁掉敏感键。
func TestOfficialStatus_AnonymousTrimmed(t *testing.T) {
	h := newStatusProbeHandler(t)
	got := statusAs(t, h, nil)
	assertTrimmed(t, got)
}

// ② 网关非管理员（可信通道但 IsAdmin=false）：与匿名同口径，仍然 200。
func TestOfficialStatus_GatewayNonAdminTrimmed(t *testing.T) {
	h := newStatusProbeHandler(t)
	got := statusAs(t, h, &User{Username: "guest", UserID: "2", Trusted: true, IsAdmin: false})
	assertTrimmed(t, got)
}

// ③ 管理员（可信通道 + X-Trim-Isadmin:true → IsAdmin=true）：can_admin=true，
//
//	面板基址与错误详情照常下发（管理界面需要它填默认端口）。
func TestOfficialStatus_AdminSeesPanelBase(t *testing.T) {
	h := newStatusProbeHandler(t)
	got := statusAs(t, h, &User{Username: "admin", UserID: "1", Trusted: true, IsAdmin: true})
	if got["can_admin"] != true {
		t.Fatalf("管理员 can_admin 应为 true，实际 %v", got["can_admin"])
	}
	if pb, _ := got["panel_base"].(string); pb == "" {
		t.Errorf("管理员应拿到 panel_base，实际 %v", got["panel_base"])
	}
	if _, ok := got["panel_base_source"]; !ok {
		t.Error("管理员应拿到 panel_base_source")
	}
	if _, ok := got["last_error"]; !ok {
		t.Error("管理员应拿到 last_error（排障需要）")
	}
}

// ④ 不可信通道即使自报管理员头也拿不到（withUser 只在 trust=true 时解析
//
//	X-Trim-Isadmin；这里直接构造 Untrusted+IsAdmin 表达「头伪造」场景）。
func TestOfficialStatus_UntrustedAdminHeaderIgnored(t *testing.T) {
	h := newStatusProbeHandler(t)
	got := statusAs(t, h, &User{Username: "fake", IsAdmin: true, Trusted: false})
	assertTrimmed(t, got)
}
