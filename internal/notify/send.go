// Package notify 通知 fan-out（企微/钉钉/飞书/Server酱/PushPlus/Bark/Webhook）。
//
// 【新增通知类别必读】internal/notify/CONVENTIONS.md——2026-10-01 用户定稿的
// 排版硬约束（三长度变体×卡片/mdv2 六种组合、措辞符号、尾部禁「完整明细+
// 链接」、防刷屏与新增 checklist）。新事件一律走 notifyEventRichV 并按
// 规范填齐 Variants。
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"moo/internal/config"
)

// Message 一条通知消息（title 单行摘要，content markdown 明细）。
type Message struct {
	Title   string
	Content string // markdown（外部渠道发送的正文）
	// Full 完整正文（0.6.143）：仅应用内通知记录展示用（方格折叠卡）；
	// 空 = 记录里展示 Content。
	Full    string
	Event   string // 事件 key
	OK      bool
	TS      int64 // Unix 秒
	// Table markdown_v2 表格正文（0.6.144，列表类事件：应用更新摘要/源同步摘要等）；
	// 空 = markdown_v2 形式用 Content。
	Table string
	// Rows 卡片两列列表（0.6.144，与 Table 同源数据；最多取 6 行）。
	Rows []CardRow
	// EmphTitle/EmphDesc 卡片大字强调（emphasis_content；源同步摘要=应用总数）。
	EmphTitle string
	EmphDesc  string
	// CardSub 卡片副题显式覆盖（0.6.157，欢迎语多行原样发送含 \n）；
	// 空 = 按既有规则从 Content/Rows 推导。
	CardSub string
	// NoCardSub 强制不留副题（0.6.162，欢迎语：hero+署名已占位，
	// 三行致谢走 Rows 独立块，副题留空防与 hero 重复）。
	NoCardSub bool
	// ConciseSub 简洁模式卡片副题显式覆盖（0.6.178）：简洁卡的副题/大字/两列
	// 行各只承载唯一信息，避免同一数字重复出现（源同步摘要=副题源成功失败数、
	// 大字应用总数、行应用源数）。仅简洁变体生效（见 ForChannel）。
	ConciseSub string
	// ViewID/ViewToken 详情页短链（0.6.144，newNotifyMsg 生成）。
	ViewID    string
	ViewToken string
	// ViewURL 详情页链接（0.6.144）：卡片形式跳转用（= NotifyViewBase +
	// /api/notify-view/{id}?t={token}）；空 = 卡片自动降级默认 markdown。
	ViewURL string

	// ── 0.6.175 通知信息长度变体（空 = 该变体回退对应主内容）──
	// ContentConcise 简洁模式正文（仅关键内容）。
	ContentConcise string
	// ContentFull 完整模式正文（全部信息，不截断不隐藏）。
	ContentFull string
	// TableFull 完整模式 markdown_v2 表格（无 Top5/前 8 截断）。
	TableFull string
	// RowsConcise 简洁模式卡片两列行（如源同步摘要只留 应用源数/应用总数）。
	RowsConcise []CardRow
	// RowsFull 完整模式卡片两列行（放宽行数上限）。
	RowsFull []CardRow
}

// CardRow 卡片两列列表一行（horizontal_content_list，0.6.144 用户定稿样式：
// 10:07 企微样例——应用更新摘要/源同步摘要 Top5 等列表类事件在卡片里
// 用「名称 | 值」两列呈现，观感最接近表格）。
type CardRow struct {
	Key   string // 左列（建议 ≤5 字）
	Value string // 右列（建议 ≤26 字）
}

// Variants 通知信息长度变体（0.6.175，pass 侧构造，Fanout 按渠道 Verbosity 选用）。
// 空字段 = 该变体回退对应主内容（Content/Table/Rows）。
type Variants struct {
	ContentConcise string    // 简洁正文（仅关键内容）
	ContentFull    string    // 完整正文（全部信息，不截断）
	TableFull      string    // 完整 markdown_v2 表格（无 Top5/前 8 截断）
	RowsConcise    []CardRow // 简洁卡片行（如只保留 应用源数/应用总数 两行）
	ConciseSub     string    // 简洁卡片副题（0.6.178，防与大字/行重复）
	RowsFull       []CardRow // 完整卡片行（放宽行数上限，仍受卡片展示上限约束）
}

