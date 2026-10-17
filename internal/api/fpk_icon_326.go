package api

// fpk_icon_326.go —— 图标兜底：从发布包 FPK 内提取图标（0.6.326 收尾）。
//
// 背景：上游会把图标文件提交坏（实测 cmbya/FnDepot 的 assets/icons/ZJJX.png
// 整份被 UTF-8 替换符污染，1166 处 EF BF BD，IHDR CRC 不符；其应用仓库的
// ICON.PNG / ICON_256.PNG 同样是坏文件）。但 FPK 内部的
// app.tgz → ui/images/icon_256.png 是构建工具生成的有效 PNG。
//
// 策略（只在前序所有图标来源全败后才调用）：
//   - 只对「声明了 icon_url 且包体积已知且 ≤8MB」的应用尝试（不为图标拉大包）；
//   - 下载与解压全程有上限（防压缩炸弹 / 内存放大）；
//   - 依次尝试：根目录 ICON_256.PNG / ICON.PNG / … → app.tgz 内 ui/images/*；
//   - 每个候选都过魔数校验（含单字节 UTF-8 污染修复），不合格跳过；
//   - 任何失败都静默返回 false，保持原有 404 行为不变。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"moo/internal/config"
	"moo/internal/logagg"
	"moo/internal/netguard"
	"moo/internal/source"
)

const (
	fpkIconMaxFPKBytes = 20 << 20 // 只为图标拉 ≤20MB 的包（实测 8.86/14.6/15.9/17.7/19MB 包内图标有效）
	fpkIconMaxEntry    = 1 << 20  // 单个图标条目上限
	fpkIconMaxUnpack   = 32 << 20 // 解压总量上限（防压缩炸弹）
)

// fpkIconNames 图标文件名 → 优先级（越小越优先；只认这些确定性的图标名）。
var fpkIconNames = map[string]int{
	"icon_256.png": 0, "icon_0_256.png": 0,
	"icon.png": 1, "icon_0.png": 2,
	"icon_64.png": 3, "icon-256.png": 0, "icon-64.png": 3,
}

func fpkIconScore(name string) (int, bool) {
	base := strings.ToLower(path.Base(strings.TrimSpace(name)))
	if sc, ok := fpkIconNames[base]; ok {
		return sc, true
	}
	// ICON.PNG / ICON_256.PNG 大小写不敏感
	switch base {
	case "icon.png", "icon_256.png", "icon-256.png":
		return 1, true
	}
	return 0, false
}

// validIconBytes 返回可用图标字节（含单字节 UTF-8 污染修复）与 MIME。
func validIconBytes(b []byte) ([]byte, string, bool) {
	if fixed := repairPngUTF8Mangle(b); fixed != nil {
		return fixed, "image/png", true
	}
	if !looksLikeImage(b) {
		return nil, "", false
	}
	ct := sniffImageMime(b)
	if ct == "" {
		ct = "image/png"
	}
	return b, ct, true
}

