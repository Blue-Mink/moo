package config

// MirrorOption 是一个加速源选项（GitHub 文件加速 / Docker 镜像加速）。
// 列表与 New Store 对齐（2026-09 实测排序），声明顺序 = 无健康数据时的静态回退顺序。
type MirrorOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// GitHubMirrorOptions GitHub 文件加速源（含 auto/custom/direct 特殊项）。
func GitHubMirrorOptions() []MirrorOption {
	return []MirrorOption{
		{Key: "auto", Label: "智能 · 自动选最快", Description: "系统周期性测速各源，自动选用最快且稳定的加速源"},
		{Key: "gh-proxy-hk", Label: "GH-Proxy HK", URL: "https://hk.gh-proxy.org/", Description: "HK 节点，实测最快（419ms）"},
		{Key: "cdn-ghproxy", Label: "CDN GHProxy", URL: "https://cdn.gh-proxy.org/", Description: "CDN 节点 GitHub 加速（450ms）"},
		{Key: "ghproxy-cxkpro", Label: "GHProxy CXK", URL: "https://ghproxy.cxkpro.top/", Description: "社区 GitHub 加速（实测 534ms）"},
		{Key: "yylx", Label: "YYLX Git", URL: "https://git.yylx.win/", Description: "社区 GitHub 加速（实测 535ms）"},
		{Key: "gh-proxy", Label: "GH-Proxy", URL: "https://gh-proxy.com/", Description: "公共 GitHub 文件代理，长期稳定运营（552ms）"},
		{Key: "gitproxy-mrhjx", Label: "GitProxy MRHJX", URL: "https://gitproxy.mrhjx.cn/", Description: "社区 GitHub 加速（实测 559ms）"},
		{Key: "gh-proxy-org", Label: "GH-Proxy.ORG", URL: "https://gh-proxy.org/", Description: "社区 GitHub 加速（实测 563ms）"},
		{Key: "felicity", Label: "GH Felicity", URL: "https://gh.felicity.ac.cn/", Description: "社区 GitHub 加速（实测 584ms）"},
		{Key: "wget-la", Label: "WGET.LA", URL: "https://wget.la/", Description: "GitHub 下载加速（实测 606ms）"},
		{Key: "dpik", Label: "GitHub DPIK", URL: "https://github.dpik.top/", Description: "社区 GitHub 加速（实测 664ms）"},
		{Key: "ghproxy-net", Label: "GHProxy.net", URL: "https://ghproxy.net/", Description: "社区维护的 GitHub 加速代理（769ms）"},
		{Key: "cors-isteed", Label: "Cors Proxy", URL: "https://cors.isteed.cc/", Description: "Cloudflare Workers GitHub 代理（804ms）"},
		{Key: "gh-dpik", Label: "GH DPIK", URL: "https://gh.dpik.top/", Description: "社区 GitHub 加速（实测 880ms）"},
		{Key: "memory-echoes", Label: "GHProxy ME", URL: "https://github-proxy.memory-echoes.cn/", Description: "Z 图床公益 GitHub 加速（实测 1020ms）"},
		{Key: "ghfast", Label: "GHFast", URL: "https://ghfast.top/", Description: "高速 GitHub 文件加速（1171ms）"},
		{Key: "conversun", Label: "Conversun Hub", URL: "https://hub.conversun.com/", Description: "Conversun 自建 GitHub 加速，仅代理 conversun 仓库"},
		{Key: "gh-ddlc", Label: "GH DDLC", URL: "https://gh.ddlc.top/", Description: "GitHub 文件下载加速（当前限流 429，智能监测会自动降权）"},
		{Key: "custom", Label: "自定义", Description: "使用自定义加速地址"},
		{Key: "direct", Label: "直连 GitHub", Description: "直接从 GitHub 下载，适合有代理的用户"},
	}
}

// DockerMirrorOptions Docker 镜像加速源（M4 接入 docker 拉取管线前仅展示/探测）。
func DockerMirrorOptions() []MirrorOption {
	return []MirrorOption{
		{Key: "auto", Label: "智能 · 自动选最快", Description: "系统周期性测速各源，自动选用最快且稳定的加速源"},
		{Key: "daocloud", Label: "DaoCloud", URL: "https://m.daocloud.io/", Description: "DaoCloud 公共 Docker 镜像加速"},
		// kspeeder = 独立 KSpeeder 应用（Blue-Mink 源，appname=kspeeder）的本地
		// 镜像缓存 registry（127.0.0.1:5443，仅 docker.io）——与 New Store 同款
		// 依赖关系：Moo 不内嵌引擎，只探测本地端口，未安装/未运行 = 失败降权。
		// 安装入口在设置页 Docker 区「安装 KSpeeder」按钮（走标准应用安装管线）。
		{Key: "kspeeder", Label: "KSpeeder (本地)", Description: "独立 KSpeeder 应用的本地镜像缓存（127.0.0.1:5443），未安装/未运行时自动降权"},
		{Key: "nju-ghcr", Label: "NJU ghcr", URL: "https://ghcr.nju.edu.cn/", Description: "南京大学 ghcr.io 专用镜像（ghcr 应用推荐）"},
		{Key: "docker-1ms", Label: "1ms.run", URL: "https://docker.1ms.run/", Description: "社区 Docker 镜像加速"},
		{Key: "daocloud-docker", Label: "DaoCloud Docker", URL: "https://docker.m.daocloud.io/", Description: "DaoCloud Docker 镜像加速（全球可用）"},
		{Key: "ratdev", Label: "Rat.Dev", URL: "https://hub.rat.dev/", Description: "Rat 社区 Docker 镜像加速"},
		{Key: "1panel", Label: "1Panel", URL: "https://docker.1panel.live/", Description: "1Panel 官方 Docker 镜像加速（仅限国内）"},
		{Key: "dockerproxy", Label: "DockerProxy", URL: "https://dockerproxy.net/", Description: "Docker Proxy 社区镜像加速（仅限国内）"},
		{Key: "registry-cyou", Label: "Registry.cyou", URL: "https://registry.cyou/", Description: "Cloudflare Docker 镜像代理（仅限国内）"},
		{Key: "custom", Label: "自定义", Description: "使用自定义加速地址"},
		{Key: "direct", Label: "直连 Docker Hub", Description: "直接拉取，适合有代理的用户"},
	}
}

// KSpeederLocalAddr 独立 KSpeeder 应用默认的本地镜像代理地址（探测用）。
const KSpeederLocalAddr = "127.0.0.1:5443"

// MirrorURL 按 key 查镜像地址（auto/direct/custom 返回空）。
func MirrorURL(options []MirrorOption, key string) string {
	for _, m := range options {
		if m.Key == key {
			return m.URL
		}
	}
	return ""
}
