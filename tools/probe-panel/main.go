// probe-panel 一次性调试工具：登录本地面板并 dump app-center 原始
// JSON，用于找 FPK/TPK 包类型字段（用完即删，不进 FPK）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"moo/internal/panel"
)

func main() {
	user := "fnos"
	pass := os.Getenv("PROBE_PASS")
	base := ""
	if len(os.Args) > 1 {
		base = os.Args[1]
	}
	c := panel.NewClient(base, user, pass)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.EnsureLoggedIn(ctx); err != nil {
		fmt.Println("登录失败:", err)
		os.Exit(1)
	}
	endpoint := "/app-center/v1/app/list"
	if len(os.Args) > 2 {
		endpoint = os.Args[2]
	}
	raw, err := c.RawJSON(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		fmt.Println("请求失败:", err)
		os.Exit(1)
	}
	// 美化输出前 8000 字符
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		b, _ := json.MarshalIndent(v, "", " ")
		s := string(b)
		if len(s) > 8000 {
			s = s[:8000]
		}
		fmt.Println(s)
	} else {
		s := string(raw)
		if len(s) > 8000 {
			s = s[:8000]
		}
		fmt.Println(s)
	}
}
