package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 审计 MEDIUM-2：短串不得 panic（第三方源写 icon_url:"x" 会走到这里）。
func TestUnwrapMirrorURL_ShortInput_326(t *testing.T) {
	defer func() {
		if e := recover(); e != nil {
			t.Fatalf("panic: %v", e)
		}
	}()
	for _, in := range []string{"", "x", "abc", "https://", "https://a/b"} {
		if got := unwrapMirrorURL(in); got != "" {
			t.Fatalf("unwrapMirrorURL(%q) = %q, want empty", in, got)
		}
	}
}

// 审计 MEDIUM-1：自动发现路径不挂镜像候选（镜像变体形如
// "<镜像前缀>https://…"，出现两个 https:// 即判为镜像变体）。
func TestReadmeCandidateURLsMirrorToggle_326(t *testing.T) {
	s := &Server{Mirrors: NewMirrorMonitor()}
	raw := "https://raw.githubusercontent.com/o/r/main/app/README.md"
	plain := s.readmeCandidateURLs(raw, false)
	withM := s.readmeCandidateURLs(raw, true)
	if len(plain) == 0 {
		t.Fatal("no candidates")
	}
	for _, u := range plain {
		if strings.Count(strings.ToLower(u), "https://") > 1 {
			t.Fatalf("自动发现不应含镜像变体: %s", u)
		}
	}
	if len(withM) < len(plain) {
		t.Fatalf("声明路径候选不应少于自动路径: %d < %d", len(withM), len(plain))
	}
}

// 审计 MEDIUM-3 的判定基础：404=dead、5xx=瞬态（非 dead）、200=served。
func TestServeReadmeURLDeadSemantics_326(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404":
			http.NotFound(w, r)
		case "/500":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			_, _ = w.Write([]byte("# hi\n"))
		}
	}))
	defer srv.Close()
	s := &Server{}
	ctx := context.Background()
	req := httptest.NewRequest(http.MethodGet, "/asset", nil).WithContext(ctx)

	rec := httptest.NewRecorder()
	if served, dead := s.serveReadmeURL(rec, req, srv.URL+"/404", false); served || !dead {
		t.Fatalf("404 应为 dead: served=%v dead=%v", served, dead)
	}
	rec = httptest.NewRecorder()
	if served, dead := s.serveReadmeURL(rec, req, srv.URL+"/500", false); served || dead {
		t.Fatalf("5xx 应属瞬态: served=%v dead=%v", served, dead)
	}
	rec = httptest.NewRecorder()
	if served, dead := s.serveReadmeURL(rec, req, srv.URL+"/README.md", false); !served || dead {
		t.Fatalf("200 应 served: served=%v dead=%v", served, dead)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("200 未写出正文")
	}
}
