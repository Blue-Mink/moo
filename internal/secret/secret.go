// Package secret 提供 Moo 配置中敏感字段（面板口令、通知渠道密钥）的
// 落盘加密：AES-256-GCM，密文形如 `enc:v1:<base64(nonce||ciphertext)>`。
//
// 设计取舍（0.6.220，用户选定「方案 X」）：
//
//   - **密钥 = 每次安装独有的 32 字节随机值**，存于 `<dataDir>/.moo-secret.key`
//     （0600）。**刻意不用 `/etc/machine-id` 派生**：实测两台 fnOS 设备
//     （主 NAS SpaceX 与测试机 fnos）的 machine-id 完全相同
//     （`6f956896308758a951aa9a8b6a5e0b43`，同镜像克隆），派生出来的
//     「机器绑定」是假绑定，反而给人错误的安全感。
//
//   - 因此本层防的是「**离线泄漏**」：配置文件被单独拷贝、贴到聊天里、
//     被运维脚本/备份误打包——拿到的只是密文，没有同机的密钥文件解不开。
//
//   - **防不住**已经拿到 root 的进程：密钥文件与 config.json 同目录，
//     能读配置的进程通常也能读密钥。这是刻意的折中（成本小、用户无感），
//     设置页文案与文档都写明；要真正隔离只能「不落盘」（每次用时输入）。
//
//   - 值以 `enc:v1:` 前缀区分：非该前缀一律按明文处理（兼容存量配置、
//     旧版本回滚时可预期地把密文当明文而登录失败，但不会崩）。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Prefix 密文前缀（版本化，便于将来换算法）。
const Prefix = "enc:v1:"

// KeyFile 密钥文件名（位于应用数据目录，权限 0600）。
const KeyFile = ".moo-secret.key"

// ErrDecrypt 解密失败（密钥不符 / 密文被篡改 / 格式损坏）。
var ErrDecrypt = errors.New("凭据解密失败（密钥不匹配或密文损坏）")

// ErrNoCodec 未配置加解密器。
var ErrNoCodec = errors.New("未配置凭据加解密器")

// Sealer 持有 AES-256-GCM 密钥。
type Sealer struct {
	key   []byte
	keyID string // 密钥指纹（日志用；不泄露密钥本身）
}

// IsSealed 判断某个值是否是本方案产出的密文。
func IsSealed(v string) bool { return strings.HasPrefix(v, Prefix) }

// LoadOrCreate 读取 `<dataDir>/.moo-secret.key`；不存在则生成 32 字节随机密钥
// （0600）并落盘。文件存在但内容不是 32 字节 → 视为损坏，返回错误
// （调用方应降级为「不加密」并提示用户，而不是静默换密钥，否则老密文全废）。
func LoadOrCreate(dataDir string) (*Sealer, error) {
	path := filepath.Join(dataDir, KeyFile)
	if raw, err := os.ReadFile(path); err == nil {
		if len(raw) != 32 {
			return nil, fmt.Errorf("密钥文件 %s 长度异常（%d 字节，期望 32）", KeyFile, len(raw))
		}
		return New(raw), nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil { // 存量文件 mode 修正，不依赖 umask
		return nil, err
	}
	return New(key), nil
}

// New 用给定密钥构造（测试用）。
func New(key []byte) *Sealer {
	k := make([]byte, len(key))
	copy(k, key)
	return &Sealer{key: k, keyID: fingerprint(k)}
}

// KeyID 返回密钥指纹（sha256 前 8 字节 hex），用于日志区分密钥版本。
func (s *Sealer) KeyID() string { return s.keyID }

// Seal 加密明文；空串原样返回（空值不加密）。
func (s *Sealer) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if IsSealed(plain) { // 已是密文：避免二次加密
		return plain, nil
	}
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil) // nonce || ciphertext
	return Prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open 解密；非密文（旧明文）原样返回，便于存量配置平滑迁移。
func (s *Sealer) Open(value string) (string, error) {
	if !IsSealed(value) {
		return value, nil
	}
	body := strings.TrimPrefix(value, Prefix)
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("%w: base64 解码失败", ErrDecrypt)
	}
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("%w: 密文长度不足", ErrDecrypt)
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return string(plain), nil
}

func (s *Sealer) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func fingerprint(key []byte) string {
	sum := sha256.Sum256(key)
	return fmt.Sprintf("%x", sum[:8])
}
