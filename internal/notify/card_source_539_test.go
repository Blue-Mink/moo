package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.539（用户截图批注）：企微卡片顶部那行 source「Moo」与群机器人发送者名
// 重复 → 去掉，让 main_title（Moo · 标题）+ 其下时间直接成为卡片首行。
func TestWeComCardOmitsSource_539(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	ch := config.NotifyChannel{Type: "wecom", Name: "w", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{
		Title:   "Moo · 源同步失败",
		Content: "本轮 60 个源成功 / 3 个失败",
		Event:   "source_sync",
		OK:      false,
		TS:      time.Date(2026, 11, 7, 14, 12, 0, 0, time.Local).Unix(),
		ViewURL: "http://example.local/api/notify-view/abcd1234?t=wxy",
		Rows: []CardRow{
			{"17funnyway8-ux", "所有候选地址均失败"},
			{"hhxs2025", "fnpack.json 不是 JSON"},
		},
	}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送应成功: %v", err)
	}
	tc, _ := got["template_card"].(map[string]any)
	if tc == nil {
		t.Fatalf("应为 template_card, got %v", got)
	}
	// 1) 不再有 source（顶部「Moo」行）
	if src, has := tc["source"]; has {
		t.Fatalf("0.6.539 起卡片不应再带 source 行, got %v", src)
	}
	// 2) 标题 + 时间仍是卡片首行（main_title），时间格式 MM-DD HH:MM
	mt, _ := tc["main_title"].(map[string]any)
	if mt == nil {
		t.Fatalf("main_title 必填, got %v", tc)
	}
	if title, _ := mt["title"].(string); title != "Moo · 源同步失败" {
		t.Errorf("main_title.title 应为「Moo · 源同步失败」, got %q", title)
	}
	desc, _ := mt["desc"].(string)
	if !regexp.MustCompile(`^\d\d-\d\d \d\d:\d\d$`).MatchString(desc) {
		t.Errorf("main_title.desc 应为 MM-DD HH:MM 时间, got %q", desc)
	}
	if desc != "11-07 14:12" {
		t.Errorf("时间应为 11-07 14:12, got %q", desc)
	}
	// 3) 既有内容不回归：正文摘要 / 两列列表 / 整卡跳转
	if sub, _ := tc["sub_title_text"].(string); sub != "本轮 60 个源成功 / 3 个失败" {
		t.Errorf("sub_title_text 回归: %q", sub)
	}
	if hcl, _ := tc["horizontal_content_list"].([]any); len(hcl) != 2 {
		t.Errorf("horizontal_content_list 应保留 2 行, got %v", tc["horizontal_content_list"])
	}
	ca, _ := tc["card_action"].(map[string]any)
	if ca == nil || ca["url"] != msg.ViewURL {
		t.Errorf("card_action.url 回归: %v", tc["card_action"])
	}
}
