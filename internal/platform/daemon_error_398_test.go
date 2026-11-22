package platform

import (
	"strings"
	"testing"
)

// 0.6.398 起 10500 会给出中文提示；本版（10500 重试）把文案精简为
// 「应用中心正忙，请稍后重试」——重试 3 次仍忙才把它交给用户。
func TestDaemonErrorHint10500(t *testing.T) {
	err := &DaemonError{Path: "/rpc/v1/install/task", Code: daemonCodeStatusUnsupported}
	msg := err.Error()
	for _, want := range []string{"10500", "应用中心正忙，请稍后重试"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("10500 错误信息缺少 %q: %q", want, msg)
		}
	}
	// 精简：不再出现旧的长文案
	if strings.Contains(msg, "当前状态不支持该操作") || strings.Contains(msg, "已安装或正在安装") {
		t.Fatalf("10500 文案未精简: %q", msg)
	}
}

// 平台自带 msg 时优先展示平台原文，不被本地提示覆盖。
func TestDaemonErrorKeepsPlatformMessage(t *testing.T) {
	err := &DaemonError{Path: "/rpc/v1/install/task", Code: daemonCodeStatusUnsupported, Msg: "busy"}
	if got := err.Error(); got != "busy（10500）" {
		t.Fatalf("平台 msg 应优先（10500 走精简格式）: %q", got)
	}
}