// applyVariants 按渠道 Verbosity 选出实际发送的消息副本（0.6.175）：
//   - friendly/空 = 原样（现有行为）；
//   - concise = Content→ContentConcise（空回退 Content）、Table 弃用
//     （markdown_v2 回退简洁正文）、Rows→RowsConcise（空 = 卡片不带列表）；
//   - full = Content→ContentFull（空回退 Content）、Table→TableFull（空回退
//     Table）、Rows→RowsFull（空回退 Rows）。
//
// 返回浅拷贝（不改共享的 msg）；其他渠道并行发送互不影响。
func (m *Message) ForChannel(ch config.NotifyChannel) *Message {
	v := strings.ToLower(strings.TrimSpace(ch.Verbosity))
	if v == "" || v == "friendly" {
		return m
	}
	c := *m
	switch v {
	case "concise":
		if t := strings.TrimSpace(m.ContentConcise); t != "" {
			c.Content = t
		}
		c.Table = ""
		if len(m.RowsConcise) > 0 {
			c.Rows = m.RowsConcise
		} else {
			c.Rows = nil
		}
		// 0.6.178 简洁卡副题去重：显式 ConciseSub 覆盖推导副题（防同一数字
		// 在副题/大字/两列行重复出现）。
		if t := strings.TrimSpace(m.ConciseSub); t != "" {
			c.CardSub = t
		}
	case "full":
		if t := strings.TrimSpace(m.ContentFull); t != "" {
			c.Content = t
		}
		if t := strings.TrimSpace(m.TableFull); t != "" {
			c.Table = t
		}
		if len(m.RowsFull) > 0 {
			c.Rows = m.RowsFull
		}
	}
	return &c
}

// FormatText 统一纯文本形态（飞书 text / webhook 兜底）。
func (m *Message) FormatText() string {
	okMark := "成功"
	if !m.OK {
		okMark = "失败/告警"
	}
	return fmt.Sprintf("%s\n%s\n\n[状态: %s] [%s]", m.Title, m.Content, okMark,
		time.Unix(m.TS, 0).Format("2006-01-02 15:04:05"))
}

const defaultTimeout = 10 * time.Second

// retryBackoffs 瞬时故障重试退避（2 次）。测试可临时覆盖为短间隔。
var retryBackoffs = []time.Duration{8 * time.Second, 20 * time.Second}

// isTransient 判断渠道发送错误是否为瞬时网络故障（值得重试）。
// 业务错误（errcode 非 0、参数缺失、HTTP 4xx 业务拒绝）不重试。
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "deadline exceeded"),
		strings.Contains(s, "Client.Timeout exceeded"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "EOF"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "no such host"),
		strings.Contains(s, "temporary failure in name resolution"):
		return true
	}
	return false
}

// Send 向单个渠道发送消息。渠道自身错误（网络/HTTP 非 2xx/业务码非 0）返回 error。
// 瞬时网络故障（超时/连接重置等）自动重试 2 次（退避 8s/20s）——源同步轮次
// 大量并发外网抓取期间出口瞬时拥塞是主要场景，退避后大概率已消退。
func Send(ctx context.Context, ch config.NotifyChannel, msg *Message) error {
	timeout := defaultTimeout
	if ch.Timeout > 0 {
		timeout = time.Duration(ch.Timeout) * time.Second
	}
	client := &http.Client{Timeout: timeout}

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			delay := retryBackoffs[attempt-1]
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return lastErr
			}
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		lastErr = sendOnce(cctx, client, ch, msg)
		cancel()
		if lastErr == nil {
			return nil
		}
		if !isTransient(lastErr) {
			return lastErr
		}
	}
	return fmt.Errorf("%v（已重试 2 次仍失败）", lastErr)
}

