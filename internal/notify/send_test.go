package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// TestValidate 必填校验 + 未知类型。
func TestValidate(t *testing.T) {
	if errs := Validate(config.NotifyChannel{Type: "wecom", Name: "a"}); len(errs) == 0 {
		t.Error("缺 webhook_url 应报错")
	}
	if errs := Validate(config.NotifyChannel{Type: "wecom", Name: "a", Params: map[string]string{"webhook_url": "https://x"}}); len(errs) != 0 {
		t.Errorf("完整参数不应报错: %v", errs)
	}
	if errs := Validate(config.NotifyChannel{Type: "nope"}); len(errs) == 0 {
		t.Error("未知类型应报错")
	}
	if errs := Validate(config.NotifyChannel{Type: "webhook", Name: "a", Params: map[string]string{"url": "https://x", "method": "PUT"}}); len(errs) == 0 {
		t.Error("webhook method=PUT 应报错")
	}
}

// TestMasked 敏感字段脱敏保留尾 4 位；非敏感字段不动。
func TestMasked(t *testing.T) {
	ch := config.NotifyChannel{
		Type:   "wecom",
		Name:   "bot",
		Params: map[string]string{
			"webhook_url":    "https://qyapi.weixin.qq.com/x?key=abcdef123456",
			"mentioned_list": "@all",
		},
	}
	m := Masked(ch)
	if strings.Contains(m.Params["webhook_url"], "abcdef") {
		t.Errorf("敏感字段应脱敏: %s", m.Params["webhook_url"])
	}
	if !strings.HasSuffix(m.Params["webhook_url"], "3456") {
		t.Errorf("应保留尾 4 位: %s", m.Params["webhook_url"])
	}
	if m.Params["mentioned_list"] != "@all" {
		t.Errorf("非敏感字段不应动: %s", m.Params["mentioned_list"])
	}
}

// TestSendWeCom 成功 + 业务码非 0。
// TestSendWeCom 默认形式 + 业务码报错。
func TestSendWeCom(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()
	ch := config.NotifyChannel{Type: "wecom", Name: "w", Params: map[string]string{"webhook_url": srv.URL}}
	if err := Send(context.Background(), ch, TestMessage()); err != nil {
		t.Fatalf("发送应成功: %v", err)
	}
	markdown, _ := got["markdown"].(map[string]any)
	if !strings.Contains(markdown["content"].(string), "Moo") {
		t.Errorf("markdown 内容应含标题: %v", markdown)
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid key"}`))
	}))
	defer srv2.Close()
	ch2 := config.NotifyChannel{Type: "wecom", Name: "w", Params: map[string]string{"webhook_url": srv2.URL}}
	if err := Send(context.Background(), ch2, TestMessage()); err == nil || !strings.Contains(err.Error(), "93000") {
		t.Errorf("业务码非 0 应报错: %v", err)
	}
}

