package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// buildMateFPK 造一个最小可解包的 FPK：manifest + app.tgz(moo-mate 二进制 + ui/config)。
func buildMateFPK(t *testing.T, version, platform string, machine uint16, binSize int) []byte {
	t.Helper()
	bin := make([]byte, binSize)
	copy(bin, []byte{0x7f, 'E', 'L', 'F'})
	binary.LittleEndian.PutUint16(bin[18:20], machine)

	var appBuf bytes.Buffer
	agz := gzip.NewWriter(&appBuf)
	atw := tar.NewWriter(agz)
	write := func(name string, body []byte) {
		if err := atw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := atw.Write(body); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	write("moo-mate", bin)
	write("ui/config", []byte(`{".url":{"moo-mate.Application":{"title":"Moo Mate"}}}`))
	if err := atw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := agz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	manifest := "appname = moo-mate\nversion = " + version + "\nplatform = " + platform + "\n"
	var fpkBuf bytes.Buffer
	fgz := gzip.NewWriter(&fpkBuf)
	ftw := tar.NewWriter(fgz)
	add := func(name string, body []byte) {
		if err := ftw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("fpk tar header: %v", err)
		}
		if _, err := ftw.Write(body); err != nil {
			t.Fatalf("fpk tar write: %v", err)
		}
	}
	add("manifest", []byte(manifest))
	add("app.tgz", appBuf.Bytes())
	if err := ftw.Close(); err != nil {
		t.Fatalf("fpk tar close: %v", err)
	}
	if err := fgz.Close(); err != nil {
		t.Fatalf("fpk gzip close: %v", err)
	}
	return fpkBuf.Bytes()
}

func hexOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// ---- 兜底通道必须给全三件套 ----

func TestMateConventionTripleCarriesThree(t *testing.T) {
	for _, arch := range []string{"x86", "arm"} {
		fpk, sha, sig, err := mateConventionTriple("0.2.36", arch)
		if err != nil {
			t.Fatalf("%s 不应报错：%v", arch, err)
		}
		if !strings.HasSuffix(fpk, "moo-mate_0.2.36_"+arch+".fpk") ||
			!strings.HasSuffix(sha, ".fpk.sha256") || !strings.HasSuffix(sig, ".fpk.minisig") {
			t.Fatalf("%s 三件套命名不符：%s / %s / %s", arch, fpk, sha, sig)
		}
		if !strings.Contains(fpk, "/releases/download/mate-v0.2.36/") {
			t.Fatalf("%s tag 路径不对：%s", arch, fpk)
		}
	}
	// 每个架构都必须落到自己的包上（arm 设备不能装到 x86 包）
	armFPK, _, _, _ := mateConventionTriple("0.2.36", "arm")
	if !strings.Contains(armFPK, "_arm.fpk") {
		t.Fatalf("arm 架构取到了错误的包：%s", armFPK)
	}
}

// ---- 解包核事实 ----

func TestInspectMateFPK(t *testing.T) {
	fpk := buildMateFPK(t, "0.2.35", "x86", 0x3E, mateWatchMinBinary+1024)
	tmp := t.TempDir() + "/m.fpk"
	if err := writeFileBytes(tmp, fpk); err != nil {
		t.Fatal(err)
	}
	info, err := inspectMateFPK(tmp)
	if err != nil {
		t.Fatalf("解包失败：%v", err)
	}
	if info.App != "moo-mate" || info.Version != "0.2.35" || info.Platform != "x86" {
		t.Fatalf("manifest 解析错：%+v", info)
	}
	if info.ELFMachine != "x86_64" || info.BinarySize < mateWatchMinBinary {
		t.Fatalf("ELF 判定错：%+v", info)
	}
	if !info.HasUIConfig {
		t.Fatal("应识别到 ui/config")
	}
}

// ---- 通道验收：真下载 + 侧车 + 验签 ----

func serveFPK(t *testing.T, fpk, sidecar, sig []byte) (*httptest.Server, func() (string, string, string, error)) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/m.fpk", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(fpk) })
	mux.HandleFunc("/m.fpk.sha256", func(w http.ResponseWriter, r *http.Request) {
		if len(sidecar) == 0 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(sidecar)
	})
	mux.HandleFunc("/m.fpk.minisig", func(w http.ResponseWriter, r *http.Request) {
		if len(sig) == 0 {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(sig)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() (string, string, string, error) {
		u := srv.URL + "/m.fpk"
		return u, u + ".sha256", u + ".minisig", nil
	}
}

func TestVerifyMateChannelAcceptsSignedPackage(t *testing.T) {
	fpk := buildMateFPK(t, "0.2.35", "x86", 0x3E, mateWatchMinBinary+1024)
	sg := newTestSigner(t)
	_, urls := serveFPK(t, fpk, []byte(hexOf(fpk)+"  m.fpk\n"), sg.sign(fpk))
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: hexOf(fpk)}
	if err := verifyMateChannel("client", "0.2.35", "x86", exp, urls); err != nil {
		t.Fatalf("应通过：%v", err)
	}
}

func TestVerifyMateChannelRejectsBadSignature(t *testing.T) {
	fpk := buildMateFPK(t, "0.2.35", "x86", 0x3E, mateWatchMinBinary+1024)
	sg := newTestSigner(t)
	forged := sg.sign([]byte("another payload"))
	_, urls := serveFPK(t, fpk, []byte(hexOf(fpk)+"  m.fpk\n"), forged)
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: hexOf(fpk)}
	err := verifyMateChannel("client", "0.2.35", "x86", exp, urls)
	if err == nil || !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("签名不符必须失败，实际：%v", err)
	}
}

