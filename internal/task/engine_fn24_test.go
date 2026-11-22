package task

import (
	"strings"
	"testing"
)

// 2026-11-21 FnDepot#24：同一个 5xx，停在镜像跳与停在源站要分开报。
// 反向验证：把 HTTPHintFor 还原成 HTTPHint(code)，本文件必 FAIL。
func TestHTTPHintFor24_MirrorVsOrigin(t *testing.T) {
	wrapped := "https://cdn.gh-proxy.org/https://raw.githubusercontent.com/Wyf841015/FnDepot/master/fnclearup/fnclearup.fpk"
	if got := HTTPHintFor(500, wrapped); !strings.Contains(got, "加速源") {
		t.Errorf("镜像壳 500 应提示加速源故障，got %q", got)
	}
	if strings.Contains(HTTPHintFor(500, wrapped), "源站故障") {
		t.Errorf("镜像壳 500 不应说源站故障，got %q", HTTPHintFor(500, wrapped))
	}
	direct := "https://raw.githubusercontent.com/Wyf841015/FnDepot/master/fnclearup/fnclearup.fpk"
	if got := HTTPHintFor(500, direct); !strings.Contains(got, "源站") {
		t.Errorf("直链 500 应提示源站故障，got %q", got)
	}
	// 404 优先于「加速源」判定：镜像壳上的 404 仍是地址不存在
	if got := HTTPHintFor(404, wrapped); !strings.Contains(got, "不存在") {
		t.Errorf("404 应提示地址不存在，got %q", got)
	}
}

func TestIsMirrorWrappedURL24(t *testing.T) {
	cases := []struct {
		u    string
		want bool
	}{
		{"https://cdn.gh-proxy.org/https://raw.githubusercontent.com/o/r/main/x.fpk", true},
		{"https://gh-proxy.com/http://example.com/x.fpk", true},
		{"https://raw.githubusercontent.com/o/r/main/x.fpk", false},
		{"https://github.com/o/r/releases/download/v1/x.fpk", false},
		{"", false},
		{"cdn.gh-proxy.org/https://x", true},
	}
	for _, c := range cases {
		if got := isMirrorWrappedURL(c.u); got != c.want {
			t.Errorf("isMirrorWrappedURL(%q) = %v, want %v", c.u, got, c.want)
		}
	}
}

func TestAggregateHint24_MirrorFailConclusion(t *testing.T) {
	wrapped := "https://cdn.gh-proxy.org/https://raw.githubusercontent.com/o/r/main/x.fpk"
	errs := []string{
		wrapped + ": HTTP 500（加速源故障（镜像不可用），与源站无关）",
	}
	got := aggregateHint(errs)
	if !strings.Contains(got, "加速镜像") {
		t.Errorf("全镜像故障应给出加速源结论，got %q", got)
	}
	if strings.Contains(got, "源索引里的下载地址已失效") {
		t.Errorf("不应误判为索引失效，got %q", got)
	}
}
