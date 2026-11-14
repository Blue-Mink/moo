package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ghAPIBase GitHub REST API 根地址（测试可覆盖为 httptest server）。
var ghAPIBase = "https://api.github.com"

const (
	maxRepoFPKs    = 800             // 仓库文件扫描最多收 800 个 .fpk（防超大仓库打爆任务）
	maxBuildFPKs   = 600             // 单次生成最多下载解析 600 个候选（小包仓库如 conversun 有 593 个）
	maxBuildBytes  = int64(3 << 30)  // 候选累计体积上限 3GiB（大包仓库按体积提前收口）
	maxRepoFPKSize = int64(1 << 30)  // 与 inspectFPK 的 1GiB 读取上限一致
	maxGhTreeBytes = int64(64 << 20) // git tree 响应上限（GitHub 截断线 7MB，留足余量）
)

// ghRepoMeta 仓库元信息（只取扫描需要的字段）。
type ghRepoMeta struct {
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
}

// ghTreeEntry git tree 单个条目。
type ghTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

// ghTreeResp git tree 响应。
type ghTreeResp struct {
	Tree      []ghTreeEntry `json:"tree"`
	Truncated bool          `json:"truncated"`
}

// ghAPIGet 带鉴权与 UA 的 GitHub GET；非 200 返回含状态码的错误。
func ghAPIGet(ctx context.Context, u, token string, httpc *http.Client) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "moo-mate")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, maxGhTreeBytes))
	if rerr != nil {
		return nil, rerr
	}
	if resp.StatusCode != http.StatusOK {
		body := strings.TrimSpace(string(data))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(body), "rate limit") {
			return nil, fmt.Errorf("GitHub API 限流（403）：在 Moo 设置里配置 GitHub Token 可解除 60 次/小时限制，或稍后重试")
		}
		return nil, fmt.Errorf("GitHub API HTTP %d: %s", resp.StatusCode, body)
	}
	return data, nil
}

// ghRepoMetaFetch 读取仓库元信息（默认分支 / 是否私有）。
func ghRepoMetaFetch(ctx context.Context, owner, repo, token string, httpc *http.Client) (*ghRepoMeta, error) {
	data, err := ghAPIGet(ctx, fmt.Sprintf("%s/repos/%s/%s", ghAPIBase, owner, repo), token, httpc)
	if err != nil {
		return nil, err
	}
	var m ghRepoMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ghTreeFetch 拉取整棵文件树（recursive=1）。
func ghTreeFetch(ctx context.Context, owner, repo, branch, token string, httpc *http.Client) ([]ghTreeEntry, bool, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/git/trees/%s?recursive=1", ghAPIBase, owner, repo, escapeRef(branch))
	data, err := ghAPIGet(ctx, u, token, httpc)
	if err != nil {
		return nil, false, err
	}
	var t ghTreeResp
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, false, err
	}
	return t.Tree, t.Truncated, nil
}

// escapeRef 按段转义分支名（feature/x → feature/x，保留路径分隔符）。
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// escapePathSegments 按段转义仓库内文件路径。
func escapePathSegments(p string) string { return escapeRef(p) }

