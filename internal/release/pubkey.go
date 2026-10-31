package release

import (
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"aead.dev/minisign"
)

// 内嵌 minisign 公钥（与 internal/api/self_update_pubkey.txt 同为
// 51B18BC7E27A3069）。验签不联网取公钥——否则攻击者可连公钥一起替换。
//
//go:embed pubkey.txt
var pubkeyText string

var (
	pubOnce sync.Once
	pubKey  minisign.PublicKey
	pubErr  error
)

// Pubkey 返回解析后的内嵌公钥（首次调用时解析，失败则缓存错误）。
func Pubkey() (minisign.PublicKey, error) {
	pubOnce.Do(func() {
		pubErr = pubKey.UnmarshalText([]byte(strings.TrimSpace(pubkeyText)))
		if pubErr != nil {
			pubErr = fmt.Errorf("内嵌 minisign 公钥无效: %w", pubErr)
		}
	})
	return pubKey, pubErr
}

// pubkeyOverride 仅供测试替换验签公钥（生产路径恒用内嵌公钥）。
var pubkeyOverride *minisign.PublicKey

// pubkeyForVerify 返回验签用公钥：测试覆盖优先，否则内嵌公钥。
func pubkeyForVerify() (minisign.PublicKey, error) {
	if pubkeyOverride != nil {
		return *pubkeyOverride, nil
	}
	return Pubkey()
}

// PubkeyID 返回公钥短 ID（如 51B18BC7E27A3069），用于报告里自证"用哪把钥匙验的"。
func PubkeyID() string {
	const line = "untrusted comment: minisign public key: "
	for _, ln := range strings.Split(pubkeyText, "\n") {
		if strings.HasPrefix(ln, line) {
			return strings.TrimSpace(strings.TrimPrefix(ln, line))
		}
	}
	return ""
}
