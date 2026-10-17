package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndexNameRank_326(t *testing.T) {
	cases := []struct {
		u    string
		want int
	}{
		{"https://raw.githubusercontent.com/o/r/main/moo.json", 0},
		{"https://gh-proxy.com/https://raw.githubusercontent.com/o/r/main/moo.json", 0},
		{"https://example.com/path/my-own-index.json", 0}, // 用户直链：显式优先
		{"https://raw.githubusercontent.com/o/r/main/fnpack.json", 1},
		{"https://raw.githubusercontent.com/o/r/main/fndepot.json", 2},
		{"https://raw.githubusercontent.com/o/r/main/fndepot_v2.json", 3},
		{"https://raw.githubusercontent.com/o/r/main/apps.json", 4},
	}
	for _, c := range cases {
		if got := indexNameRank(c.u); got != c.want {
			t.Errorf("indexNameRank(%q) = %d, want %d", c.u, got, c.want)
		}
	}
}

// 首名（moo.json）在窗口内返回 → 即使 fnpack 更快也不该被抢先。
func TestRaceFetchOrdered_FirstWaveWins_326(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/repo/") {
		case "moo.json":
			time.Sleep(120 * time.Millisecond)
			_, _ = w.Write([]byte(`{"apps":{}}`))
		case "fnpack.json":
			_, _ = w.Write([]byte(`{"apps":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cands := []string{srv.URL + "/repo/moo.json", srv.URL + "/repo/fnpack.json"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, winner, err := raceFetchOrdered(cands, 600*time.Millisecond, ctx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(winner, "/moo.json") {
		t.Fatalf("首名优先失效，winner = %s", winner)
	}
}

// 首名慢于窗口 → 放开其余候选，fnpack 先回即胜（不让窗口拖住热路径）。
func TestRaceFetchOrdered_FastSecondWins_326(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/repo/") {
		case "moo.json":
			time.Sleep(1500 * time.Millisecond)
			_, _ = w.Write([]byte(`{"apps":{}}`))
		case "fnpack.json":
			_, _ = w.Write([]byte(`{"apps":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cands := []string{srv.URL + "/repo/moo.json", srv.URL + "/repo/fnpack.json"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, winner, err := raceFetchOrdered(cands, 200*time.Millisecond, ctx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(winner, "/fnpack.json") {
		t.Fatalf("慢首名挡住了次名，winner = %s", winner)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("等待过久（%v），窗口放开逻辑有问题", d)
	}
}

// 首组快速 404 → 立即放开其余候选，不等窗口。
func TestRaceFetchOrdered_First404ReleasesRest_326(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/moo.json") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"apps":{}}`))
	}))
	defer srv.Close()

	cands := []string{srv.URL + "/moo.json", srv.URL + "/fnpack.json"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, winner, err := raceFetchOrdered(cands, 3*time.Second, ctx) // 窗口远大于耗时
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.HasSuffix(winner, "/fnpack.json") {
		t.Fatalf("winner = %s", winner)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("首组 404 后没有立即放开（%v）", d)
	}
}
