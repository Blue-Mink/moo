package release

import "strings"

// ArchSuffix 返回发布约定的资产架构后缀（含下划线）：amd64→"_x86"，arm64/arm→"_arm"。
// 与客户端 selfUpdateArchSuffix 同源——这是「规则单源」的第一条：架构映射只有一份。
func ArchSuffix(goarch string) string {
	switch strings.ToLower(goarch) {
	case "arm64", "arm":
		return "_arm"
	default:
		return "_x86"
	}
}

// ArchSupported 判断该 GOARCH 是否有发布资产（amd64/arm64/arm）。
// 其它架构（386/riscv64 等）不提供自更新，与 0.6.354 前的行为一致。
func ArchSupported(goarch string) bool {
	switch strings.ToLower(goarch) {
	case "amd64", "arm64", "arm":
		return true
	}
	return false
}

// ArchFromSuffix 把资产后缀（"_x86"/"_arm"）反解成 manifest platform 取值。
func ArchFromSuffix(suffix string) string {
	if strings.TrimPrefix(suffix, "_") == "arm" {
		return "arm"
	}
	return "x86"
}

// FallbackAssets 按发布约定直拼三件套 URL（web 302 / 索引 / 镜像等只拿得到
// 版本号、拿不到资产清单的通道使用）。返回的三件套必然完整——这是 10-29 事故的
// 结构性防线：兜底通道不可能再产出 shaURL=""/minisigURL="" 的半成品。
func FallbackAssets(webBase, repo, tag, archSuffix string) Assets {
	prefix := strings.TrimSuffix(webBase, "/") + "/" + repo + "/releases/download/v" + NormalizedTag(tag)
	name := DefaultAppName + "_" + NormalizedTag(tag) + archSuffix + ".fpk"
	base := prefix + "/" + name
	return Assets{FPK: base, SHA256: base + ".sha256", Minisig: base + ".minisig"}
}
