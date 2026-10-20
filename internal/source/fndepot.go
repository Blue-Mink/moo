package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"moo/internal/netx"
)

// FnDepot 源：解析 fnpack.json（兼容扁平 V1 与 V2 包裹两种格式）。
type FnDepot struct {
	name string
	url  string

	// 0.6.330 WP1/WP2：索引名记忆 + 镜像分级（见 indexpref.go / mooindex.go）。
	prefName    string // 非空 = 只试这个索引名（记忆命中路径）
	mirrorLimit int    // >=0 只用前 N 个镜像；-1 = 全部（后台重探/探测）
	lastIndex   string // 本次 Fetch 命中的索引文件名（写记忆用）
	lastStage   int    // 命中所在阶段（1=常规名，2=扩展名，3=CDN，4=apps.json）
	lastWinner  string // 命中地址（判断是否 CDN 兜底）
	noCDN       bool   // 0.6.331 P0：真源复核模式——候选里剔除 jsDelivr（见 withRawOnly）
}

// NewFnDepot 创建一个 FnDepot 源。url 可以是 fnpack.json 直链，
// 也可以是仓库地址（github.com/user/repo 或任意托管根）。
func NewFnDepot(name, url string) *FnDepot {
	ml := fastMirrorCount
	if !mirrorFastEnabled() {
		ml = -1 // MOO_MIRROR_FAST=0：回到全量镜像（回滚路径）
	}
	return &FnDepot{name: name, url: strings.TrimRight(url, "/"), mirrorLimit: ml}
}

// withPref 限定只尝试记忆中的索引名（找不到候选时自动回退全量）。
func (f *FnDepot) withPref(name string) *FnDepot {
	f.prefName = name
	return f
}

// withFullMirrors 放开全部镜像（后台全量重探/添加为源探测使用）。
func (f *FnDepot) withFullMirrors() *FnDepot {
	f.mirrorLimit = -1
	return f
}

// withRawOnly 0.6.331 P0：剔除 jsDelivr 候选与记忆限制，只走 raw/镜像直连。
// 用于「条目数异常下降」时的真源复核——CDN 与镜像都可能返回陈旧副本，
// 只有作者仓库的 raw 是权威（允许真实下架，但不允许缓存滞后缩表）。
func (f *FnDepot) withRawOnly() *FnDepot {
	f.noCDN = true
	f.prefName = ""
	return f
}

// NewSource 按地址自动选择源适配器：conversun/fnos-apps 目录走 Conversun
// （apps.json 格式），其余一律 FnDepot（fnpack.json 格式）。
func NewSource(name, url string) Source {
	if IsConversunURL(url) {
		return NewConversun(name)
	}
	return NewFnDepot(name, url)
}

func (f *FnDepot) Name() string { return f.name }

// fetchClient 源同步出网（0.6.206 起带「科学加速」代理：GitHub 域名走本机
// 代理、镜像域名直连；代理配置变更即时生效）。
var fetchClient = netx.NewClient(30 * time.Second)

