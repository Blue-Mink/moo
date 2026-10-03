// notifyfire — 一次性预览工具：29 类通知 × 3 形式（默认 markdown / markdown_v2 表格 / 模板卡片）
// 发到已配置企微渠道（3.5s 间隔防 20 条/分钟限流，87 条约 5.5 分钟）。
// 独立进程：只读 config.json，不写 config、不落通知记录、不碰主进程。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"moo/internal/config"
)

type ev struct {
	key    string
	label  string
	group  string
	plain  string // 列表/普通文案（F1 默认 markdown + F3 卡片用；classic markdown 不支持表格）
	table  string // markdown_v2 表格版（F2 用）；空 = 与 plain 相同
	ok     bool
	link   bool // 是否带详情页链接（源同步摘要）
}

var events = []ev{
	// A. 应用生命周期（10）— 简约单行
	{"install_success", "安装成功", "应用生命周期", "应用 **Fluxor** v1.3.0 安装成功（源：SaucdX233）", "", true, false},
	{"install_error", "安装失败", "应用生命周期", "应用 **KSpeeder** v2.0.1 安装失败\n> 向导端口 5003 被占用", "", false, false},
	{"update_success", "更新成功", "应用生命周期", "应用 **moo** 0.6.142 → 0.6.143 更新成功", "", true, false},
	{"update_error", "更新失败", "应用生命周期", "应用 **Gitea** 更新失败\n> FPK 下载中断（网络超时）", "", false, false},
	{"uninstall_success", "卸载成功", "应用生命周期", "应用 **test-app** 已卸载（数据目录保留）", "", true, false},
	{"uninstall_error", "卸载失败", "应用生命周期", "应用 **Vaultwarden** 卸载失败\n> 进程退出超时", "", false, false},
	{"download_done", "下载完成", "应用生命周期", "**moo_0.6.143_x86.fpk** 下载完成（7.0 MB，12s）", "", true, false},
	{"download_error", "下载失败", "应用生命周期", "**big-app.fpk** 下载失败\n> 上游源 502 Bad Gateway", "", false, false},
	{"backup_done", "自动备份完成", "应用生命周期", "自动备份完成（12 KB）", "", true, false},
	{"backup_error", "自动备份失败", "应用生命周期", "自动备份失败\n> 磁盘空间不足", "", false, false},
	// B. 应用更新（4）— 表格
	{"updates_available", "应用更新摘要", "应用更新",
		"3 个已装应用有新版本：\n\n- Fluxor 1.3.0 → 1.3.1\n- KSpeeder 2.0.1 → 2.0.3\n- 轻阅读 3.4.6.2 → 3.4.7",
		"3 个已装应用有新版本：\n\n| 应用 | 当前 | 最新 |\n| :-- | :-- | :-- |\n| Fluxor | 1.3.0 | 1.3.1 |\n| KSpeeder | 2.0.1 | 2.0.3 |\n| 轻阅读 | 3.4.6.2 | 3.4.7 |", true, false},
	{"favorite_update", "收藏应用有更新", "应用更新",
		"1 个收藏应用有新版本：\n\n- Gitea 1.20.0 → 1.21.3（fnos-store）",
		"1 个收藏应用有新版本：\n\n| 应用 | 当前 | 最新 | 源 |\n| :-- | :-- | :-- | :-- |\n| Gitea | 1.20.0 | 1.21.3 | fnos-store |", true, false},
	{"auto_update_round", "自动更新应用完成", "应用更新", "自动更新完成：2 个已更新 / 0 个失败", "", true, false},
	{"favorite_source_apps", "关注源新增应用", "应用更新",
		"关注源「ctllo-bit」新增 2 个应用：\n\n- 应用 A（1.2.0）\n- 应用 B（0.9.1）",
		"关注源「ctllo-bit」新增 2 个应用：\n\n| 应用 | 版本 |\n| :-- | :-- |\n| 应用 A | 1.2.0 |\n| 应用 B | 0.9.1 |", true, false},
	// C. Moo 自身（8）— 简约
	{"store_update_available", "Moo 有新版本", "Moo 自身", "Moo v0.6.143 已发布，点设置页右上角版本号更新", "", true, false},
	{"store_update_success", "Moo 自更新成功", "Moo 自身", "Moo 自更新成功：0.6.142 → 0.6.143（已自动重启）", "", true, false},
	{"store_update_error", "Moo 自更新失败", "Moo 自身", "Moo 自更新失败\n> 下载 FPK 超时（仓库不可达）", "", false, false},
	{"resource_mem_alert", "内存占用告警", "Moo 自身", "Moo 内存占用 **312 MB** 超 256 MB 阈值（持续 5 分钟）", "", false, false},
	{"resource_cpu_alert", "CPU 占用告警", "Moo 自身", "Moo CPU 占用 **87%** 超 80% 阈值（持续 5 分钟）", "", false, false},
	{"health_error", "后端健康异常", "Moo 自身", "后端健康异常\n> appcenter daemon RPC 不可达（连接超时）", "", false, false},
	{"health_recovered", "后端健康恢复", "Moo 自身", "后端健康已恢复", "", true, false},
	{"disk_space_alert", "磁盘空间告警", "Moo 自身", "数据目录使用率 **92%**（>90%），建议清理下载/缓存", "", false, false},
	// D. 源与网络（6）— 摘要带表格
	{"source_sync_failed", "源同步失败", "源与网络",
		"本轮 154 源成功 / 2 源失败\n\n- Hxido-RXM：连接超时\n- mac1256：404 Not Found",
		"本轮 154 源成功 / 2 源失败\n\n| 源 | 失败原因 |\n| :-- | :-- |\n| Hxido-RXM | 连接超时 |\n| mac1256 | 404 Not Found |", false, false},
	{"source_recovered", "源同步恢复", "源与网络", "源同步恢复：Hxido-RXM", "", true, false},
	{"source_sync_summary", "源同步摘要", "源与网络",
		"源同步：156 源成功 / 0 源失败\n\n应用总数 1778（含多源重复）\n关注源 1 · 收藏 3 个\n\n应用数最多的源（等 156 个源）：\n- 飞牛应用中心：375\n- fnos-store：165\n- shuangji66：100\n- wabisabi926：100\n- SaucdX233：91",
		"源同步：156 源成功 / 0 源失败\n\n| 源 | 应用数 |\n| :-- | --: |\n| 飞牛应用中心 | 375 |\n| fnos-store | 165 |\n| shuangji66的应用源 | 100 |\n| wabisabi926 | 100 |\n| SaucdX233 | 91 |\n\n应用总数 1778（含多源重复）· 关注源 1 · 收藏 3 个（共 156 源）", true, true},
	{"mirror_switched", "加速源自动切换优选", "源与网络", "加速源切换：hk.gh-proxy.org → ghproxy.net（连续 3 次测速最慢）", "", true, false},
	{"mirrors_recovered", "GitHub 加速源恢复", "源与网络", "GitHub 加速源已恢复（当前：ghproxy.net）", "", true, false},
	{"mirrors_all_failed", "GitHub 加速源全部不可用", "源与网络", "全部 GitHub 加速镜像 + 直连探测失败，网络层异常（下载将集体变慢）", "", false, false},
	// E. 数据面（1）
	{"catalog_drop", "目录数量异常下跌", "数据面", "目录条目 1778 → 1320（跌幅 26%），上游源数据可能异常", "", false, false},
}

