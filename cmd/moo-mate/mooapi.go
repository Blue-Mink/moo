// Package main 是 Moo Mate：从第三方仓库发现飞牛应用，生成 moo.json，
// 托管在自身 HTTP 端口并自动加源到本机 Moo（免上传 GitHub）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// mooClient 调用本机 Moo 的 admin API。两种通道：
//   - unix:///vol1/@appcenter/moo/app.sock → 直连 Moo 统一网关 socket（标准，无外部依赖）；
//   - http://127.0.0.1:13813/app/moo → 走调试代理（运维场景）。
type mooClient struct {
	base     string // unix 模式下 = /app/moo 前缀；http 模式下 = 完整 base
	unixSock string // 非空 = 走 unix socket
	httpc    *http.Client
}

func newMooClient(base string) *mooClient {
	if strings.HasPrefix(base, "unix://") {
		sock := strings.TrimPrefix(base, "unix://")
		return &mooClient{
			base:     "/app/moo",
			unixSock: sock,
			httpc: &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", sock)
					},
				},
				Timeout: 30 * time.Second,
			},
		}
	}
	return &mooClient{
		base:  strings.TrimRight(base, "/"),
		httpc: &http.Client{Timeout: 30 * time.Second},
	}
}

// sourceInfo 是 Moo 加源/同步响应里的 source 字段（SourceEntry 子集）。
type sourceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	AppCount int    `json:"app_count"`
	Error    string `json:"error,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// do 发请求；非 GET 自动带 X-Moo-Admin。unix 模式下另注入 X-Trim-Isadmin
// （可信身份头，模拟面板网关通道）。
func (c *mooClient) do(method, path string, body any) ([]byte, int, error) {
	return c.doTimeout(method, path, body, 0)
}

// doTimeout 同 do，但可指定本次调用的超时（0 = 用客户端默认 30s）。
// 用于设置页/UI 路径的短超时探取（Moo 不可达时不至于让页面卡 30 秒）。
func (c *mooClient) doTimeout(method, path string, body any, timeout time.Duration) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	var err error
	if c.unixSock != "" {
		// unix socket：URL host 无意义，路径须带 /app/moo 前缀。
		req, err = http.NewRequest(method, "http://unix"+c.base+path, rd)
	} else {
		req, err = http.NewRequest(method, c.base+path, rd)
	}
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Trim-Isadmin", "true")
	// 0.2.28：伴侣专用标记——Moo 的 /api/mate/* 端点据此只服务本伴侣。
	req.Header.Set("X-Moo-Mate", "1")
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Moo-Admin", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.httpc
	if timeout > 0 {
		cp := *c.httpc
		cp.Timeout = timeout
		client = &cp
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// addSource 调 POST /api/sources 加源，返回 Moo 回报的源信息。
func (c *mooClient) addSource(name, url string) (*sourceInfo, error) {
	data, code, err := c.do(http.MethodPost, "/api/sources", map[string]string{
		"name": name, "url": url,
	})
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Moo 加源失败 HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var out struct {
		Source sourceInfo `json:"source"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析加源响应失败: %v", err)
	}
	return &out.Source, nil
}

// syncSource 调 POST /api/sources/{id}/sync 强制刷新，返回刷新后的源信息。
func (c *mooClient) syncSource(id string) (*sourceInfo, error) {
	data, code, err := c.do(http.MethodPost, "/api/sources/"+id+"/sync", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Moo 同步失败 HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var out struct {
		Source sourceInfo `json:"source"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析同步响应失败: %v", err)
	}
	return &out.Source, nil
}

// ping 探活：GET /api/about 返回 200 即 Moo 可达。
func (c *mooClient) ping() error {
	data, code, err := c.do(http.MethodGet, "/api/about", nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("Moo 探活 HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return nil
}

// listSources GET /api/sources 返回 Moo 现有源列表。
func (c *mooClient) listSources() ([]sourceInfo, error) {
	data, code, err := c.do(http.MethodGet, "/api/sources", nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Moo 列出源失败 HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	var out struct {
		Sources []sourceInfo `json:"sources"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析源列表失败: %v", err)
	}
	return out.Sources, nil
}

// deleteSource DELETE /api/sources/{id} 移除源。
func (c *mooClient) deleteSource(id string) error {
	_, code, err := c.do(http.MethodDelete, "/api/sources/"+id, nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("Moo 删源失败 HTTP %d", code)
	}
	return nil
}

// getMooRaw 只读端点：GET path（带 /api 前缀），返回原始 JSON（排障工具层再格式化）。
// path 形如 "/api/status"、"/api/apps"、"/api/logs?lines=200"。
func (c *mooClient) getMooRaw(path string) ([]byte, error) {
	data, code, err := c.do(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("Moo HTTP %d: %s", code, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// fireMoo 触发写端点（SSE 长任务：install/update/uninstall 等），只校验状态码后
// 立即关闭响应体——Moo 操作在后台继续，不阻塞 AI 对话。
func (c *mooClient) fireMoo(method, path string, body any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	var req *http.Request
	var err error
	if c.unixSock != "" {
		req, err = http.NewRequest(method, "http://unix"+c.base+path, rd)
	} else {
		req, err = http.NewRequest(method, c.base+path, rd)
	}
	if err != nil {
		return err
	}
	req.Header.Set("X-Trim-Isadmin", "true")
	// 0.2.28：伴侣专用标记——Moo 的 /api/mate/* 端点据此只服务本伴侣。
	req.Header.Set("X-Moo-Mate", "1")
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-Moo-Admin", "1")
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	// 只读少量 body 拿错误信息，不读完（SSE 长任务）；读完会阻塞到操作结束。
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("Moo HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
