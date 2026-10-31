package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"moo/internal/config"
)

// releaseDownloadPrefix 返回某个 repo 「v<tag>」的下载前缀。
func releaseDownloadPrefix(base, repo, tag string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s", strings.TrimSuffix(base, "/"), repo, tag)
}

// probeWeb302 走 github.com 的 release 下载 302 通道。
//
// 该通道免 API 限流（国内出口 api.github.com 常 remaining=0），只需对
// 三个 URL 各发一次 HEAD/GET 探测可达性。**三件套必须全部可达**。
func probeWeb302(ctx context.Context, o Opts) (Result, error) {
	o = o.normalize()
	res := Result{Channel: ChannelWeb302}
	repo := o.Repos[0]
	assets := AssetsFor(releaseDownloadPrefix(webBase, repo, o.Tag), o.Tag, o.Arch)
	if err := assetsComplete(assets); err != nil {
		return res, err
	}
	res.Assets = assets
	if err := headAll(ctx, o.Client, []string{assets.FPK, assets.SHA256, assets.Minisig}); err != nil {
		return res, err
	}
	res.OK = true
	res.Version = o.Tag
	return res, nil
}

// headAll 逐个探测 URL 是否可下载。
//
// 说明：GitHub 的 releases/download 会 302 到 CDN，HEAD 可能被 CDN 拒绝
// （405/403），因此先 HEAD，失败再退化为 GET 只读前 1 字节。
func headAll(ctx context.Context, c *http.Client, urls []string) error {
	for _, u := range urls {
		code, err := headOnce(ctx, c, u)
		if err == nil {
			continue
		}
		if code == http.StatusMethodNotAllowed || code == http.StatusForbidden {
			if _, err2 := getProbe(ctx, c, u); err2 == nil {
				continue
			} else {
				return fmt.Errorf("%s: GET %v", shortURL(u), err2)
			}
		}
		return fmt.Errorf("%s: %v", shortURL(u), err)
	}
	return nil
}

func headOnce(ctx context.Context, c *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "moo-release-probe")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
}

// getProbe 只读 1 字节即可，避免把几十 MB 的 FPK 拉下来。
func getProbe(ctx context.Context, c *http.Client, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "moo-release-probe")
	req.Header.Set("Range", "bytes=0-0")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
}

func shortURL(u string) string {
	if i := strings.LastIndex(u, "/"); i >= 0 && i+1 < len(u) {
		return u[i+1:]
	}
	return u
}

// mooIndex 是 moo.json 自索引的最小结构。
type mooIndex struct {
	Apps map[string]struct {
		Version  string `json:"version"`
		Releases map[string]struct {
			Changelog string `json:"changelog"`
		} `json:"releases"`
	} `json:"apps"`
}

// probeSelfIndex 走 moo.json 自索引通道。
//
// 侧车 URL 由 release 下载前缀按约定拼装——索引本身不含侧车字段，
// 但**发布会永远把三件套放在同一前缀下**，因此拼装是可验证的：
// 若拼出来后探测不到，就是「索引领先于 release」的发布事故。
func probeSelfIndex(ctx context.Context, o Opts) (Result, error) {
	o = o.normalize()
	res := Result{Channel: ChannelSelfIdx}
	url := o.IndexURL
	if strings.TrimSpace(url) == "" {
		url = fmt.Sprintf("%s/%s/main/%s", rawBase, DefaultSelfIdxRepo, DefaultSelfIdxFile)
	}
	body, _, err := httpGet(ctx, o.Client, url)
	if err != nil {
		return res, err
	}
	var idx mooIndex
	decErr := json.NewDecoder(body).Decode(&idx)
	body.Close()
	if decErr != nil {
		return res, fmt.Errorf("解析自索引失败: %v", decErr)
	}
	app, ok := idx.Apps[o.AppName]
	if !ok {
		return res, fmt.Errorf("自索引中无 apps.%s 条目", o.AppName)
	}
	if NormalizedTag(app.Version) != o.Tag {
		res.Extra = append(res.Extra, "index_version="+app.Version)
		return res, fmt.Errorf("自索引顶层 version=%s，与目标 tag %s 不一致（索引领先/落后于 release）", app.Version, o.Tag)
	}
	if _, ok := app.Releases[o.Tag]; !ok {
		return res, fmt.Errorf("自索引 releases 中无 %s 节点", o.Tag)
	}
	res.Assets = AssetsFor(releaseDownloadPrefix(webBase, o.Repos[0], o.Tag), o.Tag, o.Arch)
	if err := assetsComplete(res.Assets); err != nil {
		return res, err
	}
	res.OK = true
	res.Version = o.Tag
	return res, nil
}

