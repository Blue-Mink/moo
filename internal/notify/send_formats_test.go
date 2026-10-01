package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"moo/internal/config"
)

// capture 起一个回固定 body 的测试 webhook，记录收到的请求体。
func capture(reply string) (*httptest.Server, *string) {
	var sink string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sink = string(b) + "||" + r.URL.String()
		w.Write([]byte(reply))
	}))
	return srv, &sink
}

func TestWeComFullGoesMarkdown(t *testing.T) {
	srv, sink := capture(`{"errcode":0}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "wecom", Verbosity: "full", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{Title: "Moo · 源同步摘要", Content: "全量列表内容",
		ViewURL: "http://detail.example/v1?t=1"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"msgtype":"markdown"`) {
		t.Fatalf("full 模式应发 classic markdown: %s", *sink)
	}
	if !strings.Contains(*sink, "全量列表内容") {
		t.Fatalf("full 正文缺失: %s", *sink)
	}
	// 0.6.213：尾部「完整明细：链接」已按用户要求移除（正文已全量列出，链接属冗余）
	if strings.Contains(*sink, "http://detail.example/v1?t=1") {
		t.Fatalf("0.6.213 起 full 模式不应再附详情链接: %s", *sink)
	}
}

func TestDingTalkCardFormat(t *testing.T) {
	srv, sink := capture(`{"errcode":0}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "dingtalk", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{Title: "Moo · 更新", Content: "正文", ViewURL: "http://d.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"msgtype":"actionCard"`) || !strings.Contains(*sink, "singleURL") {
		t.Fatalf("钉钉 card 应为 actionCard: %s", *sink)
	}
}

func TestDingTalkCardFallbackNoViewURL(t *testing.T) {
	srv, sink := capture(`{"errcode":0}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "dingtalk", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{Title: "Moo · x", Content: "正文"} // 无 ViewURL
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"msgtype":"markdown"`) {
		t.Fatalf("无详情页应降级 markdown: %s", *sink)
	}
}

func TestFeishuCardFormat(t *testing.T) {
	srv, sink := capture(`{"code":0}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "feishu", Format: "card",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{Title: "Moo · 源同步摘要", Content: "- a：1 个\n- b：2 个",
		ViewURL: "http://f.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"msg_type":"interactive"`) || !strings.Contains(*sink, `"tag":"button"`) {
		t.Fatalf("飞书 card 应为 interactive+按钮: %s", *sink)
	}
	if !strings.Contains(*sink, "http://f.example/v") {
		t.Fatalf("按钮跳转 URL 缺失: %s", *sink)
	}
}

func TestFeishuDefaultPost(t *testing.T) {
	srv, sink := capture(`{"code":0}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "feishu",
		Params: map[string]string{"webhook_url": srv.URL}}
	msg := &Message{Title: "Moo · 源同步摘要", Content: "- a：1 个\n- b：2 个"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"msg_type":"post"`) || !strings.Contains(*sink, `"tag":"text"`) {
		t.Fatalf("飞书默认应为 post 富文本: %s", *sink)
	}
}

func TestBarkCardURL(t *testing.T) {
	srv, sink := capture(`{"code":200,"message":"success"}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "bark", Format: "card",
		Params: map[string]string{"server_url": srv.URL, "device_key": "k1"}}
	msg := &Message{Title: "标题", Content: "正文", ViewURL: "http://b.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, "url=http%3A%2F%2Fb.example%2Fv") {
		t.Fatalf("bark card 应带 url 跳转参数: %s", *sink)
	}
}

func TestServerChanCardLink(t *testing.T) {
	srv, sink := capture(`{"code":200}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "serverchan", Format: "card",
		Params: map[string]string{"server_url": srv.URL, "sendkey": "SCTx"}}
	msg := &Message{Title: "标题", Content: "正文", ViewURL: "http://s.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, "[打开通知详情](http://s.example/v)") {
		t.Fatalf("serverchan card 应追加详情链接: %s", *sink)
	}
}

func TestWebhookDetailURLPlaceholder(t *testing.T) {
	srv, sink := capture(`ok`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "webhook",
		Params: map[string]string{"url": srv.URL, "body_template": "{\"detail\":\"{{detail_url}}\",\"t\":\"{{title}}\"}"}}
	msg := &Message{Title: "标题", Content: "正文", ViewURL: "http://w.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"detail":"http://w.example/v"`) {
		t.Fatalf("webhook {{detail_url}} 未替换: %s", *sink)
	}
}

func TestPushPlusCardLink(t *testing.T) {
	srv, sink := capture(`{"code":200}`)
	defer srv.Close()
	ch := config.NotifyChannel{Type: "pushplus", Format: "card",
		Params: map[string]string{"server_url": srv.URL, "token": "tok1"}}
	msg := &Message{Title: "标题", Content: "正文", ViewURL: "http://p.example/v"}
	if err := Send(context.Background(), ch, msg); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if !strings.Contains(*sink, `"template":"html"`) || !strings.Contains(*sink, "打开通知详情") {
		t.Fatalf("pushplus card 应 html+链接: %s", *sink)
	}
}
