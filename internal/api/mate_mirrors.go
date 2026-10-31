package api

// mate_mirrors.go —— 0.6.380：Moo Mate 复用 Moo 的 GitHub 加速镜像表。
//
// 背景（用户 2026-10-31 令「moo 的 github 加速能不能给 mate 侧复用」）：
// Mate 自更新下载安装包原先直连 github.com（国内出口常 0 字节、进度卡死），
// 而 Moo 侧本来就有镜像健康度数据（状态/吞吐/延迟）。这里把「只有 ok、
// 按吞吐降序」的前缀列表暴露给伴侣，Mate 不再维护第二份镜像表。
//
// 安全边界（与 /api/mate/github-token 同口径）：
//   - requireAdmin：只能走面板网关 / 本机 socket 的可信管理员通道；
//   - 必须带 X-Moo-Mate: 1（伴侣专用标记）；
//   - 只返回公开镜像前缀，不含任何凭据；GET 只读。

import (
	"errors"
	"net/http"

	"moo/internal/config"
)

// mateMirrorMax 单次返回的镜像上限（伴侣按顺序试，给多了只是白等）。
const mateMirrorMax = 6

// mateMirrors GET /api/mate/mirrors
// 返回健康 GitHub 加速前缀（含尾斜杠，按吞吐降序）；无健康数据时回落到
// config.GitHubMirrorOptions 的声明顺序（与 Moo 自更新的静态回退一致）。
func (s *Server) mateMirrors(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Moo-Mate") == "" {
		writeErr(w, http.StatusForbidden, errors.New("缺少 X-Moo-Mate 请求头（该端点为 Moo Mate 专用）"))
		return
	}
	prefixes := selfUpdateMirrorPrefixes(s)
	if len(prefixes) == 0 {
		// 无监控实例/无健康数据 → 按 Moo 自更新同一份声明顺序回退，
		// 保证伴侣侧永远拿得到可用前缀（而不是空列表）。
		for _, o := range config.GitHubMirrorOptions() {
			if o.URL != "" && o.Key != "conversun" && o.Key != "custom" {
				prefixes = append(prefixes, o.URL)
			}
		}
	}
	if len(prefixes) > mateMirrorMax {
		prefixes = prefixes[:mateMirrorMax]
	}
	if prefixes == nil {
		prefixes = []string{}
	}
	writeJSON(w, map[string]any{"prefixes": prefixes, "count": len(prefixes)})
}
