package api

import "moo/internal/source"

// applyDetailFallback（0.6.337）：详情页信息行「官网 + 架构」两条纯内存兜底。
// 只补源未声明的展示字段，零网络、零 I/O，不碰加载热路径。
// 调用位置：buildCatalog 内、applySourceLineage 之后（上游字段已派生，
// 官网回退链才拿得到 UpstreamURL/UpstreamRepo）。
func applyDetailFallback(out []AppInfo) {
	// 官网：源没写 homepage 时按「上游仓 URL → 上游仓 owner/repo → 作者声明
	// maintainer_url」三级回退填充（搬运卡有上游、普通卡有 maintainer_url）。
	for i := range out {
		ai := &out[i]
		if ai.Homepage != "" {
			continue
		}
		switch {
		case ai.UpstreamURL != "":
			ai.Homepage = ai.UpstreamURL
		case ai.UpstreamRepo != "":
			ai.Homepage = "https://github.com/" + ai.UpstreamRepo
		case ai.MaintainerURL != "":
			ai.Homepage = ai.MaintainerURL
		}
	}

	// 架构：源未声明 arch/archs 时，从 Release 资产文件名推断（`_x86.fpk` /
	// `_arm.fpk` / `amd64` 等）。官方卡无 GitHub Release 链接，ParseGhReleaseURL
	// 返回 false 自然跳过（面板契约无 arch 字段，兜不了）。
	for i := range out {
		ai := &out[i]
		if ai.Arch != "" || len(ai.Archs) > 0 || ai.ReleaseURL == "" {
			continue
		}
		if _, _, asset, ok := source.ParseGhReleaseURL(ai.ReleaseURL); ok {
			if arch := source.ArchFromAssetName(asset); arch != "" {
				ai.Arch = arch
			}
		}
	}
}
