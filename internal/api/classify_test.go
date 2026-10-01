package api

import "testing"

// 第二轮全量审计（2026-09-26）的回归保护：
// 精选表最高优先、driver 关键词收紧、关键词先于源标签、标签仅兜底。

func appFor(name string) *AppInfo {
	return &AppInfo{AppName: name, DisplayName: "Display",
		Description: "由 Rust & Tokio 驱动，无需显卡，游戏影音工具。"}
}

func TestClassifyAllCuratedBeatsEverything(t *testing.T) {
	cases := map[string]string{
		"amd.rocm-7.2":             "driver", // 官方 [AI,Drive]：AMD AI 驱动族归驱动（用户定稿）
		"trim.ai-runtime-amd-migraphx": "driver",
		"fn-ntfs3":         "driver",
		"fn-aic8800":       "driver",
		"komari":           "devtools", // 监控，非网络
		"komari-agent":     "devtools",
		"sunshine":         "game",     // 游戏串流
		"nexplay":          "media",    // IPTV 播放器，非摄影摄像
		"vibenvr":          "photo",    // AI NVR
		"bililive-go":      "automation",
		"fygo-browser":     "browser",
		"fn-88jizhang":     "lifestyle",
		"homebox":          "lifestyle", // 家庭物品资产，非开发工具
		"docker-baota":     "devtools",  // 服务器面板
		"docker-homepage":  "efficiency", // 仪表盘
		"trim.vm":          "devtools",  // 虚拟机
		"wol":              "network",   // 局域网唤醒
	}
	out := make([]AppInfo, 0, len(cases))
	for name := range cases {
		a := appFor(name)
		// 即使源标签冲突（影音娱乐）也必须按精选表归类
		a.Category = "影音娱乐"
		out = append(out, *a)
	}
	classifyAll(out)
	for i := range out {
		want := cases[out[i].AppName]
		if out[i].Category != want {
			t.Errorf("%s = %q, want %q (curated)", out[i].AppName, out[i].Category, want)
		}
	}
}

func TestClassifyDriverKeywordTightened(t *testing.T) {
	// 旧 bug：「无需显卡」命中 driver 强词「显卡」
	out := []AppInfo{{AppName: "mtranservertest", DisplayName: "离线翻译服务器",
		Description: "一个超低资源消耗速度超快的离线翻译模型服务器，无需显卡。"}}
	classifyAll(out)
	if out[0].Category == "driver" {
		t.Errorf("「无需显卡」不应归 driver")
	}
	// 真驱动特征短语
	out = []AppInfo{{AppName: "some-gpu-pkg", DisplayName: "GPU 驱动包",
		Description: "提供显卡驱动支持，安装后重启生效"}}
	classifyAll(out)
	if out[0].Category != "driver" {
		t.Errorf("显卡驱动包 want driver, got %q", out[0].Category)
	}
	// 「由 Rust 驱动」不是驱动应用（无驱动特征短语时）
	out = []AppInfo{{AppName: "rusttool", DisplayName: "工具",
		Description: "由 Rust 编写的高性能工具，无需显卡"}}
	classifyAll(out)
	if out[0].Category == "driver" {
		t.Errorf("「由 Rust 编写」不应归 driver")
	}
}

func TestClassifyKeywordBeforeLabel(t *testing.T) {
	// fndepot 第三方标签「影音」不可信：描述是游戏 → game
	out := []AppInfo{{AppName: "some-webgame", DisplayName: "方块网页游戏",
		Description: "经典网页游戏，支持联机对战", Category: "影音"}}
	classifyAll(out)
	if out[0].Category != "game" {
		t.Errorf("游戏描述+影音标签 want game, got %q", out[0].Category)
	}
	// 描述是 nginx → devtools，而非标签说的影音
	out = []AppInfo{{AppName: "nginxx", DisplayName: "Nginx",
		Description: "高性能的 HTTP 和反向代理服务器", Category: "影音"}}
	classifyAll(out)
	if out[0].Category != "devtools" {
		t.Errorf("nginx want devtools, got %q", out[0].Category)
	}
}

func TestClassifyLabelFallbackOnly(t *testing.T) {
	// 无任何关键词信号 → 回退源标签
	out := []AppInfo{{AppName: "zzz-unknown-app", DisplayName: "未知应用",
		Description: "一个普通的工具。", Category: "备份同步"}}
	classifyAll(out)
	if out[0].Category != "backup" {
		t.Errorf("标签兜底 want backup, got %q", out[0].Category)
	}
	// 无信号也无标签 → 空
	out = []AppInfo{{AppName: "zzz-unknown-app-2", DisplayName: "未知应用",
		Description: "一个普通的工具。"}}
	classifyAll(out)
	if out[0].Category != "" {
		t.Errorf("无信号 want empty, got %q", out[0].Category)
	}
}

func TestClassifyAllValidKeys(t *testing.T) {
	if len(validCategoryKeys) != 13 {
		t.Fatalf("分类数应为 13（官方10+媒体自动化/网络工具/浏览器），got %d", len(validCategoryKeys))
	}
	for name, cat := range curatedCategories {
		if !validCategoryKeys[cat] {
			t.Errorf("精选表 %q -> 非法分类 %q", name, cat)
		}
	}
	if len(curatedCategories) < 600 {
		t.Errorf("精选表应 ≥600 条，got %d", len(curatedCategories))
	}
}
