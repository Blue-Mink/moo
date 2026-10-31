package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// 0.6.382 回归：镜像的 404 只在声明地址是 raw 直链时才算「源缺失」票。
// 反向验证：把 deadVoteEligible 改回「非 jsDelivr 一律算票」→ 本用例必红。
func TestDeadVoteEligible(t *testing.T) {
	mir := "https://gh-proxy.com/"
	cases := []struct {
		name  string
		u     string
		cands []string
		want  bool
	}{
		{"声明候选本身（非 raw）", "https://github.com/user-attachments/assets/a.png",
			[]string{"https://github.com/user-attachments/assets/a.png"}, true},
		{"非镜像候选（源真身）", "https://user-images.githubusercontent.com/1/a.png",
			[]string{"https://raw.githubusercontent.com/o/r/main/a.png"}, true},
		{"镜像候选 + 非 raw 声明", mir + "https://github.com/user-attachments/assets/a.png",
			[]string{"https://github.com/user-attachments/assets/a.png"}, false},
		{"镜像候选 + raw 声明（1:1 语义）", mir + "https://raw.githubusercontent.com/o/r/main/a.png",
			[]string{"https://raw.githubusercontent.com/o/r/main/a.png"}, true},
		{"空候选表", "https://github.com/x.png", nil, true},
	}
	for _, c := range cases {
		if got := deadVoteEligible(c.u, c.cands); got != c.want {
			t.Fatalf("%s: deadVoteEligible=%v want %v", c.name, got, c.want)
		}
	}
}

func TestRaceFetch_MirrorOnlyVotesNotDead(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer slow.Close()
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer m1.Close()
	m2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer m2.Close()

	old := mirrorPrefixesForVote
	mirrorPrefixesForVote = func() []string { return []string{m1.URL, m2.URL} }
	defer func() { mirrorPrefixesForVote = old }()

	s := &Server{Mirrors: NewMirrorMonitor()}
	decl := slow.URL + "/user-attachments/assets/x.png"
	cands := []string{decl, m1.URL + "/" + decl, m2.URL + "/" + decl}
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, _, dead, _, err := s.raceFetch(ctx, cands)
	if dead {
		t.Fatalf("两条镜像 404 + 声明候选挂住，不得判死（err=%v）", err)
	}
}

// 声明候选自己 404（另有镜像也 404）→ 仍要判死，避免真缺失退化成 502。
func TestRaceFetch_Declared404StillDead(t *testing.T) {
	d := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer d.Close()
	m1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer m1.Close()

	old := mirrorPrefixesForVote
	mirrorPrefixesForVote = func() []string { return []string{m1.URL} }
	defer func() { mirrorPrefixesForVote = old }()

	s := &Server{Mirrors: NewMirrorMonitor()}
	decl := d.URL + "/user-attachments/assets/x.png"
	cands := []string{decl, m1.URL + "/" + decl}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, _, dead, _, err := s.raceFetch(ctx, cands)
	if !dead {
		t.Fatalf("声明候选明确 404 应判死（err=%v）", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("判死不应等满预算，实际 %v", time.Since(start))
	}
}

func TestGitHubUserAssetURL(t *testing.T) {
	cases := []struct {
		u    string
		want bool
	}{
		{"https://github.com/user-attachments/assets/abc", true},
		{"https://github.com/o/r/assets/123/abc", true},
		{"https://user-images.githubusercontent.com/1/a.png", true},
		{"https://github.com/o/r/releases/download/v1/a.png", false},
		{"https://github.com/o/r/raw/main/a.png", false},
		{"https://raw.githubusercontent.com/o/r/main/a.png", false},
		{"https://camo.githubusercontent.com/abc", false},
	}
	for _, c := range cases {
		pu, _ := url.Parse(c.u)
		if got := githubUserAssetURL(pu); got != c.want {
			t.Fatalf("%s: githubUserAssetURL=%v want %v", c.u, got, c.want)
		}
	}
}