// sendOnce 单次发送（原 Send 的 switch 主体）。
func sendOnce(ctx context.Context, client *http.Client, ch config.NotifyChannel, msg *Message) error {
	switch ch.Type {
	case "wecom":
		return sendWeCom(client, ch, msg)
	case "dingtalk":
		return sendDingTalk(client, ch, msg)
	case "feishu":
		return sendFeishu(client, ch, msg)
	case "serverchan":
		return sendServerChan(client, ch, msg)
	case "pushplus":
		return sendPushPlus(client, ch, msg)
	case "bark":
		return sendBark(client, ch, msg)
	case "webhook":
		return sendWebhook(ctx, client, ch, msg)
	}
	return fmt.Errorf("未知渠道类型: %s", ch.Type)
}

// sendWeCom 企业微信（0.6.144：按渠道 Format 乌形式分流；0.6.177：
// 完整模式统一走 classic markdown 全量列表——卡片列表平台上限 6 行（官方文档）/
// md_v2 表格 20 行上限，都扑不下全量内容；markdown 4096 字节是企微最能装的单条容量）。
func sendWeCom(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	if strings.ToLower(strings.TrimSpace(ch.Verbosity)) == "full" {
		return sendWeComFull(client, ch, msg)
	}
	switch strings.ToLower(strings.TrimSpace(ch.Format)) {
	case "", "markdown":
		return sendWeComMarkdown(client, ch, msg)
	case "markdown_v2":
		return sendWeComV2(client, ch, msg)
	case "card":
		if strings.TrimSpace(msg.ViewURL) == "" {
			// 卡片必须有跳转链接；详情页地址未配置 → 降级默认形式（不报错，
			// 用户先配「详情页地址」即可生效，见推送渠道 tab 提示）。
			return sendWeComMarkdown(client, ch, msg)
		}
		return sendWeComCard(client, ch, msg)
	default:
		return fmt.Errorf("未知通知形式: %s", ch.Format)
	}
}

