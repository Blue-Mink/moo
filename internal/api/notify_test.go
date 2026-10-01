package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
	"moo/internal/notify"
)

// doNotify 可信通道（网关 socket 模拟：trust=true + X-Trim-* 管理员头）——
// 0.6.144 安全审计 P1-3 后通知设置/渠道路由全挂管理员门，测试须走可信身份。
func doNotify(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, GatewayPrefix+path, strings.NewReader(body)).WithContext(t.Context())
	r.Header.Set("X-Trim-Username", "fnos")
	r.Header.Set("X-Trim-Userid", "1000")
	r.Header.Set("X-Trim-Isadmin", "true")
	r.Header.Set("X-Moo-Admin", "1") // 0.6.207-panel D2：非 GET 变更请求必带头
	rec := httptest.NewRecorder()
	s.Handler(true).ServeHTTP(rec, r)
	return rec
}

// TestNotifySettingsRoundTrip 读→改→写→回读校验：
// 缺省按目录 Default → 关单事件（合并语义，其余不动）→ 关总开关 → 回读一致。
func TestNotifySettingsRoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	// 初始：按目录缺省
	rec := doNotify(t, s, "GET", "/api/notify-settings", "")
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Enabled bool            `json:"enabled"`
		Events  map[string]bool `json:"events"`
		Catalog []notifyEvent   `json:"catalog"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if !got.Enabled || len(got.Catalog) != 33 {
		t.Fatalf("缺省应总开关开且 33 类事件: enabled=%v catalog=%d", got.Enabled, len(got.Catalog))
	}
	defMap := make(map[string]bool, len(got.Catalog))
	groups := make(map[string]bool, len(got.Catalog))
	for _, e := range got.Catalog {
		defMap[e.Key] = e.Default
		groups[e.Group] = true
		if got.Events[e.Key] != e.Default {
			t.Errorf("缺省事件 %s 应为 %v, got %v", e.Key, e.Default, got.Events[e.Key])
		}
	}
	// 0.6.133 新增 6 事件 + favorite_update 移入「应用更新」组
	for _, k := range []string{"updates_available", "auto_update_round", "source_sync_summary", "mirror_switched", "mirrors_recovered", "disk_space_alert"} {
		if !defMap[k] {
			t.Errorf("目录缺 0.6.133 事件或默认未开: %s", k)
		}
	}
	if !groups["应用更新"] {
		t.Error("缺「应用更新」分组")
	}

	// 关单事件（合并语义）
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"events":{"install_error":false}}`)
	if rec.Code != 200 {
		t.Fatalf("PUT 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if s.IsNotifyEventOn("install_error") {
		t.Error("install_error 应关")
	}
	if !s.IsNotifyEventOn("install_success") {
		t.Error("未提交的 install_success 不应被合并语义波及")
	}

	// 关总开关
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"enabled":false}`)
	if rec.Code != 200 || cfg.IsNotifyEnabled() {
		t.Fatalf("enabled=false 应保存: %d %s", rec.Code, rec.Body.String())
	}

	// 回读校验：重开总开关后逐事件一致
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"enabled":true,"events":{"update_error":false,"favorite_update":false}}`)
	if rec.Code != 200 {
		t.Fatalf("PUT 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	rec = doNotify(t, s, "GET", "/api/notify-settings", "")
	if rec.Code != 200 {
		t.Fatalf("GET 应 200")
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	want := map[string]bool{
		"install_error": false, "update_error": false, "favorite_update": false,
	}
	for k, v := range got.Events {
		if w, off := want[k]; off {
			if v != w {
				t.Errorf("事件 %s 应为 %v, got %v", k, w, v)
			}
		} else if v != defMap[k] {
			t.Errorf("事件 %s 应保持缺省 %v, got %v", k, defMap[k], v)
		}
	}

	// 未知事件拒绝
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"events":{"nope":true}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知事件应 400, got %d", rec.Code)
	}
}

// TestNotifyChannelDraftTestMaskedBackfill（0.6.144）：编辑弹窗「测试提供商」提交脱敏
// 回显参数（****尾4位）+渠道 ID，后端必须回填存储真实值再发送——不能把打码串
// 当 URL POST（unsupported protocol scheme）。
func TestNotifyChannelDraftTestMaskedBackfill(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"errcode":0}`))
	}))
	defer ts.Close()
	full := ts.URL + "/hook"

	rec := doNotify(t, s, "POST", "/api/notify-channels",
		`{"type":"wecom","name":"群机器人","params":{"webhook_url":"`+full+`"}}`)
	if rec.Code != 200 {
		t.Fatalf("新增应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	id := cfg.NotifyChannels[0].ID

	masked := "****" + full[len(full)-4:]
	rec = doNotify(t, s, "POST", "/api/notify-channels/test",
		`{"id":"`+id+`","type":"wecom","name":"群机器人","params":{"webhook_url":"`+masked+`"},"format":"markdown"}`)
	if rec.Code != 200 {
		t.Fatalf("草稿测试应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK {
		t.Fatalf("测试应成功（回填存储值）: %s", got.Error)
	}
	if hits != 1 {
		t.Errorf("mock webhook 应恰好命中 1 次（打码值不得外发），got %d", hits)
	}
	if cfg.NotifyChannels[0].Params["webhook_url"] != full {
		t.Errorf("存储值不得被改动: %v", cfg.NotifyChannels[0].Params)
	}
}

// TestNotifyChannelsCRUD 渠道增删改查 + 脱敏 + 测试路由存在性。
func TestNotifyChannelsCRUD(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	// 缺省空列表 + 7 类定义
	rec := doNotify(t, s, "GET", "/api/notify-channels", "")
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rec.Code)
	}
	var got struct {
		Channels    []config.NotifyChannel `json:"channels"`
		Definitions []struct {
			Type string `json:"type"`
		} `json:"definitions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Channels) != 0 || len(got.Definitions) != 7 {
		t.Fatalf("缺省应 0 渠道 7 定义: %d/%d", len(got.Channels), len(got.Definitions))
	}

	// 缺必填参数拒绝
	rec = doNotify(t, s, "POST", "/api/notify-channels", `{"type":"wecom","name":"群机器人"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 webhook_url 应 400, got %d", rec.Code)
	}

	// 新增
	rec = doNotify(t, s, "POST", "/api/notify-channels",
		`{"type":"wecom","name":"群机器人","params":{"webhook_url":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc12345"}}`)
	if rec.Code != 200 {
		t.Fatalf("新增应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if len(cfg.NotifyChannels) != 1 || cfg.NotifyChannels[0].ID == "" {
		t.Fatalf("应保存 1 渠道: %+v", cfg.NotifyChannels)
	}

	// GET 脱敏：敏感字段只留尾 4 位
	rec = doNotify(t, s, "GET", "/api/notify-channels", "")
	body := rec.Body.String()
	if strings.Contains(body, "abc12345") {
		t.Errorf("webhook_url 应脱敏: %s", body)
	}
	if !strings.Contains(body, "****2345") {
		t.Errorf("应保留尾 4 位: %s", body)
	}

	// 改名（脱敏参数写回 = 保留原值）
	id := cfg.NotifyChannels[0].ID
	rec = doNotify(t, s, "PUT", "/api/notify-channels/"+id,
		`{"name":"改名机器人","enabled":true,"params":{"webhook_url":"****12345"}}`)
	if rec.Code != 200 {
		t.Fatalf("更新应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if cfg.NotifyChannels[0].Name != "改名机器人" {
		t.Errorf("名称未更新: %+v", cfg.NotifyChannels[0])
	}
	if cfg.NotifyChannels[0].Params["webhook_url"] != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc12345" {
		t.Errorf("脱敏写回应保留原值: %v", cfg.NotifyChannels[0].Params["webhook_url"])
	}

	// 删除
	rec = doNotify(t, s, "DELETE", "/api/notify-channels/"+id, "")
	if rec.Code != 200 || len(cfg.NotifyChannels) != 0 {
		t.Fatalf("删除应生效: %d %d", rec.Code, len(cfg.NotifyChannels))
	}

	// 不存在 ID 404
	rec = doNotify(t, s, "DELETE", "/api/notify-channels/ch_nope", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("不存在渠道应 404, got %d", rec.Code)
	}
}

// TestNotifyLogGateAndCap 记录语义（0.6.126）：前端应用内事件（POST /api/notify-log）
// 应用内通知栏始终开 → 必记；外部 fan-out 受总开关+事件开关控制。
// 超上限先进先出。
func TestNotifyLogGateAndCap(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	// install_success 缺省开：记 1 条
	rec := doNotify(t, s, "POST", "/api/notify-log", `{"event":"install_success","msg":"mihomo 安装成功","ok":true}`)
	if rec.Code != 200 {
		t.Fatalf("默认应入记录: %d %s", rec.Code, rec.Body.String())
	}
	if len(cfg.NotifyLog) != 1 || cfg.NotifyLog[0].Event != "install_success" {
		t.Fatalf("应记 1 条: %+v", cfg.NotifyLog)
	}

	// 事件关：只拦外部 fanout；应用内通知栏始终开 → 照常入记录（0.6.126）
	doNotify(t, s, "PUT", "/api/notify-settings", `{"events":{"install_error":false}}`)
	rec = doNotify(t, s, "POST", "/api/notify-log", `{"event":"install_error","msg":"x","ok":false}`)
	if rec.Code != 200 || len(cfg.NotifyLog) != 2 {
		t.Fatalf("应用内事件应照常在事件关时入记录: %s len=%d", rec.Body.String(), len(cfg.NotifyLog))
	}

	// 外部渠道总开关关：同样只拦外部 fanout，应用内记录照常
	doNotify(t, s, "PUT", "/api/notify-settings", `{"enabled":false}`)
	rec = doNotify(t, s, "POST", "/api/notify-log", `{"event":"install_success","msg":"y","ok":true}`)
	if rec.Code != 200 || len(cfg.NotifyLog) != 3 {
		t.Fatalf("外部总关不应影响应用内记录: %s len=%d", rec.Body.String(), len(cfg.NotifyLog))
	}
	// 后端直记（无应用内 toast）受外部总开关影响与否：事件开关开 = 照常记录
	s.logNotify("backup_done", "z", true)
	if len(cfg.NotifyLog) != 4 {
		t.Fatalf("logNotify 应照常记录, len=%d", len(cfg.NotifyLog))
	}

	// 上限：填满 + 超限，先进先出
	doNotify(t, s, "PUT", "/api/notify-settings", `{"enabled":true}`)
	for i := 0; i < config.NotifyLogCap+50; i++ {
		s.logNotify("backup_done", "m", true)
	}
	if len(cfg.NotifyLog) != config.NotifyLogCap {
		t.Fatalf("应截断到 %d, got %d", config.NotifyLogCap, len(cfg.NotifyLog))
	}

	// GET 新→旧
	rec = doNotify(t, s, "GET", "/api/notify-log", "")
	var got struct {
		Entries []config.NotifyLogEntry `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Entries) != config.NotifyLogCap {
		t.Fatalf("GET 应返回全部, got %d", len(got.Entries))
	}

	// 清空
	rec = doNotify(t, s, "POST", "/api/notify-log/clear", "")
	if rec.Code != 200 || len(cfg.NotifyLog) != 0 {
		t.Fatalf("清空应生效: %d len=%d", rec.Code, len(cfg.NotifyLog))
	}

	// 未知事件上报拒绝
	rec = doNotify(t, s, "POST", "/api/notify-log", `{"event":"nope","msg":"x","ok":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知事件应 400, got %d", rec.Code)
	}
}

// TestNotifyEventDispatch 事件 dispatch：事件开关关 = 完全静默；
// 外部渠道总开关关 = 不 fanout 但照常落记录（应用内始终开）；无渠道 = 只落记录。
func TestNotifyEventDispatch(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	// 无渠道：只落记录，Channels 空
	s.notifyEvent("install_success", "mihomo 安装成功", "v1.0", true)
	if len(cfg.NotifyLog) != 1 {
		t.Fatalf("应记 1 条: %+v", cfg.NotifyLog)
	}

	// 事件缺省关（store_update_success）：不记
	s.notifyEvent("store_update_success", "x", "", true)
	if len(cfg.NotifyLog) != 1 {
		t.Fatalf("缺省关事件不应记录, len=%d", len(cfg.NotifyLog))
	}

	// 外部渠道总开关关：不 fanout，但应用内通知始终开 → 照常记录
	cfg.NotifyEnabled = boolPtr(false)
	s.notifyEvent("install_success", "y", "", true)
	if len(cfg.NotifyLog) != 2 {
		t.Fatalf("外部总关不应影响应用内记录, len=%d", len(cfg.NotifyLog))
	}
}

func boolPtr(b bool) *bool { return &b }

// TestAboutGet 关于页数据：版本/架构/应用信息齐备。
func TestAboutGet(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Version: "0.6.121", Cfg: &config.Config{WebPort: "38100"}}
	rec := doNotify(t, s, "GET", "/api/about", "")
	if rec.Code != 200 {
		t.Fatalf("GET 应 200, got %d", rec.Code)
	}
	var got struct {
		Version string `json:"version"`
		Arch    string `json:"arch"`
		Port    string `json:"port"`
		App     struct {
			DisplayName string `json:"display_name"`
			Author      string `json:"author"`
			Homepage    string `json:"homepage"`
		} `json:"app"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if got.Version != "0.6.121" || got.Port != "38100" || got.Arch == "" {
		t.Errorf("基础信息不全: %+v", got)
	}
	if got.App.DisplayName != "Moo" || got.App.Author != "Blue-Mink" || !strings.Contains(got.App.Homepage, "Blue-Mink/moo") {
		t.Errorf("应用信息不全: %+v", got.App)
	}
}

// TestNotifyViewEndpoint 0.6.144 详情页短链：token 校验 + 404 + 0000test 演示页。
func TestNotifyViewEndpoint(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	// 0000test 演示页（无需 token；0.6.164 起展示欢迎语内容）
	rec := doNotify(t, s, "GET", "/api/notify-view/0000test", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "欢迎使用Moo") {
		t.Fatalf("0000test 应 200 欢迎语演示页, got %d", rec.Code)
	}

	// 未知 id → 404
	rec = doNotify(t, s, "GET", "/api/notify-view/ffffffff?t=deadbeef", "")
	if rec.Code != 404 {
		t.Errorf("未知 id 应 404, got %d", rec.Code)
	}

	// 写入一条带 ID/Token 的记录
	cfg.NotifyLog = append(cfg.NotifyLog, config.NotifyLogEntry{
		TS: 1758000000, Event: "install_success", Msg: "Moo · 安装成功",
		OK: true, Content: "应用 Fluxor v1.3.0 安装成功",
		ID: "abcd1234", Token: "0123456789abcdef",
	})
	// 正确 token → 200 + 内容
	rec = doNotify(t, s, "GET", "/api/notify-view/abcd1234?t=0123456789abcdef", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Fluxor") {
		t.Errorf("正确短链应 200 含正文, got %d", rec.Code)
	}
	// 错 token → 404
	rec = doNotify(t, s, "GET", "/api/notify-view/abcd1234?t=wrong", "")
	if rec.Code != 404 {
		t.Errorf("错 token 应 404, got %d", rec.Code)
	}
}

// TestNotifyViewBaseRoundTrip 0.6.144 详情页基础地址：PUT→GET 回读（尾斜杠去除）；
// 非 http(s) 前缀拒绝。
func TestNotifyViewBaseRoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	rec := doNotify(t, s, "PUT", "/api/notify-settings", `{"view_base":"http://<NAS_IP>:8090/"}`)
	if rec.Code != 200 {
		t.Fatalf("PUT view_base 应 200: %s", rec.Body.String())
	}
	rec = doNotify(t, s, "GET", "/api/notify-settings", "")
	var got struct {
		ViewBase string `json:"view_base"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ViewBase != "http://<NAS_IP>:8090" {
		t.Errorf("view_base 应去尾斜杠回读, got %q", got.ViewBase)
	}
	// 非法前缀
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"view_base":"nas.local:8090"}`)
	if rec.Code != 400 {
		t.Errorf("非 http(s) 前缀应 400, got %d", rec.Code)
	}
	// 空串 = 清除
	rec = doNotify(t, s, "PUT", "/api/notify-settings", `{"view_base":""}`)
	if rec.Code != 200 {
		t.Fatalf("清空 view_base 应 200: %s", rec.Body.String())
	}
	rec = doNotify(t, s, "GET", "/api/notify-settings", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ViewBase != "" {
		t.Errorf("清除后 view_base 应为空, got %q", got.ViewBase)
	}
}

// TestNotifyChannelFormatPUT 0.6.144 回归：PUT 更新渠道必须持久化 format。
func TestNotifyChannelFormatPUT(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}
	rec := doNotify(t, s, "POST", "/api/notify-channels",
		`{"type":"wecom","name":"w","params":{"webhook_url":"https://x"},"format":"markdown"}`)
	if rec.Code != 200 {
		t.Fatalf("POST 应 200: %s", rec.Body.String())
	}
	var got struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	rec = doNotify(t, s, "PUT", "/api/notify-channels/"+got.Channel.ID,
		`{"name":"w","enabled":true,"params":{"webhook_url":"****abcd"},"format":"card"}`)
	if rec.Code != 200 {
		t.Fatalf("PUT 应 200: %s", rec.Body.String())
	}
	rec = doNotify(t, s, "GET", "/api/notify-channels", "")
	var g2 struct {
		Channels []struct {
			Format string `json:"format"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &g2)
	if len(g2.Channels) != 1 || g2.Channels[0].Format != "card" {
		t.Errorf("PUT 后 format 应为 card, got %+v", g2.Channels)
	}
}

// TestSecurityLoopbackGating（0.6.144 安全审计 P1-3）：回环 TCP 通道（不可信，
// 模拟同机应用直连 127.0.0.1:38100）对敏感读写路由 403；网关管理员通道正常；
// 公开路由（version/目录）保持开放。
func TestSecurityLoopbackGating(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg}

	untrusted := func(method, path, body string) int {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(t.Context())
		rec := httptest.NewRecorder()
		s.Handler(false).ServeHTTP(rec, r)
		return rec.Code
	}
	if c := untrusted("GET", "/api/notify-channels", ""); c != http.StatusForbidden {
		t.Errorf("不可信 GET notify-channels 应 403, got %d", c)
	}
	if c := untrusted("PUT", "/api/settings", `{"download_dir":"/tmp"}`); c != http.StatusForbidden {
		t.Errorf("不可信 PUT settings 应 403, got %d", c)
	}
	if c := untrusted("GET", "/api/settings", ""); c != http.StatusForbidden {
		t.Errorf("不可信 GET settings 应 403, got %d", c)
	}
	if c := untrusted("GET", "/api/installed", ""); c != http.StatusForbidden {
		t.Errorf("不可信 GET installed 应 403, got %d", c)
	}
	if c := untrusted("GET", "/api/backups", ""); c != http.StatusForbidden {
		t.Errorf("不可信 GET backups 应 403, got %d", c)
	}
	if c := untrusted("GET", "/api/version", ""); c != http.StatusOK {
		t.Errorf("公开路由 /api/version 应保持开放, got %d", c)
	}
	if rec := doNotify(t, s, "GET", "/api/notify-channels", ""); rec.Code != http.StatusOK {
		t.Errorf("管理员通道应可读 notify-channels, got %d", rec.Code)
	}
}

// TestBackupMasksSecrets（0.6.144 安全审计 P1-1）：备份快照不含面板口令与
// 渠道敏感参数明文；备份文件权限 600。
func TestBackupMasksSecrets(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	cfg.PanelPassword = "super-secret-panel-pass"
	cfg.NotifyChannels = []config.NotifyChannel{{
		ID: "ch_test", Type: "wecom", Name: "g", Enabled: true,
		Params: map[string]string{"webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc12345def67890"},
	}}
	s := &Server{Cfg: cfg}
	name, err := s.writeBackup()
	if err != nil {
		t.Fatalf("writeBackup: %v", err)
	}
	p := filepath.Join(s.backupDirOf(), name)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "super-secret-panel-pass") {
		t.Error("备份不应含面板口令明文")
	}
	if strings.Contains(string(data), "key=abc12345def67890") {
		t.Error("备份不应含 webhook key 明文")
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("备份文件权限应 600, got %v", fi.Mode().Perm())
	}
}

// TestMaskSecretInErr（0.6.144 安全审计 P1-4）：错误串中的敏感查询参数被掩码。
func TestMaskSecretInErr(t *testing.T) {
	in := `Post "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcDEF123xyz": context deadline exceeded`
	got := notify.MaskSecretInErr(in)
	if strings.Contains(got, "abcDEF123xyz") {
		t.Errorf("key 应被掩码: %s", got)
	}
	if !strings.Contains(got, "key=****") {
		t.Errorf("应含 key=****: %s", got)
	}
	if notify.MaskSecretInErr("plain error") != "plain error" {
		t.Error("无命中串应原样返回")
	}
}
