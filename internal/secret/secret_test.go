package secret

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	s := New([]byte("0123456789abcdef0123456789abcdef"))
	cases := []string{"fixture-pw-1x9", "短", strings.Repeat("x", 4096), "含 空格 与 🐳 emoji"}
	for _, plain := range cases {
		sealed, err := s.Seal(plain)
		if err != nil {
			t.Fatalf("Seal(%q): %v", plain, err)
		}
		if !IsSealed(sealed) {
			t.Fatalf("密文缺少 %s 前缀: %q", Prefix, sealed)
		}
		if strings.Contains(sealed, plain) && plain != "" {
			t.Fatalf("密文里竟出现明文")
		}
		got, err := s.Open(sealed)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if got != plain {
			t.Fatalf("往返不一致: got %q want %q", got, plain)
		}
	}
}

func TestSealEmptyAndIdempotent(t *testing.T) {
	s := New([]byte("0123456789abcdef0123456789abcdef"))
	if v, err := s.Seal(""); err != nil || v != "" {
		t.Fatalf("空串应原样返回: %q %v", v, err)
	}
	once, _ := s.Seal("token-abc")
	twice, err := s.Seal(once)
	if err != nil || twice != once {
		t.Fatalf("二次加密应幂等: %q %v", twice, err)
	}
}

func TestOpenPlaintextPassthrough(t *testing.T) {
	s := New([]byte("0123456789abcdef0123456789abcdef"))
	// 存量配置里的明文值：Open 应原样返回（平滑迁移）
	got, err := s.Open("plain-old-password")
	if err != nil || got != "plain-old-password" {
		t.Fatalf("明文应原样返回: %q %v", got, err)
	}
}

func TestTamperDetected(t *testing.T) {
	s := New([]byte("0123456789abcdef0123456789abcdef"))
	sealed, _ := s.Seal("fixture-pw-1x9")
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, Prefix))
	raw[len(raw)-1] ^= 0x01
	bad := Prefix + base64.StdEncoding.EncodeToString(raw)
	if _, err := s.Open(bad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("篡改应报 ErrDecrypt，得到 %v", err)
	}
	// 垃圾 base64
	if _, err := s.Open(Prefix + "!!!not-base64!!!"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("坏 base64 应报 ErrDecrypt，得到 %v", err)
	}
}

func TestWrongKeyFails(t *testing.T) {
	a := New([]byte("0123456789abcdef0123456789abcdef"))
	b := New([]byte("fedcba9876543210fedcba9876543210"))
	sealed, _ := a.Seal("fixture-pw-1x9")
	if _, err := b.Open(sealed); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("换密钥应解密失败，得到 %v", err)
	}
	if a.KeyID() == b.KeyID() {
		t.Fatalf("不同密钥的指纹不应相同")
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	s1, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	p := filepath.Join(dir, KeyFile)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("密钥文件未生成: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 0600，实际 %o", st.Mode().Perm())
	}
	// 二次加载：同一密钥、可解先前密文
	s2, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("二次加载失败: %v", err)
	}
	if s1.KeyID() != s2.KeyID() {
		t.Fatalf("二次加载密钥不一致")
	}
	sealed, _ := s1.Seal("hello")
	if got, err := s2.Open(sealed); err != nil || got != "hello" {
		t.Fatalf("跨加载解密失败: %q %v", got, err)
	}
	// 损坏的密钥文件 → 报错（不静默换密钥）
	if err := os.WriteFile(p, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatalf("密钥文件损坏时应报错")
	}
}