func TestVerifyMateChannelRejectsMissingSignature(t *testing.T) {
	fpk := buildMateFPK(t, "0.2.35", "x86", 0x3E, mateWatchMinBinary+1024)
	_, urls := serveFPK(t, fpk, []byte(hexOf(fpk)+"  m.fpk\n"), nil)
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: hexOf(fpk)}

	// case 1：签名 URL 有、服务器 404 → 下载失败即拒绝
	err := verifyMateChannel("fallback", "0.2.35", "x86", exp, urls)
	if err == nil || !strings.Contains(err.Error(), ".minisig 下载失败") {
		t.Fatalf("签名 404 必须失败，实际：%v", err)
	}

	// case 2：URL 选择阶段就没签名（release 未附签名）→ 三件套不全即拒绝
	badURLs := func() (string, string, string, error) {
		u := urls
		fpkURL, shaURL, _, _ := mustURLs(t, u)
		return fpkURL, shaURL, "", nil
	}
	err = verifyMateChannel("client", "0.2.35", "x86", exp, badURLs)
	if err == nil || !strings.Contains(err.Error(), "三件套不全") {
		t.Fatalf("缺签名 URL 必须失败，实际：%v", err)
	}
}

// mustURLs 取一次三件套 URL（测试内辅助）。
func mustURLs(t *testing.T, f func() (string, string, string, error)) (string, string, string, error) {
	t.Helper()
	return f()
}

func TestVerifyMateChannelRejectsIndexMismatch(t *testing.T) {
	fpk := buildMateFPK(t, "0.2.35", "x86", 0x3E, mateWatchMinBinary+1024)
	sg := newTestSigner(t)
	_, urls := serveFPK(t, fpk, []byte(hexOf(fpk)+"  m.fpk\n"), sg.sign(fpk))
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: strings.Repeat("a", 64)}
	err := verifyMateChannel("client", "0.2.35", "x86", exp, urls)
	if err == nil || !strings.Contains(err.Error(), "与索引不符") {
		t.Fatalf("与索引不符必须失败，实际：%v", err)
	}
}

func TestVerifyMateChannelRejectsWrongArchPackage(t *testing.T) {
	// 包本身是 arm，却按 x86 验收 → 必须拦（装错包）
	fpk := buildMateFPK(t, "0.2.35", "arm", 0xB7, mateWatchMinBinary+1024)
	sg := newTestSigner(t)
	_, urls := serveFPK(t, fpk, []byte(hexOf(fpk)+"  m.fpk\n"), sg.sign(fpk))
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: hexOf(fpk)}
	err := verifyMateChannel("client", "0.2.35", "x86", exp, urls)
	if err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("架构不符必须失败，实际：%v", err)
	}
}

// ---- 索引解析 ----

func TestMateIndexVersionAndExpect(t *testing.T) {
	idx := &mateWatchIndex{Apps: map[string]struct {
		Version  string `json:"version"`
		Releases map[string]struct {
			Packages map[string]struct {
				Size   int64  `json:"size"`
				SHA256 string `json:"sha256"`
			} `json:"packages"`
		} `json:"releases"`
	}{}}
	v, err := mateIndexVersion(idx)
	if err == nil {
		t.Fatalf("空索引应报错，实际拿到 %q", v)
	}
}

func writeFileBytes(p string, b []byte) error {
	return os.WriteFile(p, b, 0o644)
}

// TestVerifyMateChannelRealArtifact 用**真实产物**跑一整条通道验收：
// MATE_TEST_FPK=<fpk 路径>。要求同目录有 <fpk>.sha256 与 <fpk>.minisig，
// 版本/架构从 manifest 读，索引期望值取实算 sha/size。
// 这是「包真的能过客户端那套校验」的最终证据，合成包只证明逻辑分支。
func TestVerifyMateChannelRealArtifact(t *testing.T) {
	p := os.Getenv("MATE_TEST_FPK")
	if p == "" {
		t.Skip("未设 MATE_TEST_FPK")
	}
	info, err := inspectMateFPK(p)
	if err != nil {
		t.Fatalf("解包失败：%v", err)
	}
	fpk, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	side, err := os.ReadFile(p + ".sha256")
	if err != nil {
		t.Fatalf("缺 .sha256：%v", err)
	}
	sig, err := os.ReadFile(p + ".minisig")
	if err != nil {
		t.Fatalf("缺 .minisig：%v", err)
	}
	_, urls := serveFPK(t, fpk, side, sig)
	exp := mateWatchExpect{Size: int64(len(fpk)), SHA256: hexOf(fpk)}
	if err := verifyMateChannel("real", info.Version, strings.ToLower(info.Platform), exp, urls); err != nil {
		t.Fatalf("真实产物未通过客户端同款验收：%v", err)
	}
}
