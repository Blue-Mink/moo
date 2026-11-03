package platform

import (
	"strings"
	"testing"
)

// 0.6.398：10500 曾以光秃秃的「app center 返回错误 10500 (…)」弹给用户，
// 用户只看到「错误代码10500」。现在必须给出中文提示且保留错误码。
func TestDaemonErrorHint10500(t *testing.T) {
	err := &DaemonError{Path: "/rpc/v1/install/task", Code: daemonCodeStatusUnsupported}
	msg := err.Error()
	for _, want := range []string{"10500", "当前状态不支持该操作", "已安装或正在安装"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("10500 错误信息缺少 %q: %q", want, msg)
		}
	}
}

// 平台自带 msg 时优先展示平台原文，不被本地提示覆盖。
func TestDaemonErrorKeepsPlatformMessage(t *testing.T) {
	err := &DaemonError{Path: "/rpc/v1/install/task", Code: daemonCodeStatusUnsupported, Msg: "busy"}
	if got := err.Error(); got != "app center 返回错误 10500: busy" {
		t.Fatalf("平台 msg 应优先: %q", got)
	}
}