// candidateURLs 把用户给的地址展开成一组候选 JSON 地址，按优先级排列。
// 0.6.233：实际展开逻辑移到 mooindex.go 的 buildCandidates（moo.json 优先、
// fnpack.json 回退），本处保留为薄封装以免改动调用点。
//
// 0.6.330：镜像按 f.mirrorLimit 分级（默认 fastMirrorCount=2）；有记忆时
// 只保留该索引名的候选（0 条则回退全量，避免记忆写坏后永久失败）。
func (f *FnDepot) candidateURLs() []string {
	cands := buildCandidatesLimited(f.url, f.mirrorLimit)
	if f.noCDN {
		// 0.6.331 P0：真源复核模式——jsDelivr 边缘缓存可能是陈旧副本，
		// 复核必须只看作者仓库（raw 与镜像前缀），否则「复核」等于再赌一次。
		kept := make([]string, 0, len(cands))
		for _, u := range cands {
			if strings.Contains(u, "cdn.jsdelivr.net") {
				continue
			}
			kept = append(kept, u)
		}
		cands = kept
	}
	if f.prefName == "" {
		return cands
	}
	out := make([]string, 0, 4)
	for _, u := range cands {
		if indexNameOf(u) == f.prefName {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// fetchJSON 拉取源索引：见 fetchIndex（分阶段），本处只把胜出的索引地址
// 折算成相对资源 base（indexBaseDir）。
func (f *FnDepot) fetchJSON() ([]byte, string, error) {
	data, winner, err := f.fetchIndex()
	if err != nil {
		return nil, "", err
	}
	return data, indexBaseDir(winner), nil
}

// fetchIndex 拉取索引，返回 (内容, 胜出 URL)。
//
// 候选名全集 = jsonIndexNames（moo.json / fnpack.json / fndepot.json /
// fndepot_v2.json / apps.json，与 source_key.go 的识别集合一致）。
// 分阶段抓取（0.6.326e）——现有源几乎全部命中在 moo.json/fnpack.json，
// 不该让每次刷新都为扩展名多打一轮 404：
//
//	阶段 1：常规名（moo.json / fnpack.json / 用户直接贴的 .json 直链），12s 预算；
//	阶段 2：扩展名（fndepot.json / fndepot_v2.json）；
//	阶段 3：CDN（jsDelivr）兜底，常规名 + 扩展名；
//	阶段 4：apps.json 兜底——**必须内容校验**（ParseFnpack 能解析出非空
//	        应用集合），否则工程项目自带的无关 apps.json 会被误当源。
//
// 阶段 2–4 共用一份 12s 预算（阶段 1 另有一份），把「无索引仓库」的最坏等待
// 钉在 ~24s，与旧实现（主候选 12s + CDN 12s）同量级。
func (f *FnDepot) fetchIndex() ([]byte, string, error) {
	var plain, plainExt, plainApps []string
	var cdn, cdnExt, cdnApps []string
	for _, u := range f.candidateURLs() {
		isCDN := strings.Contains(u, "cdn.jsdelivr.net")
		switch indexNameOf(u) {
		case "apps.json":
			if isCDN {
				cdnApps = append(cdnApps, u)
			} else {
				plainApps = append(plainApps, u)
			}
		case "fndepot.json", "fndepot_v2.json":
			if isCDN {
				cdnExt = append(cdnExt, u)
			} else {
				plainExt = append(plainExt, u)
			}
		default: // moo.json / fnpack.json / 直接贴的 .json 直链
			if isCDN {
				cdn = append(cdn, u)
			} else {
				plain = append(plain, u)
			}
		}
	}

	// 阶段 1：常规名。热路径，绝大多数源在此命中。
	// 0.6.326：按索引名优先级竞速（moo.json / 用户直链先发，700ms 窗口后
	// 才放开 fnpack.json 等）——见 raceFetchOrdered 注释：旧实现是同时打、
	// 谁先回谁赢，同仓库并存两个索引时「moo.json 优先」并不成立。
	// 0.6.330 WP1：有记忆时只试一个索引名，预算收紧到 4s（当轮快速失败，
	// 失败由 Manager 触发后台全量重探）；无记忆仍是 12s。
	budget := 12 * time.Second
	if f.prefName != "" {
		budget = 4 * time.Second
	}
	pctx, pcancel := context.WithTimeout(context.Background(), budget)
	data, winner, err := raceFetchOrdered(plain, 700*time.Millisecond, pctx)
	// 0.6.330 WP2：首波只用了前 fastMirrorCount 个镜像。首波全败时立刻放开
	// 全部镜像补打一轮（等价于「窗口后放开其余」的批处理版，避免为了少数
	// 长尾镜像在命中路径上多发几个数量级的请求）。
	if err != nil && f.mirrorLimit >= 0 {
		tried := make(map[string]bool, len(plain))
		for _, u := range plain {
			tried[u] = true
		}
		f.mirrorLimit = -1
		var extra []string
		for _, u := range f.candidateURLs() {
			if tried[u] {
				continue
			}
			if n := indexNameOf(u); n == MooIndexFile || n == "fnpack.json" {
				extra = append(extra, u)
			}
		}
		if len(extra) > 0 {
			if d, w, e := raceFetchOrdered(extra, 300*time.Millisecond, pctx); e == nil {
				data, winner, err = d, w, nil
			}
		}
	}
	pcancel()
	if err == nil {
		f.lastStage, f.lastIndex, f.lastWinner = 1, indexNameOf(winner), winner
		return data, winner, nil
	}
	lastErr := err

	// 阶段 2–4：共用 12s 预算。
	rctx, rcancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer rcancel()

	if len(plainExt) > 0 {
		if d, w, e := raceFetchOrdered(plainExt, 500*time.Millisecond, rctx); e == nil {
			f.lastStage, f.lastIndex, f.lastWinner = 2, indexNameOf(w), w
			return d, w, nil
		} else {
			lastErr = e
		}
	}
	if all := append(append([]string{}, cdn...), cdnExt...); len(all) > 0 {
		if d, w, e := raceFetchOrdered(all, 500*time.Millisecond, rctx); e == nil {
			f.lastStage, f.lastIndex, f.lastWinner = 3, indexNameOf(w), w
			return d, w, nil
		} else {
			lastErr = e
		}
	}
	for _, set := range [][]string{plainApps, cdnApps} {
		if len(set) == 0 {
			continue
		}
		d, w, e := raceFetch(set, rctx)
		if e != nil {
			lastErr = e
			continue
		}
		if looksLikeApps(d) {
			f.lastStage, f.lastIndex, f.lastWinner = 4, indexNameOf(w), w
			return d, w, nil
		}
		lastErr = fmt.Errorf("%s: 内容不是应用集合", w)
	}
	return nil, "", lastErr
}

// looksLikeApps 校验一份候选 JSON 是否确为「应用集合」（apps.json 兜底专用）。
// 结构须可解析（V2 包裹或 V1 平铺）且至少一条条目带 display_name / version /
// download_url 之一——避免把工程项目自带的无关 apps.json（构建产物清单等）
// 误当成源。
func looksLikeApps(data []byte) bool {
	entries, err := ParseFnpack(data)
	if err != nil || len(entries) == 0 {
		return false
	}
	for _, m := range entries {
		if strings.TrimSpace(str(m, "display_name")) != "" ||
			strings.TrimSpace(anyScalarStr(m, "version")) != "" ||
			strings.TrimSpace(str(m, "download_url")) != "" {
			return true
		}
	}
	return false
}

// ProbeIndex 探测一个仓库/目录地址是否发布可解析的索引，返回命中的索引
// 文件名（"" = 无索引 / 网络失败）。与 Fetch 走同一套候选展开与内容校验，
// 故「探测到文件名」⇒「添加为源必成功」。
//
// 仅供展示层预判「添加为源」按钮可用性（0.6.326e），不写任何缓存。
func ProbeIndex(repoURL string) string {
	if strings.TrimSpace(repoURL) == "" {
		return ""
	}
	_, winner, err := NewFnDepot("", repoURL).fetchIndex()
	if err != nil {
		return ""
	}
	return indexNameOf(winner)
}

// indexBaseDir 0.6.271：相对资源（icon/readme/preview）的 base = 索引文件
// 所在目录。旧实现只剥 /fnpack.json——moo.json 源的 base 残留 "…/moo.json"，
// 全部相对资源拼成 "…/moo.json/<rel>" → 404（10-04 主 NAS moo.log
// readme-warm 0/30 命中实锤）。规则：末段是 *.json → 取其目录，
// 否则地址本身即目录。
func indexBaseDir(winner string) string {
	p := winner
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if i := strings.LastIndex(p, "/"); i >= 0 {
		if strings.HasSuffix(p[i+1:], ".json") {
			return p[:i]
		}
	}
	return p
}

// raceFetch 并发竞速一组 URL，返回第一个 200 的 JSON（包级：FnDepot 与
// Conversun 等适配器共用）。
func raceFetch(cands []string, parent context.Context) ([]byte, string, error) {
	if len(cands) == 0 {
		return nil, "", fmt.Errorf("无候选地址")
	}
	type result struct {
		data   []byte
		winner string
		err    error
	}
	ch := make(chan result, len(cands))
	for _, cand := range cands {
		go func(u string) {
			req, err := http.NewRequestWithContext(parent, http.MethodGet, u, nil)
			if err != nil {
				ch <- result{nil, "", err}
				return
			}
			resp, err := fetchClient.Do(req)
			if err != nil {
				ch <- result{nil, "", err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			if resp.StatusCode != 200 || err != nil {
				ch <- result{nil, "", fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)}
				return
			}
			ch <- result{body, u, nil}
		}(cand)
	}
	var lastErr error
	for i := 0; i < len(cands); i++ {
		r := <-ch
		if r.err == nil {
			return r.data, r.winner, nil
		}
		lastErr = r.err
	}
	return nil, "", fmt.Errorf("所有候选地址均失败: %w", lastErr)
}

// indexNameRank 索引文件名的优先级（0 最高）：
//   - 用户直接贴的 .json 直链（文件名不在协议集合里）= 显式指定，0；
//   - 其余按 jsonIndexNames 顺序：moo.json=0 / fnpack.json=1 /
//     fndepot.json=2 / fndepot_v2.json=3 / apps.json=4。
func indexNameRank(u string) int {
	n := indexNameOf(u)
	if n == "" {
		return 0
	}
	for i, x := range jsonIndexNames {
		if n == x {
			return i
		}
	}
	return 0 // 自定义 .json 直链：用户显式指定，视为最高优先
}

// raceFetchOrdered 按索引文件名优先级竞速：
//   - 只先发最高优先组（rank 0：moo.json / 用户直链）的请求；
//   - headStart 窗口内命中即返回（首名快时保证「moo.json 优先」真的生效）；
//   - 首组全部失败（404 很快）或窗口到期仍未命中 → 放开其余候选一起竞速。
//
// 背景：旧实现把同一仓库的 moo.json / fnpack.json 等**同时**打出去，谁先回
// 200 谁赢——「按优先级」只存在于候选列表顺序里，实际是抽签。只有 fnpack.json
// 的仓库代价可控：moo.json 通常快速 404，首组失败后立即放开，不额外等待整个窗口。
func raceFetchOrdered(cands []string, headStart time.Duration, parent context.Context) ([]byte, string, error) {
	if len(cands) == 0 {
		return nil, "", fmt.Errorf("无候选地址")
	}
	type result struct {
		data   []byte
		winner string
		err    error
		first  bool
	}
	ch := make(chan result, len(cands))
	fetch := func(u string, first bool) {
		req, err := http.NewRequestWithContext(parent, http.MethodGet, u, nil)
		if err != nil {
			ch <- result{err: err, first: first}
			return
		}
		resp, err := fetchClient.Do(req)
		if err != nil {
			ch <- result{err: err, first: first}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if resp.StatusCode != 200 || err != nil {
			ch <- result{err: fmt.Errorf("%s: HTTP %d", u, resp.StatusCode), first: first}
			return
		}
		ch <- result{data: body, winner: u, first: first}
	}

	var firstWave, rest []string
	for _, u := range cands {
		if indexNameRank(u) == 0 {
			firstWave = append(firstWave, u)
		} else {
			rest = append(rest, u)
		}
	}
	if len(firstWave) == 0 { // 理论上不会（rank 0 兜底），保底退回全量竞速
		return raceFetch(cands, parent)
	}
	for _, u := range firstWave {
		go fetch(u, true)
	}
	var lastErr error
	pendingFirst, pendingRest := len(firstWave), 0
	startedRest := false
	startRest := func() {
		if startedRest {
			return
		}
		startedRest = true
		pendingRest = len(rest)
		for _, u := range rest {
			go fetch(u, false)
		}
	}
	if len(rest) == 0 {
		startedRest = true
	}
	window := time.After(headStart)
	for {
		if startedRest && pendingFirst == 0 && pendingRest == 0 {
			break
		}
		select {
		case r := <-ch:
			if r.err == nil {
				return r.data, r.winner, nil
			}
			lastErr = r.err
			if r.first {
				pendingFirst--
			} else {
				pendingRest--
			}
			// 首组全败（如 moo.json 快速 404）→ 立刻放开，不等窗口
			if pendingFirst == 0 {
				startRest()
			}
		case <-window:
			startRest()
			window = nil // 只触发一次；nil channel 永远阻塞
		case <-parent.Done():
			return nil, "", parent.Err()
		}
	}
	return nil, "", fmt.Errorf("所有候选地址均失败: %w", lastErr)
}

// v2Doc 是 V2 包裹格式（schema_version + source_info + apps）。
// 0.6.326e：SchemaVersion 放宽为 RawMessage——协议只要求该键**存在**，
// 取值形态（"2" / 2 / "moo"）不应影响解析（conversun 系用数字，其它用字符串）。
type v2Doc struct {
	SchemaVersion json.RawMessage           `json:"schema_version"`
	SourceInfo    map[string]any            `json:"source_info"`
	Apps          map[string]map[string]any `json:"apps"`
}

// ParseFnpack 解析 fnpack.json 的两种已知格式：
// 有顶层 schema_version → V2 包裹（取 apps 字段）；否则 → 扁平（顶层 key 即 appname）。
func ParseFnpack(data []byte) (map[string]map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("fnpack.json 不是 JSON 对象: %w", err)
	}
	if _, hasSchema := raw["schema_version"]; hasSchema {
		var doc v2Doc
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("V2 fnpack.json 解析失败: %w", err)
		}
		if len(doc.Apps) == 0 {
			return nil, fmt.Errorf("V2 fnpack.json 的 apps 为空")
		}
		return doc.Apps, nil
	}
	var flat map[string]map[string]any
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("fnpack.json 格式无法识别: %w", err)
	}
	if len(flat) == 0 {
		return nil, fmt.Errorf("fnpack.json 为空")
	}
	return flat, nil
}

// str 安全取字符串字段。
func str(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// pickStr 依次取多个候选键，返回第一个非空字符串（兼容源端字段名变体，
// 如 author/maintainer）。
func pickStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m, k); s != "" {
			return s
		}
	}
	return ""
}

