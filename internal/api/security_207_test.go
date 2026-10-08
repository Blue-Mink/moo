package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
)

// ── 0.6.207-panel 安全修复单测（D1 sha256 / D2 头校验 / D4 body 上限 / 残留清理）──

// TestCleanResidualCredentials 模式删除 + 备份边界（明文删/密文留/无字段留/坏 JSON 留）。
// 0.6.312：official_session.json 是现役 OAuth 会话文件，断言从「应删」改为「应留」。
func TestCleanResidualCredentials(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config.json", `{"panel_password":"enc:v1:abc"}`)
	write("config.json.bak-old", `{"panel_password":"root"}`)
	write("dc_session.json.bak-removed", "x")
	write("official_session.json", `{"token":"t"}`)
	write("official_session.json.bak", `{"token":"t"}`)
	write("backups/moo-backup-flat.json", `{"panel_password":"root"}`)
	write("backups/moo-backup-nested.json", `{"backup_time":"x","version":"0.6.205","config":{"panel_password":"root"}}`)
	write("backups/moo-backup-enc.json", `{"config":{"panel_password":"enc:v1:secret"}}`)
	write("backups/moo-backup-empty.json", `{"config":{}}`)
	write("backups/moo-backup-broken.json", `{not json`)
	write("downloads/keep.fpk", "binary")
	write(".panel-key", "key")

	removed := CleanResidualCredentials(dir)
	got := map[string]bool{}
	for _, n := range removed {
		got[n] = true
	}
	for _, want := range []string{
		"config.json.bak-old", "dc_session.json.bak-removed",
		"moo-backup-flat.json", "moo-backup-nested.json",
	} {
		if !got[want] {
			t.Errorf("应删除 %s, removed=%v", want, removed)
		}
	}
	if len(removed) != 4 {
		t.Errorf("应恰好删 4 个, got %d: %v", len(removed), removed)
	}
	for _, keep := range []string{
		"config.json",
		// 0.6.312：现役 OAuth 会话文件，启动清理不得误删
		"official_session.json", "official_session.json.bak",
		"backups/moo-backup-enc.json", "backups/moo-backup-empty.json", "backups/moo-backup-broken.json",
		"downloads/keep.fpk", ".panel-key",
	} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("不应删除 %s: %v", keep, err)
		}
	}
}

// TestVerifySha256 匹配通过 / 不匹配报「sha256 不匹配」/ 404 报错。
func TestVerifySha256(t *testing.T) {
	fp := filepath.Join(t.TempDir(), "x.fpk")
	payload := bytes.Repeat([]byte("moo-fpk-data "), 4096)
	if err := os.WriteFile(fp, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.sha256":
			fmt.Fprint(w, good+"\t moo_x86.fpk\n")
		case "/bad.sha256":
			fmt.Fprint(w, strings.Repeat("0", 64))
		case "/missing.sha256":
			http.Error(w, "nf", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := srv.Client()

	if err := verifySha256(t.Context(), client, srv.URL+"/good.sha256", fp); err != nil {
		t.Fatalf("匹配应通过: %v", err)
	}
	err := verifySha256(t.Context(), client, srv.URL+"/bad.sha256", fp)
	if err == nil || !strings.Contains(err.Error(), "sha256 不匹配") {
		t.Fatalf("不匹配应报 sha256 不匹配, got %v", err)
	}
	if err := verifySha256(t.Context(), client, srv.URL+"/missing.sha256", fp); err == nil {
		t.Fatal("404 应报错")
	}
}

// doSec 可信通道请求（网关 socket 模拟 + 可选 X-Moo-Admin 头）。
func doSec(t *testing.T, s *Server, method, path, body string, withHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, GatewayPrefix+path, strings.NewReader(body)).WithContext(t.Context())
	r.Header.Set("X-Trim-Username", "fnos")
	r.Header.Set("X-Trim-Userid", "1000")
	r.Header.Set("X-Trim-Isadmin", "true")
	if withHeader {
		r.Header.Set("X-Moo-Admin", "1")
	}
	rec := httptest.NewRecorder()
	s.Handler(true).ServeHTTP(rec, r)
	return rec
}

// TestD2AdminHeader 非 GET 缺头 → 400 缺头错误；带头 → 越过头门；GET 豁免。
func TestD2AdminHeader(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: &config.Config{}}

	rec := doSec(t, s, "POST", "/api/notify-log", "{}", false)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "X-Moo-Admin") {
		t.Fatalf("POST 缺头应 400+缺头提示, got %d %s", rec.Code, rec.Body.String())
	}
	rec = doSec(t, s, "POST", "/api/notify-log", "{}", true)
	if strings.Contains(rec.Body.String(), "缺少请求校验头") {
		t.Fatalf("带头 POST 不应被头门拦截, got %s", rec.Body.String())
	}
	rec = doSec(t, s, "GET", "/api/notify-log", "", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应豁免头检查, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestD4JSONBodyLimit 超 10MB 的合法 JSON 必须被 MaxBytesReader 拒；限内可解码。
func TestD4JSONBodyLimit(t *testing.T) {
	build := func(size int) []byte {
		head, tail := []byte(`{"x":"`), []byte(`"}`)
		b := make([]byte, size)
		copy(b, head)
		for i := len(head); i < size-len(tail); i++ {
			b[i] = 'A'
		}
		copy(b[size-len(tail):], tail)
		return b
	}
	var v map[string]any
	req := httptest.NewRequest("POST", "/api/x", bytes.NewReader(build(11<<20)))
	if err := jsonDecode(req, &v); err == nil {
		t.Fatal("11MB body 应超上限报错")
	}
	req = httptest.NewRequest("POST", "/api/x", bytes.NewReader(build(9<<20)))
	if err := jsonDecode(req, &v); err != nil {
		t.Fatalf("9MB body 应在限内解码, got %v", err)
	}
}
