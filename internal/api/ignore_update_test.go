package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

func newIgnoreServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MOO_DATA", dir)
	return &Server{Cfg: config.Default(), Src: source.NewManager(config.Default())}
}

func TestIgnoreUpdateToggle(t *testing.T) {
	s := newIgnoreServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/apps/{key}/ignore-update", s.ignoreUpdate)
	mux.HandleFunc("DELETE /api/apps/{key}/ignore-update", s.ignoreUpdate)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	call := func(method, key string) map[string]any {
		req, _ := http.NewRequest(method, ts.URL+"/api/apps/"+key+"/ignore-update", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s 应 200, got %d", method, key, res.StatusCode)
		}
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}

	// PUT 加入集合
	out := call("PUT", "gitea")
	ign, _ := out["ignored"].([]any)
	if len(ign) != 1 {
		t.Fatalf("首次忽略应入集 1 个: %#v", out)
	}
	// 重复 PUT 幂等（不重复入集）
	out = call("PUT", "gitea")
	ign, _ = out["ignored"].([]any)
	if len(ign) != 1 {
		t.Fatalf("重复忽略应仍 1 个: %#v", ign)
	}
	// 带源后缀的 key 解析为同一 appname（不重复）
	out = call("PUT", "gitea@Blue-Mink")
	ign, _ = out["ignored"].([]any)
	if len(ign) != 1 {
		t.Fatalf("@源名 后缀应归一到同一 appname: %#v", ign)
	}
	// 第二个应用
	out = call("PUT", "mihomo")
	ign, _ = out["ignored"].([]any)
	if len(ign) != 2 {
		t.Fatalf("应 2 个: %#v", ign)
	}
	// DELETE 移除（幂等）
	call("DELETE", "gitea")
	out = call("DELETE", "gitea")
	ign, _ = out["ignored"].([]any)
	if len(ign) != 1 {
		t.Fatalf("移除后应剩 1 个: %#v", ign)
	}
	if s.Cfg.UpdateIgnored[0] != "mihomo" {
		t.Fatalf("剩余应是 mihomo: %#v", s.Cfg.UpdateIgnored)
	}
}

// TestApplyIgnoredUpdates 目录层抑制语义：已装+被忽略 → HasUpdate 置假 +
// UpdateIgnored 置真；AvailableVersion 保留（忽略更新列表要展示版本）；
// 未装条目与未忽略条目不受影响；空集合 no-op。
func TestApplyIgnoredUpdates(t *testing.T) {
	out := []AppInfo{
		{AppName: "gitea", Installed: true, HasUpdate: true, AvailableVersion: "1.22.0"},
		{AppName: "mihomo", Installed: true, HasUpdate: true},
		{AppName: "gitea-clone", Installed: false, HasUpdate: true},
	}
	applyIgnoredUpdates(out, []string{"gitea"})
	if !out[0].UpdateIgnored || out[0].HasUpdate {
		t.Fatalf("gitea 应被忽略且更新信号抑制: %#v", out[0])
	}
	if out[0].AvailableVersion != "1.22.0" {
		t.Fatalf("available_version 应保留: %q", out[0].AvailableVersion)
	}
	if out[1].UpdateIgnored || !out[1].HasUpdate {
		t.Fatalf("mihomo 不应受影响: %#v", out[1])
	}
	if out[2].UpdateIgnored || !out[2].HasUpdate {
		t.Fatalf("未装条目不应受影响: %#v", out[2])
	}

	out2 := []AppInfo{{AppName: "x", Installed: true, HasUpdate: true}}
	applyIgnoredUpdates(out2, nil)
	if !out2[0].HasUpdate || out2[0].UpdateIgnored {
		t.Fatalf("空忽略集合应 no-op: %#v", out2[0])
	}
}

// TestApplyIgnoredUpdatesPending（0.6.197）：确有更新被压住 →
// UpdateIgnoredPending=true（dock「有更新」列表含它）；已最新的忽略
// app（HasUpdate=false）→ Pending=false（不进列表）。
func TestApplyIgnoredUpdatesPending(t *testing.T) {
	out := []AppInfo{
		{AppName: "a", Installed: true, HasUpdate: true, AvailableVersion: "2.0.0"},
		{AppName: "b", Installed: true, HasUpdate: false},
	}
	applyIgnoredUpdates(out, []string{"a", "b"})
	if !out[0].UpdateIgnoredPending {
		t.Fatalf("有更新被压住应 Pending=true: %#v", out[0])
	}
	if out[1].UpdateIgnoredPending {
		t.Fatalf("已最新的忽略 app 不应 Pending: %#v", out[1])
	}
	if !out[1].UpdateIgnored {
		t.Fatalf("已最新的忽略 app 仍应 UpdateIgnored=true（发现页忽略区可见）: %#v", out[1])
	}
}
