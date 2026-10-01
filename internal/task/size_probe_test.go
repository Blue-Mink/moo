package task

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeContentLengthHead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.Header().Set("Content-Length", "123456")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if got := ProbeContentLength([]string{srv.URL}); got != 123456 {
		t.Fatalf("ProbeContentLength = %d, want 123456", got)
	}
}

func TestProbeContentLength405RangeFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("expected Range: bytes=0-0, got %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 0-0/777888")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer srv.Close()

	if got := ProbeContentLength([]string{srv.URL}); got != 777888 {
		t.Fatalf("ProbeContentLength (405→Range) = %d, want 777888", got)
	}
}

func TestProbeContentLengthAllFail(t *testing.T) {
	if got := ProbeContentLength([]string{"http://127.0.0.1:1/x.fpk", ""}); got != 0 {
		t.Fatalf("ProbeContentLength = %d, want 0 (all candidates failed)", got)
	}
}

func TestProbeContentLengthFirstHealthyWins(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "42")
		w.WriteHeader(http.StatusOK)
	}))
	defer good.Close()

	if got := ProbeContentLength([]string{bad.URL, good.URL}); got != 42 {
		t.Fatalf("ProbeContentLength = %d, want 42 (skip bad, take first good)", got)
	}
}