// TestSendWeComFormats 0.6.144 三形式分流：默认 markdown / markdown_v2（表格版优先）/
// 卡片（有 ViewURL）；卡片缺 ViewURL 自动降级默认 markdown；未知形式报错。
func TestSendWeComFormats(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()
	msg := &Message{
		Title:   "Moo · 应用更新摘要",
		Content: "3 个已装应用有新版本：\n- A 1.0 → 1.1",
		Table:   "3 个已装应用有新版本：\n\n| 应用 | 当前 | 最新 |\n| :-- | :-- | :-- |\n| A | 1.0 | 1.1 |",
		OK:      true,
		TS:      1758000000,
	}

	// 1) 默认 markdown
	ch := config.NotifyChannel{Type: "wecom", Name: "w", Params: map[string]string{"webhook_url": srv.URL}}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("默认 markdown 应成功: %v", err)
	}
	if got["msgtype"] != "markdown" {
		t.Errorf("默认形式应发 markdown: %v", got["msgtype"])
	}
	wantTime := "> " + time.Unix(1758000000, 0).Format("01-02 15:04") + "\n\n"
	if md, _ := got["markdown"].(map[string]any); md == nil ||
		!strings.Contains(md["content"].(string), "### Moo · 应用更新摘要") {
		t.Errorf("markdown 应含 ### 标题: %v", got)
	}
	if md, _ := got["markdown"].(map[string]any); md == nil ||
		!strings.Contains(md["content"].(string), wantTime) {
		t.Errorf("0.6.161 起 markdown 标题下应带事件时间引用行: %v", got)
	}

	// 2) markdown_v2（有 Table 用表格版）
	ch2 := config.NotifyChannel{Type: "wecom", Name: "w", Format: "markdown_v2",
		Params: map[string]string{"webhook_url": srv.URL}}
	if err := Send(context.Background(), ch2, msg); err != nil {
		t.Fatalf("markdown_v2 应成功: %v", err)
	}
	if got["msgtype"] != "markdown_v2" {
		t.Errorf("应发 markdown_v2: %v", got["msgtype"])
	}
	if v2, _ := got["markdown_v2"].(map[string]any); v2 == nil ||
		!strings.Contains(v2["content"].(string), "| 应用 | 当前 | 最新 |") {
		t.Errorf("markdown_v2 应用 Table 表格版: %v", got)
	}
	if v2, _ := got["markdown_v2"].(map[string]any); v2 == nil ||
		!strings.Contains(v2["content"].(string), wantTime) {
		t.Errorf("0.6.161 起 markdown_v2 标题下应带事件时间引用行: %v", got)
	}

	// 2b) markdown_v2 无 Table 回退 Content
	msgNoTable := *msg
	msgNoTable.Table = ""
	if err := Send(context.Background(), ch2, &msgNoTable); err != nil {
		t.Fatalf("markdown_v2 无表格应成功: %v", err)
	}
	if v2, _ := got["markdown_v2"].(map[string]any); v2 == nil ||
		!strings.Contains(v2["content"].(string), "- A 1.0 → 1.1") {
		t.Errorf("markdown_v2 无 Table 应回退 Content: %v", got)
	}

	// 3) 卡片（有 ViewURL）
	ch3 := config.NotifyChannel{Type: "wecom", Name: "w", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msgCard := *msg
	msgCard.ViewURL = "http://nas/moo/api/notify-view/abcd1234?t=wxy"
	msgCard.Rows = []CardRow{{"Fluxor", "1.3.0 → 1.3.1"}, {"KSpeeder", "2.0.1 → 2.0.3"}}
	msgCard.EmphTitle = "1778"
	msgCard.EmphDesc = "应用总数"
	if err := Send(context.Background(), ch3, &msgCard); err != nil {
		t.Fatalf("卡片应成功: %v", err)
	}
	if got["msgtype"] != "template_card" {
		t.Fatalf("应发 template_card: %v", got["msgtype"])
	}
	card, _ := got["template_card"].(map[string]any)
	if card == nil || card["card_type"] != "text_notice" {
		t.Fatalf("card_type 应为 text_notice: %v", got)
	}
	ca, _ := card["card_action"].(map[string]any)
	if ca == nil || ca["url"] != msgCard.ViewURL {
		t.Errorf("card_action.url 应为详情页链接: %v", card["card_action"])
	}
	sub, _ := card["sub_title_text"].(string)
	if strings.Contains(sub, "\n") || len([]rune(sub)) > 112 {
		t.Errorf("sub_title_text 应折行且 ≤112 字: %q", sub)
	}
	// 列表式卡片：两列列表 + 大字强调（10:07 样例样式）
	hcl, _ := card["horizontal_content_list"].([]any)
	if len(hcl) != 2 {
		t.Errorf("horizontal_content_list 应有 2 行: %v", card["horizontal_content_list"])
	}
	if hcl != nil {
		r0, _ := hcl[0].(map[string]any)
		if r0["keyname"] != "Fluxor" || r0["value"] != "1.3.0 → 1.3.1" {
			t.Errorf("第一行应为 Fluxor/1.3.0 → 1.3.1: %v", r0)
		}
	}
	emph, _ := card["emphasis_content"].(map[string]any)
	if emph == nil || emph["title"] != "1778" {
		t.Errorf("emphasis_content 应为 1778: %v", card["emphasis_content"])
	}
	if !strings.HasPrefix(sub, "3 个已装应用有新版本") {
		t.Errorf("列表式卡片 sub 应为正文首行摘要: %q", sub)
	}

	// 4) 卡片缺 ViewURL → 降级默认 markdown
	if err := Send(context.Background(), ch3, msg); err != nil {
		t.Fatalf("卡片降级应成功: %v", err)
	}
	if got["msgtype"] != "markdown" {
		t.Errorf("卡片缺 ViewURL 应降级 markdown: %v", got["msgtype"])
	}

	// 5) 未知形式报错
	ch5 := config.NotifyChannel{Type: "wecom", Name: "w", Format: "fancy",
		Params: map[string]string{"webhook_url": srv.URL}}
	if err := Send(context.Background(), ch5, msg); err == nil {
		t.Error("未知形式应报错")
	}
}

