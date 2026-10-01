package api

import (
	"context"
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/notify"
)

// 通知设置（0.6.121）：外部推送渠道（fn-knock 事件中心形式）
//
//	渠道 = 外部推送通道（企微/钉钉/飞书/Server酱/PushPlus/Bark/通用 Webhook），
//	       连接参数 schema 化 + 敏感字段脱敏 + 一键测试消息；
//	规则 = 逐事件开关（22 类事件，缺省见 catalog）；
//	记录 = 实际通知流水（含各渠道发送结果），持久化 config.json，
//	       上限 config.NotifyLogCap 条。
//
// 语义约定：事件 key 缺失 = 用目录缺省（Default）；仅显式 false 为关。
// 关闭的事件既不推送也不入记录（用户关了就是不想看）。
// 应用内顶部通知栏是本地操作反馈（下载进度/安装结果），不属于推送渠道。

// notifyEvent 是一类可通知事件。
type notifyEvent struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Desc    string `json:"desc"`
	Group   string `json:"group"`   // 前端分组折叠用
	Default bool   `json:"default"` // 缺省开/关（key 缺失时生效）
}

// notifyEventCatalog 事件目录（顺序 = 设置页「通知规则」行序）。
var notifyEventCatalog = []notifyEvent{
	// A. 应用生命周期
	{Key: "install_success", Label: "安装成功", Desc: "应用安装完成后推送", Group: "应用生命周期", Default: true},
	{Key: "install_error", Label: "安装失败", Desc: "应用安装失败时推送原因", Group: "应用生命周期", Default: true},
	{Key: "update_success", Label: "更新成功", Desc: "应用更新完成后推送", Group: "应用生命周期", Default: true},
	{Key: "update_error", Label: "更新失败", Desc: "应用更新失败时推送原因", Group: "应用生命周期", Default: true},
	{Key: "uninstall_success", Label: "卸载成功", Desc: "应用卸载完成后推送", Group: "应用生命周期", Default: true},
	{Key: "uninstall_error", Label: "卸载失败", Desc: "应用卸载失败时推送原因", Group: "应用生命周期", Default: true},
	{Key: "download_done", Label: "下载完成", Desc: "FPK 下载完成后推送", Group: "应用生命周期", Default: true},
	{Key: "download_error", Label: "下载失败", Desc: "FPK 下载失败时推送原因", Group: "应用生命周期", Default: true},
	{Key: "backup_done", Label: "自动备份完成", Desc: "定时自动备份写入成功后推送", Group: "应用生命周期", Default: true},
	{Key: "backup_error", Label: "自动备份失败", Desc: "定时自动备份写入失败时推送原因", Group: "应用生命周期", Default: true},
	// B. 应用更新（0.6.133 用户定稿：更新信息带数目+逐应用详情）
	{Key: "updates_available", Label: "应用更新摘要", Desc: "源同步后比对已装应用版本：更新集合变化时推送——更新应用数目 + 每个应用的旧→新版本与来源（前 8 个）", Group: "应用更新", Default: true},
	{Key: "favorite_update", Label: "收藏应用有更新", Desc: "源同步后发现收藏应用有新版本，推送数目+每个应用的旧→新版本与来源（按应用×版本去重，每版只推一次）", Group: "应用更新", Default: true},
	{Key: "auto_update_round", Label: "自动更新应用完成", Desc: "「自动更新应用」后台轮次结束推送：成功/失败数目 + 应用列表（一轮一条）", Group: "应用更新", Default: true},
	// C. Moo 自身
	{Key: "welcome", Label: "欢迎使用Moo", Desc: "默认测试渠道通知，测完请关闭", Group: "Moo 自身", Default: true},
	{Key: "store_update_available", Label: "有新版本", Desc: "探测到 GitHub 发布新版 Moo 时推送（与设置页红点同源）", Group: "Moo 自身", Default: true},
	{Key: "store_update_success", Label: "自更新成功", Desc: "应用内自更新（daemon 就地升级）完成后推送", Group: "Moo 自身", Default: false},
	{Key: "store_update_error", Label: "自更新失败", Desc: "应用内自更新失败时推送原因", Group: "Moo 自身", Default: true},
	{Key: "resource_mem_alert", Label: "内存占用告警", Desc: "进程内存超阈值（默认 256MB）且持续 5 分钟告警；回落 80% 后推送恢复。30 分钟冷却", Group: "Moo 自身", Default: false},
	{Key: "resource_cpu_alert", Label: "CPU 占用告警", Desc: "进程 CPU 超阈值（默认 80%）且持续 5 分钟告警；回落 80% 后推送恢复。30 分钟冷却", Group: "Moo 自身", Default: false},
	{Key: "health_error", Label: "后端健康异常", Desc: "5 分钟自检：appcenter daemon RPC 不可达 / 配置不可读（API 存活由自检协程隐含）。恢复后推送 health_recovered", Group: "Moo 自身", Default: true},
	{Key: "health_recovered", Label: "后端健康恢复", Desc: "健康异常恢复后推送", Group: "Moo 自身", Default: true},
	{Key: "disk_space_alert", Label: "磁盘空间告警", Desc: "数据目录使用率 >90% 推送告警（30 分钟冷却）；回落 85% 以下推送恢复", Group: "Moo 自身", Default: true},
	// D. 源与网络
	{Key: "source_sync_failed", Label: "源同步失败", Desc: "每轮源刷新后聚合推送：失败源列表（前 5）+ 总数，一轮一条", Group: "源与网络", Default: true},
	{Key: "source_recovered", Label: "源同步恢复", Desc: "此前失败的源本轮恢复（同源 24 小时内不重复推）", Group: "源与网络", Default: false},
	{Key: "source_sync_summary", Label: "源同步摘要", Desc: "每轮源同步后各源应用数 / 应用总数（含多源重复）/ 关注与收藏数目；仅数字变化时推送（无变化不重复推）。外部渠道发紧凑版，应用内记录可展开看全部源明细", Group: "源与网络", Default: true},
	{Key: "favorite_source_apps", Label: "关注源新增应用", Desc: "关注（星标）的源出现新增应用时推送（源×应用只推一次；首次关注记基线不推；每个关注源单独一条通知）", Group: "应用更新", Default: true},
	{Key: "favorite_source_report", Label: "关注源报表", Desc: "关注（星标）源的应用清单变化（新增/移除）时推送：当前应用总数 + 本轮新增/移除 + 全部应用清单（含源地址）。每个关注源单独一条通知，多源分开不挤一张卡；首次关注记基线不推；与「关注源新增应用」独立，可分别开关", Group: "应用更新", Default: true},
	{Key: "mirror_switched", Label: "加速源自动切换优选", Desc: "智能优选生效的加速源变化时推送：旧源 → 新源 + 原因（30 分钟冷却防抖动）", Group: "源与网络", Default: true},
	{Key: "mirrors_recovered", Label: "GitHub 加速源恢复", Desc: "「全部加速源不可用」后恢复时推送，附当前优选源", Group: "源与网络", Default: true},
	{Key: "mirrors_all_failed", Label: "GitHub 加速源全部不可用", Desc: "全部加速镜像 + 直连探测失败 = 网络层异常（下载会集体变慢/失败）。30 分钟冷却", Group: "源与网络", Default: true},
	// 0.6.188：Docker 组全挂监测（与 GitHub 组对偶，挂 dk 探测循环）
	{Key: "docker_mirrors_all_failed", Label: "Docker 加速源全部不可用", Desc: "全部 Docker 远程加速源探测失败（本地 KSpeeder 缓存不计入）= 网络层或 Docker Hub 异常（镜像拉取会集体变慢/失败）。30 分钟冷却", Group: "源与网络", Default: true},
	{Key: "docker_mirrors_recovered", Label: "Docker 加速源恢复", Desc: "「Docker 加速源全部不可用」后恢复时推送，附当前优选源", Group: "源与网络", Default: true},
	// E. 数据面
	{Key: "catalog_drop", Label: "目录数量异常下跌", Desc: "本轮目录条目比上轮跌幅 > 20%（源数据异常信号）", Group: "数据面", Default: true},
}

