package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// VerifyLocal 对本地目录里的三件套做「客户端同款验收」——与 VerifyAssets 同一套规则，
// 但对象是磁盘上的文件而不是远端 URL：
//
//  1. moo_<tag>_<arch>.fpk 必须存在；
//  2. .sha256 侧车必须存在，且首字段 == 实算 sha256；
//  3. .minisig 必须存在，且用内嵌公钥验签通过（复用 verifySignatureFile）；
//  4. 解包核 manifest 版本 / 平台 / ELF 架构 / 前端 bundle 名（CheckAgainst）。
//
// 全程**不联网**：不发任何 HTTP 请求，公钥恒取 go:embed 的内嵌副本。
// 返回与远端验收同构的 *Artifact，便于脚本统一消费。
func VerifyLocal(tag, arch, dir string) (*Artifact, error) {
	if _, err := normalizeArch(arch); err != nil {
		return nil, err
	}
	if dir == "" {
		dir = "."
	}
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("本地目录不可用: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("本地路径不是目录: %s", dir)
	}

	name := FPKName(tag, arch)
	fpkPath := filepath.Join(dir, name)

	// 1. FPK 本体
	fi, err := os.Stat(fpkPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("本地缺少应用包 %s（目录 %s）", name, dir)
		}
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不是文件", fpkPath)
	}
	if fi.Size() > maxFPKSize {
		return nil, fmt.Errorf("%s 超过大小上限 %d 字节", name, maxFPKSize)
	}

	sha, err := fileSHA256(fpkPath)
	if err != nil {
		return nil, err
	}

	// 2. .sha256 侧车内容比对（客户端 fail-closed 的同一条规则）
	shaPath := fpkPath + ".sha256"
	b, err := os.ReadFile(shaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("本地缺少 %s.sha256 侧车（≤0.6.328 老客户端的升级通道依赖它）", name)
		}
		return nil, err
	}
	want, err := ParseSHA256Sidecar(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s.sha256 无法解析: %w", name, err)
	}
	if want != sha {
		return nil, fmt.Errorf("sha256 不符：侧车 %s… != 实算 %s…", truncate(want, 12), truncate(sha, 12))
	}

	// 3. .minisig 验签（复用全仓唯一实现，公钥不联网取）
	sigPath := fpkPath + ".minisig"
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("本地缺少 %s.minisig 签名（0.6.329+ 客户端 fail-closed 会拒绝自更新）", name)
		}
		return nil, err
	}
	if err := verifySignatureFile(fpkPath, sig); err != nil {
		return nil, err
	}

	// 4. 解包核 manifest / ELF / 前端 bundle
	info, err := InspectFPK(fpkPath)
	if err != nil {
		return nil, fmt.Errorf("解包校验失败: %w", err)
	}
	if err := info.CheckAgainst(tag, arch); err != nil {
		return nil, err
	}

	return &Artifact{Path: fpkPath, Size: fi.Size(), SHA256: sha, FPK: info}, nil
}

// normalizeArch 校验并归一架构后缀（x86 / arm）。
func normalizeArch(arch string) (string, error) {
	switch a := filepath.Base(arch); a {
	case "x86", "amd64", "x86_64":
		return "x86", nil
	case "arm", "arm64", "aarch64":
		return "arm", nil
	default:
		return "", fmt.Errorf("未知架构 %q（只支持 x86 / arm）", arch)
	}
}

// fileSHA256 流式计算文件 sha256，返回小写 hex。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
