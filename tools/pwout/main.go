// pwout — 把配置里解密的账号/口令写到 root-only 临时文件（一次性诊断，不进发布包）。
package main

import (
	"fmt"
	"os"

	"moo/internal/config"
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
	if err := os.WriteFile("/tmp/panel_user.txt", []byte(cfg.PanelUsername), 0o600); err != nil {
		panic(err)
	}
	if err := os.WriteFile("/tmp/panel_pw.txt", []byte(cfg.PanelPassword), 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("written user=%s pw_len=%d\n", cfg.PanelUsername, len(cfg.PanelPassword))
}
