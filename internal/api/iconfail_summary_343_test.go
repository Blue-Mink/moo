package api

import (
	"strings"
	"testing"
)

// 0.6.343：iconfail 候选清单压缩打印（日志页「很乱」的根因之一）。
func TestSummarizeTried_343(t *testing.T) {
	if got := summarizeTried(nil); got != "[]" {
		t.Fatalf("空清单: %q", got)
	}

	two := []string{
		"https://a/x.png(ok=false,miss=true)",
		"https://b/x.png(ok=false,miss=false)",
	}
	got := summarizeTried(two)
	if strings.Contains(got, "…") {
		t.Fatalf("未超上限不应省略: %q", got)
	}
	if !strings.Contains(got, "共 2 条") || !strings.Contains(got, "确定缺失 1") {
		t.Fatalf("计数不对: %q", got)
	}

	var many []string
	for i := 0; i < 24; i++ {
		flag := "false"
		if i < 6 {
			flag = "true"
		}
		many = append(many, "https://mirror/"+strings.Repeat("p", 40)+"(ok=false,miss="+flag+")")
	}
	got = summarizeTried(many)
	if !strings.Contains(got, "…") || !strings.Contains(got, "共 24 条") {
		t.Fatalf("超上限应省略并给总数: %q", got)
	}
	if !strings.Contains(got, "确定缺失 6") || !strings.Contains(got, "其它 18") {
		t.Fatalf("缺失/失败拆分不对: %q", got)
	}
	// 关键：压缩后仍必须远短于原样打印（原样 24 条 ≈ 3KB）
	rawLen := len(strings.Join(many, " "))
	if len(got) > rawLen/4 {
		t.Fatalf("压缩不足: %d vs raw %d", len(got), rawLen)
	}
}
