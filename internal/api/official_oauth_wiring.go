package api

// 0.6.253：官方应用中心 OAuth 免登录通道接线。
//
// 背景：官方目录/详情此前只走面板 WS 登录（账号密码 → 限流 errno 131072 →
// 0.6.252 退避）。面板 /signin 的 OAuth 2.0 + PKCE 授权页允许 iframe 嵌入
// （无 X-Frame-Options/CSP，2026-10-03 实测），授权后 Bearer token 走
// /ogh/ac/h 代理直调同一批 appcenter 端点，token 1h + refresh 自动续期，
// 与面板登录限流完全解耦。
//
// 策略：OAuth 会话有效时目录/详情/安装查询走 OAuth；会话缺失或 OAuth 拉取
// 失败时回退面板 WS 通道（含 0.6.252 失败退避），双通道行为对用户透明。

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"moo/internal/official"
	"moo/internal/panel"
)

// errOfficialNotConnected 官方源未连接（OAuth 未授权且无面板账号）时的
// 统一错误文案——目录行/详情/安装入口都会透传这条提示引导用户去授权。
var errOfficialNotConnected = errors.New("官方应用中心未连接：请点「应用源 → 飞牛应用中心 → 🔑」完成授权")

// WireOfficialOAuth 把 OAuth 免登录通道接到官方目录抓取链路上。
// 0.6.255：官方源 = 纯 OAuth。会话有效走免登录通道；会话缺失且面板无账号
// （账号已随 0.6.255 移除）→ 返回明确的「未连接」错误（不再静默回退）。
func WireOfficialOAuth(s *Server) {
	if s == nil || s.Official == nil || s.Panel == nil {
		return
	}
	store := s.officialStoreV()
	p := s.Panel

	origList := p.listApps
	p.listApps = func(ctx context.Context) ([]panel.PanelApp, error) {
		apps, err, ok := store.oauthPanelAppsErr(ctx)
		if ok {
			return apps, nil
		}
		// 会话有效但 /ogh 拉取失败 → 透传真实错误（区别于「未连接」）。
		if err != nil {
			return nil, err
		}
		if !p.Enabled() {
			return nil, errOfficialNotConnected
		}
		return origList(ctx)
	}
	origDetail := p.detailFn
	p.detailFn = func(ctx context.Context, appName string) (*panel.PanelDetail, error) {
		if d, ok := store.oauthDetail(ctx, appName); ok {
			return d, nil
		}
		if !p.Enabled() {
			return nil, errOfficialNotConnected
		}
		return origDetail(ctx, appName)
	}
	log.Printf("官方应用中心：OAuth 免登录通道已接线（0.6.255 纯 OAuth：未授权时明确报错引导连接）")
}

// hasOAuthSession 会话存在且 access token 未过期（快速路径判断；
// 临期未过期的 token 由 Manager.Do 内部自动刷新，不算无效）。
func (s *officialStore) hasOAuthSession() bool {
	sess := s.mgr.Session()
	return sess != nil && sess.Valid(time.Now())
}

// oauthPanelApps 从 OAuth 通道拉全量官方目录并转成 panel.PanelApp。
// 仅当会话有效且拉取成功且非空时返回 ok=true。
func (s *officialStore) oauthPanelApps(ctx context.Context) ([]panel.PanelApp, bool) {
	if !s.hasOAuthSession() {
		return nil, false
	}
	apps, err := s.list(ctx)
	if err != nil || len(apps) == 0 {
		return nil, false
	}
	return s.convertPanelApps(apps), true
}

// oauthPanelAppsErr 同 oauthPanelApps，但返回底层真实错误（区分
// 「会话有效但 /ogh 拉取失败」与「未连接」，便于给诚实报错）。
// ok=false 且 err=nil = 未连接或拉空（由调用方判定）。
func (s *officialStore) oauthPanelAppsErr(ctx context.Context) (apps []panel.PanelApp, err error, ok bool) {
	if !s.hasOAuthSession() {
		return nil, nil, false
	}
	list, err := s.list(ctx)
	if err != nil {
		return nil, err, false
	}
	if len(list) == 0 {
		return nil, nil, false
	}
	return s.convertPanelApps(list), nil, true
}

func (s *officialStore) convertPanelApps(apps []official.StoreApp) []panel.PanelApp {
	out := make([]panel.PanelApp, 0, len(apps))
	for _, a := range apps {
		out = append(out, panel.PanelApp{
			SourceID: a.SourceID,
			AppName:  a.AppName,
			Name:     a.Name,
			Tags:     a.Tags,
			Icon:     a.Icon,
			Download: a.Download,
			Version:  a.Version,
			Beta:     a.Beta,
			Docker:   a.Docker,
			Status:   a.Status,
			Source:   a.Source,
		})
	}
	return out
}

// oauthDetail 从 OAuth 通道拉单个应用详情（与面板 app/detail 同端点同构）。
func (s *officialStore) oauthDetail(ctx context.Context, appName string) (*panel.PanelDetail, bool) {
	if !s.hasOAuthSession() {
		return nil, false
	}
	detail, err := s.mgr.StoreDetail(ctx, appName)
	if err != nil || len(detail) == 0 {
		return nil, false
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return nil, false
	}
	var d panel.PanelDetail
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, false
	}
	return &d, true
}