// pathBase 取路径最后一段（不用 path 包，避免与变量名混淆）。
func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// scanRepoFilesGitHub 扫描 GitHub 仓库文件树里的 .fpk（仓库里直接放包的形态）。
// 私有仓库直接跳过：raw 链接对外不可下载，索引出来也用不了。
func scanRepoFilesGitHub(ctx context.Context, owner, repo, token string, httpc *http.Client, logf func(string, string)) ([]releaseFPK, error) {
	log := func(l, m string) {
		if logf != nil {
			logf(l, m)
		}
	}
	meta, err := ghRepoMetaFetch(ctx, owner, repo, token, httpc)
	if err != nil {
		return nil, err
	}
	if meta.Private {
		return nil, fmt.Errorf("私有仓库，仓库文件不可公开下载，已跳过")
	}
	branch := strings.TrimSpace(meta.DefaultBranch)
	if branch == "" {
		branch = "main"
	}
	log("info", "仓库文件：分支 "+branch+"，读取文件树…")
	entries, truncated, err := ghTreeFetch(ctx, owner, repo, branch, token, httpc)
	if err != nil {
		return nil, err
	}
	if truncated {
		log("warn", "仓库文件：文件树过大被截断，只扫描已返回部分")
	}
	var out []releaseFPK
	skippedBig := 0
	for _, e := range entries {
		if e.Type != "blob" || !strings.HasSuffix(strings.ToLower(e.Path), ".fpk") {
			continue
		}
		if e.Size > maxRepoFPKSize {
			skippedBig++
			continue
		}
		base := pathBase(e.Path)
		out = append(out, releaseFPK{
			name:     base,
			url:      fmt.Sprintf("https://github.com/%s/%s/raw/%s/%s", owner, repo, escapeRef(branch), escapePathSegments(e.Path)),
			size:     e.Size,
			ver:      versionFromFilename(base),
			fromRepo: true,
		})
		if len(out) >= maxRepoFPKs {
			log("warn", fmt.Sprintf("仓库文件：候选超过 %d 个，只取前 %d 个", maxRepoFPKs, maxRepoFPKs))
			break
		}
	}
	if skippedBig > 0 {
		log("warn", fmt.Sprintf("仓库文件：%d 个 .fpk 超过 1GiB，已跳过", skippedBig))
	}
	if len(out) == 0 {
		log("info", "仓库文件：未发现 .fpk")
	}
	return out, nil
}

// mergeFPKs 合并 release 与仓库文件两组候选：同名取更高版本；同版本 release 资产优先。
// 输出按文件名排序，保证进度日志顺序可复现。
func mergeFPKs(release, repo []releaseFPK) []releaseFPK {
	best := map[string]releaseFPK{}
	put := func(it releaseFPK) {
		key := strings.ToLower(it.name)
		cur, ok := best[key]
		if !ok {
			best[key] = it
			return
		}
		// GitHub：高版本优先；同版本 release 资产优先（发布产物比源码树里的包稳定）。
		if it.ver != "" && (cur.ver == "" || versionGreater(it.ver, cur.ver)) {
			best[key] = it
			return
		}
		if it.ver == cur.ver && !it.fromRepo && cur.fromRepo {
			best[key] = it
		}
	}
	for _, it := range release {
		put(it)
	}
	for _, it := range repo {
		put(it)
	}
	out := make([]releaseFPK, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].name), strings.ToLower(out[j].name)
		if a != b {
			return a < b
		}
		return versionGreater(out[i].ver, out[j].ver)
	})
	return out
}

// normalizeAppToken 去掉分隔符与大小写差异，用于文件名 ↔ appname 的模糊对齐。
func normalizeAppToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// filterCandidatesForApp 单应用更新检查时只下载文件名可能对应 appname 的候选；
// 一个都对不上（有些仓库文件名完全不含应用名）则原样返回，保持旧行为。
func filterCandidatesForApp(items []discoveredApp, appname string) []discoveredApp {
	key := normalizeAppToken(appname)
	if key == "" {
		return items
	}
	hit := make([]discoveredApp, 0, len(items))
	for _, it := range items {
		if strings.Contains(normalizeAppToken(it.Name), key) {
			hit = append(hit, it)
		}
	}
	if len(hit) == 0 {
		return items
	}
	return hit
}

