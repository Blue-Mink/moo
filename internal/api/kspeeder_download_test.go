package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 0.6.524：KSpeeder 下载清单解析 / 架构选择 / 本地包校验。
func TestParseKSList(t *testing.T) {
	sha := "665e76bca64f8b547b60660f2a5de59dc74adf8211a68bc5f2b6e74836085e06"
	body := "\ufeff# comment\n\n" +
		"x86|0.8.9|kspeeder-0.8.9-fnos-amd64.fpk|https://example.com/a.fpk|" + sha + "|6459687\n" +
		"bad line without columns\n" +
		"arm|0.8.9|k-arm.fpk|https://example.com/b.fpk|" + sha + "|6111640\n" +
		"arm|0.8.9|dup-arm.fpk|https://example.com/c.fpk|" + sha + "|6111640\n" +
		"x86|0.8.9|bad-hash.fpk|https://example.com/d.fpk|deadbeef|1\n"
	got := parseKSList(body)
	if len(got) != 3 {
		t.Fatalf("want 3 valid entries, got %d: %+v", len(got), got)
	}
	if got[0].Arch != "x86" || got[0].Size != 6459687 || got[0].Version != "0.8.9" {
		t.Fatalf("unexpected first entry: %+v", got[0])
	}
	if got[0].Filename != "kspeeder-0.8.9-fnos-amd64.fpk" {
		t.Fatalf("filename not parsed: %+v", got[0])
	}
}

func TestPickKSByArch(t *testing.T) {
	entries := []ksEntry{{Arch: "x86", Version: "0.8.9"}, {Arch: "arm", Version: "0.8.9"}}
	want := "x86"
	if runtime.GOARCH == "arm64" {
		want = "arm"
	}
	e, err := pickKS(entries)
	if err != nil || e.Arch != want {
		t.Fatalf("pickKS = %+v, %v; want arch %s", e, err, want)
	}
	// 只有 x86 时 arm 设备回退到 x86（宁可给出可下载条目，校验仍会拦住错包）
	e, err = pickKS([]ksEntry{{Arch: "x86", Version: "1"}})
	if err != nil || e.Arch != "x86" {
		t.Fatalf("fallback pickKS = %+v, %v", e, err)
	}
	if _, err := pickKS(nil); err == nil {
		t.Fatal("empty list should error")
	}
}

func TestKSFileMatches(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k.fpk")
	body := []byte("kspeeder-test-payload")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	e := ksEntry{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	if !ksFileMatches(p, e) {
		t.Fatal("matching file rejected")
	}
	e.Size++
	if ksFileMatches(p, e) {
		t.Fatal("size mismatch accepted")
	}
	e.Size--
	e.SHA256 = "00" + e.SHA256[2:]
	if ksFileMatches(p, e) {
		t.Fatal("sha mismatch accepted")
	}
}
