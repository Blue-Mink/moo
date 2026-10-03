// Package panel implements a client for the fnOS panel's app-center Web API
// (the official App Center served at :5666).
//
// The panel has no public API token flow for third-party apps; its Web UI
// authenticates over a WebSocket (user.login) and receives a one-shot ticket,
// exchanged for the HttpOnly "ost" session cookie. This client reproduces that
// flow with a plain username/password (both are panel accounts of the local
// NAS, so the panel is reached over localhost).
//
// Verified against fnOS 1.2.05xx (2026-09-19, test box):
//
//   - WS  GET /websocket?type=main
//     frame: {"user","password","deviceName","deviceType","stay":true,
//     "did","ver":2,"reqid","req":"user.login"}  → resp carries "ticket"
//     ("ver":2 is required; without it the ticket is not issued.)
//   - HTTP POST /app/ticket {"ticket":...}  → Set-Cookie: ost=...
//   - GET  /app-center/v1/app/list?language=zh-CN   (355 entries, single page)
//   - GET  /app-center/v1/app/detail?appName=X      (installDepApps = resolved
//     dependency objects with live status)
//   - POST /app-center/v1/download/task
//     {"packageSourceType":"cloud","appName","sourceID","version","volumeID"}
//     → {"downloadTaskId":"cloud_<ts>"}
//   - GET  /app-center/v1/download/status?downloadTaskId=...
//     → {"status":1|2,"progress":0..1,"path":"/vol1/appcenter-downloads/..."}
//   - POST /app-center/v1/install/task
//     {"appName","version","packageType":"cloud",
//     "systemParameters":{"agreedToProtocol":true,"installVolumeID":N,
//     "dataVolumeId":N,"immediateStart":true,"apiScope":{}}
//     ,"customParameters":[]}
//     → {"taskId":"<ts>-<appName>"}
//   - POST /app-center/v1/install/status {"taskId"}
//     → {"outputText","progress","status":1|2,"taskId"}
//
// The same request contract was captured from the panel's own UI traffic
// (tcpdump, real user install of fnos.sudoku 1.0.7), not inferred.
package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is the panel on the local NAS (the store daemon runs on
	// the same machine, so localhost bypasses any firewall rules).
	DefaultBaseURL = "http://127.0.0.1:5666"

	loginFrameVer = 2
)

// Client talks to the panel app-center API using an ost session cookie
// obtained through the WS login flow.
type Client struct {
	BaseURL  string
	Username string
	Password string

	mu       sync.Mutex
	cookie   string // raw ost value
	loggedIn bool
	http     *http.Client
}

// NewClient creates a client for base (empty → DefaultBaseURL).
func NewClient(base, username, password string) *Client {
	if strings.TrimSpace(base) == "" {
		base = DefaultBaseURL
	}
	return &Client{
		BaseURL:  strings.TrimRight(strings.TrimSpace(base), "/"),
		Username: username,
		Password: password,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Configured reports whether credentials are present.
func (c *Client) Configured() bool {
	return strings.TrimSpace(c.Username) != "" && c.Password != ""
}

// PanelApp is one entry of GET /app-center/v1/app/list.
type PanelApp struct {
	SourceID string   `json:"sourceID"`
	AppName  string   `json:"appName"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
	Icon     string   `json:"icon"`
	Download int64    `json:"download"`
	Version  string   `json:"version"`
	Beta     bool     `json:"beta"`
	Docker   bool     `json:"docker"`
	Status   string   `json:"status"`
	Source   string   `json:"source"` // official / thirdparty / community
}

// PanelDepApp is a resolved dependency from app/detail (installDepApps).
type PanelDepApp struct {
	SourceID string `json:"sourceID"`
	AppName  string `json:"appName"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Version  string `json:"version"`
	Status   string `json:"status"` // noinstall / nostart / running
	Docker   bool   `json:"docker"`
}

// PanelDetail is GET /app-center/v1/app/detail.
type PanelDetail struct {
	SourceID           string         `json:"sourceID"`
	AppName            string         `json:"appName"`
	Name               string         `json:"name"`
	Tags               []string       `json:"tags"`
	Icon               string         `json:"icon"`
	Download           int64          `json:"download"`
	Version            string         `json:"version"`
	Docker             bool           `json:"docker"`
	Status             string         `json:"status"`
	DependencyAppNames []string       `json:"dependencyAppNames"`
	InstallDepApps     []PanelDepApp  `json:"installDepApps"`
	AppDetail          PanelAppDetail `json:"appDetail"`
}

// PanelAppDetail is the appDetail sub-object (description etc.).
type PanelAppDetail struct {
	Desc           string `json:"desc"`
	Maintainer     string `json:"maintainer"`
	MaintainerURL  string `json:"maintainerUrl"`
	Distributor    string `json:"distributor"`
	DistributorURL string `json:"distributorUrl"`
	InstallSize    int64  `json:"installSize"`
	InstallType    string `json:"installType"`
	OSMinVersion   string `json:"osMinVersion"`
	// Poster 是官方详情页的预览截图 URL 列表（部分应用为空）。
	// otherPoster（{url, meta.size} 多尺寸版）与 poster 同源，不重复展示。
	Poster []string `json:"poster"`
}

// WizardParam is one answer to an app's install wizard, in the exact
// {key,value} shape the panel's install/task accepts (name/value and
// paramKey/paramValue are both rejected with code 10030).
type WizardParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// InstallInfoWizard is the wizardInfo sub-object of GET
// /app-center/v1/install/info. WizardContent is the raw form definition
// (same item shape as the FPK's fnos/wizard/install), passed through
// untouched so the UI can render whatever field types fnOS supports.
type InstallInfoWizard struct {
	HasWizard     bool            `json:"hasWizard"`
	WizardContent json.RawMessage `json:"wizardContent"`
}

// InstallInfo is the response of GET /app-center/v1/install/info.
// NOTE: the panel only answers this for a package that has already been
// downloaded (otherwise it replies 10100 package-not-found). Callers must
// run download/task first — mirroring the native App Center's own flow.
type InstallInfo struct {
	WizardInfo InstallInfoWizard `json:"wizardInfo"`
}

// DownloadStatus is GET /app-center/v1/download/status.
type DownloadStatus struct {
	Status   int     `json:"status"` // 1=running 2=success 3=failed
	Message  string  `json:"message"`
	Progress float64 `json:"progress"`
	Path     string  `json:"path"`
	AppName  string  `json:"appName"`
	Version  string  `json:"version"`
}

// InstallStatus is POST /app-center/v1/install/status.
type InstallStatus struct {
	OutputText string  `json:"outputText"`
	Progress   float64 `json:"progress"`
	Status     int     `json:"status"` // 1=running 2=success 3=failed
	TaskID     string  `json:"taskId"`
}

// Status constants shared by download/status and install/status.
const (
	TaskRunning = 1
	TaskSuccess = 2
	TaskFailed  = 3
)

// EnsureLoggedIn guarantees a valid ost cookie, logging in via WS when needed.
func (c *Client) EnsureLoggedIn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loggedIn {
		return nil
	}
	if !c.Configured() {
		return fmt.Errorf("面板账号未配置")
	}
	ticket, err := wsLogin(ctx, c.BaseURL, c.Username, c.Password)
	if err != nil {
		return err
	}
	cookie, err := exchangeTicket(ctx, c.BaseURL, c.http, ticket)
	if err != nil {
		return err
	}
	c.cookie = cookie
	c.loggedIn = true
	return nil
}

// Invalidate clears the cached session (call after an auth error).
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.cookie = ""
	c.loggedIn = false
	c.mu.Unlock()
}

func (c *Client) cookieLocked() string { return c.cookie }

// ErrEndpointNotFound marks a panel HTTP 404: the panel build in front of us
// has no such endpoint (e.g. an older panel without install/info). Callers
// can degrade gracefully instead of failing the whole operation.
var ErrEndpointNotFound = errors.New("面板端点不存在")

// DoJSON is a public passthrough of the authenticated JSON request path,
// for API exploration / debugging (e.g. probing undocumented endpoints).
func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.doJSON(ctx, method, path, query, body)
}

