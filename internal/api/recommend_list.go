package api

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"moo/internal/config"
	"moo/internal/netguard"
	"moo/internal/reco"
)

// 0.6.379（用户令）：发现页推荐位不再写死在内嵌快照里，改为读远程纯文本
// 清单（与 repo_list.txt 同一套拉取/镜像链逻辑），改清单不用重建 FPK。
// 清单格式：一行一条 `appname@源名`（源名可省略），`#` 注释；
// 顺序即展示顺序；目录里找不到的条目**整条跳过**（不留空位、不随机补位）。

//go:embed default_recommend.txt
var embeddedRecommendList string

// recommendEntry 清单里的一条：AppName 必填，Source（源名）可选。
type recommendEntry struct {
	AppName string
	Source  string
	// Color 0.6.536（用户令）：该位横幅的极光配色（`aurora-1`..`aurora-12`）。
	// 行尾第三段 / 空白后写 `aurora-N` 或裸数字 N；空 = 前端按应用名哈希取色。
	Color string
}

const (
	recoListTTL      = 10 * time.Minute
	recoListMaxItems = 12
	recoListMaxBytes = 64 << 10
)

// 0.6.380：TTL 过期后**不再同步阻塞**（曾每个候选等 15s，默认清单地址
// raw.githubusercontent.com 在部分网络不可达 → 每 10 分钟首请求稳定卡
// 16.6s）。有旧值 → 先回旧值、后台单飞刷新；冷启动（无任何缓存）才允许
// 同步等待 recoColdFetchBudget，超时立刻回内嵌清单并转后台。
// var 不是 const —— 单测要把预算压到毫秒级。
var (
	recoColdFetchBudget  = 1500 * time.Millisecond
	recoCandidateTimeout = 8 * time.Second
	recoRefreshBudget    = 40 * time.Second
	// recoFallbackTTL：拉取失败后回退内嵌清单时的退避窗口。没有它的话
	// 「从未成功过」会让每个请求都走冷启动同步抓取（实测每次 1~2.5s），
	// 反而把发现页拖慢；退避期内直接回内嵌清单，过期后再按 SWR 后台重试。
	recoFallbackTTL = 2 * time.Minute
)

// parseRecoColor 归一化一行里的配色写法：`aurora-3` / `Aurora3` / `3` → `aurora-3`；
// 越界或认不出 → 空串（前端回落哈希取色，不因写错一个色号就丢掉整条推荐）。
func parseRecoColor(tok string) string {
	t := strings.ToLower(strings.TrimSpace(tok))
	if t == "" {
		return ""
	}
	t = strings.TrimPrefix(t, "aurora-")
	t = strings.TrimPrefix(t, "aurora")
	n, err := strconv.Atoi(strings.TrimSpace(t))
	if err != nil || n < 1 || n > 12 {
		return ""
	}
	return "aurora-" + strconv.Itoa(n)
}

// parseRecommendList 解析推荐清单文本。
//
// 0.6.536（用户令）：行尾可带极光配色——`appname@源名 aurora-3`（或裸 `3`），
// 也接受第三段 `appname@源名@aurora-3`。配色不写 = 前端按应用名哈希取色（旧行为）。
func parseRecommendList(body string) []recommendEntry {
	seen := map[string]bool{}
	out := make([]recommendEntry, 0, 8)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 行内 # 注释（配色不含 #，可直接截断）
		if i := strings.Index(line, "#"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		// 拆「条目 + 行尾附加段」：附加段里第一个合法色号即配色，其余忽略
		// （兼容旧清单里 `appname 行尾注释` 的写法）。
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		head, color := fields[0], ""
		for _, f := range fields[1:] {
			if color = parseRecoColor(f); color != "" {
				break
			}
		}
		name, src := head, ""
		parts := strings.Split(head, "@")
		if len(parts) >= 2 {
			// 第三段及以后：末段是合法色号 → 当配色，源码名取中间各段。
			if len(parts) >= 3 {
				if c := parseRecoColor(parts[len(parts)-1]); c != "" {
					color = c
					parts = parts[:len(parts)-1]
				}
			}
			name = strings.TrimSpace(parts[0])
			src = strings.TrimSpace(strings.Join(parts[1:], "@"))
		}
		if name == "" || strings.ContainsAny(name, "/\\") {
			continue
		}
		k := strings.ToLower(name) + "@" + strings.ToLower(src)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, recommendEntry{AppName: name, Source: src, Color: color})
		if len(out) >= recoListMaxItems {
			break
		}
	}
	return out
}

// recommendListURLs 生效的清单地址候选：自定义地址优先；否则 lists/ 新路径
// → 根目录旧地址（0.6.512 清单目录化，兼容旧文件与回滚）。
func (s *Server) recommendListURLs() []string {
	if u := strings.TrimSpace(s.Cfg.RecommendListURL); u != "" {
		return []string{u}
	}
	return config.ListFileURLs(s.Cfg.ListBaseURL, "recommend_list.txt", config.DefaultRecommendListURL)
}

