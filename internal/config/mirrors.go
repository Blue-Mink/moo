package config

import (
	"fmt"
	"strings"
	"sync"
)

// MirrorOption 是一个加速源选项（GitHub 文件加速 / Docker 镜像加速）。
// 列表与 New Store 对齐（2026-09 实测排序），声明顺序 = 无健康数据时的静态回退顺序。
type MirrorOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// 0.6.512：加速源清单改为「远端清单驱动 + 本机覆盖」。
//   - SetRemoteMirrorOptions：lists/github_mirrors.txt / docker_mirrors.txt 拉到的
//     内容，覆盖内置清单（空 = 回落内置，保证离线/拉取失败时行为不变）；
//   - SetHiddenMirrors：本机「删除默认源」的屏蔽集合，只影响本机展示与候选，
//     远端清单一更新也不会把它顶回来；「恢复」= 清空该集合。
var (
	mirrorMu     sync.RWMutex
	remoteGH     []MirrorOption
	remoteDK     []MirrorOption
	hiddenMirror = map[string]bool{}
	customGH     []string
	customDK     []string
)

// 0.6.521：多条自定义加速源。选项表按 custom / custom-2 / custom-3 … 注入，
// 第一条沿用旧 key「custom」（存量 mirror="custom" 配置无需迁移）。
func SetCustomMirrors(gh, dk []string) {
	mirrorMu.Lock()
	customGH = normalizeCustomList(gh)
	customDK = normalizeCustomList(dk)
	mirrorMu.Unlock()
}

func normalizeCustomList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// GitHubCustomMirrors / DockerCustomMirrors 返回配置里的完整自定义列表
// （旧单值字段在前；用于启动预热与保存后同步内存选项表）。
func (c *Config) GitHubCustomMirrors() []string {
	return mergeCustomMirrors(c.CustomGitHubMirror, c.CustomGitHubMirrors)
}

func (c *Config) DockerCustomMirrors() []string {
	return mergeCustomMirrors(c.CustomDockerMirror, c.CustomDockerMirrors)
}

func mergeCustomMirrors(single string, list []string) []string {
	out := make([]string, 0, len(list)+1)
	if v := strings.TrimSpace(single); v != "" {
		out = append(out, v)
	}
	out = append(out, list...)
	return normalizeCustomList(out)
}

// injectCustomMirrors 把自定义地址注入选项表：第一条占旧「custom」位，
// 其余依次 custom-2/custom-3…，并跳过远端清单里可能存在的同前缀项。
func injectCustomMirrors(opts []MirrorOption, customs []string) []MirrorOption {
	out := make([]MirrorOption, 0, len(opts)+len(customs))
	inserted := false
	for _, o := range opts {
		if strings.HasPrefix(o.Key, "custom-") {
			continue
		}
		if o.Key != "custom" {
			out = append(out, o)
			continue
		}
		if len(customs) == 0 {
			out = append(out, o)
			inserted = true
			continue
		}
		first := o
		first.URL = customs[0]
		out = append(out, first)
		for i, u := range customs[1:] {
			n := i + 2
			out = append(out, MirrorOption{
				Key:   fmt.Sprintf("custom-%d", n),
				Label: fmt.Sprintf("自定义 %d", n),
				URL:   u,
			})
		}
		inserted = true
	}
	if !inserted && len(customs) > 0 {
		idx := len(out)
		for i, o := range out {
			if o.Key == "direct" {
				idx = i
				break
			}
		}
		ins := make([]MirrorOption, 0, len(customs))
		for i, u := range customs {
			key, label := "custom", "自定义"
			if i > 0 {
				key, label = fmt.Sprintf("custom-%d", i+1), fmt.Sprintf("自定义 %d", i+1)
			}
			ins = append(ins, MirrorOption{Key: key, Label: label, URL: u})
		}
		out = append(out[:idx], append(ins, out[idx:]...)...)
	}
	return out
}

// SetRemoteMirrorOptions 用远端清单覆盖内置加速源（空切片 = 回落内置）。
func SetRemoteMirrorOptions(gh, dk []MirrorOption) {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	remoteGH, remoteDK = gh, dk
}

// RemoteMirrorOptions 当前生效的远端清单（调试/接口回显用）。
func RemoteMirrorOptions() (gh, dk []MirrorOption) {
	mirrorMu.RLock()
	defer mirrorMu.RUnlock()
	return append([]MirrorOption(nil), remoteGH...), append([]MirrorOption(nil), remoteDK...)
}

