package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// FPKInfo 是 FPK 内部可核对的事实：发布门用它证明「包里确实是这一版、这一架构」，
// 而不是只看文件名与大小（2026-09-30 错包事故的直接对策）。
type FPKInfo struct {
	App            string
	Version        string
	Platform       string
	BinarySize     int64
	ELFMachine     string // x86_64 / aarch64
	BinarySHA256   string
	FrontendBundle string
}

const (
	elfMachineX86   = 0x3E
	elfMachineARM64 = 0xB7
	binaryMinSize   = 5 << 20
)

var (
	reFPKApp  = regexp.MustCompile(`(?m)^\s*appname\s*=\s*(\S+)`)
	reFPKVer  = regexp.MustCompile(`(?m)^\s*version\s*=\s*(\S+)`)
	reFPKPlat = regexp.MustCompile(`(?m)^\s*platform\s*=\s*(\S+)`)
	reBundle  = regexp.MustCompile(`index-[A-Za-z0-9_-]{6,}\.js`)
)

// InspectFPK 解包 FPK（gzip tar → manifest / app.tgz → moo-server）并核对内部事实。
func InspectFPK(path string) (*FPKInfo, error) {
	man, err := tarMember(path, "manifest")
	if err != nil {
		return nil, fmt.Errorf("FPK 缺少 manifest: %w", err)
	}
	info := &FPKInfo{}
	if m := reFPKApp.FindSubmatch(man); m != nil {
		info.App = string(m[1])
	}
	if m := reFPKVer.FindSubmatch(man); m != nil {
		info.Version = string(m[1])
	}
	if m := reFPKPlat.FindSubmatch(man); m != nil {
		info.Platform = string(m[1])
	}
	if info.Version == "" || info.Platform == "" {
		return nil, errors.New("manifest 缺 version 或 platform 字段")
	}

	appTgz, err := tarMember(path, "app.tgz")
	if err != nil {
		return nil, fmt.Errorf("FPK 缺少 app.tgz: %w", err)
	}
	bin, err := tarMemberBytes(appTgz, "moo-server")
	if err != nil {
		return nil, fmt.Errorf("app.tgz 缺少 moo-server: %w", err)
	}
	info.BinarySize = int64(len(bin))
	if info.BinarySize < binaryMinSize {
		return nil, fmt.Errorf("moo-server 体积异常（%d 字节 < %d）", info.BinarySize, binaryMinSize)
	}
	if len(bin) < 20 || bin[0] != 0x7f || bin[1] != 'E' || bin[2] != 'L' || bin[3] != 'F' {
		return nil, errors.New("moo-server 不是 ELF 可执行文件")
	}
	switch binary.LittleEndian.Uint16(bin[18:20]) {
	case elfMachineX86:
		info.ELFMachine = "x86_64"
	case elfMachineARM64:
		info.ELFMachine = "aarch64"
	default:
		info.ELFMachine = fmt.Sprintf("unknown(0x%x)", binary.LittleEndian.Uint16(bin[18:20]))
	}
	info.BinarySHA256 = sha256Hex(bin)
	if m := reBundle.Find(bin); m != nil {
		info.FrontendBundle = string(m)
	}
	return info, nil
}

// CheckAgainst 断言包内事实与期望版本/架构一致（arch 为发布约定后缀 x86/arm）。
func (i *FPKInfo) CheckAgainst(version, arch string) error {
	if NormalizedTag(i.Version) != NormalizedTag(version) {
		return fmt.Errorf("包内 manifest version=%s != 目标 %s", i.Version, version)
	}
	want := map[string]string{"x86": "x86", "arm": "arm"}[arch]
	if want == "" {
		return fmt.Errorf("未知架构 %q", arch)
	}
	if i.Platform != want {
		return fmt.Errorf("包内 manifest platform=%s != 目标 %s", i.Platform, want)
	}
	wantELF := map[string]string{"x86": "x86_64", "arm": "aarch64"}[arch]
	if i.ELFMachine != wantELF {
		return fmt.Errorf("包内二进制 ELF 架构=%s != 目标 %s（装错包）", i.ELFMachine, wantELF)
	}
	if i.FrontendBundle == "" {
		return errors.New("二进制内未找到前端 bundle 名（index-*.js），前端可能未内嵌")
	}
	return nil
}

// tarMember 从 gzip tar 中按文件名（忽略路径前缀）取成员内容。
func tarMember(path, base string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == base {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
	return nil, errors.New("成员不存在: " + base)
}

// tarMemberBytes 从内存中的 gzip tar 取成员。
func tarMemberBytes(tgz []byte, base string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == base {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
	return nil, errors.New("成员不存在: " + base)
}
