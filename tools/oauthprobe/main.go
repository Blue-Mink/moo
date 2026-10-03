// oauthprobe — 探测主 NAS 旧版面板是否支持 OAuth 授权码链路（一次性诊断工具，
// 不进发布包）：面板 WS 登录 → 调 /oauthapi/* 端点 → 打印原始响应。
//
// 用法: go run ./tools/oauthprobe [dataDir]   （dataDir 默认 /vol1/@appdata/moo）
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

	"moo/internal/config"
	"moo/internal/panel"
)

func main() {
	dataDir := "/vol1/@appdata/moo"
	if len(os.Args) > 1 {
		dataDir = os.Args[1]
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		fmt.Println("config load error:", err)
		os.Exit(1)
	}
	if cfg.PanelUsername == "" || cfg.PanelPassword == "" {
		fmt.Println("面板账号未配置")
		os.Exit(1)
	}
	fmt.Printf("panel user=%s (password len=%d)\n", cfg.PanelUsername, len(cfg.PanelPassword))

	c := panel.NewClient("", cfg.PanelUsername, cfg.PanelPassword)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := c.EnsureLoggedIn(ctx); err != nil {
		fmt.Println("LOGIN FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("LOGIN OK")

	q := url.Values{}
	q.Set("client_id", "YJNMPJUGA9")
	q.Set("scope", "trim.appcenter.all")
	q.Set("code_challenge", "probechallenge000000000000000000000000")
	q.Set("code_challenge_method", "S256")
	q.Set("device_id", "probe-device-001")
	q.Set("device_name", "oauthprobe")
	q.Set("device_model", "CLI")

	for _, p := range []string{"/oauthapi/authorize", "/oauthapi/app/info"} {
		b, err := c.DoJSON(ctx, "GET", p, q, nil)
		if err != nil {
			fmt.Printf("%s -> ERR: %v\n", p, err)
			continue
		}
		fmt.Printf("%s -> %s\n", p, b)
	}
	// 匿名对照（新 client 不登录）
	c2 := panel.NewClient("", "", "")
	b, err := c2.RawJSON(ctx, "GET", "/oauthapi/authorize", q, nil)
	fmt.Printf("anon /oauthapi/authorize -> err=%v body=%s\n", err, b)
}
