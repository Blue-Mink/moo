package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/notify"
)

// TestWelcomeInCatalog 事件目录含 welcome（Moo 自身组，缺省开）。
func TestWelcomeInCatalog(t *testing.T) {
	found := false
	for _, e := range notifyEventCatalog {
		if e.Key == "welcome" {
			found = true
			if e.Group != "Moo 自身" || !e.Default {
				t.Errorf("welcome 应在 Moo 自身组且缺省开: group=%s default=%v", e.Group, e.Default)
			}
		}
	}
	if !found {
		t.Fatal("事件目录缺少 welcome")
	}
}

// TestWelcomeContentForms 三形式内容非空、关键文案齐全、
// 默认 markdown 无段落空行（企微 classic 限制）、md_v2 从 ## 起（# 由发送侧前置）。
func TestWelcomeContentForms(t *testing.T) {
	for name, s := range map[string]string{
		"content": welcomeContent,
		"table":   welcomeTable,
	} {
		if strings.TrimSpace(s) == "" {
			t.Fatalf("%s 为空", name)
		}
		for _, phrase := range []string{"Moo is more", "蓝貂", "Blue-Mink", "为爱发电"} {
			if !strings.Contains(s, phrase) {
				t.Errorf("%s 缺关键文案 %q", name, phrase)
			}
		}
	}
	if strings.Contains(welcomeContent, "\n\n") {
		t.Error("默认 markdown 不应含段落空行（企微 classic 不渲染）")
	}
	if !strings.HasPrefix(welcomeTable, "## ") {
		t.Error("markdown_v2 正文应从 ## 起（# 大标题由 sendWeComV2 前置）")
	}
	// 0.6.159：md_v2 三行致谢必须 \n\n 断段——单 \n 是软换行，企微渲染成一段
	if !strings.Contains(welcomeTable, "项目测试者**\n\n💡") || !strings.Contains(welcomeTable, "反馈\n\n❤️") {
		t.Error("markdown_v2 三行致谢应用段落空行 \\n\\n 分隔（单 \\n 不断行）")
	}
}

// TestWelcomeFireEndpoint 手动触发：落记录（event=welcome、标题带 Moo · 前缀）；
// 事件开关关 = 静默不落记录（sent=false）。
func TestWelcomeFireEndpoint(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}}

	rec := doNotify(t, s, "POST", "/api/notify/welcome", "")
	if rec.Code != 200 {
		t.Fatalf("POST 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		OK   bool `json:"ok"`
		Sent bool `json:"sent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.OK || !out.Sent {
		t.Fatalf("应 sent=true, got %s", rec.Body.String())
	}
	if len(s.Cfg.NotifyLog) != 1 {
		t.Fatalf("应落 1 条记录, got %d", len(s.Cfg.NotifyLog))
	}
	e := s.Cfg.NotifyLog[0]
	if e.Event != "welcome" || e.Msg != "Moo · 欢迎使用Moo" || !e.OK {
		t.Errorf("记录不符: event=%s msg=%s ok=%v", e.Event, e.Msg, e.OK)
	}
	if !strings.Contains(e.Content, "Moo is more") {
		t.Error("记录正文应含欢迎语内容")
	}

	// 事件开关关 = 静默
	s.Cfg.NotifyEvents = map[string]bool{"welcome": false}
	rec = doNotify(t, s, "POST", "/api/notify/welcome", "")
	if rec.Code != 200 {
		t.Fatalf("关开关 POST 应 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"sent":false`) {
		t.Errorf("关开关应 sent=false, got %s", rec.Body.String())
	}
	if len(s.Cfg.NotifyLog) != 1 {
		t.Errorf("关开关不应新增记录, got %d", len(s.Cfg.NotifyLog))
	}
}

