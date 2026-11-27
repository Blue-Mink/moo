package notify

// send_wecom_cap_4d_test.go —— 盲点补测 批次 4d（企微 4096 字节上限）边界用例。
//
// 4a 的两条红（internal/api/notify_auto_care_oversize_4a_test.go）断言的是
// "真实发出去的载荷 ≤4096"。本文件补齐 capWeComContent/weComCompose 的边界：
// 恰好等于预算、超 1 字节、全换行、超长单行无换行、prefix 吃光预算、
// 以及 UTF-8 不得被切出半个汉字（去掉 rune 回退即 FAIL）。

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const weComTestHead = "### T\n\n"

// weComTestBudget 与实现同口径的正文预算（不含 prefix 时）。
func weComTestBudget(prefix string) int {
	return weComSingleLimit - len(weComCutMark) - len(prefix)
}

// checkWeCom 统一断言：≤4096 字节、合法 UTF-8、无 U+FFFD 替换符。
func checkWeCom(t *testing.T, name, got string) {
	t.Helper()
	if n := len([]byte(got)); n > weComSingleLimit {
		t.Fatalf("%s 超限: %d 字节 > %d", name, n, weComSingleLimit)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("%s 输出不是合法 UTF-8（切出了半个汉字）: %q", name, got[len(got)-30:])
	}
	if strings.Contains(got, "\uFFFD") {
		t.Fatalf("%s 输出含 U+FFFD 替换符: %q", name, got[len(got)-30:])
	}
}

// 恰好等于预算 → 一字不改、不加尾注；多 1 字节 → 截断 + 尾注。
func TestWeComCap_ExactBudgetAndPlusOne(t *testing.T) {
	budget := weComTestBudget("")
	exact := strings.Repeat("a", budget)
	if got := capWeComContent(exact); got != exact {
		t.Fatalf("恰好等于预算的内容应原样返回: len=%d", len([]byte(got)))
	}
	over := strings.Repeat("a", budget+1)
	got := capWeComContent(over)
	checkWeCom(t, "budget+1", got)
	if len([]byte(got)) != weComSingleLimit {
		t.Fatalf("budget+1 应截到恰好 %d 字节，实际 %d", weComSingleLimit, len([]byte(got)))
	}
	if !strings.HasSuffix(got, weComCutMark) {
		t.Fatalf("budget+1 应带尾注: %q", got[len(got)-40:])
	}
}

// 全是换行：不能死循环、不能超限，仍要有尾注。
func TestWeComCap_AllNewlines(t *testing.T) {
	got := capWeComContentAfter(weComTestHead, strings.Repeat("\n", 9000))
	checkWeCom(t, "全换行", got)
	if !strings.HasSuffix(got, weComCutMark) {
		t.Fatalf("全换行超长也应带尾注")
	}
}

// 超长单行无换行：按预算硬切，且尽量保留内容（不得退到很靠前的换行）。
func TestWeComCap_SingleLongLineNoNewline(t *testing.T) {
	got := weComCompose("### ", "T", 0, strings.Repeat("x", 20000))
	checkWeCom(t, "超长单行", got)
	head := weComTestHead // timeLine(0) == ""
	body := strings.TrimSuffix(strings.TrimPrefix(got, head), weComCutMark)
	if len([]byte(body)) < weComTestBudget(head)-weComNewlineSlack {
		t.Fatalf("单行硬切保留了 %d 字节，应接近预算 %d（不得因退回靠前换行而大幅丢失内容）",
			len([]byte(body)), weComTestBudget(head))
	}
}

// UTF-8 安全：预算落在汉字中间时只能回退到 rune 起点（去掉 rune 回退即 FAIL）。
func TestWeComCap_NoHalfRune(t *testing.T) {
	budget := weComTestBudget(weComTestHead) // 3998，位于 3 字节汉字的中间
	if budget%3 == 0 {
		t.Fatalf("测试前提：预算 %d 应不是 3 的倍数（否则切不出半个汉字）", budget)
	}
	got := capWeComContentAfter(weComTestHead, strings.Repeat("很", 3000))
	checkWeCom(t, "汉字边界", got)
	body := strings.TrimSuffix(strings.TrimPrefix(got, weComTestHead), weComCutMark)
	if !utf8.ValidString(body) || len([]byte(body))%3 != 0 {
		t.Fatalf("正文保留了 %d 字节（%d 整除 3），切出了半个汉字", len([]byte(body)), len([]byte(body))%3)
	}
}

// prefix 自己吃掉全部预算：不得 panic，输出仍在限内。
func TestWeComCap_PrefixEatsBudget(t *testing.T) {
	// 预算恰好归零：prefix + 尾注 = 4096，正文完全省略。
	prefix := strings.Repeat("H", weComSingleLimit-len(weComCutMark))
	got := capWeComContentAfter(prefix, "正文正文正文")
	if got != weComCutMark {
		t.Fatalf("预算耗尽时应只留尾注，实际 %q", got)
	}
	checkWeCom(t, "prefix 吃光预算", prefix+got)
	// 预算为负（prefix 本身就超限）：不 panic、不返回正文；此时只有截断标题行
	// 才能压到 4096，属调用方（标题恒为固定短串）不可能触发的防御分支。
	if got := capWeComContentAfter(prefix+"HHHHHHHHHH", "正文"); got != weComCutMark {
		t.Fatalf("负预算也应只留尾注，实际 %q", got)
	}
}

// 短内容（3KB）不得被误截、不得加尾注。
func TestWeComCap_ShortContentUntouched(t *testing.T) {
	body := strings.Repeat("短内容占位，", 150) // 18 B × 150 = 2700 B
	if len([]byte(body)) >= weComTestBudget(weComTestHead) {
		t.Fatalf("测试前提：正文 %d 字节应小于预算", len([]byte(body)))
	}
	got := weComCompose("### ", "T", 0, body)
	if got != weComTestHead+body {
		t.Fatalf("短正文被改动: %d → %d 字节", len([]byte(body)), len([]byte(got)))
	}
}
