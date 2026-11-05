package main

// 0.2.54（B1–B4 / MM-01a / MM-05 / MM-06）单测。

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B1：nosniff 必须对所有响应生效（含被拒请求）。
func TestNoSniffHeaderOnAllResponses(t *testing.T) {
	h, _ := csrfTestHandler(csrfTestPolicy(true))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, csrfReq(http.MethodGet, "/api/status", ""))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET 应 200 且带 nosniff，得 %d %q", rec.Code, rec.Header().Get("X-Content-Type-Options"))
	}

	r := csrfReq(http.MethodPost, "/api/clear-data", "{}")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r)
	if rec2.Code != http.StatusForbidden || rec2.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("被拒请求也应 403 且带 nosniff，得 %d %q", rec2.Code, rec2.Header().Get("X-Content-Type-Options"))
	}
}

// B2：只认 /volN。
func TestIsVolumePath(t *testing.T) {
	yes := []string{"/vol1", "/vol2", "/vol12", "/vol1/", "/vol1/a/b", "/vol8/x"}
	no := []string{"/vol", "/volume", "/volx", "/vol0", "/vol1abc", "/volume1", "/tmp/vol1", "/Vol1"}
	for _, p := range yes {
		if !isVolumePath(p) {
			t.Fatalf("%q 应判为卷路径", p)
		}
	}
	for _, p := range no {
		if isVolumePath(p) {
			t.Fatalf("%q 不应判为卷路径", p)
		}
	}
}

func TestHandleDirsRejectsLookalikeVolumePath(t *testing.T) {
	c := &companion{cfg: &mateConfig{}, idx: &index{path: "/tmp/mate-test/index.json"}}
	for _, p := range []string{"/volume", "/volx", "/vol", "/vol0", "/volume1"} {
		req := httptest.NewRequest(http.MethodGet, "/api/dirs?path="+url.QueryEscape(p), nil)
		rec := httptest.NewRecorder()
		c.handleDirs(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%q 应 400，得 %d", p, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/dirs?path=/vol1", nil)
	rec := httptest.NewRecorder()
	c.handleDirs(rec, req)
	// /vol1 的 200 依赖宿主机真实存在该目录（发布机有、CI runner 没有）：
	// 白名单只负责「不把合法卷根误判成非法」，存在性交给 404 分支。
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("/vol1 不应被卷根白名单拒绝（400 说明 isVolumePath 误判）")
	}
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Fatalf("/vol1 期望 200/404（存在/不存在），得 %d", rec.Code)
	}
}

// B3：含引号的文件名必须仍是合法头（裸拼接会得到无法解析的头）。
func TestContentDispositionSafeForQuotedNames(t *testing.T) {
	got := contentDisposition(`a"b.tar.gz`)
	if strings.Contains(got, `filename="a"b`) { // 裸拼接的典型形态
		t.Fatalf("不得用裸拼接（会拼出坏头）：%q", got)
	}
	mt, params, err := mime.ParseMediaType(got)
	if err != nil {
		t.Fatalf("头必须可解析：%q err=%v", got, err)
	}
	if mt != "attachment" || params["filename"] != `a"b.tar.gz` {
		t.Fatalf("解析结果不对：mt=%q filename=%q", mt, params["filename"])
	}
	if v := contentDisposition("moo-mate-backup-20260101-000000.tar.gz"); !strings.Contains(v, "moo-mate-backup-20260101-000000.tar.gz") {
		t.Fatalf("普通文件名应原样保留：%q", v)
	}
}

// MM-05：数据目录不能是 /volN 卷根（备份目录不受此限）。
func TestDataDirRejectsVolumeRoot(t *testing.T) {
	c := &companion{idx: &index{path: "/tmp/mate-test/index.json"}}
	req := httptest.NewRequest(http.MethodPost, "/api/datadir", strings.NewReader(`{"path":"/vol1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c.handleDataDirSet(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "卷根") {
		t.Fatalf("卷根应 400 且提示卷根，得 %d %s", rec.Code, rec.Body.String())
	}
}

// MM-01a / B4：监听守卫。
func TestListenGuard(t *testing.T) {
	cases := []struct {
		listen string
		allow  bool
		fatal  bool
		warn   bool
	}{
		{"127.0.0.1:38108", false, false, false},
		{"localhost:38108", false, false, false},
		{"[::1]:38108", false, false, false},
		{"0.0.0.0:38108", false, true, false},
		{"192.0.2.5:38108", false, true, false},
		{"0.0.0.0:38108", true, false, true},
	}
	for _, c := range cases {
		fatalMsg, warnMsg := listenGuard(c.listen, c.allow)
		if (fatalMsg != "") != c.fatal || (warnMsg != "") != c.warn {
			t.Fatalf("%s allow=%v：期望 fatal=%v warn=%v，得 fatal=%q warn=%q",
				c.listen, c.allow, c.fatal, c.warn, fatalMsg, warnMsg)
		}
	}
}

// B3 接线：下载头来自 contentDisposition（含引号的文件名也必须可解析）。
func TestBackupDownloadSetsParseableDisposition(t *testing.T) {
	dir := t.TempDir()
	name := `a"b.tar.gz`
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &companion{cfg: &mateConfig{BackupDir: dir}}
	req := httptest.NewRequest(http.MethodGet, "/api/backup/download?name="+url.QueryEscape(name), nil)
	rec := httptest.NewRecorder()
	c.handleBackupDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下载应 200，得 %d %s", rec.Code, rec.Body.String())
	}
	mt, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || mt != "attachment" || params["filename"] != name {
		t.Fatalf("下载头不可解析或文件名不对：%q err=%v mt=%q name=%q",
			rec.Header().Get("Content-Disposition"), err, mt, params["filename"])
	}
}
