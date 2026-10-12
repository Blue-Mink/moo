package api

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"syscall"
	"testing"
)

// F7：direct 进入候选集且最快时胜出（topSpeedKeys/bestKey 纯函数层）；
// direct fail 时回落最快镜像。开关关闭时 direct 不入池（不在 stats），
// bestKey 行为与现行为零差异。
func TestBestKeyWithDirectCandidate(t *testing.T) {
	stats := []MirrorStat{
		{Key: "gh-proxy-hk", Status: "ok", SpeedBPS: 1_000_000},
		{Key: "direct", Status: "ok", SpeedBPS: 5_000_000},
	}
	if k := bestKey(stats); k != "direct" {
		t.Fatalf("direct 最快应胜出: %s", k)
	}
	stats[1].Status = "fail"
	if k := bestKey(stats); k != "gh-proxy-hk" {
		t.Fatalf("direct fail 应回落最快镜像: %s", k)
	}
}

// F6：shortNetErr 分类。
func TestShortNetErr(t *testing.T) {
	if got := shortNetErr(nil); got != "" {
		t.Errorf("nil → %q, want 空", got)
	}
	if got := shortNetErr(&net.DNSError{Err: "no such host", Name: "x"}); got != "DNS 解析失败" {
		t.Errorf("DNS → %q", got)
	}
	if got := shortNetErr(context.DeadlineExceeded); got != "超时" {
		t.Errorf("deadline → %q", got)
	}
	if got := shortNetErr(syscall.ECONNREFUSED); got != "连接被拒" {
		t.Errorf("ECONNREFUSED → %q", got)
	}
	long := strings.Repeat("x", 200)
	if got := shortNetErr(errLong{long}); len([]rune(got)) > 61 {
		t.Errorf("超长错误应截断: %d 字符", len([]rune(got)))
	}
}

type errLong struct{ s string }

func (e errLong) Error() string { return e.s }

// F6：Error 可选字段契约——ok 态 JSON 无 error 键（旧前端兼容），
// fail 态携带原因。
func TestMirrorStatErrorOmitEmpty(t *testing.T) {
	b, err := json.Marshal(MirrorStat{Key: "k", Label: "K", Status: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"error"`) {
		t.Errorf("ok 态不应出现 error 字段: %s", b)
	}
	b2, err := json.Marshal(MirrorStat{Key: "k", Label: "K", Status: "fail", Error: "429 限流"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"error":"429 限流"`) {
		t.Errorf("fail 态应携带原因: %s", b2)
	}
}
