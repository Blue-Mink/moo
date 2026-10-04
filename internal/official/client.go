package official

// 官方应用中心业务 API 客户端（走 /ogh/ac/h 代理 + Bearer token）。
//
// 端点（09-30 trim-cli 实锤 + app-center.md 契约）：
//   - GET /app-center/v1/app/list?language=zh-CN&limit=200&page=N   → 全量商店列表
//   - GET /app-center/v1/app/search?keyword=...
//   - GET /app-center/v1/app/detail?appName=...
//   - GET /app-center/v1/app/installed
//   - GET /app-center/v1/check-update?language=zh-CN
//
// 响应统一包装: {"code":0,"msg":"","data":{...}}；list 为 {page,limit,total,list[]}。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"moo/internal/lang"
)

// StoreApp 是 /app-center/v1/app/list 条目的子集（Moo 需要的字段）。
type StoreApp struct {
	AppName    string  `json:"appName"`
	Name       string  `json:"name"`
	Version    string  `json:"version"`
	Icon       string  `json:"icon"`
	Download   int64   `json:"download"`
	Source     string  `json:"source"`    // official / thirdparty
	SourceID   string  `json:"sourceID"`
	Status     string  `json:"status"`    // noinstall / install / running ...
	Docker     bool    `json:"docker"`
	Beta       bool    `json:"beta"`
	Tags       []string `json:"tags"`
	DevName    string  `json:"devName,omitempty"`
	UpdateTime int64   `json:"updateTime,omitempty"`
	// 原始数据保留一份（详情弹窗/未映射字段兜底）
	Raw json.RawMessage `json:"-"`
}

type envelope struct {
	Code float64           `json:"code"`
	Msg  string            `json:"msg"`
	Data json.RawMessage   `json:"data"`
}

func (m *Manager) doGet(ctx context.Context, bizPath string, query url.Values) (json.RawMessage, error) {
	body, err := m.Do(ctx, "GET", bizPath, query, nil)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("官方接口响应解析失败: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("官方接口错误 code=%v msg=%s", env.Code, env.Msg)
	}
	return env.Data, nil
}

// StoreList 拉全量商店应用（自动翻页，limit 200/页）。
func (m *Manager) StoreList(ctx context.Context) ([]StoreApp, error) {
	var out []StoreApp
	const pageSize = 200
	for page := 1; page <= 20; page++ {
		q := url.Values{}
		q.Set("language", lang.From(ctx))
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		data, err := m.doGet(ctx, "/app-center/v1/app/list", q)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Page  int           `json:"page"`
			Limit int           `json:"limit"`
			Total int           `json:"total"`
			List  []StoreApp    `json:"list"`
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return nil, fmt.Errorf("列表解析失败: %w", err)
		}
		for i := range payload.List {
			payload.List[i].Raw = nil
			out = append(out, payload.List[i])
		}
		if len(payload.List) < pageSize || len(out) >= payload.Total {
			return out, nil
		}
	}
	return out, nil
}

// StoreDetail 单个应用详情。
func (m *Manager) StoreDetail(ctx context.Context, appName string) (map[string]any, error) {
	q := url.Values{}
	q.Set("language", lang.From(ctx))
	q.Set("appName", appName)
	data, err := m.doGet(ctx, "/app-center/v1/app/detail", q)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StoreSearch 关键字搜索。
func (m *Manager) StoreSearch(ctx context.Context, keyword string) ([]StoreApp, error) {
	q := url.Values{}
	q.Set("language", lang.From(ctx))
	q.Set("keyword", keyword)
	data, err := m.doGet(ctx, "/app-center/v1/app/search", q)
	if err != nil {
		return nil, err
	}
	var payload struct {
		List []StoreApp `json:"list"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	return payload.List, nil
}

// Installed 官方已装列表（含运行状态）。
func (m *Manager) Installed(ctx context.Context) (map[string]any, error) {
	data, err := m.doGet(ctx, "/app-center/v1/app/installed", url.Values{"language": {lang.From(ctx)}})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CheckUpdate 检查更新可用性。
func (m *Manager) CheckUpdate(ctx context.Context) (map[string]any, error) {
	data, err := m.doGet(ctx, "/app-center/v1/check-update", url.Values{"language": {lang.From(ctx)}})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
