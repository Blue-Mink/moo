package official

// 面板前端 OAuth 支持探测（0.6.254）。
//
// 背景：/signin 的 PKCE 授权 UI 只在 fnOS 1.2.0800+ 的面板前端里（懒加载的
// oauth-*.js chunk 含 code_challenge / client_id 常量）；旧版面板前端收到
// 带 PKCE 参数的 /signin 只渲染普通登录页，登录后不出现授权码 —— 授权流程
// 走不完。后端 /oauthapi/* 端点新旧版本都有，所以只能探测前端。
//
// 探测法（全部本机回环、无登录）：
//  1. GET /signin → 取 HTML 静态引用的 /assets/*.js（层 1，约 16 个）
//  2. 下载层 1 → 任一含 code_challenge/YJNMPJUGA9 → 支持
//  3. 从层 1 内容里挖"目标 chunk"名（signin/oauth/authoriz 前缀，Vite 懒
//     加载用相对名 ./xxx.js）→ 定向下载 → 判定
//  4. 仍未命中：泛化 BFS（从已下载 JS 里再挖带哈希的 chunk 名，逐层追）兜底
//  5. 结果进程内缓存（面板前端随 fnOS 升级才变，无需反复探）
//
// 注意：入口 index chunk 能挖出 190+ 个 chunk 名，BFS 必须限量；目标 chunk
// 定向优先，避免 oauth/signin chunk 被挤出队列（真机实测教训）。

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	assetRefRe = regexp.MustCompile(`/assets/[A-Za-z0-9_.-]+\.js`)
	// 带哈希的裸 chunk 名（Vite 动态 import：./name-Hash.js）。
	chunkNameRe = regexp.MustCompile(`[A-Za-z0-9_-]+-[A-Za-z0-9_-]{6,}\.js`)
	// 目标 chunk：授权 UI 相关的命名（真机：signin-*.js 引 oauth-*.js）。
	targetChunkRe = regexp.MustCompile(`[A-Za-z0-9_-]*(signin|oauth|authoriz)[A-Za-z0-9_-]*-[A-Za-z0-9_-]{4,}\.js`)

	// 授权 UI 的协议常量（1.2.0800+ 面板 chunk 内出现；旧版前端零出现）。
	oauthMarkers = []string{"code_challenge", "YJNMPJUGA9"}
)

// ProbeUISupport 探测本机面板前端是否支持 /signin PKCE 授权 UI。
// 探测失败（面板不可达等）返回 false（按不支持处理，走面板账号通道）。
func ProbeUISupport(ctx context.Context, baseURL string) bool {
	hc := &http.Client{Timeout: 15 * time.Second}
	get := func(u string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, nil
		}
		r, err := hc.Do(req)
		if err != nil {
			return 0, nil
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		r.Body.Close()
		if err != nil {
			return r.StatusCode, nil
		}
		return r.StatusCode, b
	}
	hasMarker := func(b []byte) bool {
		for _, m := range oauthMarkers {
			if strings.Contains(string(b), m) {
				return true
			}
		}
		return false
	}

	// 1) /signin HTML → 层 1 静态引用。
	st, html := get(baseURL + "/signin")
	if st != http.StatusOK || len(html) == 0 {
		return false
	}
	seen := map[string]bool{}
	var layer1 []string
	for _, u := range assetRefRe.FindAllString(string(html), -1) {
		if !seen[u] {
			seen[u] = true
			layer1 = append(layer1, u)
		}
	}

	// 2) 下载层 1 + 收集目标 chunk 名 + 泛化 BFS 队列。
	var bfs []string
	for _, u := range layer1 {
		st, b := get(baseURL + u)
		if st != http.StatusOK {
			continue
		}
		if hasMarker(b) {
			return true
		}
		for _, nm := range targetChunkRe.FindAllString(string(b), -1) {
			fu := "/assets/" + nm
			if !seen[fu] {
				seen[fu] = true
				bfs = append(bfs, fu)
			}
		}
	}

	// 3) 定向下载目标 chunk（signin/oauth/authoriz 命名，数量极少）。
	for _, u := range bfs {
		st, b := get(baseURL + u)
		if st != http.StatusOK {
			continue
		}
		if hasMarker(b) {
			return true
		}
		// 目标 chunk 可能再引一层（signin-*.js → oauth-*.js）
		for _, nm := range targetChunkRe.FindAllString(string(b), -1) {
			fu := "/assets/" + nm
			if !seen[fu] {
				seen[fu] = true
				bfs = append(bfs, fu)
			}
		}
	}

	// 4) 泛化 BFS 兜底（限量，防入口 chunk 挖出 190+ 名字挤爆队列）。
	queue := bfs
	total := len(layer1)
	for len(queue) > 0 && total < 80 {
		u := queue[0]
		queue = queue[1:]
		st, b := get(baseURL + u)
		if st != http.StatusOK {
			continue
		}
		total++
		if hasMarker(b) {
			return true
		}
		var names []string
		names = append(names, assetRefRe.FindAllString(string(b), -1)...)
		for _, nm := range chunkNameRe.FindAllString(string(b), -1) {
			if !strings.Contains(nm, "/") {
				names = append(names, "/assets/"+nm)
			}
		}
		for _, nxt := range names {
			if !seen[nxt] && total < 80 {
				seen[nxt] = true
				queue = append(queue, nxt)
			}
		}
	}
	return false
}

// UISupportStatus 返回面板前端授权 UI 支持状态（进程内缓存）。
// known=false 表示尚未探测完成（首次调用已触发后台探测，UI 显示「检测中」）。
func (m *Manager) UISupportStatus() (support, known bool) {
	m.uiSupportMu.Lock()
	defer m.uiSupportMu.Unlock()
	if m.uiSupportProbing {
		return false, false
	}
	if m.uiSupportKnown {
		return m.uiSupport, true
	}
	// 触发一次后台探测（面板不可达等失败按不支持处理）。
	m.uiSupportProbing = true
	go func() {
		s := ProbeUISupport(context.Background(), m.baseURL)
		m.uiSupportMu.Lock()
		m.uiSupport = s
		m.uiSupportKnown = true
		m.uiSupportProbing = false
		m.uiSupportMu.Unlock()
	}()
	return false, false
}
