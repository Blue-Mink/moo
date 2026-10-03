package api

// 0.6.255：无头 OAuth 授权（无浏览器）。
//
// 旧版 fnOS 面板前端（1.2.0800 以下）的 /signin 不渲染 PKCE 授权 UI（登录后
// 不出现授权码），iframe 流程走不完；但 /oauthapi/* 后端端点新旧版本都有。
// 无头授权在请求体里临时携带面板账号（0.6.255 起设置中不再存储面板账号）：
// 一次性 WS 登录拿会话 → GET /oauthapi/authorize 取一次性 code →
// /oauthapi/third-part/token 换 token。**凭据不落盘、不缓存**，仅在本次
// 请求生命周期内使用。授权完成后官方目录走 OAuth 令牌（自动续期），
// 仅在令牌续期彻底失效需要重新授权时才再临时输入一次。
//
// 注意：/oauthapi/authorize 的精确契约以真机实测为准（8.0.0 JS 里该端点
// 存在但请求/响应字段未见完整文档）——解析做多形状兜底，未知形状诚实报错
// 并附原始响应片段。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"moo/internal/official"
	"moo/internal/panel"
)

// headlessAuthorize 处理 POST /api/official/authorize-headless。
// 0.6.255：请求体 {username, password} = 本次授权临时面板账号（不落盘）。
func (s *Server) headlessAuthorize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := jsonDecode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("请求体需为 {username, password}"))
		return
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		writeErr(w, http.StatusBadRequest, errors.New("无头授权需要临时面板账号：请填写本机 Web 面板的账号与密码（仅本次使用，不保存）"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	mgr := s.officialStoreV().mgr

	// 1) 一次性临时客户端 + 面板登录（手动动作，等同 AppsForce：
	// 不受自动退避窗口约束）。凭据只活在本次请求里。
	client := panel.NewClient("", body.Username, body.Password)
	if err := client.EnsureLoggedIn(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("面板登录失败（可能在限流窗口，约 1 小时后重试）: "+err.Error()))
		return
	}

	// 2) 生成 PKCE 对。
	challenge, deviceID, scope, err := mgr.BeginHeadlessAuthorization()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	q := url.Values{}
	q.Set("client_id", official.ClientID)
	q.Set("scope", scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("device_id", deviceID)
	q.Set("device_name", official.DeviceName)
	q.Set("device_model", official.DeviceModel)

	// 3) 取一次性 code。
	b, err := client.RawJSON(ctx, http.MethodGet, "/oauthapi/authorize", q, nil)
	if err != nil {
		// 0.6.259：RawJSON 返回的是 %w 包装错误（"面板端点不存在: 404…"），
		// 必须用 errors.Is 判定，== 永远匹配不上（此前 404 分支从未命中）。
		if errors.Is(err, panel.ErrEndpointNotFound) {
			mgr.Cancel()
			// 0.6.259：老面板无 /oauthapi/authorize（OAuth 不可用）→ 面板账号兜底。
			// 第 1 步面板登录已验证通过；把凭据只写进内存里的 s.Panel.Client()
			//（不落盘、不 Save），此后 listApps/detailFn 的 OAuth 失败会回退到面板
			// WS 通道（client.AppList/AppDetail），官方目录即可加载。会话用到失效
			// 或 Moo 重启后需重填（方案 A，不存密码）。
			if pc := s.Panel.Client(); pc != nil {
				pc.Username = body.Username
				pc.Password = body.Password
			}
			s.Panel.resetFailState()
			s.officialStoreV().invalidate()
			writeJSON(w, map[string]any{
				"ok":      true,
				"mode":    "panel",
				"message": "此面板不支持 OAuth 授权，已改用面板账号兜底连接官方源（会话失效后需重新填写）",
			})
			return
		}
		mgr.Cancel()
		writeErr(w, http.StatusBadGateway, errors.New("取授权码失败: "+err.Error()))
		return
	}
	code, rawHint, ok := extractOAuthCode(b)
	if !ok {
		mgr.Cancel()
		writeErr(w, http.StatusBadGateway, errors.New("授权响应中没有可用的 code（原始响应: "+rawHint+"）"))
		return
	}

	// 4) code 换 token（Manager 内部走 /oauthapi/third-part/token）。
	if err := mgr.CompleteAuthorization(ctx, code); err != nil {
		writeErr(w, http.StatusBadGateway, errors.New("code 换 token 失败: "+err.Error()))
		return
	}
	// 授权成功 → 清失败退避 + 失效目录缓存（同 /callback 路径）。
	s.Panel.resetFailState()
	s.officialStoreV().invalidate()
	writeJSON(w, map[string]any{"ok": true})
}

// extractOAuthCode 从 /oauthapi/authorize 响应里尽力提取一次性 code。
// 多形状兜底：{code:0,data:{code}} / {code:0,data:{access_code}} /
// {code:0,data:{token}} / data 直接是字符串。
func extractOAuthCode(b []byte) (code string, rawHint string, ok bool) {
	if rawHint = strings.TrimSpace(string(b)); len(rawHint) > 160 {
		rawHint = rawHint[:160] + "…"
	}
	var env struct {
		Code float64         `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err != nil || env.Code != 0 {
		return "", rawHint, false
	}
	if len(env.Data) == 0 {
		return "", rawHint, false
	}
	// data 直接是字符串 code
	var s string
	if err := json.Unmarshal(env.Data, &s); err == nil {
		if t := strings.TrimSpace(s); t != "" {
			return t, rawHint, true
		}
	}
	var obj map[string]any
	if err := json.Unmarshal(env.Data, &obj); err != nil {
		return "", rawHint, false
	}
	for _, k := range []string{"code", "access_code", "token_code", "one_time_code"} {
		if v, okk := obj[k].(string); okk && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), rawHint, true
		}
	}
	return "", rawHint, false
}
