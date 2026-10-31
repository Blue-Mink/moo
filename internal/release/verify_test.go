package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// makeFPK 构造最小可用 FPK（gzip tar: manifest + app.tgz{moo-server}）。
func makeFPK(t *testing.T, version, platform string, machine uint16) []byte {
	t.Helper()
	bin := make([]byte, binaryMinSize+1024)
	copy(bin, []byte{0x7f, 'E', 'L', 'F'})
	binary.LittleEndian.PutUint16(bin[18:20], machine)
	copy(bin[100:], []byte("index-AbCdEf12.js"))

	var appTgz bytes.Buffer
	gw := gzip.NewWriter(&appTgz)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "moo-server", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bin); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	manifest := "appname = moo\nversion = " + version + "\nplatform = " + platform + "\n"
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarw := tar.NewWriter(gz)
	for _, m := range []struct {
		name string
		data []byte
	}{{"manifest", []byte(manifest)}, {"app.tgz", appTgz.Bytes()}} {
		if err := tarw.WriteHeader(&tar.Header{Name: m.name, Mode: 0o644, Size: int64(len(m.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarw.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	tarw.Close()
	gz.Close()
	return out.Bytes()
}

func TestParseSHA256Sidecar(t *testing.T) {
	good := strings.Repeat("a", 64)
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"裸摘要", good + "\n", true},
		{"带文件名", good + "  moo_0.6.377_x86.fpk\n", true},
		{"大写归一", strings.ToUpper(good), true},
		{"空内容", "   \n", false},
		{"长度不足", "abc123", false},
		{"非 hex", strings.Repeat("z", 64), false},
	}
	for _, c := range cases {
		got, err := ParseSHA256Sidecar(c.in)
		if c.ok && (err != nil || got != good) {
			t.Fatalf("%s: 期望成功，实际 got=%q err=%v", c.name, got, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s: 期望失败，实际通过（got=%q）", c.name, got)
		}
	}
}

// TestVerifyAssetsRejectsBadSHA：侧车摘要与服务端文件不符 → 必须失败（反向用例）。
func TestVerifyAssetsRejectsBadSHA(t *testing.T) {
	fpk := makeFPK(t, "0.6.377", "x86", elfMachineX86)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".fpk"):
			w.Write(fpk)
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			w.Write([]byte(strings.Repeat("d", 64) + "  x.fpk\n"))
		default:
			w.Write([]byte("sig"))
		}
	}))
	defer srv.Close()

	base := srv.URL + "/moo_0.6.377_x86.fpk"
	_, err := VerifyAssets(context.Background(), srv.Client(), Assets{FPK: base, SHA256: base + ".sha256", Minisig: base + ".minisig"}, "0.6.377", "x86", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "sha256 不符") {
		t.Fatalf("期望 sha256 不符，实际 err=%v", err)
	}
}

// TestVerifyAssetsRejectsMissingSidecar：缺 .minisig → 三件套不完整，且在联网前拦下。
func TestVerifyAssetsRejectsMissingSidecar(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	base := srv.URL + "/moo_0.6.377_x86.fpk"
	_, err := VerifyAssets(context.Background(), srv.Client(), Assets{FPK: base, SHA256: base + ".sha256"}, "0.6.377", "x86", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "三件套不完整") {
		t.Fatalf("期望三件套不完整，实际 err=%v", err)
	}
	if hits != 0 {
		t.Fatalf("缺侧车时不应发起任何网络请求，实际 %d 次", hits)
	}
}

// TestVerifyAssetsRejectsBadSignature：签名无效 → 必须失败。
func TestVerifyAssetsRejectsBadSignature(t *testing.T) {
	fpk := makeFPK(t, "0.6.377", "x86", elfMachineX86)
	sum := sha256Hex(fpk)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".fpk"):
			w.Write(fpk)
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			w.Write([]byte(sum + "  x.fpk\n"))
		default:
			w.Write([]byte("not-a-signature"))
		}
	}))
	defer srv.Close()
	base := srv.URL + "/moo_0.6.377_x86.fpk"
	_, err := VerifyAssets(context.Background(), srv.Client(), Assets{FPK: base, SHA256: base + ".sha256", Minisig: base + ".minisig"}, "0.6.377", "x86", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "验签失败") {
		t.Fatalf("期望验签失败，实际 err=%v", err)
	}
}

// TestInspectFPKRejectsWrongVersion：包内版本与目标不符 → 必须失败。
func TestInspectFPKRejectsWrongVersion(t *testing.T) {
	p := t.TempDir() + "/x.fpk"
	if err := writeFile(p, makeFPK(t, "0.6.376", "x86", elfMachineX86)); err != nil {
		t.Fatal(err)
	}
	info, err := InspectFPK(p)
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if err := info.CheckAgainst("0.6.377", "x86"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("期望 version 不符，实际 err=%v", err)
	}
}

// TestInspectFPKRejectsWrongArch：arm 包冒充 x86 → 必须失败（错包防线）。
func TestInspectFPKRejectsWrongArch(t *testing.T) {
	p := t.TempDir() + "/x.fpk"
	if err := writeFile(p, makeFPK(t, "0.6.377", "arm", elfMachineARM64)); err != nil {
		t.Fatal(err)
	}
	info, err := InspectFPK(p)
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if err := info.CheckAgainst("0.6.377", "x86"); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("期望 platform 不符，实际 err=%v", err)
	}
	if info.ELFMachine != "aarch64" {
		t.Fatalf("ELF 架构解析错误: %s", info.ELFMachine)
	}
}

// writeFile 写临时文件（测试辅助）。
func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }
