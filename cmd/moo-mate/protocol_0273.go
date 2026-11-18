package main

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// 0.2.73：把上游 release 元数据搬进 moo.json 协议字段（README / 更新日志 / 历史版本）。
// 设计约束：
//   - 只读上游已有数据，不额外下载 FPK（历史版本只取 asset 列表里的 url/size/digest）。
//   - 任何字段缺失都留空，由 moo.json 的 omitempty 决定是否出现，不改变旧行为。
//   - 更新日志是「第三方文案」，必须清洗：去资产表、裸链接、Full Changelog、HTML 注释。

// maxNotesRunes 更新日志上限（字符）。超长截断并加省略号，避免索引膨胀。
const maxNotesRunes = 1200

var (
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reMDLinkLine  = regexp.MustCompile(`^\s*[-*]?\s*\[[^\]]*\]\([^)]*\)\s*$`)
	reBareURL     = regexp.MustCompile(`^\s*<?https?://\S+>?\s*$`)
	reAssetRow    = regexp.MustCompile(`(?i)^\s*\|.*\.(fpk|sha256|minisig|zip|tar\.gz).*\|\s*$`)
	reRule        = regexp.MustCompile(`^\s*([-*_]\s*){3,}$`)
)

// shaFromDigest 把 GitHub asset digest（"sha256:abcd…"）归一成裸 sha256。
func shaFromDigest(d string) string {
	d = strings.TrimSpace(d)
	if i := strings.IndexByte(d, ':'); i >= 0 {
		if !strings.EqualFold(d[:i], "sha256") {
			return ""
		}
		d = d[i+1:]
	}
	if len(d) != 64 {
		return ""
	}
	for _, r := range d {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return ""
		}
	}
	return strings.ToLower(d)
}

// cleanReleaseNotes 清洗第三方 release notes：去 HTML 注释、资产表行、纯链接行、
// 分隔线、Full Changelog 段及其后的裸链接；压缩连续空行；截断到 maxNotesRunes。
func cleanReleaseNotes(body string) string {
	s := strings.ReplaceAll(body, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = reHTMLComment.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(strings.ToLower(strings.TrimLeft(t, "*_ ")), "full changelog") {
			break
		}
		if reAssetRow.MatchString(t) || reMDLinkLine.MatchString(t) || reBareURL.MatchString(t) || reRule.MatchString(t) {
			continue
		}
		// Markdown 表格整块丢弃（上游常把资产清单排成表格）。
		if strings.HasPrefix(t, "|") && strings.HasSuffix(t, "|") {
			continue
		}
		out = append(out, strings.TrimRight(ln, " \t"))
	}
	// 压缩 3 行以上连续空行为 1 行空行，并去掉首尾空行。
	compact := make([]string, 0, len(out))
	blank := 0
	for _, ln := range out {
		if strings.TrimSpace(ln) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		compact = append(compact, ln)
	}
	res := strings.TrimSpace(strings.Join(compact, "\n"))
	if utf8.RuneCountInString(res) > maxNotesRunes {
		r := []rune(res)
		res = strings.TrimSpace(string(r[:maxNotesRunes])) + "…"
	}
	return res
}

// prevByAppKey 取每个应用「次高版本」的资产集合（只含带版本号的候选）。
// 用于 moo.json 的 releases 历史节点：历史版本不重新下载，直接用 asset 列表里的
// url / size / digest，因此这里只做纯数据挑选，零额外请求。
func prevByAppKey(items []releaseFPK) map[string][]releaseFPK {
	maxVer := map[string]string{}
	for _, it := range items {
		if it.ver == "" || it.url == "" {
			continue
		}
		k := appKeyFromFilename(it.name)
		if cur, ok := maxVer[k]; !ok || versionGreater(it.ver, cur) {
			maxVer[k] = it.ver
		}
	}
	prevVer := map[string]string{}
	for _, it := range items {
		if it.ver == "" || it.url == "" {
			continue
		}
		k := appKeyFromFilename(it.name)
		if it.ver == maxVer[k] {
			continue
		}
		if cur, ok := prevVer[k]; !ok || versionGreater(it.ver, cur) {
			prevVer[k] = it.ver
		}
	}
	out := map[string][]releaseFPK{}
	for _, it := range items {
		if it.ver == "" || it.url == "" {
			continue
		}
		k := appKeyFromFilename(it.name)
		if pv, ok := prevVer[k]; ok && it.ver == pv {
			out[k] = append(out[k], it)
		}
	}
	return out
}

// prevFields 把历史版本资产集合压成 discoveredApp 的历史字段（版本号/时间/日志/按架构包）。
func prevFields(list []releaseFPK) (ver, at, notes string, pkgs map[string]*appPackage) {
	pkgs = map[string]*appPackage{}
	for _, it := range list {
		if ver == "" {
			ver, at, notes = it.ver, it.updatedAt, it.changelog
		}
		arch := normArch(archFromFilename(it.name))
		if arch == "" {
			arch = "all"
		}
		p := &appPackage{DownloadURL: it.url, Sha256: it.sha256, SizeBytes: it.size}
		if cur, ok := pkgs[arch]; ok && cur.DownloadURL != "" && arch != "all" {
			continue
		}
		pkgs[arch] = p
	}
	return
}