var (
	notifyEventKeys   = func() map[string]bool {
		m := make(map[string]bool, len(notifyEventCatalog))
		for _, e := range notifyEventCatalog {
			m[e.Key] = true
		}
		return m
	}()
	notifyEventDefault = func() map[string]bool {
		m := make(map[string]bool, len(notifyEventCatalog))
		for _, e := range notifyEventCatalog {
			m[e.Key] = e.Default
		}
		return m
	}()
)

// IsNotifyEventOn 单事件开关（key 缺失 = 目录缺省；仅显式 false 为关）。
func (s *Server) IsNotifyEventOn(key string) bool {
	if s.Cfg.NotifyEvents != nil {
		if on, ok := s.Cfg.NotifyEvents[key]; ok {
			return on
		}
	}
	if d, ok := notifyEventDefault[key]; ok {
		return d
	}
	return true
}

// notifyMu 保护 NotifyLog 的读改写与配置落盘（后台协程与 HTTP 并发）。
var notifyMu sync.Mutex

// writeNotifyLog 追加一条通知记录（含各渠道发送结果）并落盘，
// 最多保留 NotifyLogCap 条（先进先出）。
func writeNotifyLog(cfg *config.Config, msg *notify.Message, chErrs map[string]string) {
	content := msg.Content
	if msg.Full != "" {
		content = msg.Full
	}
	notifyMu.Lock()
	cfg.NotifyLog = append(cfg.NotifyLog, config.NotifyLogEntry{
		TS:       msg.TS,
		Event:    msg.Event,
		Msg:      msg.Title,
		OK:       msg.OK,
		Content:  content,
		Channels: chErrs,
		ID:       msg.ViewID,
		Token:    msg.ViewToken,
	})
	if n := len(cfg.NotifyLog); n > config.NotifyLogCap {
		cfg.NotifyLog = cfg.NotifyLog[n-config.NotifyLogCap:]
	}
	_ = cfg.Save(dataDirOf(nil))
	notifyMu.Unlock()
}

// newNotifyMsg 构造通知消息（标题统一加 Moo · 前缀；
// 0.6.144：附带详情页短链 ID/Token，卡片形式跳转用）。
func newNotifyMsg(key, title, content string, ok bool) *notify.Message {
	b := make([]byte, 12)
	if _, err := crand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i * 8) & 0xff)
		}
	}
	return &notify.Message{
		Title:     "Moo · " + title,
		Content:   content,
		Event:     key,
		OK:        ok,
		TS:        time.Now().Unix(),
		ViewID:    hex.EncodeToString(b[:4]),
		ViewToken: hex.EncodeToString(b[4:]),
	}
}