// sendWeComFull 完整详细模式（0.6.177）：classic markdown 发全量列表（Content 已经
// ForChannel 选为 ContentFull），超限按行截断+显式标注。
// 0.6.213：去掉尾部「[完整明细：链接]」——用户反馈不要（完整内容已全量列出，
// 详情页跳转仍保留在卡片按钮）。
func sendWeComFull(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	content := capWeComContent(msg.Content)
	body := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			"content": "### " + msg.Title + "\n\n" + timeLine(msg.TS) + content,
		},
	}
	if ml := strings.TrimSpace(ch.Params["mentioned_list"]); ml != "" {
		parts := strings.Split(ml, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		body["mentioned_list"] = parts
	}
	return postJSON(client, u, body, func(d map[string]any) error {
		if code, _ := d["errcode"].(float64); code != 0 {
			return fmt.Errorf("企微 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
		}
		return nil
	})
}

// capWeComContent 企微 markdown/markdown_v2 单条上限 4096 字节的保护
// （0.6.175 完整详细模式的全量列表可能超限）：按行边界截断到 ≤3800 字节，
// 并显式标注截断去向——不静默隐藏。
func capWeComContent(s string) string {
	const limit = 3800
	if len([]byte(s)) <= limit {
		return s
	}
	b := []byte(s)
	cut := limit
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	for cut < len(b) && b[cut] != '\n' {
		cut++
	}
	if cut >= len(b) {
		cut = limit
	}
	return string(b[:cut]) + "\n…（超出企业微信单条消息长度上限，其余条目见应用内通知记录）"
}

// timeLine md / md_v2 共用的事件时间引用行（0.6.161）。企微 markdown 类消息
// 没有原生时间字段（聊天角标=发送时间），故在标题下显式带事件时间，
// 格式与卡片 main_title.desc 一致（01-02 15:04）。
func timeLine(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return "> " + time.Unix(ts, 0).Format("01-02 15:04") + "\n\n"
}

// sendWeComMarkdown 默认形式：### 标题 + 时间引用行 + 正文（classic markdown，不支持表格）。
func sendWeComMarkdown(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	body := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{
			// 0.6.144 排版：标题与正文之间空行，观感更清爽；
			// 0.6.161：标题下加事件时间引用行（对齐卡片时间）；
			// 0.6.175：完整详细模式全量正文按行截断保护（防超企微单条上限）。
			"content": "### " + msg.Title + "\n\n" + timeLine(msg.TS) + capWeComContent(msg.Content),
		},
	}
	if ml := strings.TrimSpace(ch.Params["mentioned_list"]); ml != "" {
		// 逗号分隔 → 数组；@all 直接透传
		parts := strings.Split(ml, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		body["mentioned_list"] = parts
	}
	return postJSON(client, u, body, func(d map[string]any) error {
		if code, _ := d["errcode"].(float64); code != 0 {
			return fmt.Errorf("企微 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
		}
		return nil
	})
}

// sendWeComV2 富文本形式：H1 标题 + 正文（列表类事件用 Table 表格版，
// 表格上限 20 行/4 列，超出截断——企微 markdown_v2 表格规格）。
func sendWeComV2(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	bodyText := msg.Content
	if t := strings.TrimSpace(msg.Table); t != "" {
		bodyText = t
	}
	body := map[string]any{
		"msgtype": "markdown_v2",
		"markdown_v2": map[string]any{
			// 0.6.161：标题下加事件时间引用行（对齐卡片时间）；
			// 0.6.175：完整详细模式全量表格按行截断保护（防超企微单条上限）。
			"content": "# " + msg.Title + "\n\n" + timeLine(msg.TS) + capWeComContent(bodyText),
		},
	}
	if ml := strings.TrimSpace(ch.Params["mentioned_list"]); ml != "" {
		parts := strings.Split(ml, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		body["mentioned_list"] = parts
	}
	return postJSON(client, u, body, func(d map[string]any) error {
		if code, _ := d["errcode"].(float64); code != 0 {
			return fmt.Errorf("企微 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
		}
		return nil
	})
}

// sendWeComCard 卡片形式（template_card / text_notice）：
// 大字标题 + 单行摘要（正文折行截断 ≤112 字）+ 整卡/列表跳转详情页。
func sendWeComCard(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	viewURL := strings.TrimSpace(msg.ViewURL)
	sub := ""
	if !msg.NoCardSub {
		if cs := strings.TrimSpace(msg.CardSub); cs != "" {
			// 0.6.157：显式副题（欢迎语三行），原样发送（保留 \n 换行）
			sub = cs
		} else if len(msg.Rows) > 0 {
			// 列表式卡片：正文第一行作摘要头，明细走两列列表（10:07 样例样式）
			sub = msg.Content
			if i := strings.IndexByte(sub, '\n'); i >= 0 {
				sub = sub[:i]
			}
			sub = strings.TrimSuffix(sub, "：")
		} else {
			// 普通卡片：逐行清洗后折成单行（去引用符 "> "、空行、加粗标记）
			lines := strings.Split(msg.Content, "\n")
			cleaned := make([]string, 0, len(lines))
			for _, ln := range lines {
				ln = strings.TrimSpace(ln)
				ln = strings.TrimPrefix(ln, "> ")
				ln = strings.TrimSpace(ln)
				if ln == "" {
					continue
				}
				cleaned = append(cleaned, ln)
			}
			sub = strings.Join(cleaned, " · ")
		}
	}
	sub = strings.ReplaceAll(sub, "**", "")
	if r := []rune(sub); len(r) > 110 {
		sub = string(r[:107]) + "…"
	}
	title := strings.TrimPrefix(msg.Title, "Moo · ")
	if r := []rune(title); len(r) > 24 {
		title = string(r[:21]) + "…"
	}
	tc := map[string]any{
		"card_type": "text_notice",
		"source":    map[string]any{"desc": "Moo"},
		"main_title": map[string]any{"title": "Moo · " + title, "desc": time.Unix(msg.TS, 0).Format("01-02 15:04")},
		"jump_list": []map[string]any{
			{"type": 1, "url": viewURL, "title": "打开通知详情"},
		},
		"card_action": map[string]any{"type": 1, "url": viewURL},
	}
	if sub != "" { // 0.6.162：副题空时省略字段（防空白行/与 hero 重复）
		tc["sub_title_text"] = sub
	}
	if len(msg.Rows) > 0 {
		rows := make([]map[string]any, 0, len(msg.Rows))
		for i, r := range msg.Rows {
			if i >= 6 { // 企微 horizontal_content_list 上限 6 行
				break
			}
			rows = append(rows, map[string]any{"keyname": r.Key, "value": r.Value})
		}
		tc["horizontal_content_list"] = rows
	}
	if msg.EmphTitle != "" {
		tc["emphasis_content"] = map[string]any{"title": msg.EmphTitle, "desc": msg.EmphDesc}
	}
	body := map[string]any{
		"msgtype":       "template_card",
		"template_card": tc,
	}
	return postJSON(client, u, body, func(d map[string]any) error {
		if code, _ := d["errcode"].(float64); code != 0 {
			return fmt.Errorf("企微 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
		}
		return nil
	})
}

// ---------- 钉钉（加签） ----------

func sendDingTalk(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	if secret := strings.TrimSpace(ch.Params["secret"]); secret != "" {
		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		stringToSign := ts + "\n" + secret
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(stringToSign))
		sign := url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		sep := "&"
		if !strings.Contains(u, "?") {
			sep = "?"
		}
		u = u + sep + "timestamp=" + ts + "&sign=" + sign
	}
	// 0.6.177 通知形式分流（对齐企微三形式）：钉钉 markdown 不支持表格语法
	// → md_v2 以列表呈现；card = actionCard 整卡跳转（singleTitle/singleURL）。
	switch strings.ToLower(strings.TrimSpace(ch.Format)) {
	case "card":
		if vu := strings.TrimSpace(msg.ViewURL); vu != "" {
			body := map[string]any{
				"msgtype": "actionCard",
				"actionCard": map[string]any{
					"title":       msg.Title,
					"text":        "### " + msg.Title + "\n\n" + msg.Content,
					"singleTitle": "打开通知详情",
					"singleURL":   vu,
				},
			}
			return postJSON(client, u, body, func(d map[string]any) error {
				if code, _ := d["errcode"].(float64); code != 0 {
					return fmt.Errorf("钉钉 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
				}
				return nil
			})
		}
		// 详情页未配置 → 降级 markdown
		fallthrough
	default:
		body := map[string]any{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"title": msg.Title,
				"text":  "### " + msg.Title + "\n\n" + msg.Content,
			},
		}
		return postJSON(client, u, body, func(d map[string]any) error {
			if code, _ := d["errcode"].(float64); code != 0 {
				return fmt.Errorf("钉钉 errcode=%v errmsg=%v", d["errcode"], d["errmsg"])
			}
			return nil
		})
	}
}

// feishuPostBody post 富文本正文（0.6.177）：逐行 text 标签，空行作间行。
func feishuPostBody(msg *Message) map[string]any {
	lines := strings.Split(msg.Content, "\n")
	rows := make([][]map[string]any, 0, len(lines))
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			rows = append(rows, []map[string]any{})
			continue
		}
		rows = append(rows, []map[string]any{{"tag": "text", "text": ln}})
	}
	return map[string]any{
		"msg_type": "post",
		"content": map[string]any{
			"post": map[string]any{
				"zh-CN": map[string]any{"title": msg.Title, "content": rows},
			},
		},
	}
}

// ---------- 飞书 ----------

func sendFeishu(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["webhook_url"])
	if u == "" {
		return fmt.Errorf("webhook_url 未配置")
	}
	// 0.6.177 通知形式分流（对齐企微三形式）：默认/md_v2 = post 富文本
	//（飞书不支持表格→列表呈现）；card = interactive 卡片 + 「打开通知详情」按钮跳转。
	var body map[string]any
	switch strings.ToLower(strings.TrimSpace(ch.Format)) {
	case "card":
		if vu := strings.TrimSpace(msg.ViewURL); vu != "" {
			body = map[string]any{
				"msg_type": "interactive",
				"card": map[string]any{
					"header": map[string]any{
						"template": "blue",
						"title":    map[string]any{"tag": "plain_text", "content": msg.Title},
					},
					"elements": []map[string]any{
						{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": msg.Content}},
						{"tag": "note", "elements": []map[string]any{
							{"tag": "plain_text", "content": time.Unix(msg.TS, 0).Format("01-02 15:04")},
						}},
						{"tag": "action", "actions": []map[string]any{
							{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": "打开通知详情"}, "type": "primary", "url": vu},
						}},
					},
				},
			}
		} else {
			body = feishuPostBody(msg)
		}
	default:
		body = feishuPostBody(msg)
	}
	if secret := strings.TrimSpace(ch.Params["secret"]); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		stringToSign := ts + "\n" + secret
		mac := hmac.New(sha256.New, []byte(stringToSign))
		body["timestamp"] = ts
		body["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	return postJSON(client, u, body, func(d map[string]any) error {
		// 飞书：code / StatusCode 两种返回
		if code, ok := d["code"].(float64); ok && code != 0 {
			return fmt.Errorf("飞书 code=%v msg=%v", d["code"], d["msg"])
		}
		if code, ok := d["StatusCode"].(float64); ok && code != 0 {
			return fmt.Errorf("飞书 StatusCode=%v", d["StatusCode"])
		}
		return nil
	})
}

// ---------- Server酱 ----------

func sendServerChan(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	base := strings.TrimRight(strings.TrimSpace(ch.Params["server_url"]), "/")
	if base == "" {
		base = "https://sctapi.ftqq.com"
	}
	key := strings.TrimSpace(ch.Params["sendkey"])
	if key == "" {
		return fmt.Errorf("sendkey 未配置")
	}
	// 0.6.177 card 形式：desp 尾部追加详情链接（markdown 链接，微信侧可点）
	desp := msg.Content
	if strings.ToLower(strings.TrimSpace(ch.Format)) == "card" {
		if vu := strings.TrimSpace(msg.ViewURL); vu != "" {
			desp += "\n\n[打开通知详情](" + vu + ")"
		}
	}
	body := map[string]any{
		"title":  msg.Title,
		"desp":   desp,
		"source": "Moo",
	}
	if v := ch.Params["channel"]; v != "" {
		body["channel"] = v
	}
	if v := ch.Params["openid"]; v != "" {
		body["openid"] = v
	}
	return postJSON(client, base+"/send/"+key+".json", body, func(d map[string]any) error {
		if code, _ := d["code"].(float64); code != 200 {
			return fmt.Errorf("Server酱 code=%v message=%v", d["code"], d["message"])
		}
		return nil
	})
}

// ---------- PushPlus ----------

func sendPushPlus(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	base := strings.TrimRight(strings.TrimSpace(ch.Params["server_url"]), "/")
	if base == "" {
		base = "http://www.pushplus.plus"
	}
	token := strings.TrimSpace(ch.Params["token"])
	if token == "" {
		return fmt.Errorf("token 未配置")
	}
	// 0.6.177 card 形式：未自定义模板时用 html 模板追加详情链接
	content := msg.Content
	template := func() string {
		if t := strings.TrimSpace(ch.Params["template"]); t != "" {
			return t
		}
		return "txhtml"
	}()
	if strings.ToLower(strings.TrimSpace(ch.Format)) == "card" && strings.TrimSpace(ch.Params["template"]) == "" {
		if vu := strings.TrimSpace(msg.ViewURL); vu != "" {
			template = "html"
			content += "<br><a href=\"" + vu + "\">打开通知详情</a>"
		}
	}
	body := map[string]any{
		"title":    msg.Title,
		"content":  content,
		"template": template,
	}
	if v := ch.Params["topic"]; v != "" {
		body["topic"] = v
	}
	return postJSON(client, base+"/send/"+token+".json", body, func(d map[string]any) error {
		if code, _ := d["code"].(float64); code != 200 {
			return fmt.Errorf("PushPlus code=%v msg=%v", d["code"], d["msg"])
		}
		return nil
	})
}

// ---------- Bark ----------

func sendBark(client *http.Client, ch config.NotifyChannel, msg *Message) error {
	base := strings.TrimRight(strings.TrimSpace(ch.Params["server_url"]), "/")
	key := strings.TrimSpace(ch.Params["device_key"])
	if base == "" || key == "" {
		return fmt.Errorf("server_url / device_key 未配置")
	}
	q := url.Values{}
	if v := ch.Params["level"]; v != "" {
		q.Set("level", v)
	}
	if v := ch.Params["group"]; v != "" {
		q.Set("group", v)
	}
	if v := ch.Params["sound"]; v != "" {
		q.Set("sound", v)
	}
	// 0.6.177 card 形式：ur 参数 = 点击通知跳转（飞书/钉钉卡片跳转的 Bark 对应实现）
	if strings.ToLower(strings.TrimSpace(ch.Format)) == "card" {
		if vu := strings.TrimSpace(msg.ViewURL); vu != "" {
			q.Set("url", vu)
		}
	}
	u := fmt.Sprintf("%s/%s/%s/%s", base, key, url.PathEscape(msg.Title), url.PathEscape(msg.Content))
	if s := q.Encode(); s != "" {
		u += "?" + s
	}
	return postJSON(client, u, map[string]any{}, func(d map[string]any) error {
		if code, _ := d["code"].(float64); code != 200 {
			return fmt.Errorf("Bark code=%v message=%v", d["code"], d["message"])
		}
		return nil
	})
}

// ---------- 通用 Webhook（模板替换） ----------

// webhookRender 占位符：{{title}} {{content}} {{event}} {{ok}} {{time}} {{text}} {{detail_url}}
func webhookRender(tpl string, msg *Message) string {
	r := strings.NewReplacer(
		"{{title}}", msg.Title,
		"{{content}}", msg.Content,
		"{{text}}", msg.FormatText(),
		"{{event}}", msg.Event,
		"{{detail_url}}", msg.ViewURL, // 0.6.177：详情页短链（未配置时为空串）
		"{{time}}", time.Unix(msg.TS, 0).Format("2006-01-02 15:04:05"),
	)
	ok := "true"
	if !msg.OK {
		ok = "false"
	}
	return r.Replace(strings.ReplaceAll(tpl, "{{ok}}", ok))
}

func sendWebhook(ctx context.Context, client *http.Client, ch config.NotifyChannel, msg *Message) error {
	u := strings.TrimSpace(ch.Params["url"])
	if u == "" {
		return fmt.Errorf("url 未配置")
	}
	method := strings.ToUpper(strings.TrimSpace(ch.Params["method"]))
	if method == "" {
		method = "POST"
	}
	var bodyReader io.Reader
	if method == "POST" {
		tpl := strings.TrimSpace(ch.Params["body_template"])
		if tpl == "" {
			tpl = "{\"title\":\"{{title}}\",\"content\":\"{{content}}\",\"event\":\"{{event}}\",\"ok\":{{ok}}}"
		}
		bodyReader = strings.NewReader(webhookRender(tpl, msg))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return err
	}
	if h := strings.TrimSpace(ch.Params["headers"]); h != "" {
		var hm map[string]string
		if err := json.Unmarshal([]byte(h), &hm); err != nil {
			return fmt.Errorf("headers 不是合法 JSON: %v", err)
		}
		for k, v := range hm {
			req.Header.Set(k, v)
		}
	}
	if method == "POST" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---------- 公共 ----------

// postJSON POST JSON 并按 checkBiz 校验业务码；非 2xx 或业务码非成功返回错误。
func postJSON(client *http.Client, u string, body any, checkBiz func(map[string]any) error) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := client.Post(u, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s := strings.TrimSpace(string(data))
		if len(s) > 120 {
			s = s[:120]
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, s)
	}
	var d map[string]any
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &d); err != nil {
			// 非 JSON 应答（200 且无业务码语义）视为成功
			return nil
		}
	}
	if checkBiz != nil && d != nil {
		return checkBiz(d)
	}
	return nil
}