// doJSON performs an authenticated JSON request, re-logging in once on
// auth failure. respRaw is the response body (empty for 2xx-with-no-body).
func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			c.Invalidate()
			if err := c.EnsureLoggedIn(ctx); err != nil {
				return nil, err
			}
		}
		c.mu.Lock()
		cookie := c.cookieLocked()
		c.mu.Unlock()

		u := c.BaseURL + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		var rdr io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			rdr = strings.NewReader(string(b))
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("language", "zh-CN")
		req.Header.Set("Cookie", "language=zh-CN; ost="+cookie)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("面板请求失败: %w", err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			lastErr = fmt.Errorf("面板会话失效 (HTTP %d)", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("%w: %s", ErrEndpointNotFound, truncate(string(raw), 200))
			}
			return nil, fmt.Errorf("面板 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
		var env struct {
			Code int             `json:"code"`
			Msg  string          `json:"msg"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return raw, nil // non-envelope body: return raw
		}
		if env.Code != 0 {
			return nil, &APIError{Code: env.Code, Msg: env.Msg, Raw: truncate(string(raw), 300)}
		}
		return env.Data, nil
	}
	return nil, lastErr
}

// APIError is a business error from the panel (code != 0).
type APIError struct {
	Code int
	Msg  string
	Raw  string
}

func (e *APIError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("面板错误 code=%d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("面板错误 code=%d (%s)", e.Code, e.Raw)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// RawJSON performs an authenticated request and returns the envelope `data`
// (or the raw body when the response is not an envelope). Exposed for
// diagnostics; production paths use the typed methods below.
func (c *Client) RawJSON(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	return c.doJSON(ctx, method, path, query, body)
}

// FileDownloadTask 把一个本地 FPK 文件登记到面板官方下载系统（app-center
// 下载通道，与官方应用中心安装本地包用的是同一机制，fndepot 安装第三方
// FPK 时即走此通道）。返回下载任务 ID（形如 file_<ts>）。
//
// 注意：面板的「下载中心」（appcgi.downloadcenter.*）是独立封闭通道，
// 其 WS 会话授权只认面板桌面主连接（2026-09-20 实测：login/active/
// authToken/tokenLogin/cookie 各组合对第三方进程一律 errno 65534），
// 本方法走的是官方 app-center HTTP 下载系统，文件会进入面板下载缓存
// （/vol*/appcenter-downloads），可供面板或 New Store 直接安装复用。
func (c *Client) FileDownloadTask(ctx context.Context, path string) (string, error) {
	if err := c.EnsureLoggedIn(ctx); err != nil {
		return "", err
	}
	data, err := c.doJSON(ctx, http.MethodPost, "/app-center/v1/download/task", nil,
		map[string]any{"packageSourceType": "file", "path": path, "language": "zh-CN"})
	if err != nil {
		return "", err
	}
	var out struct {
		DownloadTaskID string `json:"downloadTaskId"`
		TaskID         string `json:"taskId"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("解析面板响应失败: %w", err)
	}
	id := out.DownloadTaskID
	if id == "" {
		id = out.TaskID
	}
	if id == "" {
		return "", errors.New("面板未返回下载任务 ID")
	}
	return id, nil
}

// AppList fetches the full official catalog (single page, ~355 entries).
func (c *Client) AppList(ctx context.Context) ([]PanelApp, error) {
	data, err := c.doJSON(ctx, http.MethodGet, "/app-center/v1/app/list", url.Values{"language": {"zh-CN"}}, nil)
	if err != nil {
		return nil, err
	}
	var env struct {
		Total int        `json:"total"`
		List  []PanelApp `json:"list"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("解析应用列表失败: %w", err)
	}
	return env.List, nil
}

// AppDetail fetches one app's detail (description + resolved dependencies).
func (c *Client) AppDetail(ctx context.Context, appName string) (*PanelDetail, error) {
	data, err := c.doJSON(ctx, http.MethodGet, "/app-center/v1/app/detail", url.Values{"appName": {appName}, "language": {"zh-CN"}}, nil)
	if err != nil {
		return nil, err
	}
	var d PanelDetail
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("解析应用详情失败: %w", err)
	}
	return &d, nil
}

// DownloadTask starts a cloud download and returns the downloadTaskId.
func (c *Client) DownloadTask(ctx context.Context, appName, sourceID, version string, volumeID int) (string, error) {
	body := map[string]any{
		"packageSourceType": "cloud",
		"appName":           appName,
		"sourceID":          sourceID,
		"version":           version,
		"volumeID":          volumeID,
		"language":          "zh-CN",
	}
	data, err := c.doJSON(ctx, http.MethodPost, "/app-center/v1/download/task", nil, body)
	if err != nil {
		return "", err
	}
	var r struct {
		DownloadTaskID string `json:"downloadTaskId"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.DownloadTaskID == "" {
		return "", fmt.Errorf("启动下载失败: %s", truncate(string(data), 200))
	}
	return r.DownloadTaskID, nil
}

// DownloadStatus polls a cloud download task.
func (c *Client) DownloadStatus(ctx context.Context, downloadTaskID string) (*DownloadStatus, error) {
	data, err := c.doJSON(ctx, http.MethodGet, "/app-center/v1/download/status", url.Values{"downloadTaskId": {downloadTaskID}, "language": {"zh-CN"}}, nil)
	if err != nil {
		return nil, err
	}
	var st DownloadStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("解析下载状态失败: %w", err)
	}
	return &st, nil
}

// InstallInfo fetches an app's install-time wizard definition.
// The package must have been downloaded first (see InstallInfo docs).
func (c *Client) InstallInfo(ctx context.Context, appName, version string) (*InstallInfo, error) {
	data, err := c.doJSON(ctx, http.MethodGet, "/app-center/v1/install/info",
		url.Values{"appName": {appName}, "version": {version}, "language": {"zh-CN"}}, nil)
	if err != nil {
		return nil, err
	}
	var info InstallInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("解析安装信息失败: %w", err)
	}
	return &info, nil
}

