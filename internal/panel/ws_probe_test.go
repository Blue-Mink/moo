package panel

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// rawWSProbe 临时探针（会话复用研究）：
// 1) 裸 WS user.login，dump 完整响应（找 token 字段）
// 2) 保持 WS 不断开，用 Authorization: trim <token> 调 /app-center 目录
// 3) 60s 内观察会话是否存活
func rawWSProbe(t *testing.T, keepAlive bool) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	base := "http://127.0.0.1:5666"
	conn, err := net.DialTimeout("tcp", "127.0.0.1:5666", 10*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if keepAlive {
		// 不关连接
		defer conn.Close()
	} else {
		defer conn.Close()
	}
	_ = conn.SetDeadline(time.Now().Add(100 * time.Second))
	key := make([]byte, 16)
	rand.Read(key)
	req := fmt.Sprintf("GET /websocket?type=main HTTP/1.1\r\nHost: 127.0.0.1:5666\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		base64.StdEncoding.EncodeToString(key))
	conn.Write([]byte(req))
	br := newBufReader(conn)
	head, err := br.readResponseHead()
	if err != nil {
		t.Fatalf("ws head: %v", err)
	}
	if len(head) < 12 || head[:12] != "HTTP/1.1 101" {
		t.Fatalf("ws handshake: %s", head[:60])
	}
	reqid := randHex(16)
	// 注意：wsSendText 内部会 json.Marshal，必须传 map 而不是 []byte
	frame := map[string]any{
		"user": "probe-user", "password": "probe-fake-password",
		"deviceName": "moo-probe", "deviceType": "pc", "stay": true,
		"did": "moo-probe", "ver": 2, "reqid": reqid, "req": "user.login",
	}
	if err := wsSendText(conn, frame); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 收完整登录响应（dump 原文）
	var token, ticket string
	_ = conn.SetReadDeadline(time.Now().Add(25 * time.Second))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		payload, opcode, err := wsRecvText(br)
		if err != nil {
			break
		}
		if opcode != 0x1 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(payload, &m) != nil {
			continue
		}
		if m["reqid"] != reqid {
			t.Logf("frame: %s", truncate(string(payload), 200))
			continue
		}
		raw := string(payload)
		t.Logf("LOGIN RESP FULL: %s", truncate(raw, 600))
		ticket, _ = m["ticket"].(string)
		token, _ = m["token"].(string)
		break
	}
	t.Logf("ticket=%q token=%q", ticket, token)

	// 同一活 WS 上发 user.tokenLogin（UI 的取 token 请求）
	reqid2 := randHex(16)
	tlFrame := map[string]any{
		"deviceType": "pc", "deviceName": "moo-probe", "did": "moo-probe",
		"ver": 2, "reqid": reqid2, "req": "user.tokenLogin",
	}
	if err := wsSendText(conn, tlFrame); err != nil {
		t.Fatalf("tokenLogin send: %v", err)
	}
	tlDeadline := time.Now().Add(15 * time.Second)
	_ = conn.SetReadDeadline(tlDeadline)
	for time.Now().Before(tlDeadline) {
		_ = conn.SetReadDeadline(tlDeadline)
		payload, opcode, err := wsRecvText(br)
		if err != nil {
			break
		}
		if opcode != 0x1 {
			continue
		}
		var m2 map[string]any
		if json.Unmarshal(payload, &m2) != nil {
			continue
		}
		if m2["reqid"] != reqid2 {
			t.Logf("tl frame: %s", truncate(string(payload), 200))
			continue
		}
		t.Logf("TOKENLOGIN RESP: %s", truncate(string(payload), 500))
		token, _ = m2["token"].(string)
		break
	}
	t.Logf("after tokenLogin: token=%q", token)

	// 用 token 调 app-center
	if token != "" {
		httpReq, _ := http.NewRequestWithContext(ctx, "GET", base+"/app-center/v1/app/list", nil)
		httpReq.Header.Set("Authorization", "trim "+token)
		resp, err := http.DefaultClient.Do(httpReq)
		if err == nil {
			buf := make([]byte, 160)
			n, _ := io.ReadFull(resp.Body, buf)
			resp.Body.Close()
			t.Logf("app/list with trim-token: %d %s", resp.StatusCode, truncate(string(buf[:n]), 150))
			return token, resp.StatusCode == 200
		}
		t.Logf("app/list error: %v", err)
	}
	// 用 ticket 换 ost，再试
	if ticket != "" {
		ex, _ := http.NewRequestWithContext(ctx, "POST", base+"/app/ticket",
			strings.NewReader(`{"ticket":"`+ticket+`"}`))
		ex.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(ex)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			ck := resp.Header.Get("Set-Cookie")
			t.Logf("ticket->ost: %d cookie=%s", resp.StatusCode, truncate(ck, 100))
			ost := extractOst(ck)
			if ost != "" {
				lr, _ := http.NewRequestWithContext(ctx, "GET", base+"/app-center/v1/app/list", nil)
				lr.Header.Set("Cookie", "ost="+ost)
				lr2, _ := http.DefaultClient.Do(lr)
				if lr2 != nil {
					b2 := make([]byte, 120)
					n2, _ := io.ReadFull(lr2.Body, b2)
					lr2.Body.Close()
					t.Logf("app/list with ost (WS kept open): %d %s", lr2.StatusCode, truncate(string(b2[:n2]), 100))
					return ost, lr2.StatusCode == 200
				}
			}
		}
	}
	return "", false
}

func extractOst(setCookie string) string {
	re := regexp.MustCompile(`ost=([^;]+)`)
	if m := re.FindStringSubmatch(setCookie); m != nil {
		return m[1]
	}
	return ""
}

// TestProbeToken 主探针
func TestProbeToken(t *testing.T) { rawWSProbe(t, false) }