// fanoutIfExternalOn 外部渠道 fan-out 门槛（0.6.126 语义）：
// 「外部渠道通知」总开关（NotifyEnabled）+ 该事件开关都开才推送。
// 应用内顶部通知栏始终开，不受这两个开关（及任何开关）控制。
func (s *Server) fanoutIfExternalOn(msg *notify.Message, key string) map[string]string {
	if !s.Cfg.IsNotifyEnabled() || !s.IsNotifyEventOn(key) {
		return nil
	}
	// 0.6.144：卡片形式需要详情页跳转链接（NotifyViewBase 未配置时为空 →
	// 发送侧自动降级默认 markdown）。
	if base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotifyViewBase), "/"); base != "" {
		msg.ViewURL = base + "/api/notify-view/" + msg.ViewID + "?t=" + msg.ViewToken
	}
	return notify.Fanout(context.Background(), s.Cfg.NotifyChannels, msg)
}

// notifyEvent 后端侧 dispatch（自动备份 / 自监控 / 源网络等后台事件，
// 无前端在场、无应用内 toast）：事件开关关 = 完全静默；
// 事件开关开 = 落记录 + 外部渠道按总开关 fan-out。
func (s *Server) notifyEvent(key, title, content string, ok bool) {
	s.notifyEventFull(key, title, content, "", ok)
}

// notifyEventFull 同 notifyEvent，但应用内记录存 fullContent（完整正文，
// 方格折叠卡展示），外部渠道仍发 content（可紧凑）。
func (s *Server) notifyEventFull(key, title, content, fullContent string, ok bool) {
	if !s.IsNotifyEventOn(key) {
		return
	}
	msg := newNotifyMsg(key, title, content, ok)
	msg.Full = fullContent
	writeNotifyLog(s.Cfg, msg, s.fanoutIfExternalOn(msg, key))
}

// notifyEventTable 同 notifyEvent，但附 markdown_v2 表格正文（0.6.144）：
// 渠道形式为 markdown_v2 时发 Table（列表类事件观感更好），
// 默认 markdown / 卡片形式忽略 Table（卡片用 Content 折行摘要）。
func (s *Server) notifyEventTable(key, title, content, table string, ok bool) {
	s.notifyEventRich(key, title, content, table, nil, "", "", ok)
}

// notifyEventRich 0.6.144：一次带齐三形式的增强内容——markdown_v2 表格（table）、
// 卡片两列列表（rows）、卡片大字强调（emphTitle/emphDesc）；
// 默认 markdown 形式只用 content（纯文本列表）。
func (s *Server) notifyEventRich(key, title, content, table string, rows []notify.CardRow, emphTitle, emphDesc string, ok bool) {
	s.notifyEventRichV(key, title, content, table, rows, emphTitle, emphDesc, ok, nil)
}

// notifyEventRichV 同 notifyEventRich，附 0.6.175 通知信息长度变体
//（v=nil = 无变体，friendly 默认行为）。
func (s *Server) notifyEventRichV(key, title, content, table string, rows []notify.CardRow, emphTitle, emphDesc string, ok bool, v *notify.Variants) {
	if !s.IsNotifyEventOn(key) {
		return
	}
	msg := newNotifyMsg(key, title, content, ok)
	msg.Table = table
	msg.Rows = rows
	msg.EmphTitle = emphTitle
	msg.EmphDesc = emphDesc
	if v != nil {
		msg.ContentConcise = v.ContentConcise
		msg.ContentFull = v.ContentFull
		msg.TableFull = v.TableFull
		msg.RowsConcise = v.RowsConcise
		msg.ConciseSub = v.ConciseSub
		msg.RowsFull = v.RowsFull
	}
	writeNotifyLog(s.Cfg, msg, s.fanoutIfExternalOn(msg, key))
}

// notifyEventApp 前端应用内事件上报（0.6.126）：应用内顶部通知栏已弹、
// 始终开、不受任何开关控制 → 必然落记录（该通知实际发生了）；
// 外部渠道 fan-out 受总开关 + 事件开关控制。
func (s *Server) notifyEventApp(key, title, content string, ok bool) {
	msg := newNotifyMsg(key, title, content, ok)
	writeNotifyLog(s.Cfg, msg, s.fanoutIfExternalOn(msg, key))
}

// logNotify 后端侧记录一条通知（自动备份等无前端在场的后台事件）。
// 尊重开关：该事件关闭时静默跳过。
func (s *Server) logNotify(event, msg string, ok bool) {
	s.notifyEvent(event, msg, "", ok)
}

// ── 欢迎使用Moo（0.6.155）──
//
// 用户定稿文案：Moo is more！蓝貂（Blue-Mink）感谢所有投身飞牛应用生态的
// 开发者、爱好者与项目测试者——Moo 的每个版本迭代都来自你们的反馈，
// 感谢你们为爱发电。
//
// 三形式排版（与 0.6.144 三形式架构一致）：
//   - 默认 markdown（Content）：企微 classic / 钉钉 / Server酱 / PushPlus 共用。
//     企微 classic markdown 不支持段落空行 → 只按单换行断句，角色词加粗；
//   - markdown_v2（Table）：富文本（小标题 + 引用），仅 wecom v2 渠道消费；
//   - 卡片（EmphTitle/EmphDesc）：text_notice 大字强调 hero + 折行摘要
//     （Rows 空 = 卡片摘要取 Content 折行，见 sendWeComCard）。

// 0.6.157 用户定稿形式（五段式，emoji 点缀；全文每句只出现一次）：
//
//	Moo is more（大字居中）
//	蓝貂（Blue-Mink）
//	感谢所有投身飞牛应用生态的开发者·爱好者·项目测试者
//	Moo 的每个版本迭代都来自你们的反馈
//	感谢你们为爱发电