// TestWelcomeCardPayload 抓企微 webhook 实际收到的卡片 JSON（0.6.163 卡片版式验收，
// 以用户 1:39 卡片截图为骨架）：大字 hero「Moo is more」+ hero 下居中小字署名
//「蓝貂（Blue-Mink）」（emphasis_content.desc 落款）+ 副题三行致谢（完整文案，
// 句间 \n　\n 空行拉开行距，sub_title_text 上限 160 字不截断）；无两列列表
//（0.6.162 的 value 渲染上限约 23 字，26 字首句被截断后废弃）；
// 全文无 🦦（用户反馈弃用），「Moo is more」「蓝貂」「为爱发电」各只出现 1 次。
func TestWelcomeCardPayload(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	ch := config.NotifyChannel{
		ID: "t1", Type: "wecom", Name: "test", Enabled: true, Format: "card",
		Params: map[string]string{"webhook_url": srv.URL},
	}
	msg := &notify.Message{
		Title:     "Moo · 欢迎使用Moo",
		Content:   welcomeContent,
		Table:     welcomeTable,
		Event:     "welcome",
		OK:        true,
		TS:        time.Now().Unix(),
		ViewURL:   "http://example.local/api/notify-view/x?t=y",
		EmphTitle: "Moo is more",
		EmphDesc:  welcomeCardSign,
		CardSub:   welcomeCardSub,
	}
	if err := notify.Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送应成功: %v", err)
	}
	tc, _ := body["template_card"].(map[string]any)
	if tc == nil {
		t.Fatalf("应为 template_card, got %v", body)
	}
	emph, _ := tc["emphasis_content"].(map[string]any)
	if emph == nil {
		t.Fatal("应带大字 hero（emphasis_content）")
	}
	if title, _ := emph["title"].(string); title != "Moo is more" {
		t.Errorf("hero 大字应为「Moo is more」, got %q", title)
	}
	if desc, _ := emph["desc"].(string); desc != "蓝貂（Blue-Mink）" {
		t.Errorf("hero 下居中小字应为署名「蓝貂（Blue-Mink）」, got %q", desc)
	}
	sub, _ := tc["sub_title_text"].(string)
	if sub != welcomeCardSub {
		t.Errorf("副题应为三行致谢（句间 \\n　\\n 空行、完整文案）, got %q", sub)
	}
	// 0.6.162 回归锁：26 字首句必须完整（两列列表 value 渲染上限约 23 字会截断）
	if !strings.Contains(sub, "项目测试者") || strings.Contains(sub, "…") {
		t.Errorf("首句致谢应完整不截断, got %q", sub)
	}
	if rows, _ := tc["horizontal_content_list"].([]any); len(rows) != 0 {
		t.Errorf("0.6.163 起欢迎语卡片不应带两列列表, got %d 行", len(rows))
	}
	raw, _ := json.Marshal(tc)
	s := string(raw)
	if strings.Contains(s, "🦦") {
		t.Error("0.6.158 起欢迎语卡片不应含 🦦（用户反馈弃用）")
	}
	for _, phrase := range []string{"Moo is more", "蓝貂", "为爱发电"} {
		if n := strings.Count(s, phrase); n != 1 {
			t.Errorf("「%s」应全文只出现 1 次, got %d", phrase, n)
		}
	}
}

// TestChannelTestSendsWelcome（0.6.164）：「测试提供商」端点发欢迎语——
// 抓测试 webhook 实际收到的卡片 JSON：hero「Moo is more」+ 三行致谢副题；
// 卡片跳转链接指向 0000test 演示页；测试不落 notify_log。
func TestChannelTestSendsWelcome(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{NotifyViewBase: "http://example.local"}}

	draft := `{"type":"wecom","name":"test","format":"card","params":{"webhook_url":"` + srv.URL + `"}}`
	rec := doNotify(t, s, "POST", "/api/notify-channels/test", draft)
	if rec.Code != 200 {
		t.Fatalf("POST 应 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("应 ok=true, got %s", rec.Body.String())
	}
	tc, _ := body["template_card"].(map[string]any)
	if tc == nil {
		t.Fatalf("应为 template_card, got %v", body)
	}
	emph, _ := tc["emphasis_content"].(map[string]any)
	if emph == nil || emph["title"] != "Moo is more" {
		t.Errorf("测试消息应为欢迎语（hero Moo is more）, got %v", tc)
	}
	if desc, _ := emph["desc"].(string); desc != "蓝貂（Blue-Mink）" {
		t.Errorf("测试卡片应带居中落款, got %q", desc)
	}
	if sub, _ := tc["sub_title_text"].(string); sub != welcomeCardSub {
		t.Errorf("测试卡片副题应为三行致谢, got %q", sub)
	}
	jl, _ := tc["jump_list"].([]any)
	if len(jl) == 0 {
		t.Fatal("应带 jump_list")
	}
	first, _ := jl[0].(map[string]any)
	if first["url"] != "http://example.local/api/notify-view/0000test" {
		t.Errorf("跳转链接应指向 0000test 演示页, got %v", first["url"])
	}
	if len(s.Cfg.NotifyLog) != 0 {
		t.Errorf("测试不应落 notify_log, got %d 条", len(s.Cfg.NotifyLog))
	}
}

// TestWelcomeFirstBootOnce 首启判定单飞：首次 true、之后 false（重启/升级不重复）。
func TestWelcomeFirstBootOnce(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}}
	if !s.maybeFireWelcomeOnce() {
		t.Fatal("首次应触发")
	}
	if s.Cfg.WelcomeSentAt == 0 {
		t.Fatal("首次应落 WelcomeSentAt")
	}
	if s.maybeFireWelcomeOnce() {
		t.Fatal("第二次不应再触发")
	}
}