// detailURL 卡片详情页基址：经 -detail-url / MOO_DETAIL_URL 注入（脱敏，
// 默认回环 Moo 地址，部署时按实际 NAS 入口传入）。
var detailURL = "http://127.0.0.1:38101/"

func main() {
	dataDir := flag.String("data", os.Getenv("MOO_DATA"), "moo 数据目录（读 config.json 渠道）")
	interval := flag.Duration("interval", 3500*time.Millisecond, "每条间隔（企微限 20 条/分钟）")
	formatsFlag := flag.String("formats", "md,md_v2,card", "启用形式，逗号分隔：md（默认 markdown）/md_v2/card（卡片）")
	eventsFlag := flag.String("events", "", "只发指定事件（逗号分隔 key）；空 = 全部 29 类")
	detailURLFlag := flag.String("detail-url", os.Getenv("MOO_DETAIL_URL"), "卡片详情页基址（默认回环 Moo 地址）")
	flag.Parse()
	if du := strings.TrimSpace(*detailURLFlag); du != "" {
		detailURL = strings.TrimRight(du, "/") + "/"
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*formatsFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读配置失败:", err)
		os.Exit(1)
	}
	var webhook string
	for _, ch := range cfg.NotifyChannels {
		if ch.Type == "wecom" {
			webhook = ch.Params["webhook_url"]
			break
		}
	}
	if webhook == "" {
		fmt.Fprintln(os.Stderr, "未找到企微渠道")
		os.Exit(1)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	now := time.Now().Format("01-02 15:04")
	order := []string{"md", "md_v2", "card"}
	per := 0
	for _, f := range order {
		if want[f] {
			per++
		}
	}
	eventFilter := map[string]bool{}
	if *eventsFlag != "" {
		for _, s := range strings.Split(*eventsFlag, ",") {
			if s = strings.TrimSpace(s); s != "" {
				eventFilter[s] = true
			}
		}
	}
	active := events
	if len(eventFilter) > 0 {
		active = active[:0]
		for _, e := range events {
			if eventFilter[e.key] {
				active = append(active, e)
			}
		}
	}
	total := len(active) * per
	n := 0
	for i, e := range active {
		for _, f := range order {
			if !want[f] {
				continue
			}
			n++
			var payload map[string]any
			switch f {
			case "md":
				payload = classicPayload(e)
			case "md_v2":
				payload = v2Payload(e, now)
			default:
				payload = cardPayload(e, now)
			}
			fmt.Printf("[%2d/%d] %-24s %-8s %s\n", n, total, e.key, f,
				result(post(client, webhook, payload)))
			time.Sleep(*interval)
		}
		if i < len(active)-1 {
			time.Sleep(*interval)
		}
	}
	fmt.Println("done")
}

// F1 = 生产默认 markdown（### 标题 + 正文；classic 不支持表格 → 用 plain）
func classicPayload(e ev) map[string]any {
	c := e.plain
	if e.link {
		c += "\n\n[查看完整 155 源明细](" + detailURL + ")"
	}
	return map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]string{"content": "### Moo · " + e.label + "\n\n" + c},
	}
}

