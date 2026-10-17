package api

// 0.6.329 自更新 minisign 签名验签单测。
// 覆盖：验签通过 / 内容篡改失败 / 缺签名(404)失败 / 错误公钥失败 / 内嵌公钥解析成功。
// 用临时密钥对 + 临时覆盖 selfUpdatePubkey 验证 verifyMinisig 全链路，
// 不依赖生产私钥（生产私钥口令加密、离线交付用户保管）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"

	"moo/internal/config"
)

func newTestKeyPair(t *testing.T) (minisign.PublicKey, minisign.PrivateKey) {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// serveMinisig 起一个 httptest server 返回给定签名（或给定状态码）。
func serveMinisig(t *testing.T, sig []byte, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "no sig", status)
			return
		}
		_, _ = w.Write(sig)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestVerifyMinisigValid(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	msg := []byte("moo self-update signing test payload")
	sig := minisign.Sign(priv, msg)

	fpk := filepath.Join(t.TempDir(), "moo_test.fpk")
	if err := os.WriteFile(fpk, msg, 0644); err != nil {
		t.Fatal(err)
	}

	old := selfUpdatePubkey
	selfUpdatePubkey = pub
	defer func() { selfUpdatePubkey = old }()

	if err := verifyMinisig(context.Background(), http.DefaultClient, serveMinisig(t, sig, http.StatusOK), fpk); err != nil {
		t.Fatalf("verifyMinisig = %v, want nil", err)
	}
}

func TestVerifyMinisigTampered(t *testing.T) {
	pub, priv := newTestKeyPair(t)
	sig := minisign.Sign(priv, []byte("original payload"))

	fpk := filepath.Join(t.TempDir(), "moo_test.fpk")
	if err := os.WriteFile(fpk, []byte("tampered payload!!"), 0644); err != nil {
		t.Fatal(err)
	}

	old := selfUpdatePubkey
	selfUpdatePubkey = pub
	defer func() { selfUpdatePubkey = old }()

	err := verifyMinisig(context.Background(), http.DefaultClient, serveMinisig(t, sig, http.StatusOK), fpk)
	if err == nil {
		t.Fatal("verifyMinisig = nil, want 签名不匹配错误")
	}
	if !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("err = %v, want 含「签名校验失败」", err)
	}
}

func TestVerifyMinisigMissing(t *testing.T) {
	fpk := filepath.Join(t.TempDir(), "moo_test.fpk")
	if err := os.WriteFile(fpk, []byte("whatever"), 0644); err != nil {
		t.Fatal(err)
	}
	err := verifyMinisig(context.Background(), http.DefaultClient, serveMinisig(t, nil, http.StatusNotFound), fpk)
	if err == nil {
		t.Fatal("verifyMinisig = nil, want 404 错误")
	}
	if !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v, want 含 HTTP 404", err)
	}
}

func TestVerifyMinisigWrongKey(t *testing.T) {
	_, privA := newTestKeyPair(t)
	pubB, _ := newTestKeyPair(t)
	msg := []byte("payload")
	sig := minisign.Sign(privA, msg)

	fpk := filepath.Join(t.TempDir(), "moo_test.fpk")
	if err := os.WriteFile(fpk, msg, 0644); err != nil {
		t.Fatal(err)
	}

	old := selfUpdatePubkey
	selfUpdatePubkey = pubB
	defer func() { selfUpdatePubkey = old }()

	if err := verifyMinisig(context.Background(), http.DefaultClient, serveMinisig(t, sig, http.StatusOK), fpk); err == nil {
		t.Fatal("verifyMinisig = nil, want 公钥不匹配错误")
	}
}

// 内嵌公钥须在包加载时成功解析（否则 selfUpdatePubkey 的 init 直接 panic，包都起不来）。
func TestEmbeddedPubkeyParses(t *testing.T) {
	if selfUpdatePubkey.ID() == 0 {
		t.Fatal("内嵌公钥 ID = 0，解析失败")
	}
	if strings.TrimSpace(selfUpdatePubkeyText) == "" {
		t.Fatal("内嵌公钥文本为空")
	}
}

// 反向验证锚点：downloadSelfUpdateFPK 在 release 未附 .minisig 时必须拒绝。
// （临时回退 minisig 检查后，此测试会 FAIL —— 无签名也放行，即 0.6.328 的漏洞。）
func TestDownloadSelfUpdateFPKRequiresMinisig(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2<<20) // >1MB，过大小检查
	fpkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(big)
	}))
	defer fpkSrv.Close()

	s := &Server{Cfg: config.Default()}
	rec := httptest.NewRecorder()
	_, err := s.downloadSelfUpdateFPK(rec, rec, context.Background(), fpkSrv.URL, "", "", "9.9.9")
	if err == nil {
		t.Fatal("downloadSelfUpdateFPK = nil, want 未附 .minisig 拒绝")
	}
	if !strings.Contains(err.Error(), "未附 .minisig") {
		t.Fatalf("err = %v, want 含「未附 .minisig」", err)
	}
}