// TestValidateFormat 0.6.144 通知形式校验。
func TestValidateFormat(t *testing.T) {
	base := config.NotifyChannel{Type: "wecom", Name: "w", Params: map[string]string{"webhook_url": "https://x"}}
	for _, f := range []string{"", "markdown", "markdown_v2", "card"} {
		base.Format = f
		if errs := Validate(base); len(errs) != 0 {
			t.Errorf("形式 %q 应通过: %v", f, errs)
		}
	}
	base.Format = "fancy"
	if errs := Validate(base); len(errs) == 0 {
		t.Error("未知形式应报错")
	}
}

// TestSendWebhook 模板替换 + 自定义头 + 4xx 报错。
func TestSendWebhook(t *testing.T) {
	var body, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ch := config.NotifyChannel{
		Type: "webhook", Name: "h",
		Params: map[string]string{
			"url":           srv.URL,
			"headers":       `{"Authorization":"Bearer tk"}`,
			"body_template": `{"title":"{{title}}","ok":{{ok}},"ev":"{{event}}","t":"{{time}}"}`,
		},
	}
	msg := &Message{Title: "T", Content: "C", Event: "install_success", OK: true, TS: 1700000000}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("webhook 应成功: %v", err)
	}
	if !strings.Contains(body, `"title":"T"`) || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, `"ev":"install_success"`) {
		t.Errorf("模板未替换: %s", body)
	}
	if auth != "Bearer tk" {
		t.Errorf("自定义头未生效: %q", auth)
	}

	srv400 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv400.Close()
	ch.Params["url"] = srv400.URL
	if err := Send(context.Background(), ch, msg); err == nil {
		t.Error("400 应报错")
	}
}

// TestSendBark URL 形态正确（title/body 编码 + 查询参数）。
func TestSendBark(t *testing.T) {
	var path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer srv.Close()
	ch := config.NotifyChannel{Type: "bark", Name: "b", Params: map[string]string{
		"server_url": srv.URL, "device_key": "KEY123", "level": "active", "group": "moo",
	}}
	if err := Send(context.Background(), ch, &Message{Title: "你好", Content: "内容&符", Event: "e", OK: true, TS: 1}); err != nil {
		t.Fatalf("bark 应成功: %v", err)
	}
	if !strings.Contains(path, "/KEY123/") {
		t.Errorf("path 应含 device_key: %s", path)
	}
	if !strings.Contains(query, "level=active") || !strings.Contains(query, "group=moo") {
		t.Errorf("query 应含参数: %s", query)
	}
}