// pickStrList 取字段为字符串或字符串数组的字段，统一拼成逗号分隔字符串
// （labels 字符串 / categories 数组等变体）。
func pickStrList(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		case []any:
			parts := make([]string, 0, len(t))
			for _, it := range t {
				if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
					parts = append(parts, strings.TrimSpace(s))
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ", ")
			}
		}
	}
	return ""
}

// anyScalarStr 取标量字段（字符串或数字）转字符串。
func anyScalarStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	return ""
}

// anyInt 取整数字段（数字或数字字符串）。
func anyInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

// isDockerFlag 兼容 isdocker（"true"/"false" 字符串）与 is_docker（bool）两种写法。
func isDockerFlag(m map[string]any) string {
	for _, k := range []string{"isdocker", "is_docker"} {
		switch t := m[k].(type) {
		case bool:
			if t {
				return "true"
			}
		case string:
			if strings.EqualFold(strings.TrimSpace(t), "true") || t == "1" {
				return "true"
			}
		}
	}
	return ""
}

// currentArch 把 Go 运行时架构映射到 fnpack 架构名（amd64→x86，arm64→arm）。
func currentArch() string {
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		return "arm"
	}
	return "x86"
}

// ArchFromAssetName 从 Release 资产文件名推断架构（0.6.337 详情页「架构」兜底）。
// 源未声明 releases.packages 架构信息时，从 GitHub Release 资产名（如
// `1panel_1.0_x86.fpk`、`foo_arm.fpk`、`bar_amd64.fpk`）推断，识别不出返回空串。
// 按 `_` / `-` 分词逐段精确匹配，避免「harmony / alarm」这类含 arm 的词误判。
func ArchFromAssetName(name string) string {
	base := name
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[:i]
	}
	for _, f := range strings.FieldsFunc(base, func(r rune) bool { return r == '_' || r == '-' }) {
		switch strings.ToLower(f) {
		case "x86", "amd64", "x64", "x86_64":
			return "x86"
		case "arm", "arm64", "aarch64", "armv7", "armhf":
			return "arm"
		}
	}
	return ""
}

