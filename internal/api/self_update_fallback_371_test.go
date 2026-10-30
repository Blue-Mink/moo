package api

// 0.6.371 自更新兜底扩展单测：
//  1) moo.json 自索引（直连 + 加速镜像）接入探测兜底链，且侧车 URL 齐全；
//  2) 下载阶段直连失败时改用加速镜像，每个通道都跑完整签名 + 哈希校验。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"aead.dev/minisign"

	"moo/internal/config"
)

func setSelfIndexBase(u string) func() {
	old := selfUpdateSelfIndexURL
	selfUpdateSelfIndexURL = u
	return func() { selfUpdateSelfIndexURL = old }
}

// releases 键在 JSON 里无序 → 顶层 version 缺失时必须取最大版本，不能取首个。
func TestFetchMooIndexVersionPicksMaxRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"releases":{"0.6.369":{},"0.6.371":{},"0.6.370":{}}}}}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := fetchMooIndexVersion(ctx, srv.URL)
	if err != nil {
		t.Fatalf("fetchMooIndexVersion: %v", err)
	}
	if v != "0.6.371" {
		t.Fatalf("version = %q, want 0.6.371（须取最大版本）", v)
	}
}

// API 失败 + web 404 + FnDepot 不可用 → moo.json 自索引兜底命中。
func TestDoProbeFallbackOnSelfIndex(t *testing.T) {
	suppressProbeNotify(t)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer apiSrv.Close()
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer none.Close()
	idxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"version":"0.6.380"}}}`))
	}))
	defer idxSrv.Close()

	restore := setProbeBases(apiSrv.URL, none.URL, none.URL)
	defer restore()
	restoreIdx := setSelfIndexBase(idxSrv.URL)
	defer restoreIdx()
	oldPrefixes := selfUpdateMirrorPrefixes
	selfUpdateMirrorPrefixes = func(*Server) []string { return nil }
	defer func() { selfUpdateMirrorPrefixes = oldPrefixes }()

	s := &Server{Cfg: config.Default()}
	tag, assetURL, asset, shaURL, minisigURL, _, _, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe (self index fallback): %v", err)
	}
	if tag != "0.6.380" || asset != "moo_0.6.380_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
	// 资产 URL 仍走 web base（none.URL 此处仅占位；实际由 selfUpdateWebBase 拼）
	if !strings.HasSuffix(assetURL, "/releases/download/v0.6.380/"+asset) {
		t.Fatalf("assetURL = %q", assetURL)
	}
	if shaURL != assetURL+".sha256" || minisigURL != assetURL+".minisig" {
		t.Fatalf("sidecars = %q / %q", shaURL, minisigURL)
	}
}

// moo.json 直连 404 → 索引镜像兜底命中（国内 raw 常整段断流）。
func TestDoProbeFallbackOnSelfIndexMirror(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"schema_version":"moo","apps":{"moo":{"version":"0.6.381"}}}`))
	}))
	defer mirrorSrv.Close()

	restore := setProbeBases(apiSrv.URL, none.URL, none.URL)
	defer restore()
	restoreIdx := setSelfIndexBase(none.URL) // 直连索引 404
	defer restoreIdx()
	oldPrefixes := selfUpdateMirrorPrefixes
	selfUpdateMirrorPrefixes = func(*Server) []string { return []string{mirrorSrv.URL + "/"} }
	defer func() { selfUpdateMirrorPrefixes = oldPrefixes }()

	s := &Server{Cfg: config.Default()}
	tag, _, asset, _, _, _, _, err := s.doProbe()
	if err != nil {
		t.Fatalf("doProbe (self index mirror fallback): %v", err)
	}
	if tag != "0.6.381" || asset != "moo_0.6.381_x86.fpk" {
		t.Fatalf("tag/asset = %q/%q", tag, asset)
	}
}

// 直连下载失败 → 加速镜像候选下载并通过签名 + 哈希校验。
// 反向验证锚点：去掉 selfUpdateDownloadCandidates 的镜像候选后此用例必 FAIL。
func TestDownloadSelfUpdateFPKFallsBackToMirror(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	payload := bytes.Repeat([]byte("F"), 2<<20)
	sum := sha256.Sum256(payload)
	sig := minisign.Sign(priv, payload)

	const fakeBase = "https://github.invalid/dl/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, "/dl/") {
		case "moo_9.9.9_x86.fpk":
			_, _ = w.Write(payload)
		case "moo_9.9.9_x86.fpk.sha256":
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  moo_9.9.9_x86.fpk\n"))
		case "moo_9.9.9_x86.fpk.minisig":
			_, _ = w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	oldPub := selfUpdatePubkey
	selfUpdatePubkey = pub
	defer func() { selfUpdatePubkey = oldPub }()
	oldPrefixes := selfUpdateMirrorPrefixes
	selfUpdateMirrorPrefixes = func(*Server) []string { return []string{srv.URL + "/dl/"} }
	defer func() { selfUpdateMirrorPrefixes = oldPrefixes }()
	oldRW := selfUpdateMirrorRewrite
	selfUpdateMirrorRewrite = func(prefix, u string) string { return prefix + strings.TrimPrefix(u, fakeBase) }
	defer func() { selfUpdateMirrorRewrite = oldRW }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s := &Server{Cfg: config.Default()}
	rec := httptest.NewRecorder()
	assetURL := fakeBase + "moo_9.9.9_x86.fpk"
	path, err := s.downloadSelfUpdateFPK(rec, rec, ctx, assetURL, assetURL+".sha256", assetURL+".minisig", "9.9.9")
	if err != nil {
		t.Fatalf("downloadSelfUpdateFPK (mirror fallback): %v", err)
	}
	defer os.Remove(path)
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read downloaded: %v", rerr)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("下载内容与镜像源不一致（%d vs %d 字节）", len(data), len(payload))
	}
}