// welcomeContent 默认 markdown 形式（单换行断句，兼容企微 classic 不支持段落空行）。
// 0.6.158：按用户反馈去掉 🦦（企微字体渲染成棕色河狸，弃用）。
const welcomeContent = "**Moo is more**\n" +
	"蓝貂（Blue-Mink）\n" +
	"🙌 感谢所有投身飞牛应用生态的开发者·爱好者·项目测试者\n" +
	"💡 Moo 的每个版本迭代都来自你们的反馈\n" +
	"❤️ 感谢你们为爱发电"

// welcomeTable markdown_v2 富文本形式（sendWeComV2 已前置 # 大标题，正文从 ## 起）：
// 0.6.158：标题行同样去掉 🦦。
// 0.6.159：三行致谢改 \n\n 段间空行——企微 markdown_v2 是标准 markdown 语义，
// 单 \n 是软换行（渲染成一段不断行），\n\n 才真断段（标题/署名段已验证有效）。
const welcomeTable = "## Moo is more\n\n" +
	"蓝貂（**Blue-Mink**）\n\n" +
	"🙌 感谢所有投身飞牛应用生态的**开发者·爱好者·项目测试者**\n\n" +
	"💡 Moo 的每个版本迭代都来自你们的反馈\n\n" +
	"❤️ 感谢你们**为爱发电**"

// welcomeCardSub 卡片副题（0.6.163）：三行致谢整段 + 句间空行（全角空格行）。
// 版式演进：0.6.160/0.6.161 纯折行被反馈「行间距太紧凑」→ 0.6.162 两列列表
// 块间距够但 value 客户端渲染上限约 23 字，26 字首句被截「…项目测试者」
//（用户反馈「有内容被略掉了」）→ 0.6.163 回到 sub_title_text（上限 160 字，
// 完整不截断），句间插「\n　\n」空行拉开行距（全角空格是内容字符，
// 不依赖渲染器保留连续换行，空行必现）。
const welcomeCardSub = "🙌 感谢所有投身飞牛应用生态的开发者·爱好者·项目测试者\n　\n" +
	"💡 Moo 的每个版本迭代都来自你们的反馈\n　\n" +
	"❤️ 感谢你们为爱发电"

// welcomeCardSign 大字 hero 下方的居中小字署名（emphasis_content.desc，0.6.160）。
// 0.6.157 为「🦦 蓝貂（Blue-Mink）」，0.6.158 起按用户反馈去掉 🦦。
const welcomeCardSign = "蓝貂（Blue-Mink）"

// fireWelcome 推送欢迎语（应用内记录 + 外部渠道，受总开关 + welcome 事件开关控制，
// 与其他事件同语义：开关关 = 完全静默）。自动（首启）与手动（POST 端点）共用入口。
// 0.6.163 卡片版式（用户五轮真机反馈后定稿，以 1:39 卡片截图为骨架）：
// 大字 hero「Moo is more」+ hero 下居中小字署名「蓝貂（Blue-Mink）」
//（emphasis_content.desc，落款）+ 副题三行致谢（完整不截断，句间空行
// 拉开行距）；无 🦦；全文每句只出现一次。
// welcomeMessage 欢迎语三形式消息构造（0.6.164 起真实发送与渠道测试共用）：
// 卡片 = hero 大字 + 居中落款 + 副题三行致谢（句间空行）；
// markdown_v2 = Table 富文本；默认 markdown = Content 单换行断句。
func welcomeMessage(event string) *notify.Message {
	msg := newNotifyMsg(event, "欢迎使用Moo", welcomeContent, true)
	msg.Table = welcomeTable
	msg.EmphTitle = "Moo is more"
	msg.EmphDesc = welcomeCardSign
	msg.CardSub = welcomeCardSub
	return msg
}

func (s *Server) fireWelcome() {
	if !s.IsNotifyEventOn("welcome") {
		return
	}
	msg := welcomeMessage("welcome")
	writeNotifyLog(s.Cfg, msg, s.fanoutIfExternalOn(msg, "welcome"))
}

// maybeFireWelcomeOnce 首启判定（单飞）：WelcomeSentAt 为 0 才置位并返回 true。
func (s *Server) maybeFireWelcomeOnce() bool {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if s.Cfg.WelcomeSentAt != 0 {
		return false
	}
	s.Cfg.WelcomeSentAt = time.Now().Unix()
	_ = s.Cfg.Save(dataDirOf(s))
	return true
}

// MaybeFireWelcome 安装后首次启动自动推送欢迎语一次（0.6.155）：
// WelcomeSentAt 为 0 才触发，触发即落时间戳（重启/升级不重复）。
// 延迟 15s：等配置与渠道加载稳定、首启日志收敛，观感上「服务就绪后才问候」。
func (s *Server) MaybeFireWelcome(ctx context.Context) {
	select {
	case <-time.After(15 * time.Second):
	case <-ctx.Done():
		return
	}
	if s.maybeFireWelcomeOnce() {
		s.fireWelcome()
	}
}

// notifyWelcomeFire POST /api/notify/welcome：手动重发欢迎语的 API（0.6.155
// 引入；0.6.166 起 UI 按钮已按用户要求移除，保留端点供外部调用/排障）。
// 事件开关关 = 静默（返回 sent=false，不落记录）。
func (s *Server) notifyWelcomeFire(w http.ResponseWriter, _ *http.Request) {
	if !s.IsNotifyEventOn("welcome") {
		writeJSON(w, map[string]any{"ok": true, "sent": false, "reason": "「欢迎使用Moo」事件已在通知规则中关闭"})
		return
	}
	s.fireWelcome()
	writeJSON(w, map[string]any{"ok": true, "sent": true})
}