// InstallTask submits the install task for a cloud package.
// customParams carries the user's install-wizard answers (may be empty for
// apps whose wizard has no required fields).
func (c *Client) InstallTask(ctx context.Context, appName, version string, volumeID int, customParams []WizardParam) (string, error) {
	if customParams == nil {
		customParams = []WizardParam{}
	}
	body := map[string]any{
		"appName":     appName,
		"version":     version,
		"packageType": "cloud",
		"systemParameters": map[string]any{
			"agreedToProtocol": true,
			"installVolumeID":  volumeID,
			"dataVolumeId":     volumeID,
			"immediateStart":   true,
			"apiScope":         map[string]any{},
		},
		"customParameters": customParams,
		"language":         "zh-CN",
	}
	data, err := c.doJSON(ctx, http.MethodPost, "/app-center/v1/install/task", nil, body)
	if err != nil {
		return "", err
	}
	var r struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.TaskID == "" {
		return "", fmt.Errorf("提交安装失败: %s", truncate(string(data), 200))
	}
	return r.TaskID, nil
}

// InstallStatus polls an install task.
func (c *Client) InstallStatus(ctx context.Context, taskID string) (*InstallStatus, error) {
	body := map[string]any{"taskId": taskID, "language": "zh-CN"}
	data, err := c.doJSON(ctx, http.MethodPost, "/app-center/v1/install/status", nil, body)
	if err != nil {
		return nil, err
	}
	var st InstallStatus
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("解析安装状态失败: %w", err)
	}
	return &st, nil
}

// TestLogin performs a full login + catalog probe; returns the app count.
func (c *Client) TestLogin(ctx context.Context) (int, error) {
	c.Invalidate()
	if err := c.EnsureLoggedIn(ctx); err != nil {
		return 0, err
	}
	apps, err := c.AppList(ctx)
	if err != nil {
		return 0, err
	}
	return len(apps), nil
}

// ---------------------------------------------------------------------------
// WebSocket login (stdlib only)
// ---------------------------------------------------------------------------

