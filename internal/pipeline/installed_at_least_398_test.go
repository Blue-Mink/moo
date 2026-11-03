package pipeline

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"moo/internal/platform"
)

// 0.6.398：安装失败补偿必须「按版本」判定，不能只看应用是否出现在已装列表——
// 旧版本已在列表里时那会误判成功。
func TestInstalledAtLeast(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc/v1/app/installed" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"total": 1,
				"list":  []any{map[string]any{"appName": "fnmedia-ext", "version": "0.2.65"}},
			},
		})
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	defer func() { _ = ln.Close() }()

	platform.SetDaemonSocketForTest(sock)
	defer platform.SetDaemonSocketForTest("/var/run/com.trim.app.center.sock")

	p := &Pipeline{}
	ctx := context.Background()
	if !p.installedAtLeast(ctx, "fnmedia-ext", "0.2.65", 200*time.Millisecond) {
		t.Fatal("同版本应判定已达标")
	}
	if !p.installedAtLeast(ctx, "fnmedia-ext", "0.2.60", 200*time.Millisecond) {
		t.Fatal("已装比目标新应判定达标")
	}
	if p.installedAtLeast(ctx, "fnmedia-ext", "0.2.66", 200*time.Millisecond) {
		t.Fatal("已装低于目标不应判定达标")
	}
	if p.installedAtLeast(ctx, "other-app", "0.2.65", 200*time.Millisecond) {
		t.Fatal("未安装应用不应判定达标")
	}
}
