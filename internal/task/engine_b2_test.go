package task

// 0.6.504（B2）：下载失败分类文案的回归测试。
// 反向验证：把 HTTPHint/aggregateHint 还原成空实现，本文件必红。

import (
	"strings"
	"testing"
)

func TestHTTPHintClassifiesStatus(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{404, "不存在"},
		{410, "不存在"},
		{403, "拒绝访问"},
		{429, "限流"},
		{502, "源站故障"},
	}
	for _, c := range cases {
		got := HTTPHint(c.code)
		if !strings.Contains(got, c.want) {
			t.Fatalf("HTTPHint(%d) = %q，期望含 %q", c.code, got, c.want)
		}
	}
}

func TestAggregateHintAllMissing(t *testing.T) {
	errs := []string{
		"http://a/x.fpk: HTTP 404（下载地址不存在，源可能已下架或索引地址失效）",
		"http://b/x.fpk: aria2 退出码 exit status 1: errorCode=3 Resource not found（404 下载地址不存在，源可能已下架或索引地址失效）",
	}
	got := aggregateHint(errs)
	if !strings.Contains(got, "所有候选地址都不存在") {
		t.Fatalf("aggregateHint = %q，期望给出「都不存在」结论", got)
	}
}

func TestAggregateHintAllShaMismatch(t *testing.T) {
	errs := []string{
		"http://a/x.fpk: 安装包 sha256 不匹配（期望 aaaa…，实际 bbbb…）",
		"http://b/x.fpk: 安装包 sha256 不匹配（期望 aaaa…，实际 cccc…）",
	}
	got := aggregateHint(errs)
	if !strings.Contains(got, "不同步") {
		t.Fatalf("aggregateHint = %q，期望给出「索引与文件不同步」结论", got)
	}
}

func TestAria2HintResourceNotFound(t *testing.T) {
	tail := "Exception: [AbstractCommand.cc:351] errorCode=3 URI=http://x\n  -> [HttpSkipResponseCommand.cc:218] errorCode=3 Resource not found"
	if h := aria2Hint(tail); !strings.Contains(h, "404") {
		t.Fatalf("aria2Hint = %q，期望识别 404", h)
	}
}
