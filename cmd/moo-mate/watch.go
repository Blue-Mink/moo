package main

// 0.2.35 起：`moo-mate -verify-release` = 「客户端同款验收」入口（CI 夜间复检用）。
//
// 为什么放在 mate 本体里而不是单独一个工具：验收必须与被验收的代码**同一份实现**，
// 否则又是两处规则漂移（Moo 的 0.6.370 事故就是兜底通道只拼 FPK、侧车传空串，
// 而单测把空侧车写成了期望值）。这里直接调用客户端真实的
// mateSelfUpdateAssetURLs / mateDownloadCandidates / fetchAnyMirror /
// parseSha256Sidecar / verifyMateSignature，任何一条通道漏侧车都会当场失败。
//
// 检查两条通道 × 每个架构：
//   client   —— 客户端实际会走的那条（有 release 资产清单就用清单 URL）
//   fallback —— 资产清单不可达时按发布约定拼的三件套 URL（国内几乎恒走这条）
//
// 每条通道都要：下载 FPK → .sha256 侧车比对（含与 moo.json 索引逐字一致）→
// .minisig 用内嵌公钥验签 → 解包核 manifest(version/platform) 与二进制 ELF 架构。
// 只读：不写仓库、不碰 release、不发任何写请求。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// mateWatchIndex 只取验收需要的字段：版本 + 每架构 size/sha256。
type mateWatchIndex struct {
	Apps map[string]struct {
		Version  string `json:"version"`
		Releases map[string]struct {
			Packages map[string]struct {
				Size   int64  `json:"size"`
				SHA256 string `json:"sha256"`
			} `json:"packages"`
		} `json:"releases"`
	} `json:"apps"`
}

// mateWatchExpect 单个架构的期望值（来自索引）。
type mateWatchExpect struct {
	Size   int64
	SHA256 string
}

// mateFPKInfo 解包后的事实。
type mateFPKInfo struct {
	App         string
	Version     string
	Platform    string
	BinarySize  int64
	ELFMachine  string
	HasUIConfig bool
}

const mateWatchMinBinary = 1 << 20 // mate 二进制 ~8MB，低于 1MB 视为异常

func loadMateWatchIndex(path string) (*mateWatchIndex, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx mateWatchIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("解析索引失败：%w", err)
	}
	return &idx, nil
}

// mateIndexVersion 读索引里 apps.moo-mate.version。
func mateIndexVersion(idx *mateWatchIndex) (string, error) {
	app, ok := idx.Apps["moo-mate"]
	if !ok {
		return "", errors.New("索引里没有 apps.moo-mate")
	}
	v := strings.TrimSpace(app.Version)
	if v == "" {
		return "", errors.New("索引 apps.moo-mate.version 为空")
	}
	return v, nil
}

// mateIndexExpect 读索引里 apps.moo-mate.releases.<ver>.packages.<arch>。
func mateIndexExpect(idx *mateWatchIndex, ver, arch string) (mateWatchExpect, error) {
	app, ok := idx.Apps["moo-mate"]
	if !ok {
		return mateWatchExpect{}, errors.New("索引里没有 apps.moo-mate")
	}
	rel, ok := app.Releases[ver]
	if !ok {
		return mateWatchExpect{}, fmt.Errorf("索引缺 releases.%s 节点", ver)
	}
	pkg, ok := rel.Packages[arch]
	if !ok || pkg.SHA256 == "" {
		return mateWatchExpect{}, fmt.Errorf("索引缺 releases.%s.packages.%s.sha256", ver, arch)
	}
	return mateWatchExpect{Size: pkg.Size, SHA256: strings.ToLower(pkg.SHA256)}, nil
}

// mateConventionTriple 按发布约定拼三件套（资产清单不可达时客户端走这条）。
func mateConventionTriple(tag, arch string) (string, string, string, error) {
	suffix, ok := mateArchSuffix(archOf(arch))
	if !ok {
		return "", "", "", fmt.Errorf("未知架构 %q", arch)
	}
	name := "moo-mate_" + tag + suffix
	return mateFallbackAssetURL(tag, name),
		mateFallbackAssetURL(tag, name+".sha256"),
		mateFallbackAssetURL(tag, name+".minisig"),
		nil
}

// archOf 把发布后缀（x86/arm）映射成 GOARCH，复用客户端同一套后缀函数。
func archOf(arch string) string {
	switch strings.ToLower(strings.TrimSpace(arch)) {
	case "arm", "arm64", "aarch64":
		return "arm64"
	default:
		return "amd64"
	}
}