// notifyFire POST /api/notify/fire：手动触发任意事件通知（0.6.170，
// 「通知规则」每行的门铃按钮，纯图标无文字）。welcome 走真实欢迎语；
// 其他事件以目录 label/desc 构造消息。事件开关关 = 静默（sent=false，
// 不落记录）。
func (s *Server) notifyFire(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Event string `json:"event"`
	}
	// 0.6.216 P2：改走 jsonDecode（10MB MaxBytesReader）——原先裸 json.Decoder
	// 无 body 上限，超大 payload 可撑内存（N2）。
	if err := jsonDecode(r, &in); err != nil || in.Event == "" {
		writeJSON(w, map[string]any{"ok": false, "reason": "参数错误"})
		return
	}
	var cat *notifyEvent
	for i := range notifyEventCatalog {
		if notifyEventCatalog[i].Key == in.Event {
			cat = &notifyEventCatalog[i]
			break
		}
	}
	if cat == nil {
		writeJSON(w, map[string]any{"ok": false, "reason": "未知事件"})
		return
	}
	if !s.IsNotifyEventOn(cat.Key) {
		writeJSON(w, map[string]any{"ok": true, "sent": false, "reason": "「" + cat.Label + "」事件已在通知规则中关闭"})
		return
	}
	if cat.Key == "welcome" {
		s.fireWelcome()
	} else {
		s.notifyEvent(cat.Key, cat.Label, cat.Desc, true)
	}
	writeJSON(w, map[string]any{"ok": true, "sent": true})
}

// ---------- 设置 ----------

// notifySettingsGet GET /api/notify-settings
//
//	{enabled, events: {key: 解析后开/关}, catalog: [{key,label,desc,group,default}],
//	  mem_alert_mb, cpu_alert_pct, view_base}
func (s *Server) notifySettingsGet(w http.ResponseWriter, _ *http.Request) {
	events := make(map[string]bool, len(notifyEventCatalog))
	for _, e := range notifyEventCatalog {
		events[e.Key] = s.IsNotifyEventOn(e.Key)
	}
	writeJSON(w, map[string]any{
		"enabled":        s.Cfg.IsNotifyEnabled(),
		"events":         events,
		"catalog":        notifyEventCatalog,
		"mem_alert_mb":   orDefaultInt(s.Cfg.NotifyMemAlertMB, 256),
		"cpu_alert_pct":  orDefaultInt(s.Cfg.NotifyCPUPctAlert, 80),
		"view_base":      s.Cfg.NotifyViewBase,
	})
}

// notifySettingsPut PUT /api/notify-settings
//
//	{enabled?: bool, events?: {key: bool}, mem_alert_mb?: int, cpu_alert_pct?: int,
//	  view_base?: string}
//
// 均缺省 = 不改动；events 为**合并语义**（只改提交的 key）。
func (s *Server) notifySettingsPut(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled      *bool           `json:"enabled"`
		Events       map[string]bool `json:"events"`
		MemAlertMB   *int            `json:"mem_alert_mb"`
		CPUAlertPct  *int            `json:"cpu_alert_pct"`
		ViewBase     *string         `json:"view_base"` // 0.6.144：详情页基础地址（"" = 清除）
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	changed := false
	notifyMu.Lock()
	if in.Enabled != nil {
		s.Cfg.NotifyEnabled = in.Enabled
		changed = true
	}
	if len(in.Events) > 0 {
		if s.Cfg.NotifyEvents == nil {
			s.Cfg.NotifyEvents = make(map[string]bool)
		}
		for k, v := range in.Events {
			if !notifyEventKeys[k] {
				notifyMu.Unlock()
				writeErr(w, http.StatusBadRequest, errors.New("未知事件: "+k))
				return
			}
			s.Cfg.NotifyEvents[k] = v
		}
		changed = true
	}
	if in.MemAlertMB != nil && *in.MemAlertMB >= 16 && *in.MemAlertMB <= 8192 {
		s.Cfg.NotifyMemAlertMB = *in.MemAlertMB
		changed = true
	}
	if in.CPUAlertPct != nil && *in.CPUAlertPct >= 10 && *in.CPUAlertPct <= 100 {
		s.Cfg.NotifyCPUPctAlert = *in.CPUAlertPct
		changed = true
	}
	if in.ViewBase != nil {
		vb := strings.TrimRight(strings.TrimSpace(*in.ViewBase), "/")
		if vb != "" && !strings.HasPrefix(vb, "http://") && !strings.HasPrefix(vb, "https://") {
			notifyMu.Unlock()
			writeErr(w, http.StatusBadRequest, errors.New("详情页地址需以 http:// 或 https:// 开头"))
			return
		}
		if len(vb) > 255 {
			notifyMu.Unlock()
			writeErr(w, http.StatusBadRequest, errors.New("详情页地址过长（≤255 字符）"))
			return
		}
		s.Cfg.NotifyViewBase = vb
		changed = true
	}
	if changed {
		_ = s.Cfg.Save(dataDirOf(s))
	}
	enabledAfter := s.Cfg.IsNotifyEnabled()
	eventsAfter := make(map[string]bool, len(notifyEventCatalog))
	for _, e := range notifyEventCatalog {
		eventsAfter[e.Key] = s.IsNotifyEventOn(e.Key)
	}
	notifyMu.Unlock()

	if changed {
		rel, err := config.Load(dataDirOf(s))
		if err != nil || rel.IsNotifyEnabled() != enabledAfter {
			writeErr(w, http.StatusInternalServerError, errors.New("通知设置保存后回读校验失败，请重试"))
			return
		}
	}
	writeJSON(w, map[string]any{"ok": true, "enabled": enabledAfter, "events": eventsAfter})
}

