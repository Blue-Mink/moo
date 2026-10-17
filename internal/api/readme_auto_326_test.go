package api

import (
	"testing"

	"moo/internal/source"
)

func TestGithubRepoFromURLs_326(t *testing.T) {
	if r, b := githubRepoFromURLs("https://github.com/o/r/releases/download/v1/x.fpk"); r != "o/r" || b != "" {
		t.Fatalf("release url: %q %q", r, b)
	}
	if r, b := githubRepoFromURLs("https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/x/ICON.PNG"); r != "o/r" || b != "main" {
		t.Fatalf("mirror raw: %q %q", r, b)
	}
	if r, _ := githubRepoFromURLs("https://cdn.jsdelivr.net/gh/o/r@main/x.json"); r != "" {
		t.Fatalf("非 github 主机不应反推: %q", r)
	}
}

func TestReadmeAutoCandidates_326(t *testing.T) {
	s := &Server{}
	a := &source.App{Name: "demo", DownloadURL: "https://github.com/o/r/releases/download/v1/a.fpk"}
	cands := s.readmeAutoCandidates(a)
	if len(cands) < 2 {
		t.Fatalf("应至少 main/master 两个候选: %v", cands)
	}
	if cands[0] != "https://raw.githubusercontent.com/o/r/main/demo/README.md" {
		t.Fatalf("首个候选不对: %v", cands)
	}
	// 已声明 README 时不启用自动发现（由调用方判断）——builder 仍返回候选，
	// 这里只验证分支顺序：源地址带分支时该分支优先。
	b := &source.App{Name: "demo", ReadmeURL: "https://raw.githubusercontent.com/o/r/dev/demo/README.md"}
	c2 := s.readmeAutoCandidates(b)
	if c2[0] != "https://raw.githubusercontent.com/o/r/dev/demo/README.md" {
		t.Fatalf("应优先声明分支: %v", c2)
	}
}