// verifyMateChannel 按客户端路径验收一条通道。
func verifyMateChannel(name, tag, arch string, exp mateWatchExpect,
	urls func() (string, string, string, error)) error {
	fpkURL, shaURL, sigURL, err := urls()
	if err != nil {
		return fmt.Errorf("[%s] 取 URL 失败：%w", name, err)
	}
	if shaURL == "" || sigURL == "" {
		// 0.6.369 事故同型：缺侧车 = 客户端 fail-closed 必失败。
		return fmt.Errorf("[%s] 三件套不全（sha=%q sig=%q）", name, shaURL, sigURL)
	}
	tmp, err := os.MkdirTemp("", "mate-watch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	client := mateExternalClient(mateSelfUpdateDLTime)
	fpkPath := filepath.Join(tmp, "moo-mate.fpk")
	if _, err := fetchAnyMirror(client, fpkURL, fpkPath, mateSelfUpdateMaxFpk, nil); err != nil {
		return fmt.Errorf("[%s] FPK 下载失败：%w", name, err)
	}

	shaPath := filepath.Join(tmp, "fpk.sha256")
	if _, err := fetchAnyMirror(client, shaURL, shaPath, mateSelfUpdateMaxSide, nil); err != nil {
		return fmt.Errorf("[%s] .sha256 下载失败：%w", name, err)
	}
	raw, err := os.ReadFile(shaPath)
	if err != nil {
		return err
	}
	wantSide, err := parseSha256Sidecar(raw)
	if err != nil {
		return fmt.Errorf("[%s] .sha256 不可用：%w", name, err)
	}
	got, err := fileSha256(fpkPath)
	if err != nil {
		return err
	}
	if got != wantSide {
		return fmt.Errorf("[%s] 侧车摘要与包不符（%s… != %s…）", name, wantSide[:12], got[:12])
	}
	if exp.SHA256 != "" && got != exp.SHA256 {
		return fmt.Errorf("[%s] 包摘要与索引不符（%s… != 索引 %s…）", name, got[:12], exp.SHA256[:12])
	}

	sigPath := filepath.Join(tmp, "fpk.minisig")
	if _, err := fetchAnyMirror(client, sigURL, sigPath, mateSelfUpdateMaxSide, nil); err != nil {
		return fmt.Errorf("[%s] .minisig 下载失败：%w", name, err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	if err := verifyMateSignature(matePubkeyForVerify(), fpkPath, sig); err != nil {
		return fmt.Errorf("[%s] %w", name, err)
	}

	info, err := inspectMateFPK(fpkPath)
	if err != nil {
		return fmt.Errorf("[%s] 解包校验失败：%w", name, err)
	}
	if info.App != mateAppName {
		return fmt.Errorf("[%s] 包内 manifest appname=%s（应为 %s）", name, info.App, mateAppName)
	}
	if info.Version != tag {
		return fmt.Errorf("[%s] 包内 manifest version=%s（应为 %s）", name, info.Version, tag)
	}
	if wantPlat := strings.ToLower(arch); info.Platform != wantPlat {
		return fmt.Errorf("[%s] 包内 manifest platform=%s（应为 %s）", name, info.Platform, wantPlat)
	}
	wantELF := map[string]string{"x86": "x86_64", "arm": "aarch64"}[strings.ToLower(arch)]
	if wantELF != "" && info.ELFMachine != wantELF {
		return fmt.Errorf("[%s] 二进制 ELF 架构=%s（应为 %s）", name, info.ELFMachine, wantELF)
	}
	if !info.HasUIConfig {
		return fmt.Errorf("[%s] app.tgz 缺 ui/config（桌面入口不会注册）", name)
	}
	if exp.Size > 0 {
		st, err := os.Stat(fpkPath)
		if err == nil && st.Size() != exp.Size {
			return fmt.Errorf("[%s] 包体积 %d != 索引 %d", name, st.Size(), exp.Size)
		}
	}
	logf("OK  [%s] %s %s：%d B，二进制 %d B，ELF %s，三件套齐、验签通过",
		name, arch, tag, exp.Size, info.BinarySize, info.ELFMachine)
	return nil
}

// inspectMateFPK 解包 FPK：根 manifest + app.tgz 内的 moo-mate 与 ui/config。
func inspectMateFPK(path string) (*mateFPKInfo, error) {
	man, err := tarMemberBytesFromFile(path, "manifest")
	if err != nil {
		return nil, fmt.Errorf("FPK 缺 manifest：%w", err)
	}
	info := &mateFPKInfo{
		App:      manifestField(man, "appname"),
		Version:  manifestField(man, "version"),
		Platform: manifestField(man, "platform"),
	}
	if info.Version == "" || info.Platform == "" {
		return nil, errors.New("manifest 缺 version 或 platform")
	}
	appTgz, err := tarMemberBytesFromFile(path, "app.tgz")
	if err != nil {
		return nil, fmt.Errorf("FPK 缺 app.tgz：%w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(appTgz))
	if err != nil {
		return nil, fmt.Errorf("app.tgz 不是 gzip：%w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var bin []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := filepath.Base(h.Name)
		switch {
		case name == "moo-mate" && h.Typeflag == tar.TypeReg:
			bin, err = io.ReadAll(io.LimitReader(tr, 64<<20))
			if err != nil {
				return nil, err
			}
		case h.Name == "ui/config":
			b, _ := io.ReadAll(io.LimitReader(tr, 1<<20))
			info.HasUIConfig = strings.Contains(string(b), "moo-mate.Application")
		}
	}
	if len(bin) < mateWatchMinBinary {
		return nil, fmt.Errorf("app.tgz 内 moo-mate 体积异常（%d B）", len(bin))
	}
	if len(bin) < 20 || bin[0] != 0x7f || bin[1] != 'E' || bin[2] != 'L' || bin[3] != 'F' {
		return nil, errors.New("moo-mate 不是 ELF 可执行文件")
	}
	switch binary.LittleEndian.Uint16(bin[18:20]) {
	case 0x3E:
		info.ELFMachine = "x86_64"
	case 0xB7:
		info.ELFMachine = "aarch64"
	default:
		info.ELFMachine = fmt.Sprintf("unknown(0x%x)", binary.LittleEndian.Uint16(bin[18:20]))
	}
	info.BinarySize = int64(len(bin))
	return info, nil
}

// tarMemberBytesFromFile 从 gzip tar 里按 basename 取成员内容。
func tarMemberBytesFromFile(path, base string) ([]byte, error) {
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
			return nil, fmt.Errorf("找不到成员 %s", base)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) != base || h.Typeflag != tar.TypeReg {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, 64<<20))
	}
}

// manifestField 取 manifest 里 `key = value` 的值。
func manifestField(man []byte, key string) string {
	for _, line := range strings.Split(string(man), "\n") {
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		if strings.TrimSpace(line[:i]) == key {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

// runReleaseSelfCheck 验收并返回进程退出码（0 全过 / 1 有失败 / 2 参数或索引问题）。
func runReleaseSelfCheck(tagFlag, arches, indexPath string) int {
	// 复用 Moo 侧 token（CI 用 GITHUB_TOKEN），避免匿名配额 60/h 撞限。
	mateTokenFn = func() string { return strings.TrimSpace(os.Getenv("MOO_MATE_TOKEN")) }

	idx, err := loadMateWatchIndex(indexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取索引失败：%v\n", err)
		return 2
	}
	tag := strings.TrimPrefix(strings.TrimSpace(tagFlag), "v")
	if tag == "" || strings.EqualFold(tag, "auto") {
		tag, err = mateIndexVersion(idx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "取索引版本失败：%v\n", err)
			return 2
		}
	}
	if strings.TrimSpace(arches) == "" {
		arches = "x86"
	}
	var list []string
	for _, a := range strings.Split(arches, ",") {
		a = strings.ToLower(strings.TrimSpace(a))
		switch a {
		case "":
		case "both", "all":
			list = append(list, "x86", "arm")
		case "x86", "arm":
			list = append(list, a)
		default:
			fmt.Fprintf(os.Stderr, "未知架构 %q（只支持 x86/arm/both）\n", a)
			return 2
		}
	}
	if len(list) == 0 {
		list = []string{"x86"}
	}

	logf("验收 moo-mate v%s（索引 %s，架构 %s，本机 %s/%s）", tag, indexPath, strings.Join(list, "+"), runtime.GOOS, runtime.GOARCH)
	fails := 0
	for _, arch := range list {
		exp, err := mateIndexExpect(idx, tag, arch)
		if err != nil {
			fails++
			fmt.Fprintf(os.Stderr, "FAIL [index] %v\n", err)
			continue
		}
		checks := []struct {
			name string
			urls func() (string, string, string, error)
		}{
			{"client", func() (string, string, string, error) { return mateSelfUpdateAssetURLs(tag, archOf(arch)) }},
			{"fallback", func() (string, string, string, error) { return mateConventionTriple(tag, arch) }},
		}
		for _, c := range checks {
			if err := verifyMateChannel(c.name, tag, arch, exp, c.urls); err != nil {
				fails++
				fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
			}
		}
	}
	if fails > 0 {
		fmt.Fprintf(os.Stderr, "共 %d 项失败：用户升级会失败或装到错包\n", fails)
		return 1
	}
	logf("全部通过：%d 条通道 × %d 个架构", 2, len(list))
	return 0
}