// fnpackIndex 是 FnDepot V2 协议的最小结构。
type fnpackIndex struct {
	SchemaVersion string `json:"schema_version"`
	Apps          map[string]struct {
		Version  string                 `json:"version"`
		Releases map[string]fnpackEntry `json:"releases"`
	} `json:"apps"`
}

type fnpackEntry struct {
	Changelog string          `json:"changelog"`
	Packages  json.RawMessage `json:"packages"`
}

// probeFnDepot 走 FnDepot 索引通道（V2 协议）。
func probeFnDepot(ctx context.Context, o Opts) (Result, error) {
	o = o.normalize()
	res := Result{Channel: ChannelFnDepot}
	url := o.FnURL
	if strings.TrimSpace(url) == "" {
		url = fmt.Sprintf("%s/%s/%s/main/%s", rawBase, o.Owner, o.IndexRepo, DefaultIndexFile)
	}
	body, _, err := httpGet(ctx, o.Client, url)
	if err != nil {
		return res, err
	}
	var idx fnpackIndex
	decErr := json.NewDecoder(body).Decode(&idx)
	body.Close()
	if decErr != nil {
		return res, fmt.Errorf("解析 FnDepot 索引失败: %v", decErr)
	}
	if idx.SchemaVersion != "" && idx.SchemaVersion != "2" {
		res.Extra = append(res.Extra, "schema_version="+idx.SchemaVersion)
	}
	app, ok := idx.Apps[o.AppName]
	if !ok {
		return res, fmt.Errorf("FnDepot 索引中无 apps.%s 条目", o.AppName)
	}
	if _, ok := app.Releases[o.Tag]; !ok {
		res.Extra = append(res.Extra, "index_version="+app.Version)
		return res, fmt.Errorf("FnDepot releases 中无 %s 节点", o.Tag)
	}
	res.Assets = AssetsFor(releaseDownloadPrefix(webBase, o.Repos[0], o.Tag), o.Tag, o.Arch)
	if err := assetsComplete(res.Assets); err != nil {
		return res, err
	}
	res.OK = true
	res.Version = o.Tag
	return res, nil
}

// probeMirror 走加速镜像通道。
//
// 镜像前缀只替换 https://github.com 这一段，三件套走同一前缀——
// 保证「镜像改不了签名」这一安全前提不被拼装错误破坏。
func probeMirror(ctx context.Context, o Opts) (Result, error) {
	o = o.normalize()
	res := Result{Channel: ChannelMirror}
	prefixes := o.Mirrors
	if len(prefixes) == 0 {
		prefixes = defaultMirrors()
	}
	if len(prefixes) == 0 {
		return res, fmt.Errorf("无可用镜像前缀")
	}
	repo := o.Repos[0]
	relPrefix := fmt.Sprintf("%s/releases/download/v%s", repo, o.Tag)

	var lastErr error
	for _, p := range prefixes {
		base := strings.TrimSuffix(strings.TrimSpace(p), "/") + "/https://" + strings.TrimPrefix(webBaseURL, "https://") + "/" + relPrefix
		assets := AssetsFor(base, o.Tag, o.Arch)
		if err := assetsComplete(assets); err != nil {
			return res, err
		}
		if err := headAll(ctx, o.Client, []string{assets.FPK, assets.SHA256, assets.Minisig}); err != nil {
			lastErr = fmt.Errorf("镜像 %s: %v", p, err)
			res.Extra = append(res.Extra, "fail="+p)
			continue
		}
		res.Assets = assets
		res.OK = true
		res.Version = o.Tag
		res.Extra = append(res.Extra, "mirror="+p)
		return res, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("全部镜像均不可用")
	}
	return res, lastErr
}

// defaultMirrors 复用运行时配置的 GitHub 镜像前缀列表。
//
// 只取有 URL 的常规加速前缀，排除 auto/custom/direct 这三种特殊项
// （它们不是可直接拼接的前缀）。
func defaultMirrors() []string {
	out := make([]string, 0, 8)
	for _, o := range config.GitHubMirrorOptions() {
		switch o.Key {
		case "auto", "custom", "direct":
			continue
		}
		if strings.TrimSpace(o.URL) != "" {
			out = append(out, o.URL)
		}
	}
	return out
}
