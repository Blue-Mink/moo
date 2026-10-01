// panelappnames 一次性探针：面板登录 + 官方目录 appname 列表 + 指定应用详情归属字段。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"moo/internal/panel"
)

type cfg struct {
	PanelBaseURL  string `json:"panel_base_url"`
	PanelUsername string `json:"panel_username"`
	PanelPassword string `json:"panel_password"`
}

func main() {
	path := "/vol1/@appdata/moo/config.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取配置失败:", err)
		os.Exit(1)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		fmt.Fprintln(os.Stderr, "解析配置失败:", err)
		os.Exit(1)
	}
	pc := panel.NewClient(c.PanelBaseURL, c.PanelUsername, c.PanelPassword)
	ctx := context.Background()
	if err := pc.EnsureLoggedIn(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "登录失败:", err)
		os.Exit(1)
	}
	if len(os.Args) > 2 && os.Args[2] == "names" {
		apps, err := pc.AppList(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "拉取官方目录失败:", err)
			os.Exit(1)
		}
		names := make([]string, 0, len(apps))
		for _, a := range apps {
			names = append(names, a.AppName)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(n)
		}
		fmt.Fprintf(os.Stderr, "TOTAL %d\n", len(names))
		return
	}
	for _, name := range os.Args[2:] {
		d, err := pc.AppDetail(ctx, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s 详情失败: %v\n", name, err)
			continue
		}
		fmt.Printf("%s: maintainer=%q maintainerUrl=%q distributor=%q distributorUrl=%q\n",
			name, d.AppDetail.Maintainer, d.AppDetail.MaintainerURL, d.AppDetail.Distributor, d.AppDetail.DistributorURL)
	}
}
