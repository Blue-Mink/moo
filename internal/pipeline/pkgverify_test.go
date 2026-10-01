package pipeline

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/source"
)

// makeFpk 造一个最小 FPK（tar.gz，manifest 在尾部，贴近 fnpack 真实布局：
// 大文件在前、manifest 在后，确保 ManifestVersion 走完整解压路径）。
func makeFpk(t *testing.T, dir, manifest, filler string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := bytes.Repeat([]byte("x"), len(filler))
	hdr := &tar.Header{Name: "app.tgz", Mode: 0o644, Size: int64(len(payload))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	m := []byte(manifest)
	if err := tw.WriteHeader(&tar.Header{Name: "manifest", Mode: 0o644, Size: int64(len(m))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(m); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "demo.fpk")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManifestVersion(t *testing.T) {
	dir := t.TempDir()
	p := makeFpk(t, dir, "appname = demo\ndisplay_name = 演示\nversion = 1.2.3\n", strings.Repeat("a", 4096))
	v, err := ManifestVersion(p)
	if err != nil {
		t.Fatalf("ManifestVersion: %v", err)
	}
	if v != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", v)
	}
}

func TestSha256OfFile(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello moo")
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	got, err := Sha256OfFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha mismatch: %s", got)
	}
}

func TestReuseOK(t *testing.T) {
	dir := t.TempDir()
	manifestV1 := "appname = demo\nversion = 1.0.0\n"
	manifestV2 := "appname = demo\nversion = 2.0.0\n"
	p := makeFpk(t, dir, manifestV1, strings.Repeat("a", 2048))

	// 1) sha256 匹配 → 复用
	sum := sha256.Sum256(mustRead(t, p))
	app := &source.App{Version: "1.0.0", Sha256: hex.EncodeToString(sum[:])}
	if !reuseOK(p, app, int64(len(sum)), nil) {
		t.Error("sha 匹配应复用")
	}
	// 2) sha256 不匹配（文件是别的版本）→ 不复用
	app2 := &source.App{Version: "2.0.0", Sha256: strings.Repeat("0", 64)}
	if reuseOK(p, app2, 123, nil) {
		t.Error("sha 不匹配不得复用")
	}
	// 3) 精确大小：文件实际大小 ≠ 声明 → 不复用
	if info, err := os.Stat(p); err == nil {
		app3 := &source.App{Version: "1.0.0", SizeBytes: info.Size() + 1}
		if reuseOK(p, app3, info.Size(), nil) {
			t.Error("精确大小不匹配不得复用")
		}
		app4 := &source.App{Version: "1.0.0", SizeBytes: info.Size()}
		if !reuseOK(p, app4, info.Size(), nil) {
			t.Error("精确大小+版本一致应复用")
		}
	}
	// 4) 无 sha/大小 → manifest 版本兜底：版本一致复用，不一致拒绝
	app5 := &source.App{Version: "1.0.0"}
	if !reuseOK(p, app5, 123, nil) {
		t.Error("manifest 版本一致应复用（无大小/哈希声明）")
	}
	app6 := &source.App{Version: "2.0.0"}
	if reuseOK(p, app6, 123, nil) {
		t.Error("manifest 版本不一致不得复用")
	}
	_ = manifestV2
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyDownloaded(t *testing.T) {
	dir := t.TempDir()
	p := makeFpk(t, dir, "appname = demo\nversion = 1.0.0\n", strings.Repeat("b", 1024))

	// sha 不匹配 → 明确错误（含实际版本）
	sum := sha256.Sum256(mustRead(t, p))
	app := &source.App{Version: "2.0.0", Sha256: strings.Repeat("f", 64)}
	err := verifyDownloaded(p, app)
	if err == nil {
		t.Fatal("sha 不匹配应报错")
	}
	if !strings.Contains(err.Error(), "1.0.0") {
		t.Errorf("错误应含包内实际版本 1.0.0: %v", err)
	}

	// sha 匹配 → 通过
	appOK := &source.App{Version: "1.0.0", Sha256: hex.EncodeToString(sum[:])}
	if err := verifyDownloaded(p, appOK); err != nil {
		t.Fatalf("sha 匹配应通过: %v", err)
	}

	// 精确大小不匹配 → 报错
	if info, err := os.Stat(p); err == nil {
		appSize := &source.App{Version: "1.0.0", SizeBytes: info.Size() + 5}
		if err := verifyDownloaded(p, appSize); err == nil {
			t.Error("大小不匹配应报错")
		}
	}
}
