package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testTag = "9.9.9"

// newAPIStub 起一个假的 GitHub API + 下载服务。
// assets 指定 release 里放哪些资产名；测试用「缺侧车」来构造反向用例。
func newAPIStub(t *testing.T, assets []string, draft bool) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Blue-Mink/moo/releases/tags/v"+testTag, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("请求缺少 User-Agent（GitHub 会 403）")
		}
		hits++
		type asset struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			State  string `json:"state"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		}
		rel := struct {
			TagName string  `json:"tag_name"`
			Draft   bool    `json:"draft"`
			Assets  []asset `json:"assets"`
		}{TagName: "v" + testTag, Draft: draft}
		for _, n := range assets {
			rel.Assets = append(rel.Assets, asset{
				Name: n, Size: 1234, State: "uploaded",
				Digest: "sha256:abc123",
				URL:    "https://github.com/Blue-Mink/moo/releases/download/v" + testTag + "/" + n,
			})
		}
		json.NewEncoder(w).Encode(rel)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

// withAPIBase 把包内 const 的 apiBaseURL 临时指向测试服务器。
// 由于 apiBaseURL 是 const，测试里改用注入 client 的方式不可行，
// 因此 probeAPI 内部读的是变量 apiBase（见 api.go）。这里用 setter 定位。
func testOpts(srv *httptest.Server) Opts {
	return Opts{
		Tag:     testTag,
		Arch:    "x86",
		Repos:   []string{"Blue-Mink/moo"},
		Client:  srv.Client(),
		Timeout: 5 * time.Second,
	}
}

func threeAssets() []string {
	n := FPKName(testTag, "x86")
	return []string{n, n + ".sha256", n + ".minisig"}
}

// 正向：三件套齐全 → 成功，且三个 URL 都非空。
func TestProbeAPIComplete(t *testing.T) {
	srv, hits := newAPIStub(t, threeAssets(), false)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	res, err := probeAPI(context.Background(), testOpts(srv))
	if err != nil {
		t.Fatalf("期望成功，得到错误: %v", err)
	}
	if !res.OK || res.Version != testTag {
		t.Fatalf("结果不符: ok=%v version=%q", res.OK, res.Version)
	}
	if !res.Assets.Complete() {
		t.Fatalf("三件套应齐全，实际缺 %v", res.Assets.Missing())
	}
	if res.Assets.Minisig == "" || res.Assets.SHA256 == "" {
		t.Fatalf("侧车为空 = 复现 10-29 事故: %+v", res.Assets)
	}
	if *hits != 1 {
		t.Fatalf("期望 1 次 API 调用，实际 %d", *hits)
	}
}

// 反向 1：缺 .minisig → 必须失败（fail-closed）。
func TestProbeAPIMissingMinisigFails(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv, _ := newAPIStub(t, []string{n, n + ".sha256"}, false)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	res, err := probeAPI(context.Background(), testOpts(srv))
	if err == nil {
		t.Fatalf("缺 .minisig 时必须报错，却成功了: %+v", res.Assets)
	}
	if res.OK {
		t.Fatalf("缺侧车时 OK 必须为 false")
	}
	if !strings.Contains(err.Error(), "minisig") {
		t.Fatalf("错误信息应点明缺 minisig，实际: %v", err)
	}
}

// 反向 2：缺 .sha256 → 失败（老客户端升级通道断了）。
func TestProbeAPIMissingSHAFails(t *testing.T) {
	n := FPKName(testTag, "x86")
	srv, _ := newAPIStub(t, []string{n, n + ".minisig"}, false)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	if _, err := probeAPI(context.Background(), testOpts(srv)); err == nil {
		t.Fatalf("缺 .sha256 时必须报错")
	} else if !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("错误信息应点明缺 sha256，实际: %v", err)
	}
}

// 反向 3：draft 发布 → 失败（外部不可见）。
func TestProbeAPIDraftFails(t *testing.T) {
	srv, _ := newAPIStub(t, threeAssets(), true)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	if _, err := probeAPI(context.Background(), testOpts(srv)); err == nil {
		t.Fatalf("draft 发布必须报错")
	}
}

// 反向 4：404 → 失败且信息含 HTTP 404。
func TestProbeAPINotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	_, err := probeAPI(context.Background(), testOpts(srv))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("期望 404 错误，实际: %v", err)
	}
}

// 反向 5：资产 state 非 uploaded → 失败（上传截断特征）。
func TestProbeAPIStateNotUploaded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Blue-Mink/moo/releases/tags/v"+testTag, func(w http.ResponseWriter, r *http.Request) {
		n := FPKName(testTag, "x86")
		w.Write([]byte(`{"tag_name":"v` + testTag + `","draft":false,"assets":[` +
			`{"name":"` + n + `","size":1,"state":"starter","digest":"sha256:aa","browser_download_url":"https://github.com/Blue-Mink/moo/releases/download/v9.9.9/` + n + `"},` +
			`{"name":"` + n + `.sha256","size":1,"state":"uploaded","browser_download_url":"https://github.com/Blue-Mink/moo/releases/download/v9.9.9/` + n + `.sha256"},` +
			`{"name":"` + n + `.minisig","size":1,"state":"uploaded","browser_download_url":"https://github.com/Blue-Mink/moo/releases/download/v9.9.9/` + n + `.minisig"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	if _, err := probeAPI(context.Background(), testOpts(srv)); err == nil {
		t.Fatalf("state=starter 时必须报错")
	}
}

// 反向 6：资产 URL 不在 releases/download 路径下 → 失败。
func TestProbeAPIBadAssetURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/Blue-Mink/moo/releases/tags/v"+testTag, func(w http.ResponseWriter, r *http.Request) {
		n := FPKName(testTag, "x86")
		w.Write([]byte(`{"tag_name":"v` + testTag + `","draft":false,"assets":[` +
			`{"name":"` + n + `","size":1,"state":"uploaded","digest":"sha256:aa","browser_download_url":"http://evil.example.com/x/` + n + `"},` +
			`{"name":"` + n + `.sha256","size":1,"state":"uploaded","browser_download_url":"https://github.com/Blue-Mink/moo/releases/download/v9.9.9/` + n + `.sha256"},` +
			`{"name":"` + n + `.minisig","size":1,"state":"uploaded","browser_download_url":"https://github.com/Blue-Mink/moo/releases/download/v9.9.9/` + n + `.minisig"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	apiBase = srv.URL
	defer func() { apiBase = originalAPIBase }()

	if _, err := probeAPI(context.Background(), testOpts(srv)); err == nil {
		t.Fatalf("非 https/releases 路径的资产 URL 必须报错")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
