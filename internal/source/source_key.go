package source

import (
	"regexp"
	"strings"
)

// 搜索源链接归一化（0.6.198）：搜索框贴源链接（仓库根 / raw 前缀 /
// JSON 索引 / fndepot v1v2 / 镜像形态）→ 规范 key，与前端
// frontend/src/lib/sourceKey.ts 的 sourceKey() 同语义（双端各测同组
// 用例钉死一致性）。

var (
	// 0.6.241：主机形态放宽到「域名 **或 IPv4**」——内网 Gitea（如 192.168.3.15:3033）
	// 同样是合法源主机，原实现只认域名会把内网源整个排除在「贴链接直搜」之外。
	searchKeyDomainRe = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*\.[a-z]{2,}|(\d{1,3}\.){3}\d{1,3})$`)
	// 尾部 /raw/<分支>/ 段（非 GitHub 主机用）
	searchKeyRawSegRe = regexp.MustCompile(`/raw/[^/]+/`)
)

// 0.6.240：加入 moo.json（Moo 原生源协议），与前端 sourceKey.ts 保持一致
var searchKeyIndexFiles = []string{"moo.json", "fnpack.json", "fndepot_v2.json", "fndepot.json", "apps.json"}

var searchKeyGHHosts = []string{
	"github.com/",
	"raw.githubusercontent.com/",
	"cdn.jsdelivr.net/gh/",
}

// SourceURLKey 把源链接归一化成规范 key（小写）：
//   - GitHub 系（含 raw 域 / jsDelivr / 镜像前缀）→ "github.com/owner/repo"
//     （分支、文件、.git、/tree/... 一律剥掉）
//   - 其他主机 → 剥协议、/raw/<分支>/ 段、索引文件名后的 host+path
// 非链接形态返回 ""。
func SourceURLKey(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	if u == "" {
		return ""
	}
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimRight(u, "/")
	if !strings.Contains(u, "/") {
		return "" // 裸词/裸文件名/无路径主机
	}
	hostNoPort := u
	if i := strings.Index(u, "/"); i >= 0 {
		hostNoPort = u[:i]
	}
	if i := strings.LastIndex(hostNoPort, ":"); i > 0 {
		hostNoPort = hostNoPort[:i]
	}
	if !searchKeyDomainRe.MatchString(hostNoPort) {
		return "" // host 必须域名形态（IP 主机不支持）
	}
	// 镜像前缀（gh-proxy 等）：剥掉首个已知 GitHub 主机之前的部分
	for _, h := range searchKeyGHHosts {
		if i := strings.Index(u, "/"+h); i >= 0 {
			u = u[i+1:]
			break
		}
	}
	if rest, ok := strings.CutPrefix(u, "raw.githubusercontent.com/"); ok {
		if owner, r2, ok2 := strings.Cut(rest, "/"); ok2 {
			if repo, _, ok3 := strings.Cut(r2, "/"); ok3 {
				return "github.com/" + owner + "/" + strings.TrimSuffix(repo, ".git")
			}
			return "github.com/" + owner + "/" + strings.TrimSuffix(r2, ".git")
		}
	}
	if rest, ok := strings.CutPrefix(u, "cdn.jsdelivr.net/gh/"); ok {
		if owner, r2, ok2 := strings.Cut(rest, "/"); ok2 {
			if repo, _, ok3 := strings.Cut(r2, "/"); ok3 {
				return "github.com/" + owner + "/" + strings.TrimSuffix(repo, ".git")
			}
			return "github.com/" + owner + "/" + strings.TrimSuffix(r2, ".git")
		}
	}
	if rest, ok := strings.CutPrefix(u, "github.com/"); ok {
		if owner, r2, ok2 := strings.Cut(rest, "/"); ok2 {
			if repo, _, ok3 := strings.Cut(r2, "/"); ok3 {
				return "github.com/" + owner + "/" + strings.TrimSuffix(repo, ".git")
			}
			return "github.com/" + owner + "/" + strings.TrimSuffix(r2, ".git")
		}
	}
	// 非 GitHub 通用：剥 /raw/<分支>/ 段与索引文件名
	u = searchKeyRawSegRe.ReplaceAllString(u, "/")
	for _, f := range searchKeyIndexFiles {
		if strings.HasSuffix(u, "/"+f) {
			u = strings.TrimSuffix(u, "/"+f)
			break
		}
	}
	u = strings.TrimRight(u, "/")
	return u
}