// TestMessage 通用测试消息（包内 Send/Fanout 机制测试用）。
// 0.6.164 起生产渠道测试（「测试」/「测试提供商」按钮）改发欢迎语
//（api 层 welcomeTestMessage），不再使用本函数。
func TestMessage() *Message {
	return &Message{
		Title:   "Moo 通知渠道测试",
		Content: "这是一条来自 **Moo**（飞牛第三方应用中心）的测试消息。\n\n收到即表示该推送渠道配置正确。",
		Event:   "channel_test",
		OK:      true,
		TS:      time.Now().Unix(),
		// 卡片形式预览两列列表样式（10:07 样例同款）
		Rows: []CardRow{
			{"Fluxor", "1.3.0 → 1.3.1"},
			{"KSpeeder", "2.0.1 → 2.0.3"},
			{"轻阅读", "3.4.6.2 → 3.4.7"},
		},
	}
}

// Fanout 向多个渠道并发发送（单飞保护 + 每渠道独立错误）。
// 返回 渠道名 → 错误（空 map = 全部成功；无启用渠道 = nil）。
// MaskSecretInErr（0.6.144 安全审计 P1-4）：Go 的 http 错误串含完整请求 URL
//（url.Error: Post "https://...?key=xxxx": ...），直接落盘/回显会把 webhook
// key 泄进 notify_log / API 响应。写盘与回显前统一掩码敏感查询参数值。
var secretQueryRe = regexp.MustCompile(`([?&](?:key|token|secret|password|passwd|authorization|access_token)=)[^&"']+`)

