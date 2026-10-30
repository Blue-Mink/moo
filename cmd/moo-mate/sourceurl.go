package main

// sourceurl.go —— 0.2.34：端口变化后自动修复 Moo 里已添加的 Moo Mate 源地址。
//
// 加源 url = c.base + "/moo/<app>.json"（或 /moo/mate.json），c.base 里带回环端口。
// 用户从安装/配置向导改了端口（0.2.34 起支持）后，旧 base 的源会 404 →
// 进程启动时把这些「指向本机回环 /moo/ 路径」的源重建到当前 base，避免源失效。
// 幂等：地址已是最新时零操作。重建顺序 = 先加新源、成功后再删旧源（不留空窗）。

import (
	"net/url"
	"strings"
)

// isLoopbackHost 判定 host 是否本机回环。
func isLoopbackHost(h string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// mateHostedPath 取源地址里的托管 path（本机回环 + /moo/...json），否则 ok=false。
// ⚠️ 只凭「回环 + /moo/*.json」不能证明这条源属于 Moo Mate：用户完全可能自己
// 在本机跑一个 /moo/app.json 索引。归属判定另见 isMateSourceName（按源名）。
func mateHostedPath(raw string) (string, bool) {
	pu, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	if !isLoopbackHost(pu.Hostname()) {
		return "", false
	}
	p := pu.EscapedPath()
	if !strings.HasPrefix(p, "/moo/") || !strings.HasSuffix(p, ".json") {
		return "", false
	}
	return p, true
}

// isMateSourceName 判定源名是否指 Moo Mate 自己加的那条聚合源。
// 规范名是 mateSourceName；Moo 加源时可能按 URL 消歧成 "moo-mate.json"，
// 或撞名追加数字后缀 "moo-mate-2"（0.2.34 重建流程中 rename 前的中间态）。
func isMateSourceName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == mateSourceName || n == mateSourceName+".json" {
		return true
	}
	rest, ok := strings.CutPrefix(n, mateSourceName+"-")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// mateURLForPath 用当前 base 的 scheme/host/port + 托管路径 p 拼出完整地址。
// 直接字符串拼接会踩两个坑：base 带路径前缀时拼成 /app/moo/moo/x.json，
// base 写错（非回环）时把源改到不可达地址。这里整体替换 path 并断言回环。
func (c *companion) mateURLForPath(p string) (string, bool) {
	base, err := url.Parse(strings.TrimSpace(c.base))
	if err != nil || base.Host == "" || !isLoopbackHost(base.Hostname()) {
		return "", false
	}
	if !strings.HasPrefix(p, "/moo/") || !strings.HasSuffix(p, ".json") {
		return "", false
	}
	base.Path = p
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String(), true
}

// reconcileSourceURLs 把 Moo 源列表里 base 过期的 Moo Mate 源重建到当前 base。
// 返回被修复的源数量；Moo 不可达/无差异时返回 (0, err|nil)。
//
// 顺序 = 先加新源（此时旧源还在，Moo 会给新源自动消歧名，如 moo-mate.json），
// 成功后再删旧源、把新源名改回原名。这样 add 失败时旧源仍在（不会丢源），
// 也不会因「先删后加」撞上 AddSource 的立即拉取失败回滚（新端口刚起来时尤其重要）。
func (c *companion) reconcileSourceURLs() (int, error) {
	if strings.TrimSpace(c.base) == "" {
		return 0, nil
	}
	srcs, err := c.moo.listSources()
	if err != nil {
		return 0, err
	}
	fixed := 0
	for _, s := range srcs {
		// 只碰 Moo Mate 自己的源：地址形状相同（回环 + /moo/*.json）但名字不是
		// Moo Mate 的，一律不动 —— 否则用户自建的 http://127.0.0.1:38xxx/moo/app.json
		// 会被「加新源 + 删旧源」整条换掉，等于把用户的源弄丢。
		if !isMateSourceName(s.Name) {
			continue
		}
		p, ok := mateHostedPath(s.URL)
		if !ok {
			continue
		}
		want, ok := c.mateURLForPath(p)
		if !ok {
			continue
		}
		if normalizeSrcURL(s.URL) == normalizeSrcURL(want) {
			continue
		}
		added, aerr := c.moo.addSource(s.Name, want)
		if aerr != nil || added == nil {
			continue // 加新源失败：保留旧源不动，下次启动再试
		}
		_ = c.moo.deleteSource(s.ID)
		// ⚠️ Moo 的源身份就是**源名**（POST /api/sources 返回 id==name，rename 后键名随之
		// 变成新名，旧 id 立即 404）。所以改名后必须用新名做 sync，不能再用改名前的 id。
		sid := added.ID
		if sid == "" {
			sid = added.Name
		}
		if added.Name != s.Name {
			if renamed, rerr := c.moo.renameSource(sid, s.Name); rerr == nil && renamed != "" {
				added.Name = renamed
				sid = renamed
			}
		}
		if sid != "" {
			_, _ = c.moo.syncSource(sid)
		}
		fixed++
	}
	return fixed, nil
}

// normalizeSrcURL 只做对比用的归一化（scheme/host 小写、去尾斜杠；path 保持原样，
// 因为托管的 moo.json 文件名含应用名，可能有大写字母）。
func normalizeSrcURL(u string) string {
	pu, err := url.Parse(strings.TrimSpace(u))
	if err != nil {
		return strings.TrimSpace(u)
	}
	return strings.ToLower(pu.Scheme) + "://" + strings.ToLower(pu.Host) + strings.TrimRight(pu.EscapedPath(), "/")
}
