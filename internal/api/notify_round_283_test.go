package api

import (
	"strings"
	"testing"
)

// TestShortSyncErr 0.6.283：源同步失败通知基础文本附带压缩后的错误原因。
func TestShortSyncErr(t *testing.T) {
	got := shortSyncErr(`所有候选地址均失败: Get "https://x/y": context deadline exceeded`)
	if strings.HasPrefix(got, "所有候选地址均失败") {
		t.Error("未剥共性前缀")
	}
	if !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("丢了关键错误信息: %s", got)
	}
	long := strings.Repeat("错", 100)
	tr := shortSyncErr(long)
	if len([]rune(tr)) != 61 || !strings.HasSuffix(tr, "…") {
		t.Errorf("rune 截断异常: len=%d", len([]rune(tr)))
	}
	if shortSyncErr("   ") != "" {
		t.Error("空错误应得空串")
	}
}
