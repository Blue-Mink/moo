package main

// 调试用：打印探测过程（文件数/命中/队列）。
import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	dbgAssetRef = regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.js`)
	dbgChunk    = regexp.MustCompile(`[A-Za-z0-9_-]+-[A-Za-z0-9_-]{6,}\.js`)
)

func debugProbe(base string) {
	hc := &http.Client{Timeout: 15 * time.Second}
	seen := map[string]bool{}
	var queue []string
	resp, err := hc.Get(base + "/signin")
	if err != nil {
		println("signin fetch err:", err)
		return
	}
	html, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	println("signin status:", resp.StatusCode, "len:", len(html))
	for _, u := range dbgAssetRef.FindAllString(string(html), -1) {
		if !seen[u] {
			seen[u] = true
			queue = append(queue, u)
		}
	}
	println("layer1 queue:", len(queue))
	fetched := 0
	for len(queue) > 0 && fetched < 60 {
		u := queue[0]
		queue = queue[1:]
		r2, err := hc.Get(base + u)
		if err != nil {
			println("fetch err", u, err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(r2.Body, 4<<20))
		r2.Body.Close()
		fetched++
		if r2.StatusCode != 200 {
			println("status", r2.StatusCode, u)
			continue
		}
		for _, m := range []string{"code_challenge", "YJNMPJUGA9"} {
			if strings.Contains(string(b), m) {
				println("MARKER HIT in", u, "marker:", m)
				return
			}
		}
		names := append(dbgAssetRef.FindAllString(string(b), -1),
			func() (out []string) {
				for _, nm := range dbgChunk.FindAllString(string(b), -1) {
					if !strings.Contains(nm, "/") {
						out = append(out, "/assets/"+nm)
					}
				}
				return
			}()...)
		added := 0
		for _, nxt := range names {
			if !seen[nxt] {
				seen[nxt] = true
				queue = append(queue, nxt)
				added++
			}
		}
		println("fetched", u, "len", len(b), "new:", added, "queue:", len(queue))
	}
	println("done, fetched:", fetched, "no marker")
}

var _ = context.Background
