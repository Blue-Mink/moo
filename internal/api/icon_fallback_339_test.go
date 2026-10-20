package api

// icon_fallback_339_test.go —— 0.6.339 图标兜底（小写变体 / 同 owner 构建仓）单测。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// A. 候选生成规则：只做「小写全等 + 显式别名 + 同 owner 两个固定构建仓名」。
func TestIconFBCandidates339(t *testing.T) {
	base := "https://github.com/Hxido-RXM/FnDepot"
	cases := []struct {
		app  string
		want string
	}{
		{"1Panel", "/Hxido-RXM/FnDepot/main/1panel/ICON.PNG"},
		{"filebrowser", "/Hxido-RXM/fnos-apps/main/apps/filebrowser/fnos/ICON.PNG"},
		{"Openlist-beta", "/Hxido-RXM/fnos-apps/main/apps/openlist/fnos/ICON.PNG"},
		{"AdGuard", "/Hxido-RXM/fnos-apps/main/apps/adguardhome/fnos/ICON.PNG"},
	}
	for _, c := range cases {
		got := iconFBCandidates(c.app, base)
		if len(got) == 0 {
			t.Fatalf("%s: 候选为空", c.app)
		}
		found := false
		for _, u := range got {
			if strings.HasSuffix(u, c.want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: 候选里没有 %s，实际=%v", c.app, c.want, got)
		}
		if len(got) > iconFBMaxCands {
			t.Errorf("%s: 候选 %d 超上限 %d", c.app, len(got), iconFBMaxCands)
		}
	}
}

// B. 不该猜的输入一律返回 nil：非 GitHub 源 / 路径穿越 / 空名。
func TestIconFBCandidates339Rejects(t *testing.T) {
	if got := iconFBCandidates("moo", "http://192.0.2.15:3033/bluemink/FnDepot"); got != nil {
		t.Errorf("非 GitHub 源不该生成候选: %v", got)
	}
	if got := iconFBCandidates("../evil", "https://github.com/o/r"); got != nil {
		t.Errorf("路径穿越不该生成候选: %v", got)
	}
	if got := iconFBCandidates("  ", "https://github.com/o/r"); got != nil {
		t.Errorf("空应用名不该生成候选: %v", got)
	}
}

// C. 端到端：主流程全败后，构建仓命中 → 返回真 PNG；未知应用 → 不命中。
func TestResolveIconFallbackExtra339(t *testing.T) {
	// 127.0.0.1 的 netguard 豁免由包内 TestMain 统一注册（testmain_test.go）。
	var hits int32
	raw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path == "/Hxido-RXM/fnos-apps/main/apps/filebrowser/fnos/ICON.PNG" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNG)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(raw.Close)
	oldBase := iconAliasRawBase
	t.Cleanup(func() { iconAliasRawBase = oldBase })
	iconAliasRawBase = raw.URL

	s := newIconAliasTestServer(t, config.SourceRef{Name: "Hxido-RXM", URL: "https://github.com/Hxido-RXM/FnDepot"})
	def := map[string]bool{}
	data, ct, ok := s.resolveIconFallbackExtra(t.Context(), &source.App{Name: "Filebrowser", Source: "Hxido-RXM"}, def)
	if !ok {
		t.Fatalf("应命中构建仓图标（raw 命中 %d 次）", atomic.LoadInt32(&hits))
	}
	if len(data) < 8 || data[0] != 0x89 || data[1] != 'P' || data[2] != 'N' || data[3] != 'G' {
		t.Fatalf("返回字节非 PNG 魔数（%d 字节）", len(data))
	}
	if ct != "image/png" {
		t.Errorf("Content-Type = %q，want image/png", ct)
	}

	// 反例：构建仓也没有的应用 → 不命中，且不 panic
	def2 := map[string]bool{}
	if _, _, ok := s.resolveIconFallbackExtra(t.Context(), &source.App{Name: "wb2api", Source: "Hxido-RXM"}, def2); ok {
		t.Fatal("wb2api 不该命中（上游无目录）")
	}
	if len(def2) == 0 {
		t.Error("确定性 404 应写入 defMissPaths 供负缓存")
	}
}