// ---------- 渠道 ----------

// notifyChannelsGet GET /api/notify-channels
//
//	{channels: [脱敏副本], definitions: [渠道定义]}
func (s *Server) notifyChannelsGet(w http.ResponseWriter, _ *http.Request) {
	notifyMu.Lock()
	chs := make([]config.NotifyChannel, len(s.Cfg.NotifyChannels))
	for i := range s.Cfg.NotifyChannels {
		chs[i] = notify.Masked(s.Cfg.NotifyChannels[i])
	}
	notifyMu.Unlock()
	writeJSON(w, map[string]any{"channels": chs, "definitions": notify.ChannelDefs})
}

// notifyChannelsPost POST /api/notify-channels：新增渠道。
func (s *Server) notifyChannelsPost(w http.ResponseWriter, r *http.Request) {
	var in config.NotifyChannel
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	testID := in.ID // 0.6.144：编辑模式下前端带渠道 ID，脱敏回显参数用存储真实值回填
	in.ID = ""
	if testID != "" {
		notifyMu.Lock()
		for i := range s.Cfg.NotifyChannels {
			if s.Cfg.NotifyChannels[i].ID == testID {
				for k, v := range in.Params {
					if strings.HasPrefix(v, "****") {
						if sv, ok := s.Cfg.NotifyChannels[i].Params[k]; ok {
							in.Params[k] = sv
						}
					}
				}
				break
			}
		}
		notifyMu.Unlock()
	}
	if in.Timeout <= 0 {
		in.Timeout = 10
	}
	if errList := notify.Validate(in); len(errList) > 0 {
		writeErr(w, http.StatusBadRequest, errors.New(strings.Join(errList, "；")))
		return
	}
	notifyMu.Lock()
	if len(s.Cfg.NotifyChannels) >= 10 {
		notifyMu.Unlock()
		writeErr(w, http.StatusBadRequest, errors.New("渠道数量上限 10 个"))
		return
	}
	for _, c := range s.Cfg.NotifyChannels {
		if c.Name == in.Name {
			notifyMu.Unlock()
			writeErr(w, http.StatusBadRequest, errors.New("渠道名称已存在: "+in.Name))
			return
		}
	}
	in.ID = "ch_" + time.Now().Format("20060102150405") + randShort(4)
	s.Cfg.NotifyChannels = append(s.Cfg.NotifyChannels, in)
	_ = s.Cfg.Save(dataDirOf(s))
	saved := in
	notifyMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "channel": notify.Masked(saved)})
}

// notifyChannelPut PUT /api/notify-channels/{id}：更新渠道（参数合并语义：
// 敏感字段收到 "****" 前缀 = 未改动，保留原值——防前端脱敏回显值被写回）。
func (s *Server) notifyChannelPut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in config.NotifyChannel
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	notifyMu.Lock()
	idx := -1
	for i := range s.Cfg.NotifyChannels {
		if s.Cfg.NotifyChannels[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		notifyMu.Unlock()
		writeErr(w, http.StatusNotFound, errors.New("渠道不存在"))
		return
	}
	cur := s.Cfg.NotifyChannels[idx]
	merged := cur
	merged.Name = orEmpty(in.Name, cur.Name)
	merged.Enabled = in.Enabled // 表单总是显式提交开关状态
	merged.Format = in.Format // 0.6.144：表单总是显式提交形式（wecom=markdown 默认，其他类型空串）
	merged.Verbosity = in.Verbosity // 0.6.175：表单总是显式提交信息长度（缺省 friendly）
	if in.Timeout > 0 {
		merged.Timeout = in.Timeout
	}
	if len(in.Params) > 0 {
		if merged.Params == nil {
			merged.Params = make(map[string]string)
		}
		for k, v := range in.Params {
			if strings.HasPrefix(v, "****") {
				continue // 脱敏回显值 = 未改动
			}
			merged.Params[k] = v
		}
	}
	if errList := notify.Validate(merged); len(errList) > 0 {
		notifyMu.Unlock()
		writeErr(w, http.StatusBadRequest, errors.New(strings.Join(errList, "；")))
		return
	}
	s.Cfg.NotifyChannels[idx] = merged
	_ = s.Cfg.Save(dataDirOf(s))
	saved := merged
	notifyMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "channel": notify.Masked(saved)})
}

