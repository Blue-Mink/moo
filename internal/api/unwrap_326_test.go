package api

import "testing"

func TestUnwrapMirrorURL_326(t *testing.T) {
	in := "https://gh-proxy.com/https://raw.githubusercontent.com/a/b/main/x.PNG"
	want := "https://raw.githubusercontent.com/a/b/main/x.PNG"
	if got := unwrapMirrorURL(in); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got := unwrapMirrorURL("https://raw.githubusercontent.com/a/b/main/x.PNG"); got != "" {
		t.Fatalf("plain raw 不应剥: %q", got)
	}
}
