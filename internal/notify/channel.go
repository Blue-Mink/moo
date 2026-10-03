// Package notify Moo 外部推送通知（0.6.121）：渠道定义 + 消息发送。
//
// 设计参照 fn-knock 通知模块（kci-lnk/fn-knock-turborepo）裁剪：
//   - Provider 连接参数 schema 化（前端按定义渲染动态表单，敏感字段脱敏回显）
//   - 事件 × 渠道 fan-out，单渠道失败不影响其余，超时封顶
//   - 测试消息：新增渠道后一键验证连通性
//
// 首批 7 渠道：企业微信 / 钉钉（加签）/ 飞书 / Server酱 / PushPlus / Bark /
// 通用 Webhook（QQ 机器人等走 Webhook 模板）。
package notify

import (
	"strings"

	"moo/internal/config"
)

// Field 渠道连接参数定义（前端动态表单渲染）。
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Required    bool   `json:"required"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Default     string `json:"default,omitempty"`
}

// ChannelDef 一类渠道的静态定义。
type ChannelDef struct {
	Type        string  `json:"type"`
	Label       string  `json:"label"`
	Desc        string  `json:"desc"`
	Fields      []Field `json:"fields"`
	SupportsMD  bool    `json:"supports_markdown"`
}

// ChannelDefs 渠道目录（顺序 = 前端「添加渠道」下拉序）。
var ChannelDefs = []ChannelDef{
	{
		Type: "wecom", Label: "企业微信", SupportsMD: true,
		Desc: "企业微信群机器人：群设置 → 群机器人 → 添加，拿到 Webhook 地址",
		Fields: []Field{
			{Key: "webhook_url", Label: "Webhook 地址", Required: true, Sensitive: true,
				Placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxxx"},
			{Key: "mentioned_list", Label: "@ 成员（可选）", Placeholder: "zhangsan,@all"},
		},
	},
	{
		Type: "dingtalk", Label: "钉钉", SupportsMD: true,
		Desc: "钉钉群机器人：安全设置选「加签」，填 Webhook 与 Secret",
		Fields: []Field{
			{Key: "webhook_url", Label: "Webhook 地址", Required: true, Sensitive: true,
				Placeholder: "https://oapi.dingtalk.com/robot/send?access_token=xxxx"},
			{Key: "secret", Label: "加签 Secret（可选）", Sensitive: true,
				Placeholder: "SECxxxx"},
		},
	},
	{
		Type: "feishu", Label: "飞书", SupportsMD: false,
		Desc: "飞书群自定义机器人：安全设置选「签名校验」，填 Webhook 与 Secret",
		Fields: []Field{
			{Key: "webhook_url", Label: "Webhook 地址", Required: true, Sensitive: true,
				Placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/xxxx"},
			{Key: "secret", Label: "签名 Secret（可选）", Sensitive: true},
		},
	},
	{
		Type: "serverchan", Label: "Server酱", SupportsMD: true,
		Desc: "Server酱（微信推送）：sct.ftqq.com 登录后复制 SendKey",
		Fields: []Field{
			{Key: "server_url", Label: "服务地址", Default: "https://sctapi.ftqq.com",
				Placeholder: "https://sctapi.ftqq.com"},
			{Key: "sendkey", Label: "SendKey", Required: true, Sensitive: true,
				Placeholder: "SCTxxxx"},
			{Key: "channel", Label: "发送渠道（可选）", Placeholder: "9|66"},
			{Key: "openid", Label: "OpenID / UID（可选）", Placeholder: "openid1,openid2 或 uid1|uid2"},
		},
	},
	{
		Type: "pushplus", Label: "PushPlus", SupportsMD: true,
		Desc: "PushPlus（微信/Telegram 推送）：pushplus.plus 微信扫码登录复制 token",
		Fields: []Field{
			{Key: "server_url", Label: "服务地址", Default: "http://www.pushplus.plus",
				Placeholder: "http://www.pushplus.plus"},
			{Key: "token", Label: "Token", Required: true, Sensitive: true},
			{Key: "topic", Label: "群组 topic（可选）", Placeholder: "alarm-topic"},
			{Key: "template", Label: "消息模板（可选）", Placeholder: "txhtml / html / text"},
		},
	},
	{
		Type: "bark", Label: "Bark", SupportsMD: true,
		Desc: "Bark App 推送到手机：自建或公共服务器地址 + Device Key",
		Fields: []Field{
			{Key: "server_url", Label: "服务器地址", Required: true,
				Placeholder: "https://api.day.app"},
			{Key: "device_key", Label: "Device Key", Required: true, Sensitive: true,
				Placeholder: "ynJ5Ft4atkMkWeo2PAvFhF"},
			{Key: "level", Label: "提醒级别（可选）", Placeholder: "active / idle / timeSensitive"},
			{Key: "group", Label: "分组（可选）", Placeholder: "moo"},
			{Key: "sound", Label: "提示音（可选）", Placeholder: "alarm"},
		},
	},
	{
		Type: "webhook", Label: "通用 Webhook", SupportsMD: false,
		Desc: "任意 HTTP 端点（QQ 机器人 / ntfy / 自写脚本）：请求体模板支持 {{title}} {{content}} {{event}} {{ok}} {{time}} 占位符",
		Fields: []Field{
			{Key: "url", Label: "请求地址", Required: true, Sensitive: true,
				Placeholder: "https://example.com/hook"},
			{Key: "method", Label: "方法（可选）", Default: "POST", Placeholder: "POST / GET"},
			{Key: "headers", Label: "自定义头（可选，JSON）",
				Placeholder: "{\"Authorization\":\"Bearer xxx\"}"},
			{Key: "body_template", Label: "请求体模板（可选）",
				Placeholder: "{\"text\":\"{{title}}\\n{{content}}\"}"},
		},
	},
}

// ChannelDefByKey 渠道类型 → 定义。
func ChannelDefByKey(t string) *ChannelDef {
	for i := range ChannelDefs {
		if ChannelDefs[i].Type == t {
			return &ChannelDefs[i]
		}
	}
	return nil
}

// Masked 返回渠道的可安全回显副本：敏感字段只留尾 4 位。
func Masked(ch config.NotifyChannel) config.NotifyChannel {
	out := ch
	if len(ch.Params) > 0 {
		def := ChannelDefByKey(ch.Type)
		sens := map[string]bool{}
		if def != nil {
			for _, f := range def.Fields {
				if f.Sensitive {
					sens[f.Key] = true
				}
			}
		}
		m := make(map[string]string, len(ch.Params))
		for k, v := range ch.Params {
			if sens[k] && len(v) > 4 {
				m[k] = "****" + v[len(v)-4:]
				continue
			}
			m[k] = v
		}
		out.Params = m
	}
	return out
}

// Validate 校验渠道参数（必填 + 类型合法性）。返回错误列表（空 = 通过）。
func Validate(ch config.NotifyChannel) []string {
	var errs []string
	def := ChannelDefByKey(ch.Type)
	if def == nil {
		return []string{"未知渠道类型: " + ch.Type}
	}
	if ch.Name == "" {
		errs = append(errs, "渠道名称不能为空")
	}
	for _, f := range def.Fields {
		v := ch.Params[f.Key]
		if f.Required && v == "" {
			errs = append(errs, f.Label+" 必填")
		}
	}
	if ch.Type == "webhook" {
		if ch.Params["method"] != "" && ch.Params["method"] != "POST" && ch.Params["method"] != "GET" {
			errs = append(errs, "方法只支持 POST / GET")
		}
	}
	// 0.6.144：通知形式合法性（仅 wecom 消费，其他类型忽略该字段）。
	switch ch.Format {
	case "", "markdown", "markdown_v2", "card":
	default:
		errs = append(errs, "未知通知形式: "+ch.Format)
	}
	// 0.6.175：通知信息长度合法性（全部渠道消费）。
	switch ch.Verbosity {
	case "", "friendly", "concise", "full":
	default:
		errs = append(errs, "未知通知信息长度: "+ch.Verbosity)
	}
	return errs
}

// ValidFormats 通知形式可选项（wecom；"" = 默认 markdown）。
var ValidFormats = []string{"markdown", "markdown_v2", "card"}

// FormatLabel 形式 → 中文标签（UI/测试用）。
func FormatLabel(format string) string {
	switch format {
	case "markdown_v2":
		return "markdown_v2（表格）"
	case "card":
		return "卡片"
	default:
		return "默认 markdown"
	}
}

// ValidVerbosities 通知信息长度可选项（0.6.175，全部渠道）。
var ValidVerbosities = []string{"friendly", "concise", "full"}

// VerbosityLabel 信息长度 → 中文标签（UI/测试用）。
func VerbosityLabel(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "concise":
		return "简洁"
	case "full":
		return "完整"
	default:
		return "友好"
	}
}
