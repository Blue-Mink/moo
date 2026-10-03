package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmpVersions(t *testing.T) {
	cases := []struct{ a, b string; want int }{
		{"0.6.111", "0.6.111", 0},
		{"v0.6.111", "0.6.111", 0},
		{"0.6.112", "0.6.111", 1},
		{"0.6.111", "0.6.112", -1},
		{"0.7.0", "0.6.999", 1},
		{"0.6.9", "0.6.11", -1}, // 数字比较，非字典序
		{"1.0", "1.0.0", 0},
		{"dev", "0.1.0", -1}, // 非数字按 0
	}
	for _, c := range cases {
		if got := cmpVersions(c.a, c.b); got != c.want {
			t.Errorf("cmpVersions(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

// makeFakeFpk 构造 FPK（gzip tar）内含 app.tgz（gzip tar）内含 moo-server。
func makeFakeFpk(t *testing.T, binContent []byte) string {
	t.Helper()
	// 内层 app.tgz
	var appBuf bytes.Buffer
	gz := gzip.NewWriter(&appBuf)
	tr := tar.NewWriter(gz)
	if err := tr.WriteHeader(&tar.Header{Name: "moo-server", Mode: 0o755, Size: int64(len(binContent))}); err != nil {
		t.Fatal(err)
	}
	tr.Write(binContent)
	tr.Close()
	gz.Close()

	// 外层 FPK
	dir := t.TempDir()
	fpkPath := filepath.Join(dir, "test.fpk")
	out, err := os.Create(fpkPath)
	if err != nil {
		t.Fatal(err)
	}
	gz2 := gzip.NewWriter(out)
	tr2 := tar.NewWriter(gz2)
	if err := tr2.WriteHeader(&tar.Header{Name: "manifest", Mode: 0o644, Size: 2}); err != nil {
		t.Fatal(err)
	}
	tr2.Write([]byte("ok"))
	if err := tr2.WriteHeader(&tar.Header{Name: "app.tgz", Mode: 0o644, Size: int64(appBuf.Len())}); err != nil {
		t.Fatal(err)
	}
	tr2.Write(appBuf.Bytes())
	tr2.Close()
	gz2.Close()
	out.Close()
	return fpkPath
}

func TestExtractSelfUpdateBinary(t *testing.T) {
	// 合法：ELF magic + >5MB
	elf := make([]byte, 6<<20)
	elf[0], elf[1], elf[2], elf[3] = 0x7f, 'E', 'L', 'F'
	fpk := makeFakeFpk(t, elf)
	bin, tmpDir, err := extractSelfUpdateBinary(fpk)
	if err != nil {
		t.Fatalf("合法包应解包成功: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	fi, _ := os.Stat(bin)
	if fi.Size() != int64(len(elf)) {
		t.Errorf("解包大小不符: %d", fi.Size())
	}

	// 非 ELF 拒绝
	bad := make([]byte, 6<<20)
	copy(bad, []byte("MZ..."))
	fpk2 := makeFakeFpk(t, bad)
	if _, tmp2, err := extractSelfUpdateBinary(fpk2); err == nil {
		t.Error("非 ELF 应拒绝")
	} else if !strings.Contains(err.Error(), "ELF") {
		t.Errorf("错误信息应提及 ELF: %v", err)
	} else {
		os.RemoveAll(tmp2)
	}

	// 过小拒绝
	small := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{0}, 1024)...)
	fpk3 := makeFakeFpk(t, small)
	if _, tmp3, err := extractSelfUpdateBinary(fpk3); err == nil {
		t.Error("过小二进制应拒绝")
	} else {
		os.RemoveAll(tmp3)
	}

	// 非 gzip 拒绝
	fpk4 := filepath.Join(t.TempDir(), "notgz.fpk")
	os.WriteFile(fpk4, []byte("hello"), 0o644)
	if _, _, err := extractSelfUpdateBinary(fpk4); err == nil {
		t.Error("非 gzip 应拒绝")
	}
}
