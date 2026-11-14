package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 0.2.62 A：mate.json 必须给每条应用补 icon_url（Moo 对无 icon_url 的外部源
// 卡片不会启动「下包抽图」兜底，非本机已装的卡只能显示首字母占位）。
func TestBuildMooJSONEmitsIconURL(t *testing.T) {
	ia := &indexedApp{
		Appname:   "demo",
		Source:    "https://github.com/o/r",
		Publisher: "o",
		Synced:    true,
		appEntry: appEntry{
			DisplayName: "Demo",
			DownloadURL: "https://github.com/o/r/releases/download/v1.0.0/demo.fpk",
			Version:     "1.0.0",
		},
	}
	raw, err := buildMooJSON("mate", "Blue-Mink", "https://github.com/Blue-Mink/moo", "http://127.0.0.1:38108/", map[string]*indexedApp{"demo": ia})
	if err != nil {
		t.Fatalf("buildMooJSON: %v", err)
	}
	var doc struct {
		Apps map[string]map[string]any `json:"apps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析输出: %v", err)
	}
	want := "http://127.0.0.1:38108/api/icon/demo"
	if got, _ := doc.Apps["demo"]["icon_url"].(string); got != want {
		t.Fatalf("icon_url = %q, want %q", got, want)
	}
	// 空 iconBase（单测/离线调用）不得凭空写入 icon_url。
	raw2, err := buildMooJSON("mate", "Blue-Mink", "", "", map[string]*indexedApp{"demo": ia})
	if err != nil {
		t.Fatalf("buildMooJSON(empty base): %v", err)
	}
	var doc2 struct {
		Apps map[string]map[string]any `json:"apps"`
	}
	if err := json.Unmarshal(raw2, &doc2); err != nil {
		t.Fatalf("解析输出2: %v", err)
	}
	if _, has := doc2.Apps["demo"]["icon_url"]; has {
		t.Fatalf("iconBase 为空时不应写 icon_url")
	}
}

// 0.2.62 B：/moo/{app}/{file} 必须按 Moo 的仓库布局出图，未知文件名/未知应用回 404。
func TestHostedIconRoute(t *testing.T) {
	dir := t.TempDir()
	c := newCompanion("unix:///tmp/none.sock", "http://127.0.0.1:1", "", nil, newIndex(dir+"/index.json"))
	c.bootDir = dir
	if err := c.idx.upsert(&indexedApp{Appname: "demo", Source: "https://github.com/o/r", Synced: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	c.saveIconFile("demo", makeTestPNG(t, 64, 64), "png")

	for _, f := range []string{"ICON.PNG", "ICON_256.PNG", "icon.png"} {
		req := httptest.NewRequest(http.MethodGet, "/moo/demo/"+f, nil)
		req.SetPathValue("appname", "demo")
		req.SetPathValue("file", f)
		rec := httptest.NewRecorder()
		c.handleHostedIcon(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应 200，实际 %d（%s）", f, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
			t.Errorf("%s Content-Type = %q", f, ct)
		}
	}
	// 白名单外的文件名
	req := httptest.NewRequest(http.MethodGet, "/moo/demo/secret.json", nil)
	req.SetPathValue("appname", "demo")
	req.SetPathValue("file", "secret.json")
	rec := httptest.NewRecorder()
	c.handleHostedIcon(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("非图标文件名应 404，实际 %d", rec.Code)
	}
	// 未收录应用
	req2 := httptest.NewRequest(http.MethodGet, "/moo/nope/ICON.PNG", nil)
	req2.SetPathValue("appname", "nope")
	req2.SetPathValue("file", "ICON.PNG")
	rec2 := httptest.NewRecorder()
	c.handleHostedIcon(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("未知应用应 404，实际 %d", rec2.Code)
	}
	// 根路径的图标探测不能被当成应用名
	req3 := httptest.NewRequest(http.MethodGet, "/moo/ICON_256.PNG", nil)
	req3.SetPathValue("appname", "ICON_256.PNG")
	rec3 := httptest.NewRecorder()
	c.handleHost(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("根路径图标探测应 404，实际 %d", rec3.Code)
	}
}
