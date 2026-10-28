package api

// mate.go —— 0.6.354：Moo Mate 复用 Moo 的 GitHub Token。
//
// 背景：GitHub API token 原先在 Moo（溯源加速）与 Moo Mate（仓库发现）
// 各存一份，用户要维护两处。现在统一到 Moo：Mate 通过本端点读取 Moo
// 已配置的 token，自身不再保存任何 GitHub 凭据。
//
// 安全边界：
//   - requireAdmin：必须走面板网关 / 本机 socket 的可信管理员通道；
//   - 必须带 X-Moo-Mate: 1（伴侣专用标记，非浏览器可自行附加）；
//   - token 只在响应体里出现，绝不写日志；
//   - GET 只读，不产生任何状态变更。

import (
	"errors"
	"net/http"
	"strings"
)

// mateGithubToken GET /api/mate/github-token
// 返回 Moo 当前生效的 GitHub token（未配置时为空串 + set=false）。
// Moo Mate 侧带 2 分钟缓存，不逐请求打这里。
func (s *Server) mateGithubToken(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Moo-Mate") == "" {
		writeErr(w, http.StatusForbidden, errors.New("缺少 X-Moo-Mate 请求头（该端点为 Moo Mate 专用）"))
		return
	}
	tok := strings.TrimSpace(s.Cfg.GithubToken)
	writeJSON(w, map[string]any{
		"github_token_set": tok != "",
		"github_token":     tok,
	})
}
