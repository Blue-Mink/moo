package api

// 0.6.299 自更新探测兜底/限流/强制重探单测。
// 背景：匿名 GitHub API 按出口 IP 限 60/h，国内 NAT 共享出口常被耗光 →
// 探测恒 403 → 设置页版本 chip 无红点且误报「已是最新版本」。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"moo/internal/config"
)

// setProbeBases 覆写探测入口（api/web/fnDepot 源），返回还原函数。
func setProbeBases(api, web, fd string) func() {
	oldAPI, oldWeb, oldFD := selfUpdateAPIBase, selfUpdateWebBase, selfUpdateFnDepotURL
	selfUpdateAPIBase = api
	selfUpdateWebBase = web
	selfUpdateFnDepotURL = fd
	return func() {
		selfUpdateAPIBase, selfUpdateWebBase, selfUpdateFnDepotURL = oldAPI, oldWeb, oldFD
	}
}

// 压制探测成功后的 store_update_available 通知（空 Server 无通知基建）。
func suppressProbeNotify(t *testing.T) {
	t.Helper()
	t.Setenv("MOO_SELFUPDATE_AS_VERSION", "99.99.99")
}

func TestDoProbeAPI200Baseline(t *testing.T) {
	suppressProbeNotify(t)
	var apiHits int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
		if r.URL.Path != "/repos/Blue-Mink/moo/releases/latest" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","assets":[` +
			`{"name":"moo_9.9.9_x86.fpk","browser_download_url":"https://example.invalid/dl/moo_9.9.9_x86.fpk"},` +
			`{"name":"moo_9.9.9_x86.fpk.sha256","browser_download_url":"https://example.invalid/dl/moo_9.9.9_x86.fpk.sha256"}]}`))
	}))
	defer apiSrv.Close()
	restore := setProbeBases(apiSrv.URL, "https://should-not-be-called.invalid", "https://should-not-be-called.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, assetURL, asset, shaURL, rl, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe: %v", err)
	}
	if tag != "9.9.9" || asset != "moo_9.9.9_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
	if !strings.Contains(assetURL, "moo_9.9.9_x86.fpk") {
		t.Fatalf("assetURL = %q", assetURL)
	}
	if !strings.HasSuffix(shaURL, ".sha256") {
		t.Fatalf("shaURL = %q", shaURL)
	}
	if !rl.IsZero() {
		t.Fatalf("ratelimitUntil = %v, want zero", rl)
	}
	if got := atomic.LoadInt32(&apiHits); got != 1 {
		t.Fatalf("apiHits = %d, want 1", got)
	}
}

func TestDoProbeFallbackOn403Ratelimit(t *testing.T) {
	suppressProbeNotify(t)
	reset := time.Now().Add(2 * time.Hour).Unix()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-reset", strconv.FormatInt(reset, 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Blue-Mink/moo/releases/latest" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/Blue-Mink/moo/releases/tag/v7.7.7", http.StatusFound)
	}))
	defer webSrv.Close()
	restore := setProbeBases(apiSrv.URL, webSrv.URL, "https://should-not-be-called.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, assetURL, asset, shaURL, rl, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe (fallback): %v", err)
	}
	if tag != "7.7.7" || asset != "moo_7.7.7_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
	wantURL := webSrv.URL + "/Blue-Mink/moo/releases/download/v7.7.7/moo_7.7.7_x86.fpk"
	if assetURL != wantURL {
		t.Fatalf("assetURL = %q, want %q", assetURL, wantURL)
	}
	if shaURL != "" {
		t.Fatalf("shaURL = %q, want empty（web 通道无侧车 → 降级结构校验）", shaURL)
	}
	if rl.IsZero() || !rl.After(time.Now()) {
		t.Fatalf("ratelimitUntil = %v, want future", rl)
	}
}

func TestDoProbeAllChannelsFail(t *testing.T) {
	suppressProbeNotify(t)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer apiSrv.Close()
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer none.Close()
	restore := setProbeBases(apiSrv.URL, none.URL, none.URL)
	defer restore()

	s := &Server{Cfg: config.Default()}
	_, _, _, _, rl, err := s.doProbe()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500") ||
		!strings.Contains(err.Error(), "web 兜底") ||
		!strings.Contains(err.Error(), "FnDepot 源兜底") ||
		!strings.Contains(err.Error(), "镜像兜底") {
		t.Fatalf("err = %v, want all channels reported", err)
	}
	if !rl.IsZero() {
		t.Fatalf("ratelimitUntil = %v, want zero（非 403 不置限流窗）", rl)
	}
}

// API 403 + web 404 + FnDepot 直连 404 → 镜像兜底命中（0.6.300）。
func TestDoProbeFallbackOnMirror(t *testing.T) {
	suppressProbeNotify(t)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer none.Close()
	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"version":"0.6.296"}}}`))
	}))
	defer mirrorSrv.Close()
	restore := setProbeBases(apiSrv.URL, none.URL, none.URL)
	defer restore()
	oldPrefixes := selfUpdateMirrorPrefixes
	selfUpdateMirrorPrefixes = func(*Server) []string {
		return []string{mirrorSrv.URL + "/", mirrorSrv.URL + "/second/"}
	}
	defer func() { selfUpdateMirrorPrefixes = oldPrefixes }()

	s := &Server{Cfg: config.Default()}
	tag, _, asset, _, rl, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe (mirror fallback): %v", err)
	}
	if tag != "0.6.296" || asset != "moo_0.6.296_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
	if !rl.IsZero() {
		t.Fatalf("ratelimitUntil = %v, want zero", rl)
	}
}

// API 403 + web 404（moo 仓负缓存场景）→ FnDepot 源兜底命中。
func TestDoProbeFallbackOnFnDepot(t *testing.T) {
	suppressProbeNotify(t)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer none.Close()
	fdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"version":"0.6.297"}}}`))
	}))
	defer fdSrv.Close()
	restore := setProbeBases(apiSrv.URL, none.URL, fdSrv.URL)
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, assetURL, asset, shaURL, rl, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe (fnDepot fallback): %v", err)
	}
	if tag != "0.6.297" || asset != "moo_0.6.297_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
	if want := none.URL + "/Blue-Mink/moo/releases/download/v0.6.297/moo_0.6.297_x86.fpk"; assetURL != want {
		// 资产 URL 用 web base（none.URL 此处仅为 web 通道占位，应指向 webBase）
		t.Fatalf("assetURL = %q, want %q", assetURL, want)
	}
	if shaURL != "" || !rl.IsZero() {
		t.Fatalf("shaURL=%q rl=%v, want empty/zero", shaURL, rl)
	}
}