// extractFpkIcon 从 FPK 字节里取优先级最高的有效图标。
// 纯函数（不做网络/缓存），便于单测；任何结构异常都返回 false。
func extractFpkIcon(data []byte) ([]byte, string, bool) {
	if len(data) == 0 {
		return nil, "", false
	}
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, "", false
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	type pick struct {
		score int
		data  []byte
		ctype string
	}
	best := pick{score: 1 << 30}
	var appTgz []byte
	unpacked := 0

	takeIcon := func(name string, r io.Reader) {
		sc, ok := fpkIconScore(name)
		if !ok || sc >= best.score {
			// 仍需读空该条目，tar 才能继续
			_, _ = io.Copy(io.Discard, io.LimitReader(r, fpkIconMaxEntry))
			return
		}
		b, _ := io.ReadAll(io.LimitReader(r, fpkIconMaxEntry+1))
		if len(b) > fpkIconMaxEntry { // 超限=残缺图标，宁缺勿坏
			return
		}
		unpacked += len(b)
		if unpacked > fpkIconMaxUnpack {
			return
		}
		if v, ct, ok := validIconBytes(b); ok {
			best = pick{score: sc, data: v, ctype: ct}
		}
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if strings.Contains(name, "/") {
			continue // 根目录只认直接放置的图标
		}
		if strings.EqualFold(name, "app.tgz") {
			b, _ := io.ReadAll(io.LimitReader(tr, fpkIconMaxFPKBytes))
			appTgz = b
			continue
		}
		takeIcon(name, tr)
	}

	// app.tgz 内：ui/images/*（含 app/ui/images/*），按同一优先级挑。
	if len(appTgz) > 0 {
		if zg, err := gzip.NewReader(bytes.NewReader(appTgz)); err == nil {
			inner := tar.NewReader(zg)
			for {
				hdr, err := inner.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					break
				}
				if hdr.Typeflag != tar.TypeReg {
					continue
				}
				name := strings.TrimPrefix(hdr.Name, "./")
				low := strings.ToLower(name)
				if !strings.Contains(low, "ui/images/") && !strings.Contains(low, "ui\\images\\") {
					continue
				}
				takeIcon(name, inner)
			}
			_ = zg.Close()
		}
	}

	if best.data == nil {
		return nil, "", false
	}
	return best.data, best.ctype, true
}

// fetchReleaseOne 拉取单个候选（上限 max；含 Content-Length 预检）。
func fetchReleaseOne(ctx context.Context, rawURL string, max int64) ([]byte, bool) {
	if host, gerr := netguard.Public(rawURL); gerr != nil {
		logagg.Default.Record("icon", host, "跳过（安全策略）%s: %v", rawURL, gerr)
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false
	}
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, false
	}
	if resp.ContentLength > max {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || len(b) == 0 || int64(len(b)) > max {
		return nil, false
	}
	return b, true
}

// fetchReleaseBytes 拉发布包：直连（github.com → objects CDN）+ 镜像前缀
// 并发竞速，首个 200 胜出。实测本机直连 release 资产常挂起，而镜像
// （hub.conversun.com 等）1s 内可达，故必须带镜像候选，否则兜底形同虚设。
func (s *Server) fetchReleaseBytes(ctx context.Context, rawURL string, max int64) ([]byte, bool) {
	cands := []string{rawURL}
	// 健康数据冷（刚重启）时 ghOrderedKeys 为空 → 退回声明顺序（与
	// mirrorVariants 同口径，排除 conversun/custom），否则只剩直连，
	// 本机直连 release 资产会挂到超时（便签记事本 12:08 实测）。
	var keys []string
	if s != nil && s.Mirrors != nil {
		keys = s.ghOrderedKeys()
	}
	if len(keys) == 0 {
		for _, opt := range config.GitHubMirrorOptions() {
			if opt.URL == "" || opt.Key == "conversun" || opt.Key == "custom" {
				continue
			}
			keys = append(keys, opt.Key)
		}
	}
	for _, k := range keys {
		pref := ""
		if p := mirrorURLByKey(k); p != "" {
			pref = p
		} else if k == "custom" && s != nil && s.Cfg != nil && s.Cfg.CustomGitHubMirror != "" {
			pref = s.Cfg.CustomGitHubMirror
		}
		if pref == "" {
			continue
		}
		cands = append(cands, ensureTrailingSlash(pref)+rawURL)
		if len(cands) >= 3 {
			break
		}
	}
	log.Printf("[fpkicon] 候选 %d 个: %v", len(cands), cands)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type res struct {
		b  []byte
		ok bool
	}
	ch := make(chan res, len(cands))
	for _, u := range cands {
		go func(u string) {
			b, ok := fetchReleaseOne(cctx, u, max)
			if ok {
				cancel()
			}
			ch <- res{b, ok}
		}(u)
	}
	for i := 0; i < len(cands); i++ {
		r := <-ch
		log.Printf("[fpkicon] 候选结果 ok=%v len=%d（剩 %d）", r.ok, len(r.b), len(cands)-i-1)
		if r.ok {
			return r.b, true
		}
	}
	return nil, false
}