// bestRelease 从 releases 里按版本降序挑当前架构（回退 all）第一个带
// download_url 的包，返回版本号、命中的架构键与该包（download_url/sha256/size 等）。
// 0.6.303：返回命中架构键（详情页展示 + 安装自动识别的依据）。
func bestRelease(rel map[string]any, arch string) (string, string, map[string]any) {
	versions := make([]string, 0, len(rel))
	for v := range rel {
		versions = append(versions, v)
	}
	// 0.6.330：必须是**确定性**排序。map 迭代顺序随机，若用 sort.Slice + 只有
	// 「版本更高」一个判据，判等（1.0 vs 1.0.0）或不可比（latest-… vs 1.0）时
	// 相对顺序不定 → 同一个源两次请求可能挑到不同「最新版」。
	// 这里改成稳定排序 + 判等时按字面序兜底，保证同一份数据永远选同一个包。
	sort.SliceStable(versions, func(i, j int) bool {
		oi, oj := ParseVersion(versions[i]).OK, ParseVersion(versions[j]).OK
		switch {
		case oi && !oj:
			return true // 可解析的版本排在前面
		case !oi && oj:
			return false
		}
		if c := CompareVersion(versions[i], versions[j]); c != 0 {
			return c > 0 // 版本降序
		}
		return versions[i] > versions[j] // 判等：字面序兜底（确定性）
	})
	for _, v := range versions {
		rv, _ := rel[v].(map[string]any)
		pkgs, _ := rv["packages"].(map[string]any)
		for _, k := range []string{arch, "all"} {
			if p, ok := pkgs[k].(map[string]any); ok && strings.TrimSpace(str(p, "download_url")) != "" {
				return v, k, p
			}
		}
	}
	return "", "", nil
}

