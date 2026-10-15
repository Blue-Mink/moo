package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// dkOKServer 模拟可达 registry（GET /v2/ 返回指定状态码，可加固定延迟）。
func dkOKServer(code int, delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		if r.URL.Path != "/v2/" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code)
	}))
}

// dkDeadURL 返回一个「建好立刻关掉」的服务器地址（连接必然失败）。
func dkDeadURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u := s.URL
	s.Close()
	return u
}

// auto = 并发实测选最快可达源：401 也算可达（registry 鉴权流）；
// 死源不入选；auto/custom/direct 等无 URL 虚拟项必须被跳过。
func TestPickDockerAutoMirror_FastestWins(t *testing.T) {
	fast := dkOKServer(http.StatusOK, 0)
	defer fast.Close()
	slow := dkOKServer(http.StatusUnauthorized, 400*time.Millisecond)
	defer slow.Close()

	opts := []config.MirrorOption{
		{Key: "slow", URL: slow.URL + "/"},
		{Key: "fast", URL: fast.URL + "/"},
		{Key: "dead", URL: dkDeadURL(t) + "/"},
		{Key: "auto"}, {Key: "custom"}, {Key: "direct"},
	}
	got, lat, err := pickDockerAutoMirror(context.Background(), opts)
	if err != nil {
		t.Fatalf("有可达源时应选出最快: %v", err)
	}
	if got.Key != "fast" {
		t.Errorf("应选最快可达源 fast，实际: %s（lat=%d）", got.Key, lat)
	}
}

// 全部不可达：诚实报错并列出尝试数量，不返回任何源。
func TestPickDockerAutoMirror_AllFailHonestError(t *testing.T) {
	opts := []config.MirrorOption{
		{Key: "deadA", URL: dkDeadURL(t) + "/"},
		{Key: "deadB", URL: dkDeadURL(t) + "/"},
	}
	_, _, err := pickDockerAutoMirror(context.Background(), opts)
	if err == nil {
		t.Fatal("全失败应诚实报错")
	}
	if !strings.Contains(err.Error(), "全部不可达") || !strings.Contains(err.Error(), "2 个") {
		t.Errorf("错误应列出尝试数量，实际: %v", err)
	}
}

// kspeeder（无 URL 本地源）可入选；其写入地址 = 本地代理地址。
func TestPickDockerAutoMirror_KspeederWins(t *testing.T) {
	orig := dockerAutoProbeFn
	t.Cleanup(func() { dockerAutoProbeFn = orig })
	dockerAutoProbeFn = func(_ context.Context, o config.MirrorOption) (int64, bool) {
		if o.Key == "kspeeder" {
			return 5, true
		}
		return 500, true
	}
	opts := []config.MirrorOption{
		{Key: "remote", URL: "https://docker.example.com/"},
		{Key: "kspeeder"},
	}
	got, _, err := pickDockerAutoMirror(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "kspeeder" {
		t.Fatalf("本地 5ms vs 远端 500ms 应选 kspeeder，实际: %s", got.Key)
	}
	if u := dockerMirrorApplyURL(got); u != "https://"+config.KSpeederLocalAddr+"/" {
		t.Errorf("kspeeder 应用地址应为本地代理，实际: %q", u)
	}
}

// 无任何候选（全是虚拟项）= 诚实报错。
func TestPickDockerAutoMirror_NoCandidates(t *testing.T) {
	_, _, err := pickDockerAutoMirror(context.Background(), []config.MirrorOption{
		{Key: "auto"}, {Key: "custom"}, {Key: "direct"},
	})
	if err == nil || !strings.Contains(err.Error(), "没有可探测") {
		t.Fatalf("无候选应诚实报错，实际: %v", err)
	}
}
