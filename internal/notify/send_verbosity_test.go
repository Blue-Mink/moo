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

// verbosityBase 带齐三形式 × 三长度变体的测试消息。
func verbosityBase() *Message {
	return &Message{
		Title:   "Moo · 源同步摘要",
		Content: "友好正文（紧凑）",
		Table:   "友好表格",
		Rows:    []CardRow{{Key: "源A", Value: "3 个应用"}},
		ContentConcise: "应用源 3 个 | 应用 7 个",
		RowsConcise:    []CardRow{{Key: "应用源", Value: "3 个"}, {Key: "应用", Value: "共 7 个"}},
		ContentFull: "完整正文（全量）",
		TableFull:   "完整表格",
		RowsFull:    []CardRow{{Key: "源A", Value: "3"}, {Key: "源B", Value: "2"}, {Key: "源C", Value: "2"}},
	}
}

func TestForChannelFriendly(t *testing.T) {
	m := verbosityBase()
	for _, v := range []string{"", "friendly"} {
		ch := config.NotifyChannel{Type: "wecom", Verbosity: v}
		got := m.ForChannel(ch)
		if got != m {
			t.Fatalf("friendly/%q 应原样返回，got 副本", v)
		}
		if got.Content != "友好正文（紧凑）" {
			t.Fatalf("friendly 正文被改动: %q", got.Content)
		}
	}
}

func TestForChannelConcise(t *testing.T) {
	m := verbosityBase()
	got := m.ForChannel(config.NotifyChannel{Type: "wecom", Verbosity: "concise"})
	if got == m {
		t.Fatal("concise 应返回副本")
	}
	if got.Content != "应用源 3 个 | 应用 7 个" {
		t.Fatalf("concise 正文错误: %q", got.Content)
	}
	if got.Table != "" {
		t.Fatalf("concise 应弃用表格: %q", got.Table)
	}
	if len(got.Rows) != 2 || got.Rows[0].Key != "应用源" {
		t.Fatalf("concise 卡片行错误: %+v", got.Rows)
	}
	// 原消息不受影响
	if m.Content != "友好正文（紧凑）" || len(m.Rows) != 1 || m.Table != "友好表格" {
		t.Fatal("原消息被污染")
	}
}

func TestForChannelConciseFallback(t *testing.T) {
	// 无简洁变体的事件（一次性事件）：回退友好正文
	m := &Message{Title: "Moo · x", Content: "唯一正文"}
	got := m.ForChannel(config.NotifyChannel{Type: "feishu", Verbosity: "concise"})
	if got.Content != "唯一正文" {
		t.Fatalf("无变体时应回退: %q", got.Content)
	}
	if got.Rows != nil {
		t.Fatalf("无 RowsConcise 时卡片行应置空: %+v", got.Rows)
	}
}

func TestForChannelFull(t *testing.T) {
	m := verbosityBase()
	got := m.ForChannel(config.NotifyChannel{Type: "wecom", Verbosity: "full"})
	if got.Content != "完整正文（全量）" || got.Table != "完整表格" {
		t.Fatalf("full 正文/表格错误: %q / %q", got.Content, got.Table)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("full 卡片行错误: %+v", got.Rows)
	}
}

func TestForChannelFullFallback(t *testing.T) {
	// 只有部分变体：缺失项回退主内容
	m := &Message{Title: "Moo · y", Content: "紧凑", ContentFull: "全量"}
	got := m.ForChannel(config.NotifyChannel{Type: "wecom", Verbosity: "full"})
	if got.Content != "全量" {
		t.Fatalf("full 正文错误: %q", got.Content)
	}
	if got.Table != "" || got.Rows != nil {
		t.Fatalf("缺失变体应回退: table=%q rows=%+v", got.Table, got.Rows)
	}
}