// recommendListURL 主清单地址（缓存键用第一个候选）。
func (s *Server) recommendListURL() string {
	return s.recommendListURLs()[0]
}

// recoListCandidates 清单拉取候选顺序：清单是 GitHub 地址时，**健康镜像
// （按延迟升序、最多 4 个）在前、直连最后**；其它地址只留原 URL。
//
// 0.6.380（用户令「发现页的阻塞怎么回事」）：默认清单地址在
// raw.githubusercontent.com，部分出口直连 0 字节、要等满超时；镜像 200ms
// 级即可返回。镜像只能改传输，改不了清单语义（内容仍只含 appname@源名）。
func recoListCandidates(listURL string, m *mirrorMonitor) []string {
	isGH := strings.Contains(listURL, "raw.githubusercontent.com") || strings.Contains(listURL, "github.com/")
	if m == nil || !isGH {
		return []string{listURL}
	}
	type mk struct {
		key string
		lat int64
	}
	var order []mk
	for _, st := range m.gh() {
		if st.Status == "ok" {
			order = append(order, mk{st.Key, st.LatencyMS})
		}
	}
	if len(order) == 0 {
		for _, o := range config.GitHubMirrorOptions() {
			if o.URL != "" && o.Key != "custom" {
				order = append(order, mk{o.Key, 1 << 30})
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].lat < order[j].lat })
	urlByKey := map[string]string{}
	for _, o := range config.GitHubMirrorOptions() {
		urlByKey[o.Key] = o.URL
	}
	out := make([]string, 0, 5)
	for i, o := range order {
		if i >= 4 {
			break
		}
		if u := urlByKey[o.key]; u != "" {
			out = append(out, u+listURL)
		}
	}
	return append(out, listURL)
}

// fetchRecommendListText 拉取清单：自定义地址优先；未自定义时先读 lists/
// 新路径，失败回落根目录旧地址（0.6.512 清单目录化）。
func (s *Server) fetchRecommendListText(ctx context.Context) (string, error) {
	var lastErr error
	for _, listURL := range s.recommendListURLs() {
		body, err := s.fetchRecommendListOne(ctx, listURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("推荐清单获取失败: %w", lastErr)
}

// fetchRecommendListOne 拉单个清单地址：镜像优先 → 直连兜底；单候选
// recoCandidateTimeout 超时。
func (s *Server) fetchRecommendListOne(ctx context.Context, listURL string) (string, error) {
	// 0.6.380（门 2 审计 D2）：清单地址是**管理员配置**的 URL（设置项
	// recommend_list_url），与「应用源 URL / 自建镜像 / 源列表地址」同属
	// 管理员显式配置的信任面；但清单内容只含 `appname@源名`（不含任何 URL），
	// 不会驱动二次抓取。为防配置被误改/被指向本机或云元数据端点当代打通道，
	// 这里仍按源列表同一口径过 netguard：默认只允许公共地址，
	// 需要指向内网时用既有开关 `SourceListAllowPrivate` 显式放开。
	if !s.Cfg.SourceListAllowPrivate {
		if host, gerr := netguard.Public(listURL); gerr != nil {
			return "", fmt.Errorf("推荐清单地址被安全策略拒绝（%s）: %w", host, gerr)
		}
	}
	// 候选顺序：0.6.380 起**镜像优先、直连最后**（见 recoListCandidates）。
	candidates := recoListCandidates(listURL, s.Mirrors)
	client := &http.Client{}
	var lastErr error
	for _, c := range candidates {
		cctx, cancel := context.WithTimeout(ctx, recoCandidateTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, c, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "moo/0.6")
		resp, doErr := client.Do(req)
		if doErr != nil {
			cancel()
			lastErr = doErr
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, recoListMaxBytes))
		resp.Body.Close()
		cancel()
		if rerr == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
			return string(body), nil
		}
		if rerr != nil {
			lastErr = rerr
		} else {
			lastErr = fmt.Errorf("HTTP %s", resp.Status)
		}
	}
	return "", fmt.Errorf("推荐清单获取失败: %w", lastErr)
}

