package api

// fpk_icon_326_test.go —— 从 FPK 提取图标的纯函数单测。
//
// 覆盖：根图标 vs app.tgz 内 ui/images 的优先级、坏根图标跳过、
// 单字节 UTF-8 污染修复、无图标返回 false。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"testing"
)

func mkTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, b := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(b)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractFpkIcon_Nested256(t *testing.T) {
	good := pngHeader()
	app := mkTarGz(t, map[string][]byte{
		"ui/images/icon_256.png": good,
		"ui/images/icon_64.png":  good,
	})
	fpk := mkTarGz(t, map[string][]byte{
		"ICON.PNG": bytes.Repeat([]byte{0x00}, 32), // 坏根图标：应被跳过
		"app.tgz":  app,
	})
	got, ct, ok := extractFpkIcon(fpk)
	if !ok || ct != "image/png" || !bytes.Equal(got, good) {
		t.Fatalf("应从 app.tgz 取到 256 图标：ok=%v ct=%q len=%d", ok, ct, len(got))
	}
}

func TestExtractFpkIcon_RootBeatsNested(t *testing.T) {
	root := pngHeader()
	nested := append([]byte{}, root...)
	nested[20] ^= 0x01 // 与根图标区分开，确保断言真的比较了字节来源
	app := mkTarGz(t, map[string][]byte{"ui/images/icon_256.png": nested})
	fpk := mkTarGz(t, map[string][]byte{
		"ICON_256.PNG": root,
		"app.tgz":      app,
	})
	got, _, ok := extractFpkIcon(fpk)
	if !ok || !bytes.Equal(got, root) {
		t.Fatal("根目录 ICON_256.PNG 应优先于 app.tgz 内图标")
	}
}

func TestExtractFpkIcon_RepairsMangledRoot(t *testing.T) {
	orig := pngHeader()
	mangled := append([]byte{0xEF, 0xBF, 0xBD}, orig[1:]...)
	fpk := mkTarGz(t, map[string][]byte{"ICON_256.PNG": mangled})
	got, _, ok := extractFpkIcon(fpk)
	if !ok || !bytes.Equal(got, orig) {
		t.Fatal("单字节污染的根图标应被修复后返回")
	}
}

func TestExtractFpkIcon_None(t *testing.T) {
	fpk := mkTarGz(t, map[string][]byte{"README.md": []byte("# hi")})
	if _, _, ok := extractFpkIcon(fpk); ok {
		t.Fatal("无图标时应返回 false")
	}
	if _, _, ok := extractFpkIcon([]byte("not a tar")); ok {
		t.Fatal("非 gzip 字节应返回 false")
	}
}

// TestExtractFpkIcon_Live 可选真包校验（默认跳过）：
// MOO_FPK_LIVE=/path/to/x.fpk go test ./internal/api -run Live -v
func TestExtractFpkIcon_Live(t *testing.T) {
	p := os.Getenv("MOO_FPK_LIVE")
	if p == "" {
		t.Skip("未设置 MOO_FPK_LIVE")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got, ct, ok := extractFpkIcon(b)
	if !ok {
		t.Fatalf("未取到图标：%s", p)
	}
	if len(got) < 64 || (ct != "image/png" && ct != "image/svg+xml") {
		t.Fatalf("图标字节异常: len=%d ct=%s", len(got), ct)
	}
	t.Logf("OK %s len=%d ct=%s head=%x", p, len(got), ct, got[:8])
}
