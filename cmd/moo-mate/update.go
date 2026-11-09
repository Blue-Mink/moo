package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// buildVersion 由构建脚本注入：-ldflags "-X main.buildVersion=0.2.32"
// 未注入时（本地 go run）显示 0.0.0，永远提示有更新，便于开发期验证 chip。
var buildVersion = "0.0.0"

// mateUpdateInfo 是 GET /api/update-check 的响应，供前端版本 chip 的三态渲染：
// 灰（已是最新）/ 红（有更新）/ 琥珀（探测失败，点击重探）。
type mateUpdateInfo struct {
	CurrentVersion   string `json:"current_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	HasUpdate        bool   `json:"has_update"`
	LastCheck        int64  `json:"last_check"`
	LastError        string `json:"last_error,omitempty"`
	Notes            string `json:"notes,omitempty"`
	PageURL          string `json:"page_url,omitempty"`
	DownloadURL      string `json:"download_url,omitempty"`
}

// verParts 把 "0.2.32" 拆成数字段（忽略前缀 v 与 -rc/.beta 之后的尾巴）。
func verParts(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	out := make([]int, 0, 4)
	for _, s := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		out = append(out, n)
	}
	return out
}

// verLess 判断语义化版本 a < b（缺位补 0：0.2 = 0.2.0）。
func verLess(a, b string) bool {
	pa, pb := verParts(a), verParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// mateReleaseAPI：moo-mate 的发布渠道 = Blue-Mink/moo 仓库里 tag 前缀 mate-v* 的 release。
//
// per_page=100（本轮修复起，原 30）：Moo 与 Moo Mate 的 release 同放一个仓，Moo 发版
// 频繁时会把 mate 条目挤出窗口（GitHub 列表按 created_at 倒序）→ 退化成「检测不到更新」。
// 100 条按每周 2~3 个 release 估算可覆盖一年以上，且单次响应体仍在 4MB 读取上限内。
const mateReleaseAPI = "https://api.github.com/repos/Blue-Mink/moo/releases?per_page=100"

// mateReleaseAtom：releases.atom 是 GitHub 的公开订阅源，**不计入 API 速率配额**、
// 不需要 token。国内出口的匿名配额（60/h 共享 IP）经常是 0，此时 API 通道恒 403，
// 这条通道仍能拿到版本号（2026-10-30 实测：API 403 时 atom 仍 200）。
const mateReleaseAtom = "https://github.com/Blue-Mink/moo/releases.atom"

// mateUpdateProvider 一条探测通道。auth=true 表示允许携带本机 Moo 复用的 GitHub
// token；第三方镜像必须 auth=false —— 绝不把凭据发给不信任的中转。
type mateUpdateProvider struct {
	name string
	url  string
	auth bool
}

// mateUpdateProviders 依次尝试的探测通道（前者失败才走后者）。
var mateUpdateProviders = []mateUpdateProvider{
	{name: "api", url: mateReleaseAPI, auth: true},
	{name: "gh-proxy", url: "https://gh-proxy.com/" + mateReleaseAPI, auth: false},
	{name: "atom", url: mateReleaseAtom, auth: false},
}

type mateReleaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type mateGhRelease struct {
	TagName string             `json:"tag_name"`
	Body    string             `json:"body"`
	HTMLURL string             `json:"html_url"`
	Assets  []mateReleaseAsset `json:"assets"`
	// 草稿与预发布必须跳过（本轮修复）：mate 探测复用 Moo 的 token，而 GitHub 的
	// GET /releases 对**带写权限的 token 会把 draft 一并返回**；一旦发布机改走
	// 「先 draft/prerelease 传资产、再转正」，旧逻辑会把没传完资产的半成品当正式版
	// 推给用户 → 下载 404 / sha256 对不上 / 验签失败。
	Prerelease bool `json:"prerelease"`
	Draft      bool `json:"draft"`
}

// fetchMateReleases 拉取 release 列表（8s 超时，读 4MB 上限）。
// token 非空时带 Authorization —— 匿名配额只有 60 次/小时/出口 IP，共享出口常
// 恒为 0；带上 token 后是 5000 次/小时（本机已有 Moo 复用的 token）。
func fetchMateReleases(client *http.Client, url, token string) ([]mateGhRelease, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "moo-mate/"+buildVersion)
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rels []mateGhRelease
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, fmt.Errorf("解析 release 列表失败：%w", err)
	}
	return rels, nil
}

// latestMateRelease 从 release 列表里挑出「已正式发布的 mate-v*」中版本最高的一版。
//
// 三条过滤（本轮修复，pickMateRelease 与 mateReleaseAssets 共用同一份逻辑，
// 避免两处规则漂移）：
//   - tag 必须是 mate-v<数字…>：Moo 主线的 v0.6.x 以及 mate-vnext 这类非版本 tag 一律跳过；
//   - draft 跳过：带写权限 token 调 GET /releases 会把草稿一起返回；
//   - prerelease 跳过：半成品不能推给用户。
func latestMateRelease(rels []mateGhRelease) (*mateGhRelease, string) {
	var best *mateGhRelease
	bestVer := ""
	for i := range rels {
		r := &rels[i]
		if r.Draft || r.Prerelease || !strings.HasPrefix(r.TagName, "mate-v") {
			continue
		}
		v := strings.TrimPrefix(r.TagName, "mate-v")
		if v == "" || v[0] < '0' || v[0] > '9' {
			continue
		}
		if best == nil || verLess(bestVer, v) {
			best, bestVer = r, v
		}
	}
	return best, bestVer
}

// pickMateRelease 从 release 列表里挑出 mate-v* 中版本最高的一版。
func pickMateRelease(rels []mateGhRelease) (ver, notes, page, dl string, ok bool) {
	r, v := latestMateRelease(rels)
	if r == nil {
		return "", "", "", "", false
	}
	ver, ok = v, true
	notes = strings.TrimSpace(r.Body)
	page = r.HTMLURL
	for _, a := range r.Assets {
		if strings.HasSuffix(a.Name, "_x86.fpk") {
			dl = a.URL
			break
		}
	}
	if len(notes) > 600 {
		notes = notes[:600] + "…"
	}
	return ver, notes, page, dl, ok
}

// mateAtomFeed releases.atom 的最小结构（标题 / 正文 / 链接）。
type mateAtomFeed struct {
	Entries []struct {
		Title   string `xml:"title"`
		Content string `xml:"content"`
		Links   []struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
	} `xml:"entry"`
}

var atomTagRE = regexp.MustCompile(`<[^>]*>`)

// atomText 把 atom 里的 HTML 正文压成纯文本（与 API 通道的 notes 口径一致）。
func atomText(s string) string {
	s = atomTagRE.ReplaceAllString(s, " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// pickMateFromAtom 从 atom 条目里挑最高的 mate-v* 版本。
// 只认链接形如 …/releases/tag/mate-vX.Y.Z，Moo 主线的 v0.6.x 自动被忽略；
// 带 .atom 后缀的 self 链接会被去掉后缀后再比较。
func pickMateFromAtom(feed mateAtomFeed) (ver, page, notes string, ok bool) {
	for _, e := range feed.Entries {
		for _, l := range e.Links {
			href := strings.TrimSpace(l.Href)
			i := strings.Index(href, "/releases/tag/mate-v")
			if i < 0 {
				continue
			}
			v := strings.TrimSpace(href[i+len("/releases/tag/mate-v"):])
			v = strings.TrimSuffix(v, ".atom")
			if v == "" || strings.ContainsAny(v, "/?#") {
				continue
			}
			if !ok || verLess(ver, v) {
				ver, page, notes, ok = v, strings.TrimSuffix(href, ".atom"), atomText(e.Content), true
			}
		}
	}
	return ver, page, notes, ok
}

// fetchMateAtom 拉取 releases.atom（免费、不吃 API 配额）并解析最新 mate-v*。
func fetchMateAtom(client *http.Client, url string) (ver, page, notes string, ok bool, err error) {
	req, rerr := http.NewRequest("GET", url, nil)
	if rerr != nil {
		return "", "", "", false, rerr
	}
	req.Header.Set("User-Agent", "moo-mate/"+buildVersion)
	resp, rerr := client.Do(req)
	if rerr != nil {
		return "", "", "", false, rerr
	}
	defer resp.Body.Close()
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if rerr != nil {
		return "", "", "", false, rerr
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", "", false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var feed mateAtomFeed
	if xerr := xml.Unmarshal(body, &feed); xerr != nil {
		return "", "", "", false, fmt.Errorf("解析 atom 失败：%w", xerr)
	}
	ver, page, notes, ok = pickMateFromAtom(feed)
	return ver, page, notes, ok, nil
}

// probeMateUpdate 依次尝试各通道探测最新版本；全失败时返回带 LastError 的结果。
// token 只用于 auth=true 的官方通道，第三方镜像一律匿名。
func probeMateUpdate(cur, token string) mateUpdateInfo {
	info := mateUpdateInfo{CurrentVersion: cur, LastCheck: time.Now().Unix()}
	client := mateExternalClient(6 * time.Second)
	var errs []string
	for _, p := range mateUpdateProviders {
		u := p.url
		if p.name == "atom" {
			ver, page, notes, ok, err := fetchMateAtom(client, u)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if !ok {
				errs = append(errs, "atom 里没有 mate-v* 条目")
				continue
			}
			info.AvailableVersion, info.PageURL, info.Notes = ver, page, notes
			info.HasUpdate = verLess(cur, ver)
			return info
		}
		tok := ""
		if p.auth {
			tok = token
		}
		rels, err := fetchMateReleases(client, u, tok)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		ver, notes, page, dl, ok := pickMateRelease(rels)
		if !ok {
			errs = append(errs, "release 列表里没有 mate-v* 资产")
			continue
		}
		info.AvailableVersion = ver
		info.Notes = notes
		info.PageURL = page
		info.DownloadURL = dl
		info.HasUpdate = verLess(cur, ver)
		return info
	}
	info.LastError = strings.Join(errs, " / ")
	if info.LastError == "" {
		info.LastError = "探测失败"
	}
	return info
}