// fpkIconFallback 声明图标失败后的最后兜底：从发布包内取图标。
// 门槛（避免为图标拉大包）：必须声明 icon_url、包体积已知且 ≤8MB。
// fpkIconSem 限制同时进行的「为图标下包」数量：冷启动 warm 会并发探测，
// 不限流时多张大包会同时占满上行（0.6.326）。
var fpkIconSem = make(chan struct{}, 2)

// fpkIconCall 是一次「为图标下包」的共享结果（0.6.326 低风险项 #3）。
// 冷启动 warm 与详情页可能同时为同一应用各发一次请求；只有信号量限流
// 没有去重时，两个并发请求仍会各下一遍包。
type fpkIconCall struct {
	done chan struct{}
	data []byte
	ct   string
	ok   bool
}

var (
	fpkIconMu    sync.Mutex
	fpkIconCalls = map[string]*fpkIconCall{}
)

func (s *Server) fpkIconFallback(ctx context.Context, a *source.App) ([]byte, string, bool) {
	if a == nil || a.DownloadURL == "" || a.IconURL == "" {
		return nil, "", false
	}
	if a.SizeBytes <= 0 || a.SizeBytes > fpkIconMaxFPKBytes {
		log.Printf("[fpkicon] 跳过 %s@%s：size=%d（门限 %d）", a.Source, a.Name, a.SizeBytes, fpkIconMaxFPKBytes)
		return nil, "", false
	}
	callKey := a.Source + "@" + a.Name + "@" + a.Version
	negKey := "fpkicon|" + callKey
	// 失败负缓存先判：已确认下不动的包不再排进等待队列。
	if s.iconStore().IsNegative(negKey) {
		return nil, "", false
	}
	fpkIconMu.Lock()
	if call := fpkIconCalls[callKey]; call != nil {
		// 已有同应用下载在跑：等它的结果，不再重复下包。
		fpkIconMu.Unlock()
		select {
		case <-call.done:
			return call.data, call.ct, call.ok
		case <-ctx.Done():
			return nil, "", false
		}
	}
	call := &fpkIconCall{done: make(chan struct{})}
	fpkIconCalls[callKey] = call
	fpkIconMu.Unlock()
	defer func() {
		fpkIconMu.Lock()
		delete(fpkIconCalls, callKey)
		fpkIconMu.Unlock()
		close(call.done) // 结果已写入，唤醒所有等待者
	}()
	// 共享下载跑在脱离请求的 ctx 上：某个等待者（如别名命中后取消）
	// 退出不应连累其它等待者无功而返。
	workCtx, workCancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer workCancel()
	call.data, call.ct, call.ok = s.fpkIconDownload(workCtx, a, negKey)
	return call.data, call.ct, call.ok
}

// fpkIconDownload 真正的「下包 + 解包取图标」；由 fpkIconFallback 去重后调用。
func (s *Server) fpkIconDownload(ctx context.Context, a *source.App, negKey string) ([]byte, string, bool) {
	select {
	case fpkIconSem <- struct{}{}:
		defer func() { <-fpkIconSem }()
	case <-ctx.Done():
		return nil, "", false
	}
	log.Printf("[fpkicon] 尝试 %s@%s：size=%d dl=%s", a.Source, a.Name, a.SizeBytes, a.DownloadURL)
	// 失败也记 30min 负缓存（独立键）：避免同一应用每轮都重下包。
	st := s.iconStore()
	if st.IsNegative(negKey) {
		return nil, "", false
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	data, ok := s.fetchReleaseBytes(cctx, a.DownloadURL, fpkIconMaxFPKBytes)
	if !ok {
		st.MarkNegative(negKey)
		return nil, "", false
	}
	icon, ct, found := extractFpkIcon(data)
	if !found {
		log.Printf("[fpkicon] 失败 %s@%s：包内无有效图标（%d B）", a.Source, a.Name, len(data))
		st.MarkNegative(negKey)
		return nil, "", false
	}
	log.Printf("[fpkicon] 成功 %s@%s：%s %d B", a.Source, a.Name, ct, len(icon))
	return icon, ct, true
}