// appKeyFromFilename 从文件名推导「应用键」：去掉版本号与架构后缀。
// alist_3.65.0_arm.fpk → alist；foo-1.0.0-x86.fpk → foo；bar.fpk → bar。
// 只用于按应用聚合（去掉旧版本），不参与任何展示/索引字段。
func appKeyFromFilename(name string) string {
	base := strings.ToLower(strings.TrimSpace(name))
	base = strings.TrimSuffix(base, ".fpk")
	if ver := versionFromFilename(name); ver != "" {
		if i := strings.Index(base, ver); i >= 0 {
			base = base[:i] + base[i+len(ver):]
		}
	}
	base = strings.Trim(base, "-_. ")
	for _, suf := range []string{"_x86_64", "_amd64", "_aarch64", "_x86", "_arm64", "_arm", "-x86", "-arm"} {
		if strings.HasSuffix(base, suf) {
			base = strings.TrimSuffix(base, suf)
			break
		}
	}
	return strings.Trim(base, "-_. ")
}

// pruneByAppKey 同一应用的旧版本先剔除（同键只保留最高版本；无版本信息的不动），
// 避免为了解析 manifest 把 593 个资产里的历史版本全下载一遍。
func pruneByAppKey(items []releaseFPK) []releaseFPK {
	best := map[string]string{}
	for _, it := range items {
		if it.ver == "" {
			continue
		}
		k := appKeyFromFilename(it.name)
		if cur, ok := best[k]; !ok || versionGreater(it.ver, cur) {
			best[k] = it.ver
		}
	}
	out := make([]releaseFPK, 0, len(items))
	for _, it := range items {
		k := appKeyFromFilename(it.name)
		if it.ver != "" && best[k] != "" && it.ver != best[k] {
			continue
		}
		out = append(out, it)
	}
	return out
}

// orderByAppBreadth 超出预算时按「应用分组 → 组体积升序」重排：
// 预算固定时优先覆盖更多小应用，而不是把字母序前几个应用的所有大包全塞进来。
func orderByAppBreadth(items []releaseFPK, logf func(string, string)) []releaseFPK {
	order := make([]string, 0, len(items))
	groups := map[string][]releaseFPK{}
	sizes := map[string]int64{}
	for _, it := range items {
		k := appKeyFromFilename(it.name)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], it)
		sizes[k] += it.size
	}
	sort.SliceStable(order, func(i, j int) bool {
		if sizes[order[i]] != sizes[order[j]] {
			return sizes[order[i]] < sizes[order[j]]
		}
		return order[i] < order[j]
	})
	out := make([]releaseFPK, 0, len(items))
	for _, k := range order {
		out = append(out, groups[k]...)
	}
	if logf != nil {
		logf("warn", fmt.Sprintf("候选超出单次预算：%d 个应用按体积升序处理，尽量多覆盖", len(order)))
	}
	return out
}

// dedupMerged 按应用聚合 → 体积/数量收口 → 转成 discoveredApp。
func dedupMerged(items []releaseFPK, logf func(string, string)) []discoveredApp {
	log := func(l, m string) {
		if logf != nil {
			logf(l, m)
		}
	}
	if pruned := pruneByAppKey(items); len(pruned) < len(items) {
		log("ok", fmt.Sprintf("按应用聚合：%d → %d 个候选（剔除旧版本）", len(items), len(pruned)))
		items = pruned
	}
	var totalSize int64
	for _, it := range items {
		totalSize += it.size
	}
	if len(items) > maxBuildFPKs || totalSize > maxBuildBytes {
		items = orderByAppBreadth(items, logf)
	}
	out := make([]discoveredApp, 0, len(items))
	var total int64
	for _, it := range items {
		if it.url == "" {
			continue
		}
		if len(out) >= maxBuildFPKs {
			log("warn", fmt.Sprintf("候选 %d 个，超过单次上限 %d，其余不处理", len(items), maxBuildFPKs))
			break
		}
		if it.size > 0 && total+it.size > maxBuildBytes {
			log("warn", fmt.Sprintf("候选累计体积超过 %d GiB，其余不处理（已收 %d 个）", maxBuildBytes>>30, len(out)))
			break
		}
		total += it.size
		out = append(out, discoveredApp{Name: it.name, Version: it.ver, DownloadURL: it.url, Size: it.size})
	}
	return out
}
