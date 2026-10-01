package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// 回归：两个非 jsDelivr 候选独立 404（直链/镜像路径 1:1 语义）→ 快速判
// 真缺失，不再等满 13s（GenOffice：镜像仓库缺图 + raw 黑洞，破图 13s）。
func TestRaceFetch_TwoNonJd404sDead(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srvB.Close()

	s := &Server{Mirrors: NewMirrorMonitor()}
	start := time.Now()
	_, _, dead, err := s.raceFetch(context.Background(),
		[]string{srvA.URL + "/x.webp", srvB.URL + "/y.webp"})
	if !dead {
		t.Fatalf("双 404 应判真缺失: err=%v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("双 404 应快速返回，实际 %v", time.Since(start))
	}
}

// 回归：一个 404 + 一个 200 → 200 胜出（404 证据不掩盖健康候选）。
func TestRaceFetch_404Plus200Wins(t *testing.T) {
	srv404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv404.Close()
	srv200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv200.Close()

	s := &Server{Mirrors: NewMirrorMonitor()}
	body, _, dead, err := s.raceFetch(context.Background(),
		[]string{srv404.URL + "/x", srv200.URL + "/y"})
	if err != nil || dead {
		t.Fatalf("200 应胜出: dead=%v err=%v", dead, err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q", body)
	}
}

// 回归：镜像健康数据冷（重启后首探未完成）时，候选仍要带声明序前缀
// 镜像——否则 raw 黑洞窗口内竞速无独立路径（全候选同主机全超时）。
func TestMirrorVariants_ColdMonitorFallback(t *testing.T) {
	s := &Server{Mirrors: NewMirrorMonitor()}
	cands := s.readmeCandidateURLs("https://raw.githubusercontent.com/o/r/main/README.md")
	joined := strings.Join(cands, "\n")
	want := false
	for _, opt := range config.GitHubMirrorOptions() {
		if opt.URL != "" && opt.Key != "conversun" &&
			strings.Contains(joined, opt.URL+"https://raw.githubusercontent.com/o/r/main/README.md") {
			want = true
			break
		}
	}
	if !want {
		t.Fatalf("冷启动候选缺通用镜像变体:\n%s", joined)
	}
	if strings.Contains(joined, "conversun") {
		t.Fatalf("conversun 只代理 conversun 仓库，不得进通用候选:\n%s", joined)
	}
}

// 回归：瞬态 429（上游限流窗口）不应直接判死——剩余预算内重试一次竞速。
// 2026-09-23 实锤：源同步风暴触发 GitHub raw 限流窗口，TAD README 单轮
// 竞速全部候选非 200 报「所有候选均不可达」，窗口过后同 URL 秒回。
func TestFetchReadmeRace_RetriesTransientFailure(t *testing.T) {
	orig := http.DefaultClient
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# ok"))
	}))
	defer srv.Close()
	http.DefaultClient = srv.Client()
	defer func() { http.DefaultClient = orig }()

	s := &Server{Mirrors: NewMirrorMonitor()}
	body, _, dead, err := s.fetchReadmeRace(context.Background(), srv.URL+"/README.md")
	if err != nil {
		t.Fatalf("瞬态 429 应被重试救回: dead=%v err=%v", dead, err)
	}
	if string(body) != "# ok" {
		t.Fatalf("body = %q", body)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("应至少重试一次，实际请求 %d 次", hits)
	}
}

func TestNormalizeReadmeURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"https://github.com/o/r/blob/main/README.md",
			"https://raw.githubusercontent.com/o/r/main/README.md",
		},
		{
			"https://github.com/o/r/raw/master/dir/README.md",
			"https://raw.githubusercontent.com/o/r/master/dir/README.md",
		},
		{
			"https://raw.githubusercontent.com/o/r/main/README.md",
			"https://raw.githubusercontent.com/o/r/main/README.md",
		},
		{"https://github.com/o/r/releases", "https://github.com/o/r/releases"},
		{"https://fndepot.imcq.top/app/README.md", "https://fndepot.imcq.top/app/README.md"},
	}
	for _, c := range cases {
		if got := normalizeReadmeURL(c.in); got != c.want {
			t.Errorf("normalizeReadmeURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 回归：serve 路径必须把相对路径图片按 README 目录补全（2026-09-23
// 实锤：rewriteRelImages 写了没接线，imageadmin 相对图被浏览器解析到
// 面板域名 5667 → 整排破图）。
func TestServeReadme_RewritesRelImages(t *testing.T) {
	rec := httptest.NewRecorder()
	body := "![a](Preview/a.png)\n![b](https://x.com/abs.png)\n<img src=\"sub/b.jpg\">"
	serveReadme(rec, []byte(body), "", "https://raw.githubusercontent.com/Brian099/FnDepot/main/imageadmin/README.md")
	out := rec.Body.String()
	for _, want := range []string{
		"![a](https://raw.githubusercontent.com/Brian099/FnDepot/main/imageadmin/Preview/a.png)",
		"![b](https://x.com/abs.png)",
		`<img src="https://raw.githubusercontent.com/Brian099/FnDepot/main/imageadmin/sub/b.jpg">`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("补全缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "](Preview/a.png)") {
		t.Fatalf("相对路径未被替换:\n%s", out)
	}
	// blob 页 URL 也应归一化后补全（base 由调用方 normalizeReadmeURL 给出）
	rec2 := httptest.NewRecorder()
	serveReadme(rec2, []byte("![c](shot.png)"), "",
		normalizeReadmeURL("https://github.com/o/r/blob/main/README.md"))
	if !strings.Contains(rec2.Body.String(), "![c](https://raw.githubusercontent.com/o/r/main/shot.png)") {
		t.Fatalf("blob base 补全错误:\n%s", rec2.Body.String())
	}
}

func TestReadmeCandidateURLs(t *testing.T) {
	s := &Server{Mirrors: NewMirrorMonitor()}
	cands := s.readmeCandidateURLs("https://raw.githubusercontent.com/o/r/main/9router/README.md")
	joined := strings.Join(cands, "\n")
	for _, want := range []string{
		"https://raw.githubusercontent.com/o/r/main/9router/README.md",
		"https://raw.githubusercontent.com/o/r/master/9router/README.md",
		"https://cdn.jsdelivr.net/gh/o/r@main/9router/README.md",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("候选缺 %q:\n%s", want, joined)
		}
	}
	// 无重复
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c] {
			t.Fatalf("候选重复: %q", c)
		}
		seen[c] = true
	}
}

func TestRewriteRelImages(t *testing.T) {
	readme := "https://raw.githubusercontent.com/o/r/main/dir/README.md"
	in := "![a](shot.png)\n![b](sub/deep.png)\n![c](https://x.com/abs.png)\n![d](data:image/png;base64,AA)\n<img src=\"img/rel.jpg\">\n<img src=\"https://abs.com/i.png\">"
	out := string(rewriteRelImages([]byte(in), readme))
	for _, want := range []string{
		"![a](https://raw.githubusercontent.com/o/r/main/dir/shot.png)",
		"![b](https://raw.githubusercontent.com/o/r/main/dir/sub/deep.png)",
		"![c](https://x.com/abs.png)",
		"![d](data:image/png;base64,AA)",
		`<img src="https://raw.githubusercontent.com/o/r/main/dir/img/rel.jpg">`,
		`<img src="https://abs.com/i.png">`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q\n得到:\n%s", want, out)
		}
	}
}

// officialIconURL：面板静态地址直通；社区相对地址（源徽章统一后的合并条目）
// 在无面板时退回原值（由调用方走社区通道兜底）
func TestOfficialIconURL(t *testing.T) {
	s := &Server{}
	ai := AppInfo{AppName: "qBittorrent", IconURL: "/app-center-static/icon/qBittorrent/icon.png"}
	if got := s.officialIconURL(ai); got != "/app-center-static/icon/qBittorrent/icon.png" {
		t.Fatalf("面板静态地址被改写: %q", got)
	}
	ai2 := AppInfo{AppName: "qBittorrent", IconURL: "qBittorrent/ICON.PNG"}
	if got := s.officialIconURL(ai2); got != "qBittorrent/ICON.PNG" {
		t.Fatalf("无面板时应退回原值: %q", got)
	}
	ai3 := AppInfo{AppName: "x", IconURL: "https://appcenter-static.fnnas.com/appcenter/res/icon-x-1.0-0001"}
	if got := s.officialIconURL(ai3); got != ai3.IconURL {
		t.Fatalf("官方 CDN 绝对地址被改写: %q", got)
	}
}

func TestLooksLikeImage(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want bool
	}{
		{"png", append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0}, make([]byte, 10)...), true},
		{"png转码损坏", append([]byte{0xEF, 0xBF, 0xBD, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 10)...), false},
		{"jpeg", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 8)...), true},
		{"gif", []byte("GIF89a\x01\x02\x03\x04"), true},
		{"webp", append([]byte{'R', 'I', 'F', 'F', 1, 2, 3, 4, 'W', 'E', 'B', 'P'}, make([]byte, 8)...), true},
		{"bmp", append([]byte{0, 0, 1, 0, 1, 0}, make([]byte, 8)...), true},
		{"svg", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), true},
		{"readme文本", []byte("可全局调用Java。\n"), false},
		{"json错误体", []byte(`{"error":"not found"}`), false},
		{"空", []byte{}, false},
	}
	for _, c := range cases {
		if got := looksLikeImage(c.b); got != c.want {
			t.Errorf("%s: looksLikeImage = %v, want %v", c.name, got, c.want)
		}
	}
}
