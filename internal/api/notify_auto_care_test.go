package api

// notify_auto_care_test.go —— 2026-11-26 CARE P1-1c：源自动停用 / 自动恢复
// 通知的载荷级验证（CONVENTIONS.md §6.3 本地自测方式：六个渠道指向本机
// HTTP 端点捕获真实 JSON；不出网、不发真实消息）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// careCapture 造 6 个演示渠道（卡片/mdv2 × 简洁/友好/完整）并返回载荷读取器。
func careCapture(t *testing.T) (*Server, func(string) string) {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	var mu sync.Mutex
	bodies := map[string]string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Query().Get("name")] = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	t.Cleanup(ts.Close)

	cfg := &config.Config{NotifyViewBase: "http://notify.example.test"}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg)}
	add := func(name, format, verbosity string) {
		cfg.NotifyChannels = append(cfg.NotifyChannels, config.NotifyChannel{
			ID: "ch_" + name, Type: "wecom", Name: name, Enabled: true,
			Format: format, Verbosity: verbosity, Timeout: 5,
			Params: map[string]string{"webhook_url": ts.URL + "/hook?name=" + name},
		})
	}
	add("card-concise", "card", "concise")
	add("card-friendly", "card", "friendly")
	add("card-full", "card", "full")
	add("mdv2-concise", "markdown_v2", "concise")
	add("mdv2-friendly", "markdown_v2", "friendly")
	add("mdv2-full", "markdown_v2", "full")
	return s, func(name string) string {
		mu.Lock()
		defer mu.Unlock()
		return bodies[name]
	}
}