// TestSendDingTalkSign 加签 URL 带 timestamp/sign。
func TestSendDingTalkSign(t *testing.T) {
	var u string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()
	ch := config.NotifyChannel{Type: "dingtalk", Name: "d", Params: map[string]string{
		"webhook_url": srv.URL + "/robot/send", "secret": "SEC123",
	}}
	if err := Send(context.Background(), ch, TestMessage()); err != nil {
		t.Fatalf("钉钉应成功: %v", err)
	}
	if !strings.Contains(u, "timestamp=") || !strings.Contains(u, "sign=") {
		t.Errorf("加签 URL 应带 timestamp/sign: %s", u)
	}
}

// TestIsTransient 瞬时（可重试）与非瞬时（业务拒绝，立即返回）分类。
func TestIsTransient(t *testing.T) {
	transient := []string{
		`Post "https://x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		`read tcp 1.2.3.4:5678: connection reset by peer`,
		`unexpected EOF`,
		`dial tcp: connection refused`,
		`lookup x on 1.1.1.1:53: no such host`,
	}
	for _, s := range transient {
		if !isTransient(fmt.Errorf("%s", s)) {
			t.Errorf("应判为瞬时: %s", s)
		}
	}
	permanent := []string{
		"企微 errcode=93000 errmsg=invalid webhook url",
		"webhook_url 未配置",
		"HTTP 400: bad request",
		"未知渠道类型: xxx",
	}
	for _, s := range permanent {
		if isTransient(fmt.Errorf("%s", s)) {
			t.Errorf("不应判为瞬时: %s", s)
		}
	}
	if isTransient(nil) {
		t.Error("nil 不应判为瞬时")
	}
}

// TestSendRetryTransient 瞬时故障（连接被服务端掐断 → EOF）第 1 次失败、
// 第 2 次成功：Send 应返回 nil 且服务端恰收 2 次请求。
func TestSendRetryTransient(t *testing.T) {
	orig := retryBackoffs
	retryBackoffs = []time.Duration{20 * time.Millisecond, 20 * time.Millisecond}
	defer func() { retryBackoffs = orig }()

	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			hj := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close() // 掐断 → 客户端收到 EOF（瞬时）
			return
		}
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ch := config.NotifyChannel{Type: "wecom", Name: "t", Params: map[string]string{"webhook_url": srv.URL}}
	if err := Send(context.Background(), ch, TestMessage()); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if hits != 2 {
		t.Errorf("应恰好 2 次请求: %d", hits)
	}
}

// TestSendNoRetryPermanent 业务错误（errcode 非 0）不重试，1 次即返回。
func TestSendNoRetryPermanent(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid key"}`))
	}))
	defer srv.Close()

	ch := config.NotifyChannel{Type: "wecom", Name: "t", Params: map[string]string{"webhook_url": srv.URL}}
	err := Send(context.Background(), ch, TestMessage())
	if err == nil {
		t.Fatal("业务错误应返回 error")
	}
	if hits != 1 {
		t.Errorf("业务错误不应重试: hits=%d", hits)
	}
}

// TestFanout 多渠道并发 + 单渠道失败不影响其余。
func TestFanout(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer badSrv.Close()

	chs := []config.NotifyChannel{
		{Type: "wecom", Name: "good", Enabled: true, Params: map[string]string{"webhook_url": okSrv.URL}},
		{Type: "wecom", Name: "bad", Enabled: true, Params: map[string]string{"webhook_url": badSrv.URL}},
		{Type: "wecom", Name: "off", Enabled: false, Params: map[string]string{"webhook_url": okSrv.URL}},
	}
	errs := Fanout(context.Background(), chs, TestMessage())
	if len(errs) != 1 || errs["bad"] == "" {
		t.Errorf("应只有 bad 渠道失败: %v", errs)
	}
	if _, ok := errs["off"]; ok {
		t.Errorf("禁用渠道不应发送: %v", errs)
	}

	// 无启用渠道 = nil
	errs = Fanout(context.Background(), []config.NotifyChannel{{Type: "wecom", Name: "off", Enabled: false}}, TestMessage())
	if errs != nil {
		t.Errorf("无启用渠道应返回 nil: %v", errs)
	}
}
