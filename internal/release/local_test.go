package release

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aead.dev/minisign"
)

// localFixture 在临时目录造一套 moo_<tag>_<arch>.fpk + .sha256 + .minisig 夹具。
//
// 签名用**临时密钥对**签：内嵌公钥对应的私钥不在仓库（离线保管），
// 测试通过 pubkeyOverride 把验签公钥换成临时公钥对，从而让「该通过的通过」。
// 生产路径始终走 go:embed 的内嵌公钥（pubkeyForVerify 优先取 override，测试外恒为 nil）。
type localFixture struct {
	dir string
	fpk string
}

// signWith 用给定私钥对 msg 做 minisign 分离签名，返回 .minisig 文件内容。
func signWith(t *testing.T, priv minisign.PrivateKey, msg []byte) []byte {
	t.Helper()
	return minisign.Sign(priv, msg)
}

// useTestPubkey 把验签公钥指向新生成的临时密钥对，返回其签名函数。
func useTestPubkey(t *testing.T) func([]byte) []byte {
	t.Helper()
	pub, priv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	old := pubkeyOverride
	pubkeyOverride = &pub
	t.Cleanup(func() { pubkeyOverride = old })
	return func(msg []byte) []byte { return signWith(t, priv, msg) }
}

// useOtherSigner 用「另一把与验签公钥无关」的私钥签名，验签公钥保持不动。
func useOtherSigner(t *testing.T) func([]byte) []byte {
	t.Helper()
	_, otherPriv, err := minisign.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return func(msg []byte) []byte { return signWith(t, otherPriv, msg) }
}

func newLocalFixture(t *testing.T, tag, arch, version, platform string, machine uint16, sign func([]byte) []byte) *localFixture {
	t.Helper()
	dir := t.TempDir()
	body := makeFPK(t, version, platform, machine)
	fpk := filepath.Join(dir, FPKName(tag, arch))
	if err := os.WriteFile(fpk, body, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	sidecar := hex.EncodeToString(sum[:]) + "  " + filepath.Base(fpk) + "\n"
	if err := os.WriteFile(fpk+".sha256", []byte(sidecar), 0o644); err != nil {
		t.Fatal(err)
	}
	if sign != nil {
		if err := os.WriteFile(fpk+".minisig", sign(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &localFixture{dir: dir, fpk: fpk}
}

// 正例：三件套齐全且签名正确 → 通过，且返回的 Artifact 与文件事实一致。
func TestVerifyLocalOK(t *testing.T) {
	sign := useTestPubkey(t)
	f := newLocalFixture(t, "0.6.377", "x86", "0.6.377", "x86", 0x3e, sign)

	art, err := VerifyLocal("0.6.377", "x86", f.dir)
	if err != nil {
		t.Fatalf("期望通过，实际 %v", err)
	}
	if art.Size <= 0 || len(art.SHA256) != 64 {
		t.Fatalf("artifact 字段异常: %+v", art)
	}
	if art.FPK.Version != "0.6.377" || art.FPK.Platform != "x86" {
		t.Fatalf("manifest 解析异常: %+v", art.FPK)
	}
}

// 7 个反向用例：缺 fpk / 缺 .sha256 / 侧车内容不符 / manifest 版本不符 /
// 平台不符 / 缺 .minisig / 签名无效。
func TestVerifyLocalFailures(t *testing.T) {
	const tag, arch = "0.6.377", "x86"

	cases := []struct {
		name   string
		signer string // "other" = 用另一把密钥签，验签公钥仍是本用例的
		edit   func(t *testing.T, f *localFixture)
		want   string
	}{
		{name: "缺 fpk", edit: func(t *testing.T, f *localFixture) { os.Remove(f.fpk) }, want: "本地缺少应用包"},
		{name: "缺 .sha256", edit: func(t *testing.T, f *localFixture) { os.Remove(f.fpk + ".sha256") }, want: ".sha256 侧车"},
		{
			name: "侧车内容不符",
			edit: func(t *testing.T, f *localFixture) {
				bad := strings.Repeat("b", 64) + "  " + filepath.Base(f.fpk) + "\n"
				os.WriteFile(f.fpk+".sha256", []byte(bad), 0o644)
			},
			want: "sha256 不符",
		},
		{name: "manifest 版本不符", edit: func(t *testing.T, f *localFixture) {}, want: "manifest version="},
		{name: "平台不符", edit: func(t *testing.T, f *localFixture) {}, want: "manifest platform="},
		{name: "缺 .minisig", edit: func(t *testing.T, f *localFixture) { os.Remove(f.fpk + ".minisig") }, want: ".minisig"},
		{name: "签名无效", signer: "other", edit: func(t *testing.T, f *localFixture) {}, want: "验签失败"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sign := useTestPubkey(t) // 本用例验签公钥
			if tc.signer == "other" {
				sign = useOtherSigner(t) // 另起一把签，验签公钥恢复为本用例那把
			}
			version, platform, machine := tag, arch, uint16(0x3e)
			switch tc.name {
			case "manifest 版本不符":
				version = "9.9.9"
			case "平台不符":
				platform, machine = "arm", 0xb7
			}
			f := newLocalFixture(t, tag, arch, version, platform, machine, sign)
			tc.edit(t, f)

			_, err := VerifyLocal(tag, arch, f.dir)
			if err == nil {
				t.Fatalf("期望失败，实际通过")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 %q 未包含 %q", err.Error(), tc.want)
			}
		})
	}
}

// VerifyLocal 不得发出任何 HTTP 请求。
func TestVerifyLocalNoNetwork(t *testing.T) {
	sign := useTestPubkey(t)
	f := newLocalFixture(t, "0.6.377", "x86", "0.6.377", "x86", 0x3e, sign)

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	old := http.DefaultTransport
	http.DefaultTransport = &countingTransport{base: http.DefaultTransport, n: &hits}
	defer func() { http.DefaultTransport = old }()

	if _, err := VerifyLocal("0.6.377", "x86", f.dir); err != nil {
		t.Fatalf("期望通过，实际 %v", err)
	}
	if hits != 0 {
		t.Fatalf("VerifyLocal 发出了 %d 次 HTTP 请求，应为 0", hits)
	}
}

type countingTransport struct {
	base http.RoundTripper
	n    *int
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*c.n++
	return c.base.RoundTrip(r)
}

func TestVerifyLocalBadArgs(t *testing.T) {
	if _, err := VerifyLocal("0.6.377", "mips", t.TempDir()); err == nil {
		t.Fatal("未知架构应报错")
	}
	if _, err := VerifyLocal("0.6.377", "x86", filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("不存在的目录应报错")
	}
}