func TestProbeWebLatest(t *testing.T) {
	suppressProbeNotify(t)
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Blue-Mink/moo/releases/tag/v1.2.3", http.StatusMovedPermanently)
	}))
	defer webSrv.Close()
	restore := setProbeBases("https://api.invalid", webSrv.URL, "https://fd.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	tag, err := s.probeWebLatest()
	if err != nil || tag != "1.2.3" {
		t.Fatalf("probeWebLatest = %q, %v", tag, err)
	}

	web404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer web404.Close()
	restore2 := setProbeBases("https://api.invalid", web404.URL, "https://fd.invalid")
	defer restore2()
	if _, err := s.probeWebLatest(); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}

func TestProbeFnDepotLatest(t *testing.T) {
	suppressProbeNotify(t)
	fdSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"version":"0.6.296"}}}`))
	}))
	defer fdSrv.Close()
	oldFD := selfUpdateFnDepotURL
	selfUpdateFnDepotURL = fdSrv.URL
	defer func() { selfUpdateFnDepotURL = oldFD }()

	s := &Server{Cfg: config.Default()}
	tag, err := s.probeFnDepotLatest()
	if err != nil || tag != "0.6.296" {
		t.Fatalf("probeFnDepotLatest = %q, %v", tag, err)
	}

	fd404 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer fd404.Close()
	selfUpdateFnDepotURL = fd404.URL
	if _, err := s.probeFnDepotLatest(); err == nil {
		t.Fatal("want error on 404, got nil")
	}

	fdBad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"apps":{}}`))
	}))
	defer fdBad.Close()
	selfUpdateFnDepotURL = fdBad.URL
	if _, err := s.probeFnDepotLatest(); err == nil {
		t.Fatal("want error on missing moo entry, got nil")
	}
}

// 红点态端到端：API 403（无限流窗）→ web 兜底命中 v0.6.298，
// 当前版本（env 覆写 0.6.283）落后 → has_update=true + available_version。
func TestStoreUpdateInfoRedPillViaFallback(t *testing.T) {
	t.Setenv("MOO_SELFUPDATE_AS_VERSION", "0.6.283")
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/Blue-Mink/moo/releases/tag/v0.6.298", http.StatusFound)
	}))
	defer webSrv.Close()
	restore := setProbeBases(apiSrv.URL, webSrv.URL, "https://fd.invalid")
	defer restore()

	cfg := config.Default()
	cfg.NotifyEvents = map[string]bool{"store_update_available": false}
	s := &Server{Cfg: cfg}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/store-update?force=1", nil)
	s.storeUpdateInfo(rr, req)

	var out map[string]any
	if uerr := json.Unmarshal(rr.Body.Bytes(), &out); uerr != nil {
		t.Fatalf("body = %s, %v", rr.Body.String(), uerr)
	}
	if out["current_version"] != "0.6.283" {
		t.Fatalf("current_version = %v", out["current_version"])
	}
	if out["has_update"] != true {
		t.Fatalf("has_update = %v, want true（红点态）", out["has_update"])
	}
	if out["available_version"] != "0.6.298" {
		t.Fatalf("available_version = %v", out["available_version"])
	}
	if _, ok := out["last_error"]; ok {
		t.Fatalf("last_error 不应出现: %v", out["last_error"])
	}
}

func TestProbeRatelimitWindowShortCircuits(t *testing.T) {
	suppressProbeNotify(t)
	var hits int32
	handlers := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			if code == http.StatusOK {
				_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"moo_1.0.0_x86.fpk","browser_download_url":"https://example.invalid/x.fpk"}]}`))
				return
			}
			http.Error(w, "e", code)
		}
	}
	apiSrv := httptest.NewServer(handlers(http.StatusOK))
	defer apiSrv.Close()
	webSrv := httptest.NewServer(handlers(http.StatusFound))
	defer webSrv.Close()
	restore := setProbeBases(apiSrv.URL, webSrv.URL, "https://fd.invalid")
	defer restore()

	s := &Server{Cfg: config.Default()}
	p := &selfUpdateProbe{
		srv:            s,
		latestTag:      "0.0.0",
		ratelimitUntil: time.Now().Add(10 * time.Minute),
	}
	// 限流窗内 probe(false)：回缓存、零网络
	tag, _, _, _, err := p.probe(false)
	if err != nil || tag != "0.0.0" {
		t.Fatalf("cached probe = %q, %v", tag, err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("hits = %d, want 0（限流窗内不打网络）", got)
	}
	// force 绕过限流窗
	if _, _, _, _, err := p.probe(true); err != nil {
		t.Fatalf("force probe: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
}