// SetHiddenMirrors 设置本机屏蔽的加速源 key（auto/custom/direct 永不可屏蔽）。
func SetHiddenMirrors(keys []string) {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || k == "auto" || k == "custom" || k == "direct" {
			continue
		}
		m[k] = true
	}
	mirrorMu.Lock()
	hiddenMirror = m
	mirrorMu.Unlock()
}

// HiddenMirrors 本机屏蔽的 key 列表（顺序不稳定，仅用于设置回显）。
func HiddenMirrors() []string {
	mirrorMu.RLock()
	defer mirrorMu.RUnlock()
	out := make([]string, 0, len(hiddenMirror))
	for k := range hiddenMirror {
		out = append(out, k)
	}
	return out
}

// effectiveMirrors 远端清单优先、内置兜底，再按本机屏蔽集合过滤。
func effectiveMirrors(remote []MirrorOption, builtin func() []MirrorOption) []MirrorOption {
	mirrorMu.RLock()
	defer mirrorMu.RUnlock()
	src := builtin()
	if len(remote) > 0 {
		src = remote
	}
	if len(hiddenMirror) == 0 {
		return append([]MirrorOption(nil), src...)
	}
	out := make([]MirrorOption, 0, len(src))
	for _, o := range src {
		if hiddenMirror[o.Key] {
			continue
		}
		out = append(out, o)
	}
	return out
}

// GitHubMirrorOptions GitHub 文件加速源（含 auto/custom/direct 特殊项）。
func GitHubMirrorOptions() []MirrorOption {
	mirrorMu.RLock()
	customs := append([]string(nil), customGH...)
	remote := append([]MirrorOption(nil), remoteGH...)
	mirrorMu.RUnlock()
	return injectCustomMirrors(effectiveMirrors(remote, builtinGitHubMirrorOptions), customs)
}

// DockerMirrorOptions Docker 镜像加速源（M4 接入 docker 拉取管线前仅展示/探测）。
func DockerMirrorOptions() []MirrorOption {
	mirrorMu.RLock()
	customs := append([]string(nil), customDK...)
	remote := append([]MirrorOption(nil), remoteDK...)
	mirrorMu.RUnlock()
	return injectCustomMirrors(effectiveMirrors(remote, builtinDockerMirrorOptions), customs)
}

// builtinGitHubMirrorOptions 内置 GitHub 加速源（远端清单不可用时的兜底）。
func builtinGitHubMirrorOptions() []MirrorOption {
	return []MirrorOption{
		{Key: "auto", Label: "智能 · 自动选最快", Description: "系统周期性测速各源，自动选用最快且稳定的加速源"},
		{Key: "gh-proxy-hk", Label: "GH-Proxy HK", URL: "https://hk.gh-proxy.org/", Description: "HK 节点，实测最快（419ms）"},
		{Key: "cdn-ghproxy", Label: "CDN GHProxy", URL: "https://cdn.gh-proxy.org/", Description: "CDN 节点 GitHub 加速（450ms）"},
		// 2026-10-31：从 FnDepot 服务端下发的加速源里复测挑选（顺序 curl + 6 路并发×3轮）。
		// gh-tbap：release 1MB 1.5s（577-696KB/s，本批最快），但会时段性抖动
		// （同日 12:5x 曾 44% 超时 / HTTP 500）→ 由 auto 的健康降权与下载看门狗兜底。
		// 真机测速（0.6.381，/api/mirrors/check）：372ms / 2527KB/s，列表第一。
		{Key: "gh-tbap", Label: "GH TBAP", URL: "https://github.tbap.top/", Description: "社区 GitHub 加速（实测 1.5s/1MB，偶有抖动）"},
		// 2026-10-31 未采纳 fndepot1.36337681.xyz：其 HTTPS 证书是腾讯云 CDN 泛域名
		// CN=*.cdn.myqcloud.com，SAN 不含该域名（openssl verify error 62 / Go x509 校验失败），
		// 严格 TLS 客户端一律拒绝；HTTP 虽可用但明文索引（moo.json/fnpack.json 无签名）
		// 可被中间人替换并指向任意 FPK，用户明确否决 → 不加入。
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

// builtinDockerMirrorOptions 内置 Docker 镜像加速源（远端清单不可用时的兜底）。
func builtinDockerMirrorOptions() []MirrorOption {
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
		// 0.6.325 C12：registry-cyou 已下线移除——上游 TLS 证书失效
		// （https://registry.cyou/v2/ 恒 tls: failed to verify certificate），
		// 主 NAS 连测 4 次全 fail，属恒不可用而非偶发抖动。老配置里残留的
		// 该 key 由读取侧归一（GET /api/settings → auto），不主动改用户配置。
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