// TestFanoutVerbosity 三种长度的渠道各收各的内容（Fanout 分发全链路）。
func TestFanoutVerbosity(t *testing.T) {
	capture := func(sink *string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			*sink = string(b)
			w.Write([]byte(`{"errcode":0}`))
		}))
	}
	var friendlyB, conciseB, fullB string
	s1, s2, s3 := capture(&friendlyB), capture(&conciseB), capture(&fullB)
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	chs := []config.NotifyChannel{
		{Type: "wecom", Name: "友好", Enabled: true, Verbosity: "friendly", Params: map[string]string{"webhook_url": s1.URL}},
		{Type: "wecom", Name: "简洁", Enabled: true, Verbosity: "concise", Params: map[string]string{"webhook_url": s2.URL}},
		{Type: "wecom", Name: "完整", Enabled: true, Verbosity: "full", Params: map[string]string{"webhook_url": s3.URL}},
	}
	_ = Fanout(context.Background(), chs, verbosityBase())

	if !strings.Contains(friendlyB, "友好正文（紧凑）") {
		t.Fatalf("friendly 渠道收到错误内容: %s", friendlyB)
	}
	if !strings.Contains(conciseB, "应用源 3 个 | 应用 7 个") || strings.Contains(conciseB, "友好正文") {
		t.Fatalf("concise 渠道收到错误内容: %s", conciseB)
	}
	if !strings.Contains(fullB, "完整正文（全量）") {
		t.Fatalf("full 渠道收到错误内容: %s", fullB)
	}
}

func TestCapWeComContent(t *testing.T) {
	// 短文本原样
	if got := capWeComContent("短"); got != "短" {
		t.Fatalf("短文本应原样: %q", got)
	}
	// 长文本按行截断 + 显式标注（不静默隐藏）
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("| 源编号 " + strings.Repeat("x", 10) + " | 12 个应用 |\n")
	}
	long := b.String()
	if len([]byte(long)) <= 3800 {
		t.Fatal("测试前提：长文本应超 3800 字节")
	}
	got := capWeComContent(long)
	if !strings.Contains(got, "超出企业微信单条消息长度上限") {
		t.Fatalf("超长文本应带截断标注: %q", got[len(got)-80:])
	}
	if len([]byte(got)) > 4096 {
		t.Fatalf("截断后仍超企微上限: %d 字节", len([]byte(got)))
	}
	// 截断点落在行边界（截断前最后一行完整或为标注行）
	// 0.6.214：尾部措辞由「完整明细」改为「其余条目」（实测企微右列截断更友好）
	if !strings.HasSuffix(got, "…（超出企业微信单条消息长度上限，其余条目见应用内通知记录）") {
		t.Fatalf("截断标注不完整: %q", got[len(got)-60:])
	}
}

func TestValidateVerbosity(t *testing.T) {
	base := config.NotifyChannel{
		Type: "wecom", Name: "t",
		Params: map[string]string{"webhook_url": "https://x"},
	}
	for _, v := range []string{"", "friendly", "concise", "full"} {
		base.Verbosity = v
		if errs := Validate(base); len(errs) > 0 {
			t.Fatalf("verbosity=%q 应合法: %v", v, errs)
		}
	}
	base.Verbosity = "nonsense"
	if errs := Validate(base); len(errs) == 0 {
		t.Fatal("非法 verbosity 应报错")
	}
}


func TestForChannelConciseSubOverride(t *testing.T) {
	// 0.6.178：简洁卡副题用 ConciseSub 显式覆盖（防副题/大字/两列行重复
	// 同一数字）；原消息与 friendly 不受影响。
	m := verbosityBase()
	m.ConciseSub = "3 源成功 / 0 源失败"
	got := m.ForChannel(config.NotifyChannel{Type: "wecom", Verbosity: "concise"})
	if got.CardSub != "3 源成功 / 0 源失败" {
		t.Fatalf("concise 卡副题应被 ConciseSub 覆盖: %q", got.CardSub)
	}
	if m.CardSub != "" {
		t.Fatalf("原消息 CardSub 被污染: %q", m.CardSub)
	}
	gotF := m.ForChannel(config.NotifyChannel{Type: "wecom", Verbosity: "friendly"})
	if gotF.CardSub != "" {
		t.Fatalf("friendly 副题不应被覆盖: %q", gotF.CardSub)
	}
}