// wsLogin opens the panel WS, sends the user.login frame and returns the
// ticket from the matching response.
func wsLogin(ctx context.Context, base, user, password string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	host := u.Host
	if host == "" {
		host = "127.0.0.1"
	}
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if err := ctx.Err(); err != nil {
		return "", err
	}
	conn, err = dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return "", fmt.Errorf("连接面板失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	// Handshake (no extensions: the panel accepts plain frames).
	key := base64.StdEncoding.EncodeToString(randBytes(16))
	req := fmt.Sprintf(
		"GET /websocket?type=main HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.Host, key)
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", fmt.Errorf("WS 握手发送失败: %w", err)
	}
	br := newBufReader(conn)
	respHead, err := br.readResponseHead()
	if err != nil {
		return "", fmt.Errorf("WS 握手响应失败: %w", err)
	}
	if !strings.HasPrefix(respHead, "HTTP/1.1 101") {
		return "", fmt.Errorf("WS 握手被拒: %s", strings.TrimSpace(strings.SplitN(respHead, "\r\n", 2)[0]))
	}

	reqid := randHex(16)
	frame := map[string]any{
		"user":       user,
		"password":   password,
		"deviceName": "new-store-daemon",
		"deviceType": "pc",
		"stay":       true,
		"did":        "new-store-daemon",
		"ver":        loginFrameVer,
		"reqid":      reqid,
		"req":        "user.login",
	}
	if err := wsSendText(conn, frame); err != nil {
		return "", fmt.Errorf("登录帧发送失败: %w", err)
	}

	// Read frames until the response with our reqid arrives.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		payload, opcode, err := wsRecvText(br)
		if err != nil {
			return "", fmt.Errorf("等待登录响应失败: %w", err)
		}
		if opcode != 0x1 {
			continue
		}
		var msg struct {
			ReqID  string `json:"reqid"`
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		if msg.ReqID != reqid {
			continue
		}
		if msg.Ticket == "" {
			return "", fmt.Errorf("登录失败（面板未签发 ticket，请检查账号密码）")
		}
		return msg.Ticket, nil
	}
	return "", fmt.Errorf("登录超时")
}

// exchangeTicket trades the WS ticket for the ost session cookie.
func exchangeTicket(ctx context.Context, base string, hc *http.Client, ticket string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/app/ticket",
		strings.NewReader(`{"ticket":"`+ticket+`"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("ticket 换取会话失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	for _, sc := range resp.Header.Values("Set-Cookie") {
		parts := strings.SplitN(sc, ";", 2)
		kv := strings.SplitN(parts[0], "=", 2)
		if len(kv) == 2 && kv[0] == "ost" {
			return kv[1], nil
		}
	}
	return "", fmt.Errorf("面板未签发 ost 会话 cookie")
}

// wsSendText sends one masked client text frame.
func wsSendText(conn net.Conn, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	hdr := make([]byte, 0, 14)
	hdr = append(hdr, 0x81) // FIN + text
	n := len(data)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 65536:
		hdr = append(hdr, 0x80|126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := randBytes(4)
	hdr = append(hdr, mask...)
	masked := make([]byte, n)
	for i, b := range data {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := conn.Write(append(hdr, masked...)); err != nil {
		return err
	}
	return nil
}

// wsRecvText reads one data frame; returns payload and opcode.
func wsRecvText(br *bufReader) ([]byte, byte, error) {
	b1, err := br.readByte()
	if err != nil {
		return nil, 0, err
	}
	b2, err := br.readByte()
	if err != nil {
		return nil, 0, err
	}
	opcode := b1 & 0x0f
	ln := int(b2 & 0x7f)
	switch ln {
	case 126:
		b, err := br.read(2)
		if err != nil {
			return nil, 0, err
		}
		ln = int(binary.BigEndian.Uint16(b))
	case 127:
		b, err := br.read(8)
		if err != nil {
			return nil, 0, err
		}
		ln = int(binary.BigEndian.Uint64(b))
	}
	var mask []byte
	if b2&0x80 != 0 {
		mask, err = br.read(4)
		if err != nil {
			return nil, 0, err
		}
	}
	payload, err := br.read(ln)
	if err != nil {
		return nil, 0, err
	}
	if mask != nil {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, opcode, nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func randHex(n int) string {
	return fmt.Sprintf("%x", randBytes(n))
}

// bufReader is a tiny buffered reader over a net.Conn (avoids bufio import
// churn in the read loop).
type bufReader struct {
	c   net.Conn
	buf []byte
}

func newBufReader(c net.Conn) *bufReader { return &bufReader{c: c} }

func (r *bufReader) readByte() (byte, error) {
	if len(r.buf) == 0 {
		tmp := make([]byte, 1)
		if _, err := r.c.Read(tmp); err != nil {
			return 0, err
		}
		r.buf = tmp
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	return b, nil
}

func (r *bufReader) read(n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(out) < n {
		if len(r.buf) == 0 {
			chunk := make([]byte, min(4096, n-len(out)))
			m, err := r.c.Read(chunk)
			if err != nil {
				return nil, err
			}
			if m == 0 {
				return nil, io.ErrUnexpectedEOF
			}
			r.buf = chunk[:m]
		}
		take := min(len(r.buf), n-len(out))
		out = append(out, r.buf[:take]...)
		r.buf = r.buf[take:]
	}
	return out, nil
}

func (r *bufReader) readResponseHead() (string, error) {
	var sb strings.Builder
	for {
		b, err := r.readByte()
		if err != nil {
			return "", err
		}
		sb.WriteByte(b)
		if strings.HasSuffix(sb.String(), "\r\n\r\n") {
			return sb.String(), nil
		}
		if sb.Len() > 8192 {
			return "", fmt.Errorf("WS 握手响应过长")
		}
	}
}

// Fetch 以已登录面板身份抓取资源（官方 CDN 绝对地址直连；相对路径经面板
// 基址 + ost cookie）。返回原始字节与 Content-Type。
func (c *Client) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, "", errors.New("空资源地址")
	}
	if !strings.HasPrefix(rawURL, "http") {
		if err := c.EnsureLoggedIn(ctx); err != nil {
			return nil, "", err
		}
		base := strings.TrimSuffix(c.BaseURL, "/")
		if !strings.HasPrefix(rawURL, "/") {
			rawURL = "/" + rawURL
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+rawURL, nil)
		if err != nil {
			return nil, "", err
		}
		c.mu.Lock()
		cookie := c.cookie
		c.mu.Unlock()
		if cookie != "" {
			req.Header.Set("Cookie", "ost="+cookie)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("面板资源 %s: HTTP %d", rawURL, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			return nil, "", err
		}
		return data, resp.Header.Get("Content-Type"), nil
	}
	// 绝对地址（官方静态 CDN）：直连
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("资源 %s: HTTP %d", rawURL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}
