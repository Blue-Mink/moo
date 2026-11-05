package sysupdate

// 回归测试：公开更新接口 + 本地版本文件的比较逻辑。
// 反向验证：把 Available 改成固定 false，本文件必红。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func mkVer(t *testing.T, v string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "version")
	if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func newAPI(t *testing.T, trimVer, kernel string) *httptest.Server {
	t.Helper()
	body := `{"branchId":2,"kernel":{"version":"` + kernel + `","description":"k"},
	"packages":[{"packageName":"trim","version":"` + trimVer + `","description":"[安全更新]\n- 修了 A\n- 修了 B\ntrim"}]}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestAvailableWhenNewer(t *testing.T) {
	srv := newAPI(t, "1.2.1000", "6.18.18.c1186-trim")
	defer srv.Close()
	c := New()
	c.API = srv.URL
	c.VersionFile = mkVer(t, "1.2.0800")
	c.KernelFile = mkVer(t, "6.18.18.c1126-trim")
	st := c.Get(context.Background(), true)
	if !st.OK {
		t.Fatalf("ok=false err=%s", st.Err)
	}
	if !st.Available || st.Latest != "1.2.1000" || st.Current != "1.2.0800" {
		t.Fatalf("st=%+v，期望 available 且 latest=1.2.1000", st)
	}
	if len(st.Notes) == 0 {
		t.Fatal("期望解析出更新说明")
	}
}

func TestNotAvailableWhenSame(t *testing.T) {
	srv := newAPI(t, "1.2.0800", "6.18.18.c1126-trim")
	defer srv.Close()
	c := New()
	c.API = srv.URL
	c.VersionFile = mkVer(t, "1.2.0800")
	c.KernelFile = mkVer(t, "6.18.18.c1126-trim")
	st := c.Get(context.Background(), true)
	if !st.OK || st.Available {
		t.Fatalf("st=%+v，同版本不应提示更新", st)
	}
}

func TestDegradesOnBadAPI(t *testing.T) {
	c := New()
	c.API = "http://127.0.0.1:1/never"
	c.VersionFile = mkVer(t, "1.2.0800")
	c.KernelFile = mkVer(t, "6.18.18.c1126-trim")
	st := c.Get(context.Background(), true)
	if st.OK || st.Err == "" {
		t.Fatalf("st=%+v，期望 ok=false 且有 err（静默降级，不 panic）", st)
	}
}

// TestRealAPI 真实接口冒烟（MOO_REAL_SYSUPDATE=1 时启用；CI 不跑）。
func TestRealAPI(t *testing.T) {
	if os.Getenv("MOO_REAL_SYSUPDATE") != "1" {
		t.Skip("未设置 MOO_REAL_SYSUPDATE=1")
	}
	c := New()
	st := c.Get(context.Background(), true)
	t.Logf("OK=%v err=%q current=%s latest=%s kernel=%s→%s branch=%d notes=%d",
		st.OK, st.Err, st.Current, st.Latest, st.KernelCurrent, st.KernelLatest, st.Branch, len(st.Notes))
	if !st.OK {
		t.Fatalf("真实接口失败: %s", st.Err)
	}
	if st.Current == "" || st.Latest == "" {
		t.Fatal("版本为空")
	}
}