func waitCareBodies(t *testing.T, body func(string) string) {
	t.Helper()
	names := []string{"card-concise", "card-friendly", "card-full",
		"mdv2-concise", "mdv2-friendly", "mdv2-full"}
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := 0
		for _, n := range names {
			if body(n) != "" {
				got++
			}
		}
		if got >= len(names) {
			return
		}
		if time.Now().After(deadline) {
			for _, n := range names {
				if body(n) == "" {
					t.Fatalf("渠道 %s 未收到载荷（六变体必须真发出去）", n)
				}
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 停用通知：六变体形状 + 恢复指引 + 分因文案。
func TestSourceAutoDisabledVariants(t *testing.T) {
	s, body := careCapture(t)
	s.notifySourceAutoDisabled(source.CareEvent{
		Kind: source.CareEventDisabled, Name: "galaxy-saturn", Streak: 6,
		Reason: "仓库不存在（HTTP 404）：raw 直连与镜像均不可达",
	})
	waitCareBodies(t, body)

	if c := body("card-concise"); !strings.Contains(c, `"sub_title_text":"可在应用源页恢复"`) ||
		strings.Count(c, "keyname") != 2 || strings.Contains(c, "恢复指引") {
		t.Fatalf("卡片·简洁形状不符: %s", c)
	}
	// 卡片·友好：副题 = 正文首行（结论），两列行 3 行（状态/连续失败/原因）
	if c := body("card-friendly"); !strings.Contains(c, `"sub_title_text":"源「galaxy-saturn」已自动停用"`) ||
		strings.Count(c, "keyname") != 3 || !strings.Contains(c, "已自动停用") {
		t.Fatalf("卡片·友好形状不符: %s", c)
	}
	if c := body("card-full"); strings.Contains(c, "keyname") ||
		!strings.Contains(c, `"msgtype":"markdown"`) || !strings.Contains(c, "判定口径") {
		t.Fatalf("卡片·完整形状不符: %s", c)
	}
	if c := body("mdv2-concise"); !strings.Contains(c, "连续 6 次刷新失败已停用") ||
		strings.Contains(c, "| 项目 |") {
		t.Fatalf("mdv2·简洁形状不符: %s", c)
	}
	if c := body("mdv2-friendly"); !strings.Contains(c, "| 项目 | 值 |") ||
		!strings.Contains(c, "已自动停用（可恢复）") || !strings.Contains(c, "| 原因 |") {
		t.Fatalf("mdv2·友好形状不符: %s", c)
	}
	// mdv2·完整：企微「完整」短路到 classic markdown 全量（既有设计），
	// 正文含恢复指引与判定口径（不截断）。
	if c := body("mdv2-full"); strings.Contains(c, "| 项目 |") ||
		!strings.Contains(c, `"msgtype":"markdown"`) ||
		!strings.Contains(c, "恢复指引") || !strings.Contains(c, "判定口径") {
		t.Fatalf("mdv2·完整形状不符: %s", c)
	}
	if n := countNotifyEvent(s.Cfg, "source_auto_disabled"); n != 1 {
		t.Fatalf("应用内记录应 1 条, got %d", n)
	}
	if n := countNotifyEvent(s.Cfg, "notify_channel_error"); n != 0 {
		t.Fatalf("渠道全部成功不得出渠道失败告警: %d", n)
	}
}

// 恢复通知：六变体形状 + 应用数。
func TestSourceAutoRecoveredVariants(t *testing.T) {
	s, body := careCapture(t)
	s.notifySourceAutoRecovered(source.CareEvent{
		Kind: source.CareEventRecovered, Name: "galaxy-saturn", Count: 3, Streak: 2,
	})
	waitCareBodies(t, body)

	if c := body("card-concise"); !strings.Contains(c, `"sub_title_text":"连续 2 次探测有应用"`) ||
		strings.Count(c, "keyname") != 2 {
		t.Fatalf("卡片·简洁形状不符: %s", c)
	}
	if c := body("card-friendly"); !strings.Contains(c, "已自动恢复启用") ||
		strings.Count(c, "keyname") != 3 || !strings.Contains(c, "3 个") {
		t.Fatalf("卡片·友好形状不符: %s", c)
	}
	if c := body("mdv2-concise"); !strings.Contains(c, "已自动恢复（3 个应用）") {
		t.Fatalf("mdv2·简洁形状不符: %s", c)
	}
	if c := body("mdv2-friendly"); !strings.Contains(c, "| 探测到应用 | 3 个 |") {
		t.Fatalf("mdv2·友好形状不符: %s", c)
	}
	if c := body("mdv2-full"); !strings.Contains(c, `"msgtype":"markdown"`) || !strings.Contains(c, "计数") {
		t.Fatalf("mdv2·完整形状不符: %s", c)
	}
	if c := body("card-full"); !strings.Contains(c, `"msgtype":"markdown"`) || !strings.Contains(c, "说明：") {
		t.Fatalf("卡片·完整形状不符: %s", c)
	}
	if n := countNotifyEvent(s.Cfg, "source_auto_recovered"); n != 1 {
		t.Fatalf("应用内记录应 1 条, got %d", n)
	}
}

// 空源通道的停用通知：必须分因——源是好的、只是没应用，全文不得出现
// 「刷新失败」；指引要说清「源仍可访问 + 每 6 小时复查 + 连续 2 次有应用即恢复」。
func TestSourceAutoDisabledEmptyVariants(t *testing.T) {
	s, body := careCapture(t)
	s.notifySourceAutoDisabled(source.CareEvent{
		Kind: source.CareEventDisabled, Name: "galaxy-saturn", Streak: 5,
		Reason: "连续 5 次刷新无应用", Empty: true,
	})
	waitCareBodies(t, body)

	// 卡片/表格的「友好」变体只带结论行与两列行（正文被折叠），分因那句话
	// 落在「完整」变体（全文 markdown）里——按变体实际承载面断言。
	for _, n := range []string{"card-full", "mdv2-full"} {
		if !strings.Contains(body(n), "刷新成功但没有应用") {
			t.Fatalf("空源完整文案应写「刷新成功但没有应用」（%s）: %s", n, body(n))
		}
	}
	for _, n := range []string{"card-concise", "card-friendly", "card-full", "mdv2-concise", "mdv2-friendly", "mdv2-full"} {
		if strings.Contains(body(n), "刷新失败") {
			t.Fatalf("空源通道不得写「刷新失败」（%s）: %s", n, body(n))
		}
	}
	if c := body("mdv2-friendly"); !strings.Contains(c, "| 判定 | 刷新成功但无应用 |") {
		t.Fatalf("mdv2·友好应含分因判定行: %s", c)
	}
	// 「自动复查」行只在表格完整变体里加（与失败通道同排版口径）
	if c := body("mdv2-full"); !strings.Contains(c, "每 6 小时") {
		t.Fatalf("mdv2·完整应含复查口径: %s", c)
	}
	if c := body("card-concise"); !strings.Contains(c, `"sub_title_text":"可在应用源页恢复"`) ||
		strings.Count(c, "keyname") != 2 {
		t.Fatalf("卡片·简洁形状不符: %s", c)
	}
	if n := countNotifyEvent(s.Cfg, "source_auto_disabled"); n != 1 {
		t.Fatalf("应用内记录应 1 条, got %d", n)
	}
}

// 自动恢复达上限（heal_capped）：六变体形状 + 「需手动开启」措辞。
func TestSourceAutoHealCappedVariants(t *testing.T) {
	s, body := careCapture(t)
	s.notifySourceAutoHealCapped(source.CareEvent{
		Kind: source.CareEventHealCapped, Name: "galaxy-saturn", Count: 4, Streak: source.AutoCareHealCap,
	})
	waitCareBodies(t, body)

	if c := body("card-concise"); !strings.Contains(c, `"sub_title_text":"可在应用源页开启"`) ||
		strings.Count(c, "keyname") != 2 {
		t.Fatalf("卡片·简洁形状不符: %s", c)
	}
	if c := body("card-friendly"); strings.Count(c, "keyname") != 3 || !strings.Contains(c, "保持停用") {
		t.Fatalf("卡片·友好形状不符: %s", c)
	}
	if c := body("mdv2-concise"); !strings.Contains(c, "已达自动恢复上限（2 次/30 天）") {
		t.Fatalf("mdv2·简洁形状不符: %s", c)
	}
	if c := body("mdv2-friendly"); !strings.Contains(c, "| 30 天恢复 | 2 次 |") ||
		!strings.Contains(c, "| 探测到应用 | 4 个 |") {
		t.Fatalf("mdv2·友好形状不符: %s", c)
	}
	if c := body("card-full"); !strings.Contains(c, "判定口径") {
		t.Fatalf("卡片·完整应含判定口径: %s", c)
	}
	if n := countNotifyEvent(s.Cfg, "source_auto_heal_capped"); n != 1 {
		t.Fatalf("应用内记录应 1 条, got %d", n)
	}
}