// MaskSecretInErr 掩码错误串中敏感查询参数的值（幂等，对无命中串原样返回）。
func MaskSecretInErr(str string) string {
	return secretQueryRe.ReplaceAllString(str, "$1****")
}

func Fanout(ctx context.Context, channels []config.NotifyChannel, msg *Message) map[string]string {
	var enabled []config.NotifyChannel
	for _, ch := range channels {
		if ch.Enabled {
			enabled = append(enabled, ch)
		}
	}
	if len(enabled) == 0 {
		return nil
	}
	errs := make(map[string]string, len(enabled))
	var errsMu sync.Mutex
	done := make(chan struct{}, len(enabled))
	for i := range enabled {
		ch := enabled[i]
		go func() {
			defer func() { done <- struct{}{} }()
			// 0.6.175：按渠道「通知信息长度」选用变体（friendly 原样/concise 关键/full 全量）
			if err := Send(context.WithoutCancel(ctx), ch, msg.ForChannel(ch)); err != nil {
				errsMu.Lock()
				errs[ch.Name] = MaskSecretInErr(err.Error()) // 0.6.144 安全审计 P1-4
				errsMu.Unlock()
			}
		}()
	}
	// 等待上限 = 单渠道最坏耗时（3 次尝试 × timeout + 退避 28s）+ 余量，
	// 防慢渠道拖住调用方（重试只在瞬时故障时发生，正常路径 ≈ 单次 timeout）
	maxTO := int(defaultTimeout / time.Second)
	for i := range enabled {
		if enabled[i].Timeout > maxTO {
			maxTO = enabled[i].Timeout
		}
	}
	waitCtx, cancel := context.WithTimeout(context.Background(),
		time.Duration(maxTO)*3*time.Second+40*time.Second)
	defer cancel()
	got := 0
	for {
		select {
		case <-done:
			got++
			if got >= len(enabled) {
				return errs
			}
		case <-waitCtx.Done():
			errsMu.Lock()
			for _, ch := range enabled {
				if _, ok := errs[ch.Name]; !ok {
					errs[ch.Name] = "发送超时"
				}
			}
			errsMu.Unlock()
			return errs
		}
	}
}