// archDisplayKeys 返回一组架构键的展示顺序（x86 → arm → all → 其余字母序），
// 供详情页「架构」行稳定显示（0.6.303）。
func archDisplayKeys(keys []string) []string {
	rank := map[string]int{"x86": 0, "arm": 1, "all": 2}
	sort.SliceStable(keys, func(i, j int) bool {
		ri, oki := rank[keys[i]]
		rj, okj := rank[keys[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki:
			return true
		case okj:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return keys
}

// pkgsWithDownload 收集 packages 映射里所有带 download_url 的架构键（0.6.303）。
func pkgsWithDownload(pkgs map[string]any) []string {
	out := make([]string, 0, len(pkgs))
	for k, pv := range pkgs {
		if pm, ok := pv.(map[string]any); ok && strings.TrimSpace(str(pm, "download_url")) != "" {
			out = append(out, k)
		}
	}
	return archDisplayKeys(out)
}

// 包体合理性区间（0.6.326z3）。实测：真实 FPK 包体最小 ~60KB（官方 easydown
// 85B 走面板通道不在此列），社区包最大数十 GB；而「把 MB 小数写进字节槽位」
// 的源其数值通常 <1024（如 26.0 / 3.2），两侧可清晰区分。
const (
	fpkSizeMinSane = 1 << 10  // 1 KiB
	fpkSizeMaxSane = 64 << 30 // 64 GiB
)

// entrySizeBytes 解释**条目级** size 字段（协议要求单位 MB）。
// 正常 MB 值返回 (0,false)：交下游按 MB×1e6 处理，行为不变。
// 若数值按 MB 解读明显不可能（> 64GiB）而按字节解读落在真实包体区间，则判定
// 「源把字节数写进了 MB 槽位」，返回 (字节数,true)。
// 实证：fnmusic-ext 的 moo.json 条目级 size=27257554（字节）配顶层
// version/download_url → 读不到包级正确字节值 → 按 MB 放大 1e6 倍 →
// 详情页「安装包大小」显示 25385.61 GB（真实 26.0 MB）。
// 注意：MB 值 >1024 也可能是真实大包（数 GB 应用），故此判断只在
// 「按 MB 解读超出 64GiB 上限」时才改判，不会动 900MB/9GB 这类正常值。
func entrySizeBytes(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	if v*1000*1000 > fpkSizeMaxSane && v >= fpkSizeMinSane && v <= fpkSizeMaxSane {
		return int64(v), true
	}
	return 0, false
}

// sanePkgSize 判断**包级** size（协议单位=字节）是否可信：低于 1KiB 视为源把
// MB 小数写进了字节槽位（26.0 → 26 字节），不可采信（交条目级兜底）。
func sanePkgSize(n int) bool {
	return n >= fpkSizeMinSane && int64(n) <= fpkSizeMaxSane
}

// releaseFor 取指定版本的可安装包（当前架构 → all 回退）；want 为空或该版本
// 下没有可安装包时，回落到 bestRelease（版本降序取第一个可安装包）。
func releaseFor(rel map[string]any, want string) (string, string, map[string]any) {
	if want != "" {
		if rm, ok := rel[want].(map[string]any); ok {
			if pkgs, ok2 := rm["packages"].(map[string]any); ok2 {
				for _, k := range []string{currentArch(), "all"} {
					if p, ok3 := pkgs[k].(map[string]any); ok3 && strings.TrimSpace(str(p, "download_url")) != "" {
						return want, k, p
					}
				}
			}
		}
	}
	return bestRelease(rel, currentArch())
}

// versionLess 报告版本 a 是否小于 b。
// 0.6.330：实现统一到 version.go 的 CompareVersion（此前这里是第二套比较器，
// 与 api 层splitVer 口径不同，导致「徽章不亮 / 亮了装不上」两类事故）。
func versionLess(a, b string) bool { return CompareVersion(a, b) < 0 }

// FetchWithIndex 与 Fetch 相同，额外返回本次命中的索引文件名（空 = 未命中）。
// 0.6.330 WP1：Manager 用它写入「索引名记忆」。
func (f *FnDepot) FetchWithIndex() (map[string]*App, string, error) {
	apps, err := f.Fetch()
	return apps, f.lastIndex, err
}

func (f *FnDepot) Fetch() (map[string]*App, error) {
	data, base, err := f.fetchJSON()
	if err != nil {
		return nil, err
	}
	entries, err := ParseFnpack(data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*App, len(entries))
	for name, m := range entries {
		out[name] = translateEntry(name, m, f.name, f.url, base)
	}
	return out, nil
}

// translateEntry 把一条 fnpack.json 条目转成 App（纯函数，便于单测）。
// 兼容：V1 平铺单版本、V2 多版本 releases、字段名变体（maintainer/categories/
// is_docker 等）、arch_diff 按架构分列、GitHub 仓库内 FPK 约定直链。
// base = fnpack.json 所在目录（Fetch 实测胜出的 URL 前缀）；相对路径
// （download_url/图标/预览）一律按它补全，base 为空时回退仓库 URL 猜测。
func translateEntry(name string, m map[string]any, srcName, repoURL, base string) *App {
	a := &App{
		Name:           name,
		Source:         srcName,
		DisplayName:    str(m, "display_name"),
		Desc:           str(m, "desc"),
		Version:        str(m, "version"),
		Platform:       pickStrList(m, "platform"),
		Labels:         pickStrList(m, "labels", "categories", "tags"),
		IconURL:        str(m, "icon_url"),
		HomePage:       str(m, "homepage"),
		ReadmeURL:      str(m, "readme_url"),
		BugReportURL:   str(m, "bug_report_url"),
		Author:         pickStr(m, "author", "maintainer"),
		AuthorURL:      pickStr(m, "author_url", "maintainer_url"),
		Distributor:    str(m, "distributor"),
		InstallType:    str(m, "install_type"),
		SizeMB:         anyScalarStr(m, "size"),
		IsDocker:       appTypeOf(m),
		ServicePort:    anyScalarStr(m, "service_port"),
		DownloadURL:    str(m, "download_url"),
		Changelog:      str(m, "changelog"),
		UpdatedAt:      str(m, "updated_at"),
		DownloadCount:  anyInt(m, "download_count"),
		PreviewURLs:    strListField(m, "preview_urls"),
		FirstReleaseAt: str(m, "first_release_at"),
		// moo.json 扩展（0.6.269）：富文本简介 / 许可 / 最低 fnOS 版本 / 源级向导
		DescHTML: str(m, "desc_html"),
		License:  str(m, "license"),
		MinFnos:  str(m, "min_fnos"),
		Wizard:   parseSourceWizard(m["wizard"]),
	}
	if a.DisplayName == "" {
		a.DisplayName = name
	}
	// 0.6.233（moo.json 扩展）：条目级 sha256 / size_bytes —— FnDepot 只在
	// releases.packages 里带校验和，Moo 原生协议允许直接写在条目上。
	if a.Sha256 == "" {
		a.Sha256 = pickStr(m, "sha256")
	}
	if a.SizeBytes == 0 {
		a.SizeBytes = int64(anyInt(m, "size_bytes"))
	}
	// 0.6.326z3：条目级 size 被源写成字节时按字节采信（唯一规则实现点，
	// api.parseSizeBytes 的 MB×1e6 兜底只在正常 MB 值上生效）。
	if a.SizeBytes == 0 {
		if b, ok := entrySizeBytes(a.SizeMB); ok {
			a.SizeBytes = b
		}
	}
	// releases: 版本 → {changelog, updated_at, packages: {arch: {download_url, sha256, size}}}
	// V2 多版本源（如 RROrg）顶层没有 version/download_url：按版本降序取
	// 当前架构（x86→all 回退）第一个可安装包，补齐版本/下载链接/校验和/体积。
	if rel, ok := m["releases"].(map[string]any); ok && len(rel) > 0 {
		a.ReleaseChangelogs = make(map[string]string, len(rel))
		for ver, rv := range rel {
			if rm, ok := rv.(map[string]any); ok {
				if t := str(rm, "changelog"); t != "" {
					a.ReleaseChangelogs[ver] = t
				}
			}
		}
		if a.Version == "" || a.DownloadURL == "" {
			if ver, hitArch, pkg := bestRelease(rel, currentArch()); ver != "" {
				if a.Version == "" {
					a.Version = ver
				}
				if a.DownloadURL == "" {
					a.DownloadURL = str(pkg, "download_url")
					// 0.6.303：架构信息——选中包架构 + 该版本完整架构清单
					//（详情页「架构」行展示；arm 设备安装时 currentArch()=arm
					// 自动选中 arm 包，x86→all 回退不变）
					a.Arch = hitArch
					if rm, ok := rel[ver].(map[string]any); ok {
						if pkgs, ok2 := rm["packages"].(map[string]any); ok2 {
							a.Archs = pkgsWithDownload(pkgs)
						}
					}
				}
				if s := str(pkg, "sha256"); s != "" {
					a.Sha256 = s
				}
				// 体积：优先 size_bytes（显式字节），其次 size（协议约定同为字节；
				// 0.6.234 起对「写成 MB 小数」的源亦不误读——MB 值一律 <1024，
				// 与真实包体（数 MB 起）差三个数量级，这里按字节解读并交给上层
				// 格式化即可，不做猜测性换算以免把 700B 的小包误放大。）
				if n := anyInt(pkg, "size_bytes"); n > 0 {
					a.SizeBytes = int64(n)
				} else if n := anyInt(pkg, "size"); n > 0 {
					a.SizeBytes = int64(n)
				}
				// 注意：不把 release 说明回填到顶层 Changelog——
				// parseChangelogEntries 已优先用 ReleaseChangelogs 生成
				// 版本化条目，回填会产生一条版本为空的重复条目。
				if a.UpdatedAt == "" {
					if rm, ok := rel[ver].(map[string]any); ok {
						a.UpdatedAt = str(rm, "updated_at")
					}
				}
			}
		}
		// 0.6.326z3：包级体积无条件兜底。旧版把整段包级解析门控在
		// 「顶层缺 version/download_url」里，于是「顶层 version/download_url
		// + releases」混合形态（我方参考样例本身的写法）永远读不到包级
		// size/sha256/架构；条目级体积又缺失或写错时就只能凭空显示。
		// 现改为：条目级拿不到可用体积时，一律回落到该条目版本（缺失则
		// bestRelease）的包级 size（协议单位=字节，经合理性区间校验）。
		if a.SizeBytes == 0 {
			if _, _, pkg := releaseFor(rel, a.Version); pkg != nil {
				if n := anyInt(pkg, "size_bytes"); sanePkgSize(n) {
					a.SizeBytes = int64(n)
				} else if n := anyInt(pkg, "size"); sanePkgSize(n) {
					a.SizeBytes = int64(n)
				}
			}
		}
	}
	// arch_diff: 旧格式变体源把 download_url 按 x86/arm/all 分列（无顶层链接）。
	if a.DownloadURL == "" {
		if ad, ok := m["arch_diff"].(map[string]any); ok {
			for _, k := range []string{currentArch(), "all"} {
				if p, ok2 := ad[k].(map[string]any); ok2 {
					if u := str(p, "download_url"); u != "" {
						a.DownloadURL = u
						if s := str(p, "sha256"); s != "" {
							a.Sha256 = s
						}
						if n := anyInt(p, "size"); n > 0 {
							a.SizeBytes = int64(n)
						}
						// 0.6.303：选中架构 + 全架构清单
						a.Arch = k
						a.Archs = pkgsWithDownload(ad)
						break
					}
				}
			}
		}
	}
	// 无任何下载字段（releases/平铺/arch_diff 全缺）但有版本：按 FnDepot 社区约定
	// 从仓库内 <appname>/<appname>.fpk 构造直链（Docker 应用不走此约定）。
	// 有实测 base 时按它拼（自动适配 master/main），否则回退 raw main 猜测。
	if a.DownloadURL == "" && a.Version != "" && a.IsDocker != "true" {
		if base != "" {
			a.DownloadURL = strings.TrimRight(base, "/") + "/" + name + "/" + name + ".fpk"
		} else if strings.Contains(repoURL, "github.com/") {
			trimmed := strings.TrimPrefix(repoURL, "https://")
			trimmed = strings.TrimPrefix(trimmed, "http://")
			if parts := strings.SplitN(trimmed, "/", 3); len(parts) == 3 {
				a.DownloadURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s/%s.fpk",
					parts[1], strings.TrimSuffix(parts[2], ".git"), name, name)
			}
		}
	}
	// 相对资源路径（下载链接/图标/文档/预览）补全为可访问的直链：
	// 优先按 fnpack.json 实测所在目录（base）拼接——与 JSON 同树的文件一定
	// 可达，且分支正确；base 未知时回退：GitHub 仓库 → raw main 猜测，
	// 其他托管 → 源根 URL 直接拼接。
	// 相对路径判定=不含 "://"（兼容 "./x"、"app/ICON.PNG"、"app/README.md"
	// 等写法——社区源普遍不带 ./ 前缀，只认 ./ 会让 187/231 个 README
	// 和大量图标拿不到直链，靠布局猜测兜底，命中不了就整块不显示）。
	resolveRel := func(relPath string) string {
		relPath = strings.TrimSpace(relPath)
		if relPath == "" || strings.Contains(relPath, "://") {
			return relPath
		}
		rel := strings.TrimPrefix(relPath, "./")
		if base != "" {
			return strings.TrimRight(base, "/") + "/" + rel
		}
		if strings.Contains(repoURL, "github.com/") {
			trimmed := strings.TrimPrefix(repoURL, "https://")
			trimmed = strings.TrimPrefix(trimmed, "http://")
			if parts := strings.SplitN(trimmed, "/", 3); len(parts) == 3 {
				return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s",
					parts[1], strings.TrimSuffix(parts[2], ".git"), rel)
			}
		}
		return repoURL + "/" + rel
	}
	a.DownloadURL = resolveRel(a.DownloadURL)
	a.IconURL = resolveRel(a.IconURL)
	a.ReadmeURL = resolveRel(a.ReadmeURL)
	// 预览图相对路径（"./<app>/Preview/01.png" 等）同样补全为直链
	previewURLs := make([]string, 0, len(a.PreviewURLs))
	for _, p := range a.PreviewURLs {
		if abs := resolveRel(p); abs != "" {
			previewURLs = append(previewURLs, abs)
		}
	}
	a.PreviewURLs = previewURLs
	return a
}

// parseSourceWizard 解析 moo.json 的源级向导字段（示例 7 的
// `wizard: { fields: [{key,label,default,required}] }`）。
// 结构不符/空 fields 返回 nil（调用方按「无向导」处理）。
func parseSourceWizard(v any) *SourceWizard {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := obj["fields"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	fields := make([]SourceWizardField, 0, len(raw))
	for _, it := range raw {
		fm, ok := it.(map[string]any)
		if !ok {
			continue
		}
		key, _ := fm["key"].(string)
		if strings.TrimSpace(key) == "" {
			continue // 无 key 的字段无法回传，整条丢弃
		}
		label, _ := fm["label"].(string)
		def, _ := fm["default"].(string)
		req, _ := fm["required"].(bool)
		fields = append(fields, SourceWizardField{
			Key:      strings.TrimSpace(key),
			Label:    label,
			Default:  def,
			Required: req,
		})
	}
	if len(fields) == 0 {
		return nil
	}
	return &SourceWizard{Fields: fields}
}

// strListField 取字符串数组字段（兼容 JSON 数组与单字符串两种写法）。
func strListField(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(v); s != "" {
			return []string{s}
		}
	}
	return nil
}
