package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newIndexStub 起一个返回固定 JSON 的索引服务。
func newIndexStub(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func idxOpts(srv *httptest.Server) Opts {
	return Opts{
		Tag:     testTag,
		Arch:    "x86",
		Repos:   []string{"Blue-Mink/moo"},
		Client:  srv.Client(),
		Timeout: 5 * time.Second,
	}
}

// 正向：自索引 version 与 releases 都匹配 → 成功。
func TestSelfIndexOK(t *testing.T) {
	body := `{"apps":{"moo":{"version":"9.9.9","releases":{"9.9.9":{"changelog":"x"},"9.9.8":{}}}}}`
	srv := newIndexStub(t, body, 0)
	webBase = "https://github.com"
	defer func() { webBase = webBaseURL }()

	o := idxOpts(srv)
	o.IndexURL = srv.URL + "/moo.json"
	res, err := probeSelfIndex(context.Background(), o)
	if err != nil {
		t.Fatalf("期望成功: %v", err)
	}
	if !res.Assets.Complete() {
		t.Fatalf("自索引通道也必须补齐三件套: %+v", res.Assets)
	}
}

// 反向：索引顶层 version 落后（releases 有目标节点也不行）→ 失败。
// 守护「索引必须晚于 release，且顶层字段要跟版」这条纪律。
func TestSelfIndexTopVersionStaleFails(t *testing.T) {
	body := `{"apps":{"moo":{"version":"9.9.8","releases":{"9.9.9":{}}}}}`
	srv := newIndexStub(t, body, 0)

	o := idxOpts(srv)
	o.IndexURL = srv.URL + "/moo.json"
	_, err := probeSelfIndex(context.Background(), o)
	if err == nil {
		t.Fatalf("顶层 version 落后时必须报错")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("错误应说明不一致: %v", err)
	}
}

// 反向：releases 里没有目标版本 → 失败。
func TestSelfIndexMissingReleaseFails(t *testing.T) {
	body := `{"apps":{"moo":{"version":"9.9.9","releases":{"9.9.7":{}}}}}`
	srv := newIndexStub(t, body, 0)

	o := idxOpts(srv)
	o.IndexURL = srv.URL + "/moo.json"
	if _, err := probeSelfIndex(context.Background(), o); err == nil {
		t.Fatalf("releases 缺目标版本必须报错")
	}
}

// 反向：无 apps.moo 条目 → 失败。
func TestSelfIndexNoAppFails(t *testing.T) {
	srv := newIndexStub(t, `{"apps":{"other":{"version":"1.0.0"}}}`, 0)
	o := idxOpts(srv)
	o.IndexURL = srv.URL + "/moo.json"
	if _, err := probeSelfIndex(context.Background(), o); err == nil {
		t.Fatalf("缺 apps.moo 必须报错")
	}
}

// 正向：FnDepot V2 索引含目标版本 → 成功。
func TestFnDepotOK(t *testing.T) {
	body := `{"schema_version":"2","apps":{"moo":{"version":"9.9.9","releases":{"9.9.9":{"changelog":"x","packages":{"x86":{}}}}}}}`
	srv := newIndexStub(t, body, 0)

	o := idxOpts(srv)
	o.FnURL = srv.URL + "/fnpack.json"
	res, err := probeFnDepot(context.Background(), o)
	if err != nil {
		t.Fatalf("期望成功: %v", err)
	}
	if !res.Assets.Complete() {
		t.Fatalf("FnDepot 通道也必须补齐三件套: %+v", res.Assets)
	}
}

// 反向：FnDepot releases 缺目标版本 → 失败。
func TestFnDepotMissingReleaseFails(t *testing.T) {
	body := `{"schema_version":"2","apps":{"moo":{"version":"9.9.9","releases":{"9.9.5":{}}}}}`
	srv := newIndexStub(t, body, 0)

	o := idxOpts(srv)
	o.FnURL = srv.URL + "/fnpack.json"
	if _, err := probeFnDepot(context.Background(), o); err == nil {
		t.Fatalf("FnDepot 缺目标版本必须报错")
	}
}

// 反向：索引端点 500 → 失败（网络/服务错误不许当成功）。
func TestFnDepotHTTPErrorFails(t *testing.T) {
	srv := newIndexStub(t, "", http.StatusInternalServerError)
	o := idxOpts(srv)
	o.FnURL = srv.URL + "/fnpack.json"
	if _, err := probeFnDepot(context.Background(), o); err == nil {
		t.Fatalf("HTTP 500 必须报错")
	}
}

// AssetsFor 是兜底通道唯一的拼装点，单测锁死其形态。
func TestAssetsForShape(t *testing.T) {
	a := AssetsFor("https://github.com/Blue-Mink/moo/releases/download/v9.9.9/", "v9.9.9", "X86")
	if a.FPK != "https://github.com/Blue-Mink/moo/releases/download/v9.9.9/moo_9.9.9_x86.fpk" {
		t.Fatalf("FPK URL 形态错: %s", a.FPK)
	}
	if !a.Complete() {
		t.Fatalf("AssetsFor 必须永远产出完整三件套: %+v", a)
	}
	if got, want := NormalizedTag(" v0.6.377 "), "0.6.377"; got != want {
		t.Fatalf("NormalizedTag(%q)=%q, want %q", " v0.6.377 ", got, want)
	}
}

// Probe 全通道：只跑指定通道时不应触及其他通道。
func TestProbeChannelFilter(t *testing.T) {
	srv := newIndexStub(t, `{"apps":{"moo":{"version":"9.9.9","releases":{"9.9.9":{}}}}}`, 0)
	o := idxOpts(srv)
	o.IndexURL = srv.URL + "/moo.json"
	o.Channels = []string{ChannelSelfIdx}

	rs := Probe(context.Background(), o)
	if len(rs) != 1 || rs[0].Channel != ChannelSelfIdx {
		t.Fatalf("通道过滤失效: %+v", rs)
	}
	if !rs[0].OK {
		t.Fatalf("自索引通道应成功: %+v", rs[0])
	}
}
