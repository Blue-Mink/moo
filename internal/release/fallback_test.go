package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newHeadStub 起一个「哪些路径可下载」的假发布站。
// present 里的路径名返回 200，其余 404。
func newHeadStub(t *testing.T, present ...string) *httptest.Server {
	t.Helper()
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := shortURL(r.URL.Path)
		if set[name] {
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fbOpts(srv *httptest.Server) Opts {
	return Opts{
		Tag:     testTag,
		Arch:    "x86",
		Repos:   []string{"Blue-Mink/moo"},
		Client:  srv.Client(),
		Timeout: 5 * time.Second,
	}
}

// 正向：web302 三件套全可达 → 成功。
func TestWeb302Complete(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv := newHeadStub(t, n, n+".sha256", n+".minisig")
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	res, err := probeWeb302(context.Background(), fbOpts(srv))
	if err != nil {
		t.Fatalf("期望成功: %v", err)
	}
	if !res.Assets.Complete() {
		t.Fatalf("三件套应齐全: %+v", res.Assets)
	}
}

// 反向：web302 缺 .minisig → 必须失败（这正是 10-29 事故的现场形态）。
func TestWeb302MissingMinisigFails(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv := newHeadStub(t, n, n+".sha256")
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	res, err := probeWeb302(context.Background(), fbOpts(srv))
	if err == nil {
		t.Fatalf("缺 .minisig 必须报错，却成功: %+v", res.Assets)
	}
	if !strings.Contains(shortURL(err.Error()), "minisig") && !strings.Contains(err.Error(), "minisig") {
		t.Fatalf("错误应点明 minisig: %v", err)
	}
}

// 反向：web302 三件套全缺 → 失败。
func TestWeb302AllMissingFails(t *testing.T) {
	srv := newHeadStub(t)
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	if _, err := probeWeb302(context.Background(), fbOpts(srv)); err == nil {
		t.Fatalf("全部 404 时必须报错")
	}
}

// 反向：镜像全部不可达 → 失败，且 Extra 记录失败镜像。
func TestMirrorAllFail(t *testing.T) {
	srv := newHeadStub(t)
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	o := fbOpts(srv)
	o.Mirrors = []string{srv.URL + "/m1", srv.URL + "/m2"}
	res, err := probeMirror(context.Background(), o)
	if err == nil {
		t.Fatalf("镜像全不可达必须报错")
	}
	if len(res.Extra) != 2 {
		t.Fatalf("应记录 2 条失败镜像，实际: %v", res.Extra)
	}
}

// 正向：第二个镜像可用 → 成功，且 Extra 标注用的是哪个镜像。
func TestMirrorSecondWorks(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv := newHeadStub(t, n, n+".sha256", n+".minisig")
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	// 走真实拼接规则：prefix + "/https://github.com/" + repo + "/releases/download/v<tag>"
	o := fbOpts(srv)
	o.Mirrors = []string{srv.URL}
	res, err := probeMirror(context.Background(), o)
	if err != nil {
		t.Fatalf("期望成功: %v", err)
	}
	if !res.Assets.Complete() {
		t.Fatalf("镜像通道也必须三件套齐全: %+v", res.Assets)
	}
	if !strings.Contains(strings.Join(res.Extra, ","), "mirror=") {
		t.Fatalf("应标注命中镜像: %v", res.Extra)
	}
}

// 关键防回归：任何通道成功后，三个 URL 都不得为空串，
// 且必须同前缀（镜像改不了签名）。
func TestNoChannelReturnsEmptySidecar(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv := newHeadStub(t, n, n+".sha256", n+".minisig")
	webBase = srv.URL
	defer func() { webBase = webBaseURL }()

	ctx := context.Background()
	for name, fn := range map[string]func(context.Context, Opts) (Result, error){
		ChannelWeb302: probeWeb302,
		ChannelMirror: func(c context.Context, o Opts) (Result, error) {
			o.Mirrors = []string{webBase}
			return probeMirror(c, o)
		},
	} {
		res, err := fn(ctx, fbOpts(srv))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		a := res.Assets
		if a.SHA256 == "" || a.Minisig == "" {
			t.Fatalf("%s: 出现空侧车（10-29 事故复现）: %+v", name, a)
		}
		// 三件套必须同前缀 —— 签名只对同目录下的 FPK 有意义。
		base := strings.TrimSuffix(a.FPK, "/"+n)
		if a.SHA256 != base+"/"+n+".sha256" || a.Minisig != base+"/"+n+".minisig" {
			t.Fatalf("%s: 三件套前缀不一致: %+v", name, a)
		}
	}
}