// recommendEntries 返回当前生效清单（**请求路径永不长时间阻塞**）：
//   - TTL 内 → 直接回缓存；
//   - 有上一次成功值（同 URL）但已过期 → 立刻回旧值，后台单飞刷新（SWR）；
//   - 冷启动/换地址 → 用 recoColdFetchBudget 同步试一次（镜像命中通常毫秒级），
//     超时即回内嵌清单并继续后台刷新。
//
// bool 返回值表示「本次是否拿到了新鲜清单」（供 /api/recommended/refresh 展示）。
func (s *Server) recommendEntries(ctx context.Context) ([]recommendEntry, bool) {
	url := s.recommendListURL()
	s.recoMu.Lock()
	if s.recoLoaded && s.recoURL == url && time.Since(s.recoAt) < recoListTTL {
		e := s.recoEntries
		s.recoMu.Unlock()
		return e, true
	}
	stale, staleURL := s.recoEntries, s.recoURL
	loaded := s.recoLoaded
	staleAt := s.recoAt
	s.recoMu.Unlock()

	// 上次拉取失败已回退内嵌清单：退避窗口内直接回它，别每个请求都重试。
	if !loaded && staleURL == url && len(stale) > 0 && time.Since(staleAt) < recoFallbackTTL {
		return stale, false
	}

	if staleURL == url && len(stale) > 0 {
		s.startRecoRefresh()
		return stale, false
	}

	type recoFetchResult struct {
		entries []recommendEntry
		ok      bool
	}
	cctx, cancel := context.WithTimeout(ctx, recoColdFetchBudget)
	defer cancel()
	resCh := make(chan recoFetchResult, 1)
	go func() { e, ok := s.refreshRecommendList(cctx); resCh <- recoFetchResult{e, ok} }()
	select {
	case r := <-resCh:
		if r.ok {
			return r.entries, true
		}
	case <-cctx.Done():
		// 冷启动超预算：不等了，先缓存内嵌清单并立刻返回；后台补齐（独立
		// ctx，请求断开也要刷完）。
	}
	fb := parseRecommendList(embeddedRecommendList)
	s.recoMu.Lock()
	s.recoEntries, s.recoAt, s.recoLoaded, s.recoURL = fb, time.Now(), false, url
	s.recoMu.Unlock()
	s.startRecoRefresh()
	return fb, false
}

// refreshRecommendList 同步拉取并落缓存（后台刷新与 POST /api/recommended/refresh
// 共用）。拉取失败沿用上次成功值；从未成功过 → 内嵌兜底。
func (s *Server) refreshRecommendList(ctx context.Context) ([]recommendEntry, bool) {
	url := s.recommendListURL()
	body, err := s.fetchRecommendListText(ctx)
	if err == nil {
		entries := parseRecommendList(body)
		if len(entries) > 0 {
			s.recoMu.Lock()
			s.recoEntries, s.recoAt, s.recoLoaded, s.recoURL = entries, time.Now(), true, url
			s.recoMu.Unlock()
			return entries, true
		}
		err = fmt.Errorf("清单解析为空")
	}
	log.Printf("[reco] 推荐清单拉取失败（沿用旧值/内嵌兜底）: %v", err)
	s.recoMu.Lock()
	stale, staleURL := s.recoEntries, s.recoURL
	s.recoMu.Unlock()
	if staleURL == url && len(stale) > 0 {
		return stale, false
	}
	// 首次即失败：把内嵌清单也缓存起来（recoLoaded=false 标记「这是兜底值」），
	// 让后续请求走退避窗口而不是每次都冷启动同步抓取。
	fb := parseRecommendList(embeddedRecommendList)
	s.recoMu.Lock()
	s.recoEntries, s.recoAt, s.recoLoaded, s.recoURL = fb, time.Now(), false, url
	s.recoMu.Unlock()
	return fb, false
}

// startRecoRefresh 单飞后台刷新：同一时刻最多一个 goroutine 在拉清单，
// 避免并发请求各自起一条 15s 级超时链把出口打满。
func (s *Server) startRecoRefresh() {
	s.recoMu.Lock()
	if s.recoRefreshing {
		s.recoMu.Unlock()
		return
	}
	s.recoRefreshing = true
	s.recoMu.Unlock()
	go func() {
		defer func() {
			s.recoMu.Lock()
			s.recoRefreshing = false
			s.recoMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), recoRefreshBudget)
		defer cancel()
		s.refreshRecommendList(ctx)
	}()
}

// resolveRecommendEntry 清单条目 → 目录实时卡（按 appname 归一 + 源名归一）。
// 同 appname 多源时取「有图标优先、更新时间新优先」，保证结果确定。
// 目录里没有 → ok=false，调用方跳过该位（不回落快照、不补位）。
func resolveRecommendEntry(catalog []AppInfo, e recommendEntry) (AppInfo, bool) {
	nName, nSrc := reco.NormAppName(e.AppName), reco.NormAppName(e.Source)
	var hits, exact []AppInfo
	for _, a := range catalog {
		if reco.NormAppName(a.AppName) != nName {
			continue
		}
		hits = append(hits, a)
		if nSrc != "" && reco.NormAppName(a.Source) == nSrc {
			exact = append(exact, a)
		}
	}
	pick := exact
	if len(pick) == 0 {
		pick = hits
	}
	if len(pick) == 0 {
		return AppInfo{}, false
	}
	sort.SliceStable(pick, func(i, j int) bool {
		hi, hj := pick[i].IconURL != "", pick[j].IconURL != ""
		if hi != hj {
			return hi
		}
		return pick[i].UpdatedAt > pick[j].UpdatedAt
	})
	return pick[0], true
}
