package api

// 同仓多应用隔离（2026-11-08 事故回归）：
// Moo 与 Moo Mate 的 release 同放 Blue-Mink/moo 一个仓，`/releases/latest` 是全仓
// 唯一指针。mate 发版会把 `mate-v0.2.55` 顶成 latest，旧逻辑只 TrimPrefix("v")
// 就采信 → splitVersion 拆成 [0,2,55] 判「无更新」，且 HTTP 200 无 error 短路
// 整条兜底链，外部看「一切正常」。下面的用例钉住三条防线。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moo/internal/config"
)

func TestIsMooVersionTag(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"0.6.539", true},
		{"v0.6.539", true},
		{" v1.0.0 ", true},
		{"10.0.0", true},
		{"mate-v0.2.55", false},
		{"v", false},
		{"", false},
		{"latest", false},
		{"6", false},            // 至少要有一个点，挡住裸数字 tag
		{"v0.6.539-rc1", false}, // 带后缀的不认（Moo 正式版本是纯点分）
	}
	for _, c := range cases {
		if got := isMooVersionTag(c.in); got != c.want {
			t.Errorf("isMooVersionTag(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// latest 被 mate 抢占时，必须改用 release 列表挑最高的 Moo 版本，而不是判无更新。
func TestDoProbeLatestMateTagFallsBackToList(t *testing.T) {
	suppressProbeNotify(t)
	var listHits int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/Blue-Mink/moo/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"mate-v0.2.55","body":"mate notes"}`))
		case "/repos/Blue-Mink/moo/releases":
			atomic.AddInt32(&listHits, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"tag_name":"mate-v0.2.55"},
				{"tag_name":"v10.0.0","prerelease":true},
				{"tag_name":"v11.0.0","draft":true},
				{"tag_name":"v9.9.9"},
				{"tag_name":"v9.9.8"}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiSrv.Close()
	restore := setProbeBases(apiSrv.URL, "https://should-not-be-called.invalid", "https://should-not-be-called.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, assetURL, asset, shaURL, minisigURL, body, _, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe: %v", err)
	}
	if tag != "9.9.9" || asset != "moo_9.9.9_x86.fpk" {
		t.Fatalf("应按列表选中 9.9.9，实际 tag=%q asset=%q（draft/prerelease 未过滤？）", tag, asset)
	}
	if !strings.Contains(assetURL, "moo_9.9.9_x86.fpk") ||
		!strings.HasSuffix(shaURL, ".sha256") || !strings.HasSuffix(minisigURL, ".minisig") {
		t.Fatalf("兜底资产三件套不完整: assetURL=%q sha=%q minisig=%q", assetURL, shaURL, minisigURL)
	}
	if body != "" {
		t.Fatalf("列表兜底不该带回 release body，实际 %q", body)
	}
	if got := atomic.LoadInt32(&listHits); got != 1 {
		t.Fatalf("listHits = %d, want 1", got)
	}
}

// web 通道 302 到 mate tag 时必须判失败，不能当版本号返回。
func TestProbeWebLatestRejectsMateTag(t *testing.T) {
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Blue-Mink/moo/releases/tag/mate-v0.2.55", http.StatusFound)
	}))
	defer webSrv.Close()
	restore := setProbeBases("https://should-not-be-called.invalid", webSrv.URL, "https://should-not-be-called.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, err := s.probeWebLatest()
	if err == nil {
		t.Fatalf("mate tag 应被拒绝，实际返回 tag=%q", tag)
	}
	if !strings.Contains(err.Error(), "不是 Moo 版本 tag") {
		t.Fatalf("错误信息应说明 tag 非法，实际 %v", err)
	}
}

// 凭据只发官方 api.github.com：自定义 base（镜像 / 测试 httptest）绝不带
// Authorization。共用出口 IP 时匿名配额 60/h 极易打满，带 token 是主通道稳定的
// 关键；但不能因此把凭据泄露给任何非官方域名。
func TestGitHubAPIRequestTokenScope(t *testing.T) {
	s := &Server{Cfg: config.Default()}
	s.Cfg.GithubToken = "ghp_secret_for_test"
	req, err := s.githubAPIRequest("https://api.github.com/repos/Blue-Mink/moo/releases/latest")
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "token ghp_secret_for_test" {
		t.Fatalf("官方域应带 token，实际 %q", got)
	}
	req2, err := s.githubAPIRequest("http://127.0.0.1:1234/repos/x/y")
	if err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("Authorization"); got != "" {
		t.Fatalf("自定义 base 不得携带凭据，实际 %q", got)
	}
	s2 := &Server{Cfg: config.Default()}
	req3, _ := s2.githubAPIRequest("https://api.github.com/x")
	if got := req3.Header.Get("Authorization"); got != "" {
		t.Fatalf("未配置 token 不应带 Authorization，实际 %q", got)
	}
}

// token 过期/被撤销（401）时必须自动退回匿名重试，别让一份坏凭据打掉本来可用的读。
func TestDoGitHubGETRetriesAnonymouslyOn401(t *testing.T) {
	var withTok, withoutTok int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			atomic.AddInt32(&withTok, 1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		atomic.AddInt32(&withoutTok, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	oldHost := selfUpdateTokenHost
	selfUpdateTokenHost = srv.URL + "/"
	defer func() { selfUpdateTokenHost = oldHost }()

	s := &Server{Cfg: config.Default()}
	s.Cfg.GithubToken = "ghp_expired_test"
	resp, err := s.doGitHubGET(srv.URL + "/repos/x/y")
	if err != nil {
		t.Fatalf("doGitHubGET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("401 后应匿名重试成功，实际 %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&withTok); got != 1 {
		t.Fatalf("首次应带 token，实际 %d 次", got)
	}
	if got := atomic.LoadInt32(&withoutTok); got != 1 {
		t.Fatalf("应匿名重试 1 次，实际 %d 次", got)
	}
}
