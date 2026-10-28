package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSoftstoreType(t *testing.T) {
	cases := map[string]string{
		"原生":    "fpk",
		"docker": "docker",
		"Docker": "docker",
		"容器":    "docker",
		"":      "fpk",
	}
	for in, want := range cases {
		if got := softstoreType(in); got != want {
			t.Errorf("softstoreType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickSoftstoreURL(t *testing.T) {
	// 原始链优先（GitHub/gitee 稳定，Moo 可镜像竞速）
	a := &softstoreApp{
		DownloadURL:     "http://nas.hhxs2025.top:5660/apps/x-1.0.fpk",
		OrigDownloadURL: "https://github.com/o/r/releases/download/v1/x-1.0.fpk",
	}
	if got := pickSoftstoreURL(a); got != a.OrigDownloadURL {
		t.Errorf("pickSoftstoreURL 应优先原始链, got %q", got)
	}
	// 无原始链回落软仓服务器
	b := &softstoreApp{DownloadURL: "http://nas.hhxs2025.top:5660/apps/x-1.0.fpk"}
	if got := pickSoftstoreURL(b); got != b.DownloadURL {
		t.Errorf("pickSoftstoreURL 应回落软仓服务器, got %q", got)
	}
}

// discoverFromSoftstore：httptest 模拟软仓 /api/apps，验证双架构合并、
// icon_url 透传、下载链候选、type 映射。
func TestDiscoverFromSoftstore(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": [
				{"id":"Liars-Bar","name":"骗子酒馆","version":"1.7.1","desc":"卡牌游戏","author":"Hanazar-Games","tags":"游戏","platform":"x86","type":"原生","icon":"https://gitee.com/hhxs2025/fn-appstores/raw/master/server-data/icons/Liars-Bar.PNG","download_url":"http://nas.hhxs2025.top:5660/apps/Liars-Bar-1.7.1-x86.fpk","sha256":"aaa","size":1940820,"_original_download_url":"https://github.com/shuangji66/Fndepot/releases/download/2026.7.31/Liars-Bar-1.7.1-x86.fpk","download_urls":["http://nas.hhxs2025.top:5660/apps/Liars-Bar-1.7.1-x86.fpk","https://gh-proxy.org/https://github.com/shuangji66/Fndepot/releases/download/2026.7.31/Liars-Bar-1.7.1-x86.fpk"]},
				{"id":"Liars-Bar","name":"骗子酒馆","version":"1.7.1","desc":"卡牌游戏","author":"Hanazar-Games","tags":"游戏","platform":"arm","type":"原生","icon":"https://gitee.com/hhxs2025/fn-appstores/raw/master/server-data/icons/Liars-Bar.PNG","download_url":"http://nas.hhxs2025.top:5660/apps/Liars-Bar-1.7.1-arm.fpk","sha256":"bbb","size":1940660,"_original_download_url":"https://github.com/shuangji66/Fndepot/releases/download/2026.7.31/Liars-Bar-1.7.1-arm.fpk","download_urls":["http://nas.hhxs2025.top:5660/apps/Liars-Bar-1.7.1-arm.fpk","https://gh-proxy.org/https://github.com/shuangji66/Fndepot/releases/download/2026.7.31/Liars-Bar-1.7.1-arm.fpk"]},
				{"id":"gotify","name":"Gotify","version":"3.0.0","desc":"消息推送","author":"gotify","tags":"工具","platform":"x86","type":"docker","icon":"https://gitee.com/hhxs2025/fn-appstores/raw/master/server-data/icons/gotify.PNG","download_url":"http://nas.hhxs2025.top:5660/apps/gotify-3.0.0-x86.fpk","sha256":"ccc","size":1048576,"_original_download_url":"https://github.com/gotify/server/releases/download/v3.0.0/gotify-3.0.0-x86.fpk"}
			]
		}`))
	}))
	defer srv.Close()

	apps, err := discoverFromSoftstore(context.Background(), srv.URL, http.DefaultClient)
	if err != nil {
		t.Fatalf("discoverFromSoftstore 出错: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("应用数 = %d, want 2（Liars-Bar 合并 + gotify）", len(apps))
	}

	lb := apps["Liars-Bar"]
	if lb == nil {
		t.Fatal("缺 Liars-Bar")
	}
	if lb.DisplayName != "骗子酒馆" || lb.Version != "1.7.1" {
		t.Errorf("Liars-Bar 元数据错误: %+v", lb.appEntry)
	}
	if lb.AppType != "fpk" {
		t.Errorf("Liars-Bar app_type = %q, want fpk", lb.AppType)
	}
	if lb.IconURL == "" {
		t.Error("Liars-Bar icon_url 应为空失败（软仓 icon 透传）")
	}
	// 双架构合并
	if len(lb.Packages) != 2 {
		t.Errorf("Liars-Bar packages 架构数 = %d, want 2", len(lb.Packages))
	}
	if _, ok := lb.Packages["x86"]; !ok {
		t.Error("缺 x86 包")
	}
	if _, ok := lb.Packages["arm"]; !ok {
		t.Error("缺 arm 包")
	}
	// 主 download_url 指向 x86 的原始链
	if lb.DownloadURL != "https://github.com/shuangji66/Fndepot/releases/download/2026.7.31/Liars-Bar-1.7.1-x86.fpk" {
		t.Errorf("Liars-Bar 主下载链错误: %q", lb.DownloadURL)
	}
	// size_bytes 取 x86 包体积
	if lb.SizeBytes != 1940820 {
		t.Errorf("Liars-Bar size_bytes = %d, want 1940820", lb.SizeBytes)
	}
	// 候选下载链：x86/arm 各 2 条（原始链 + 软仓服务器），合并去重后 4 条
	if len(lb.DownloadURLs) != 4 {
		t.Errorf("Liars-Bar 候选链数 = %d, want 4（x86/arm 各原始链+软仓链）", len(lb.DownloadURLs))
	}

	// docker 应用 type 映射
	g := apps["gotify"]
	if g == nil || g.AppType != "docker" {
		t.Errorf("gotify app_type = %v, want docker", g)
	}
	// 单架构应用
	if len(g.Packages) != 1 {
		t.Errorf("gotify packages 数 = %d, want 1", len(g.Packages))
	}
}

// discoverFromSoftstore：空列表与 HTTP 错误。
func TestDiscoverFromSoftstoreEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()
	if _, err := discoverFromSoftstore(context.Background(), srv.URL, http.DefaultClient); err == nil {
		t.Error("空列表应报错")
	}
}