// F2 = markdown_v2（表格版优先；摘要带详情页链接）
func v2Payload(e ev, now string) map[string]any {
	body := e.plain
	if e.table != "" {
		body = e.table
	}
	if e.link {
		body += "\n\n[查看完整 155 源明细](" + detailURL + ")"
	}
	return map[string]any{
		"msgtype": "markdown_v2",
		"markdown_v2": map[string]any{
			"content": fmt.Sprintf("# Moo · %s\n> %s · %s\n\n%s", e.label, e.group, now, body),
		},
	}
}

// F3 = 模板卡片（text_notice）——与生产 sendWeComCard 同款：
// 列表类事件走两列列表（horizontal_content_list），普通事件折行清洗后单行。
var cardRows = map[string][][2]string{
	"updates_available": {{"Fluxor", "1.3.0 → 1.3.1"}, {"KSpeeder", "2.0.1 → 2.0.3"}, {"轻阅读", "3.4.6.2 → 3.4.7"}},
	"favorite_update":   {{"Gitea", "1.20.0 → 1.21.3"}},
	"favorite_source_apps": {{"应用 A", "v1.2.0"}, {"应用 B", "v0.9.1"}},
	"source_sync_failed": {{"Hxido-RXM", "连接超时"}, {"mac1256", "404 Not Found"}},
	"source_sync_summary": {{"飞牛应用中心", "375 个应用"}, {"fnos-store", "165 个应用"}, {"shuangji66", "100 个应用"}, {"wabisabi926", "100 个应用"}, {"SaucdX233", "91 个应用"}},
}
var cardEmph = map[string][2]string{
	"source_sync_summary": {"1778", "应用总数（含多源重复）"},
}

func cardPayload(e ev, now string) map[string]any {
	sub := ""
	rows := cardRows[e.key]
	if len(rows) > 0 {
		sub = e.plain
		if i := strings.IndexByte(sub, '\n'); i >= 0 {
			sub = sub[:i]
		}
		sub = strings.TrimSuffix(sub, "：")
	} else {
		lines := strings.Split(e.plain, "\n")
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
	sub = strings.ReplaceAll(sub, "**", "")
	if r := []rune(sub); len(r) > 110 {
		sub = string(r[:107]) + "…"
	}
	jumpTitle := "打开 Moo 通知详情"
	if e.link {
		jumpTitle = "查看完整 155 源明细"
	}
	tc := map[string]any{
		"card_type":    "text_notice",
		"source":       map[string]any{"desc": "Moo"},
		"main_title":   map[string]any{"title": "Moo · " + e.label, "desc": e.group + " · " + now},
		"sub_title_text": sub,
		"jump_list": []map[string]any{
			{"type": 1, "url": detailURL, "title": jumpTitle},
		},
		"card_action": map[string]any{"type": 1, "url": detailURL},
	}
	if len(rows) > 0 {
		list := make([]map[string]any, 0, len(rows))
		for i, r := range rows {
			if i >= 6 {
				break
			}
			list = append(list, map[string]any{"keyname": r[0], "value": r[1]})
		}
		tc["horizontal_content_list"] = list
	}
	if emph, ok := cardEmph[e.key]; ok {
		tc["emphasis_content"] = map[string]any{"title": emph[0], "desc": emph[1]}
	}
	return map[string]any{
		"msgtype":       "template_card",
		"template_card": tc,
	}
}

func post(client *http.Client, webhook string, payload map[string]any) error {
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, webhook, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		ErrCode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("errcode=%d %s", out.ErrCode, out.Errmsg)
	}
	return nil
}

func result(err error) string {
	if err != nil {
		return "ERR " + err.Error()
	}
	return "ok"
}