// notifyChannelDelete DELETE /api/notify-channels/{id}
func (s *Server) notifyChannelDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	notifyMu.Lock()
	out := s.Cfg.NotifyChannels[:0]
	removed := false
	for _, c := range s.Cfg.NotifyChannels {
		if c.ID == id {
			removed = true
			continue
		}
		out = append(out, c)
	}
	s.Cfg.NotifyChannels = out
	if removed {
		_ = s.Cfg.Save(dataDirOf(s))
	}
	notifyMu.Unlock()
	if !removed {
		writeErr(w, http.StatusNotFound, errors.New("渠道不存在"))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// welcomeTestMessage 渠道测试消息（0.6.164 起改发欢迎语：测试结果 = 欢迎语
// 在该渠道形式下的真实观感，卡片/markdown/markdown_v2 三形式各预览各的版式）。
// 不写通知记录（测试只是探针，不落 notify_log）。卡片形式「打开通知详情」
// 跳转链接指向 0000test 演示页（无需 token）；NotifyViewBase 未配置时
// ViewURL 为空 → 发送侧自动降级默认 markdown。
func (s *Server) welcomeTestMessage() *notify.Message {
	msg := welcomeMessage("channel_test")
	if base := strings.TrimRight(strings.TrimSpace(s.Cfg.NotifyViewBase), "/"); base != "" {
		msg.ViewURL = base + "/api/notify-view/0000test"
	}
	return msg
}

// notifyChannelTest POST /api/notify-channels/{id}/test：发测试消息（0.6.164 起 = 欢迎语）。
func (s *Server) notifyChannelTest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	notifyMu.Lock()
	var target *config.NotifyChannel
	for i := range s.Cfg.NotifyChannels {
		if s.Cfg.NotifyChannels[i].ID == id {
			c := s.Cfg.NotifyChannels[i]
			target = &c
			break
		}
	}
	notifyMu.Unlock()
	if target == nil {
		writeErr(w, http.StatusNotFound, errors.New("渠道不存在"))
		return
	}
	err := notify.Send(context.Background(), *target, s.welcomeTestMessage())
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": notify.MaskSecretInErr(err.Error())})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// notifyChannelTestDraft POST /api/notify-channels/test：弹窗内「测试提供商」
//（0.6.139，参照 knock「新增通知提供商」弹窗的测试按钮）：用当前表单值直接
// 发一条测试消息，不落盘保存。
func (s *Server) notifyChannelTestDraft(w http.ResponseWriter, r *http.Request) {
	var in config.NotifyChannel
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	testID := in.ID // 0.6.144：编辑模式下前端带渠道 ID，脱敏回显参数用存储真实值回填
	in.ID = ""
	if testID != "" {
		notifyMu.Lock()
		for i := range s.Cfg.NotifyChannels {
			if s.Cfg.NotifyChannels[i].ID == testID {
				for k, v := range in.Params {
					if strings.HasPrefix(v, "****") {
						if sv, ok := s.Cfg.NotifyChannels[i].Params[k]; ok {
							in.Params[k] = sv
						}
					}
				}
				break
			}
		}
		notifyMu.Unlock()
	}
	if in.Name == "" {
		in.Name = "Moo 测试渠道"
	}
	if in.Timeout <= 0 {
		in.Timeout = 10
	}
	if errList := notify.Validate(in); len(errList) > 0 {
		writeErr(w, http.StatusBadRequest, errors.New(strings.Join(errList, "；")))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(in.Timeout+5)*time.Second)
	defer cancel()
	if err := notify.Send(ctx, in, s.welcomeTestMessage()); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": notify.MaskSecretInErr(err.Error())})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 详情页（0.6.144，卡片形式跳转落地页） ----------

// notifyViewGet GET /api/notify-view/{id}?t={token}：通知详情页。
// 独立短链（id 8 位 hex + token 16 位 hex 校验），无需登录——链接本身即凭证。
// 特殊 id=0000test：演示页（渠道弹窗「测试提供商」卡片链接用，无需 token）。
// 0.6.164：测试消息改发欢迎语后，演示页同步展示欢迎语内容（所见即所得）。
func (s *Server) notifyViewGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "0000test" {
		writeNotifyViewHTML(w, http.StatusOK, config.NotifyLogEntry{
			TS:      time.Now().Unix(),
			Event:   "welcome",
			Msg:     "Moo · 欢迎使用Moo",
			OK:      true,
			Content: welcomeContent,
		})
		return
	}
	tok := r.URL.Query().Get("t")
	notifyMu.Lock()
	var entry *config.NotifyLogEntry
	for i := range s.Cfg.NotifyLog {
		if s.Cfg.NotifyLog[i].ID == id {
			entry = &s.Cfg.NotifyLog[i]
			break
		}
	}
	var got config.NotifyLogEntry
	valid := entry != nil && subtle.ConstantTimeCompare([]byte(entry.Token), []byte(tok)) == 1
	if valid {
		got = *entry
	}
	notifyMu.Unlock()
	if !valid {
		writeNotifyViewHTML(w, http.StatusNotFound, config.NotifyLogEntry{
			Msg: "链接无效或记录已清理",
		})
		return
	}
	writeNotifyViewHTML(w, http.StatusOK, got)
}

// writeNotifyViewHTML 渲染详情页（独立 HTML，无外部依赖；移动端优先）。
func writeNotifyViewHTML(w http.ResponseWriter, status int, e config.NotifyLogEntry) {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		s = strings.ReplaceAll(s, "\"", "&quot;")
		return s
	}
	statusLine := "失败 / 告警"
	badgeCls := "badge-err"
	if e.OK {
		statusLine = "成功"
		badgeCls = "badge-ok"
	}
	timeStr := ""
	if e.TS > 0 {
		timeStr = time.Unix(e.TS, 0).Format("2006-01-02 15:04:05")
	}
	var chLines strings.Builder
	if len(e.Channels) > 0 {
		chLines.WriteString("<div class='sec'>渠道发送结果</div><div class='ch'>")
		for name, err := range e.Channels {
			st := "失败：" + esc(err)
			cl := "ch-err"
			if err == "" {
				st = "成功"
				cl = "ch-ok"
			}
			chLines.WriteString(fmt.Sprintf("<span class='%s'>%s %s</span>", cl, esc(name), st))
		}
		chLines.WriteString("</div>")
	}
	body := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Moo 通知详情</title>
<style>
*{box-sizing:border-box;margin:0}
body{font:14px/1.6 -apple-system,"PingFang SC","Microsoft YaHei",sans-serif;background:#f5f6f8;color:#1c1e22;padding:24px 16px}
.wrap{max-width:480px;margin:0 auto}
.card{background:#fff;border-radius:16px;padding:20px;box-shadow:0 1px 6px rgba(0,0,0,.06)}
.title{font-size:17px;font-weight:600}
.meta{display:flex;gap:8px;align-items:center;margin-top:6px;font-size:12px;color:#8a8f98}
.badge{padding:1px 8px;border-radius:999px;font-size:11px}
.badge-ok{background:#e8f7ee;color:#1a9d4b}
.badge-err{background:#fdecec;color:#d64545}
.sec{margin-top:16px;font-size:13px;font-weight:600;color:#5a6069}
pre{white-space:pre-wrap;word-break:break-word;margin-top:8px;padding:12px;background:#f7f8fa;border-radius:10px;font-size:13px}
.ch{display:flex;flex-wrap:wrap;gap:6px;margin-top:8px}
.ch span{font-size:12px;padding:2px 10px;border-radius:999px}
.ch-ok{background:#e8f7ee;color:#1a9d4b}
.ch-err{background:#fdecec;color:#d64545}
.foot{text-align:center;margin-top:14px;font-size:11px;color:#b0b5bd}
</style></head><body><div class="wrap"><div class="card">
<div class="title">%s</div>
<div class="meta"><span class="badge %s">%s</span>%s</div>
<div class="sec">通知内容</div>
<pre>%s</pre>
%s
</div><div class="foot">Moo · 飞牛第三方应用中心</div></div></body></html>`,
		esc(e.Msg), badgeCls, statusLine, esc(timeStr), esc(orViewContent(e)), chLines.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// orViewContent Content 空（测试演示页之外的正常记录都有）时回退 Msg。
func orViewContent(e config.NotifyLogEntry) string {
	if e.Content != "" {
		return e.Content
	}
	return e.Msg
}

// ---------- 记录 ----------

// notifyLogGet GET /api/notify-log → {"entries": [新→旧]}
func (s *Server) notifyLogGet(w http.ResponseWriter, _ *http.Request) {
	notifyMu.Lock()
	log := s.Cfg.NotifyLog
	notifyMu.Unlock()
	out := make([]config.NotifyLogEntry, 0, len(log))
	for i := len(log) - 1; i >= 0; i-- {
		out = append(out, log[i])
	}
	writeJSON(w, map[string]any{"entries": out})
}

// notifyLogPost POST /api/notify-log {"event","msg","ok"}：
// 前端任务事件上报（安装/更新/卸载/下载 成败）。尊重开关；触发渠道 fan-out。
func (s *Server) notifyLogPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Event string `json:"event"`
		Msg   string `json:"msg"`
		OK    bool   `json:"ok"`
	}
	if err := jsonDecode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !notifyEventKeys[in.Event] {
		writeErr(w, http.StatusBadRequest, errors.New("未知事件: "+in.Event))
		return
	}
	// 0.6.126：应用内事件（顶部通知栏已弹、始终开）→ 必落记录；
	// 外部渠道 fan-out 受总开关 + 事件开关控制。
	s.notifyEventApp(in.Event, in.Msg, "", in.OK)
	writeJSON(w, map[string]any{"ok": true})
}

// notifyLogClear POST /api/notify-log/clear：清空通知记录。
func (s *Server) notifyLogClear(w http.ResponseWriter, _ *http.Request) {
	notifyMu.Lock()
	s.Cfg.NotifyLog = nil
	_ = s.Cfg.Save(dataDirOf(s))
	notifyMu.Unlock()
	writeJSON(w, map[string]any{"ok": true})
}

// ---------- 关于 ----------

// aboutGet GET /api/about：关于页数据（版本号 + 应用信息 + 运行环境）。
// 应用静态信息以 fnos/manifest 为准（构建时写死，单一事实源）。
func (s *Server) aboutGet(w http.ResponseWriter, _ *http.Request) {
	var startedAt time.Time
	if s.StartedAt != (time.Time{}) {
		startedAt = s.StartedAt
	} else {
		startedAt = time.Now()
	}
	writeJSON(w, map[string]any{
		"version":    s.Version,
		"platform":   "fnos",
		"arch":       runtime.GOARCH,
		"go_version": runtime.Version(),
		"port":       s.Cfg.WebPort,
		"started_at": startedAt.Unix(),
		"uptime_s":   int(time.Since(startedAt).Seconds()),
		"app": map[string]any{
			"name":         "moo",
			"display_name": "Moo",
			"desc":         "Moo — 飞牛 fnOS 第三方应用中心：浏览、下载、安装、更新第三方应用，同步 FnDepot / fnos-apps / 飞牛官方应用中心三大平台。",
			"author":       "Blue-Mink",
			"author_url":   "https://github.com/Blue-Mink",
			"homepage":     "https://github.com/Blue-Mink/moo",
		},
	})
}

// orDefaultInt 0 = 用默认值。
func orDefaultInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// orEmpty 空串 = 用旧值。
func orEmpty(v, old string) string {
	if v == "" {
		return old
	}
	return v
}

// randShort n 位小写字母数字（crypto/rand，渠道 ID 后缀）。
func randShort(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(time.Now().UnixNano() >> (i * 8) & 0xff)
		}
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
