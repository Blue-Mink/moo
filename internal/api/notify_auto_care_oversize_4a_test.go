package api

// notify_auto_care_oversize_4a_test.go —— 盲点补测 批次 4a · T4（FIXPLAN 盲点矩阵 #9）
//
// 场景：care（源自动停用）通知在"长源名 / 长错误串"下超过企业微信单条上限
// （4096 字节）。断言对象是**真实发出去的载荷**（httptest 捕获的 JSON 里的
// markdown / markdown_v2 content），不是内部函数的中间值 —— 这样才覆盖
// "标题 + 时间行 + 正文"合起来的真实长度。
//
// 现有实现：notify/send.go capWeComContent 用 limit=3800 起截，截断点先回退到
// 合法 rune 边界、再**向前找换行**。既有用例 TestCapWeComContent 用的是每行
// ~45 字节的多行文本，所以截断点正好落在 4096 以内。
//
// 本文件构造两条现实路径（都是单行超长）：
//   A. 长错误串：reason 无换行（mdCell 会把换行压成空格）→ 表格里是一整行；
//   B. 长源名：名字本身 4KB+，紧跟的换行在 4096 之后。
// 两者都让"向前找换行"越过 4096，最终 content 超过企微上限。

import (
	"encoding/json"
	"strings"
	"testing"

	"moo/internal/source"
)

const weComSingleLimit = 4096

// wecomMarkdown 取企微载荷里的 markdown / markdown_v2 正文。
// 卡片形式（template_card）没有 content 字段（字段各有自己的上限，不是 4096），
// 返回 ok=false 由调用方按"不适用"处理 —— 但 full 详细度恒走 classic markdown
// （sendWeCom 里 full 先于 format 分流），所以真正可能超限的两条通道都在 ok=true。
func wecomMarkdown(t *testing.T, body string) (string, bool) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("载荷不是合法 JSON: %v", err)
	}
	for _, k := range []string{"markdown", "markdown_v2"} {
		if m, ok := payload[k].(map[string]any); ok {
			if c, ok := m["content"].(string); ok {
				return c, true
			}
		}
	}
	return "", false
}

// assertWeComLimit 断言正文 ≤4096 字节（按字节，不按 rune）。
func assertWeComLimit(t *testing.T, channel, content string) {
	t.Helper()
	n := len([]byte(content))
	if n <= weComSingleLimit {
		return
	}
	tail := content
	if len([]byte(tail)) > 120 {
		b := []byte(tail)
		tail = string(b[len(b)-120:])
	}
	t.Fatalf("%s 正文 %d 字节 > 企微上限 %d（会整条被拒/截断）\n尾部: %q\n含截断标注: %v",
		channel, n, weComSingleLimit, tail,
		strings.Contains(content, "超出企业微信单条消息长度上限"))
}

// A. 长错误串：reason 里没有换行 → 表格被压成一整行，截断点的"向前找换行"
// 会一路扫到行尾（>4096）。这是 care 停用通知最现实的触发方式
// （HTTP 502 时上游常回一整段无换行的 HTML/JSON）。
func TestCareNotify4096_LongReasonSingleLine(t *testing.T) {
	s, body := careCapture(t)
	s.notifySourceAutoDisabled(source.CareEvent{
		Kind:   source.CareEventDisabled,
		Name:   "galaxy-saturn",
		Streak: 6,
		Reason: "上游返回 502：" + strings.Repeat("失", 2000), // 6000+ 字节，单行
	})
	waitCareBodies(t, body)

	for _, ch := range []string{"mdv2-full", "card-full"} {
		c, ok := wecomMarkdown(t, body(ch))
		if !ok {
			t.Fatalf("%s（full 详细度）必须走 classic markdown 并带 content，实际是卡片载荷", ch)
		}
		t.Logf("%s 正文 %d 字节", ch, len([]byte(c)))
		if !strings.Contains(c, "超出企业微信单条消息长度上限") {
			t.Fatalf("%s 超长文本必须带截断标注（不静默隐藏）\n尾部: %.120q", ch, c)
		}
		assertWeComLimit(t, ch, c)
	}
	// friendly 详细度：正文是紧凑摘要（3934 字节，<4096），仅记录体量。
	for _, ch := range []string{"mdv2-friendly", "card-friendly"} {
		if c, ok := wecomMarkdown(t, body(ch)); ok {
			t.Logf("%s 正文 %d 字节（friendly 不承载长表格）", ch, len([]byte(c)))
		} else {
			t.Logf("%s 为卡片载荷（%d 字节原始 JSON，卡片字段另有上限）", ch, len(body(ch)))
		}
	}
}

// B. 长源名：名字 4200 字节，紧跟的换行在 4096 之后 —— 同一条根因的第二个
// 触发面（源名来自远端索引，长度不受本机控制）。
func TestCareNotify4096_LongSourceName(t *testing.T) {
	s, body := careCapture(t)
	longName := "源" + strings.Repeat("很", 1400) // 3 字节/字 ≈ 4203 字节
	s.notifySourceAutoDisabled(source.CareEvent{
		Kind:   source.CareEventDisabled,
		Name:   longName,
		Streak: 6,
		Reason: "仓库不存在（HTTP 404）",
	})
	waitCareBodies(t, body)

	for _, ch := range []string{"mdv2-full", "card-full"} {
		c, ok := wecomMarkdown(t, body(ch))
		if !ok {
			t.Fatalf("%s（full 详细度）必须走 classic markdown 并带 content", ch)
		}
		t.Logf("%s 正文 %d 字节", ch, len([]byte(c)))
		assertWeComLimit(t, ch, c)
	}
}
