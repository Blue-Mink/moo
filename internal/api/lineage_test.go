package api

// lineage_test.go —— 0.6.326 应用溯源批次单测（B12）。
//
// 覆盖：owner 口径（含目录 key 的主机前缀形态）／搬运命中（含上游版本）／
// 同 owner 自建仓不判上游／origin 无对应源时回落 UpstreamRepo／
// 自动纠正开-关与平台权威跳过／禁用展示层语义／hideApp handler 往返。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
	"moo/internal/pipeline"
	"moo/internal/platform"
	"moo/internal/source"
)

// newLineageServer 溯源测试环境：上游源（alice/awesome-apps）+ 搬运源
// （yuexps/FnDepot，包指向 alice 仓）+ 自建源（bob/mine）。
// 目录缓存预置，跳过网络抓取与 daemon。
func newLineageServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "上游源", URL: "https://github.com/alice/awesome-apps"},
		{Name: "搬运源", URL: "https://github.com/yuexps/FnDepot"},
		{Name: "自建源", URL: "https://github.com/bob/mine"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"上游源": {
			"app-a": {Name: "app-a", Source: "上游源", Version: "2.5.0"},
		},
		"搬运源": {
			"app-a": {Name: "app-a", Source: "搬运源", Version: "1.9.0"},
		},
	})
	return &Server{Cfg: cfg, Src: m}
}

// mkLineageCard 造溯源测试卡：repo = 包发布仓库（owner/repo），空则无下载链。
func mkLineageCard(key, appname, src, ver, repo string, installed bool) AppInfo {
	a := AppInfo{Key: key, AppName: appname, DisplayName: appname, Source: src, Installed: installed}
	if installed {
		a.InstalledVersion = ver
	} else {
		a.LatestVersion = ver
	}
	if repo != "" {
		a.ReleaseURL = "https://github.com/" + repo + "/releases/download/v" + ver + "/" + appname + ".fpk"
	}
	return a
}

func TestOwnerOf(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"alice/awesome-apps", "alice"},
		{"github.com/Alice/Awesome", "alice"}, // 目录 key 带主机前缀 + 大小写归一
		{"192.0.2.15:3033/bluemink/moo", "bluemink"},
		{"alice", ""},
		{"", ""},
		{"/alice/repo/", "alice"},
	} {
		if got := ownerOf(tc.in); got != tc.want {
			t.Fatalf("ownerOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ① 搬运命中：包 origin owner ≠ 源 owner，且 Moo 有该 origin 源
// → UpstreamSource 正确、UpstreamVersion 取上游源同名卡版本。
func TestApplyUpstreamLineage_MirrorHit(t *testing.T) {
	s := newLineageServer(t)
	out := []AppInfo{
		mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", false),
		mkLineageCard("app-a@上游源", "app-a", "上游源", "2.5.0", "alice/awesome-apps", false),
	}
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "alice/awesome-apps" {
		t.Fatalf("UpstreamRepo = %q", m.UpstreamRepo)
	}
	if m.UpstreamSource != "上游源" {
		t.Fatalf("UpstreamSource = %q, want 上游源", m.UpstreamSource)
	}
	if m.UpstreamVersion != "2.5.0" {
		t.Fatalf("UpstreamVersion = %q, want 2.5.0", m.UpstreamVersion)
	}
	if m.UpstreamURL != "https://github.com/alice/awesome-apps" {
		t.Fatalf("UpstreamURL = %q", m.UpstreamURL)
	}
	// 上游卡自身：本源仓库 = 包仓库 → 不是搬运
	if out[1].UpstreamRepo != "" {
		t.Fatalf("上游源自己的卡不应判为搬运：%#v", out[1])
	}
}

// ② 同 owner 自建仓（yuexps/FnDepot 里的包指向 yuexps/其他仓）→ 不判上游。
func TestApplyUpstreamLineage_SameOwnerNotUpstream(t *testing.T) {
	s := newLineageServer(t)
	out := []AppInfo{
		mkLineageCard("app-b@搬运源", "app-b", "搬运源", "1.0.0", "yuexps/other-repo", false),
		mkLineageCard("app-c@自建源", "app-c", "自建源", "1.0.0", "bob/mine", false),
	}
	s.applyUpstreamLineage(out)
	for _, a := range out {
		if a.UpstreamRepo != "" || a.UpstreamSource != "" || a.UpstreamURL != "" {
			t.Fatalf("同 owner 作者自建仓不应判为上游：%#v", a)
		}
	}
}

// newForkLineageServer 0.6.326c「整仓 fork 式搬运」测试环境：
// 上游源 = EWEDLCM/FnDepot（fork 根）；Fork搬运源 = Chordi0124/FnDepot
// （GitHub 上确认是 EWEDLCM/FnDepot 的 fork）；自打打包源 = shuangji66/FnDepot
// （fork=false，打包者就是自己）。fork 关系用桩注入，不走网络。
func newForkLineageServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "上游源", URL: "https://github.com/EWEDLCM/FnDepot"},
		{Name: "Fork搬运源", URL: "https://github.com/Chordi0124/FnDepot"},
		{Name: "自打打包源", URL: "https://github.com/shuangji66/FnDepot"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"上游源":    {"picoclaw": {Name: "picoclaw", Source: "上游源", Version: "0.3.1"}},
		"Fork搬运源": {"picoclaw": {Name: "picoclaw", Source: "Fork搬运源", Version: "0.2.8"}},
		"自打打包源":  {"app-x": {Name: "app-x", Source: "自打打包源", Version: "1.0.0"}},
	})
	f := source.NewGHForks("")
	f.SetForTest("chordi0124/fndepot", "EWEDLCM/FnDepot") // fork → 上游是 fork parent
	f.SetForTest("ewedlcm/fndepot", "")                   // 根：作者自建仓
	f.SetForTest("shuangji66/fndepot", "")                // 不是 fork（自打打包）
	return &Server{Cfg: cfg, Src: m, Forks: f}
}

// ②b 整仓 fork 式搬运（0.6.326c）：包就在本源仓库里（owner 相同），但本源仓库
// 是别人搬运仓的 fork，且卡片 distributor 写着原打包者（≠ 本源 owner）
// → 判为搬运，上游 = fork parent（若 parent 也是 Moo 的源则填 UpstreamSource）。
func TestApplyUpstreamLineage_MirrorForkHit(t *testing.T) {
	s := newForkLineageServer(t)
	out := []AppInfo{
		mkLineageCard("picoclaw@Fork搬运源", "picoclaw", "Fork搬运源", "0.2.8", "Chordi0124/FnDepot", false),
		mkLineageCard("picoclaw@上游源", "picoclaw", "上游源", "0.3.1", "EWEDLCM/FnDepot", false),
	}
	out[0].Distributor = "EWEDL" // 索引被整仓 fork 走时原样保留的打包者
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "ewedlcm/fndepot" {
		t.Fatalf("整仓 fork 应溯源到 parent，UpstreamRepo = %q", m.UpstreamRepo)
	}
	if m.UpstreamSource != "上游源" || m.UpstreamVersion != "0.3.1" {
		t.Fatalf("parent 是 Moo 的源时应填 UpstreamSource/Version：%#v", m)
	}
	// fork 根本身（fork=false）即使 distributor 不等于 owner 也不判上游
	if out[1].UpstreamRepo != "" {
		t.Fatalf("fork 根（作者自建仓）不应判上游：%#v", out[1])
	}
}

// ②c 同 owner 且「打包者 == 本源 owner」/ fork 未知 → 维持放过（保守，
// 不误报）。fork 未知的仓库应被排入队列等后台拉取。
func TestApplyUpstreamLineage_MirrorForkConservative(t *testing.T) {
	s := newForkLineageServer(t)
	// ①fork=false（shuangji66 自打）：不判
	self := mkLineageCard("app-x@自打打包源", "app-x", "自打打包源", "1.0.0", "shuangji66/FnDepot", false)
	self.Distributor = "shuangji66"
	// ②fork 未知：保守放过并排队
	unknown := mkLineageCard("app-y@搬运源", "app-y", "搬运源", "1.0.0", "yuexps/FnDepot", false)
	unknown.Distributor = "EWEDL"
	out := []AppInfo{self, unknown}
	s.applyUpstreamLineage(out)
	if out[0].UpstreamRepo != "" {
		t.Fatalf("打包者就是本源 owner 时不应判上游：%#v", out[0])
	}
	if out[1].UpstreamRepo != "" {
		t.Fatalf("fork 关系未知时应保守放过：%#v", out[1])
	}
	// ③distributor 与 maintainer 都空：无「他人打包」证据 → 不判
	empty := mkLineageCard("app-z@搬运源", "app-z", "搬运源", "1.0.0", "yuexps/FnDepot", false)
	if p, isFork, known := s.Forks.Lookup("yuexps/fndepot"); !known || isFork || p != "" {
		// 桩未注入该仓库：Lookup 应为 known=false（上面已断言排过队）
		if known {
			t.Fatalf("yuexps/fndepot 不应有桩数据")
		}
	}
	s.applyUpstreamLineage([]AppInfo{empty})
	if empty.UpstreamRepo != "" {
		t.Fatalf("无 distributor/maintainer 证据时不应判上游：%#v", empty)
	}
}

// ③ origin 无对应源（Moo 未添加该上游源）→ 回落 UpstreamRepo/URL，
// UpstreamSource/Version 留空；无源归属（Source 空）的卡整体跳过。
func TestApplyUpstreamLineage_UnknownOriginFallback(t *testing.T) {
	s := newLineageServer(t)
	out := []AppInfo{
		mkLineageCard("app-d@搬运源", "app-d", "搬运源", "1.0.0", "carol/unknown-repo", false),
		mkLineageCard("app-e@", "app-e", "", "1.0.0", "carol/unknown-repo", false),
	}
	s.applyUpstreamLineage(out)
	m := out[0]
	if m.UpstreamRepo != "carol/unknown-repo" || m.UpstreamURL != "https://github.com/carol/unknown-repo" {
		t.Fatalf("应回落 UpstreamRepo/URL：%#v", m)
	}
	if m.UpstreamSource != "" || m.UpstreamVersion != "" {
		t.Fatalf("未添加该上游源时 UpstreamSource/Version 应为空：%#v", m)
	}
	if out[1].UpstreamRepo != "" {
		t.Fatalf("无源归属的卡应跳过：%#v", out[1])
	}
}

// ④ 自动纠正：on + 已装 + 上游版本更高 → 点亮并跟随上游包字段；
// off 不动；上游不比已装新不动；平台权威应用跳过。
func TestApplyUpstreamCorrection(t *testing.T) {
	s := newLineageServer(t)
	mk := func() []AppInfo {
		out := []AppInfo{
			mkLineageCard("app-a@搬运源", "app-a", "搬运源", "1.9.0", "alice/awesome-apps", true),
			mkLineageCard("app-a@上游源", "app-a", "上游源", "2.5.0", "alice/awesome-apps", false),
		}
		out[1].Sha256 = "deadbeef"
		out[1].SizeBytes = 4242
		out[1].ReleaseNotes = "上游说明"
		s.applyUpstreamLineage(out)
		return out
	}
	// on：纠正为上游目标
	out := mk()
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	c := out[0]
	if !c.HasUpdate || c.AvailableVersion != "2.5.0" || c.UpdateFromSource != "上游源" {
		t.Fatalf("自动纠正未点亮：%#v", c)
	}
	if c.ReleaseURL != out[1].ReleaseURL || c.Sha256 != "deadbeef" || c.SizeBytes != 4242 {
		t.Fatalf("包字段未跟随上游卡：%#v", c)
	}
	// off：零变化
	out = mk()
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, false)
	if out[0].HasUpdate || out[0].AvailableVersion != "" || out[0].UpdateFromSource != "" {
		t.Fatalf("auto_correct=off 不应改动：%#v", out[0])
	}
	// 上游不比已装新：零变化
	out = mk()
	out[1].LatestVersion = "1.0.0"
	out[0].UpstreamVersion = "1.0.0"
	applyUpstreamCorrection(out, map[string]platform.InstalledApp{}, true)
	if out[0].HasUpdate {
		t.Fatalf("上游更低不应纠正：%#v", out[0])
	}
	// 平台权威（daemon sourceID 非空 / official）：跳过
	out = mk()
	byName := map[string]platform.InstalledApp{"app-a": {AppName: "app-a", SourceID: "42"}}
	applyUpstreamCorrection(out, byName, true)
	if out[0].HasUpdate {
		t.Fatalf("平台跟踪应用不应被上游纠正：%#v", out[0])
	}
	out = mk()
	byName = map[string]platform.InstalledApp{"app-a": {AppName: "app-a", Source: "official"}}
	applyUpstreamCorrection(out, byName, true)
	if out[0].HasUpdate {
		t.Fatalf("官方应用不应被上游纠正：%#v", out[0])
	}
}

// ⑤ 禁用展示层：命中卡只置 UserHidden（Hidden 必须保持 false —— 卡要留在
// /api/apps 响应里，前端才能过滤并可在发现页「禁用列表」找回；0.6.326-ui 修正）。
func TestApplyHiddenApps(t *testing.T) {
	out := []AppInfo{
		mkLineageCard("h1@src", "h1", "src", "1.0.0", "", false),
		mkLineageCard("h2@src", "h2", "src", "1.0.0", "", true),
		mkLineageCard("keep@src", "keep", "src", "1.0.0", "", false),
	}
	applyHiddenApps(out, []string{"h1@src", "h2"}) // key 精确 + appname 回落
	if out[0].Hidden || !out[0].UserHidden {
		t.Fatalf("未装禁用卡只置 UserHidden（不得 Hidden=不下发）：%#v", out[0])
	}
	if out[1].Hidden || !out[1].UserHidden {
		t.Fatalf("已装禁用卡只置 UserHidden：%#v", out[1])
	}
	if out[2].Hidden || out[2].UserHidden {
		t.Fatalf("未禁用卡不应被标记：%#v", out[2])
	}
	applyHiddenApps(out, nil) // 空集合 = 零操作
}

// hideApp handler：PUT/DELETE 往返、幂等、key 原样存（不做 daemon 名归一）、
// 上限 500 守卫。
func TestHideAppHandler(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	s := &Server{Cfg: config.Default(), Src: source.NewManager(config.Default())}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/apps/{key}/hide", s.hideApp)
	mux.HandleFunc("DELETE /api/apps/{key}/hide", s.hideApp)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	call := func(method, key string) []any {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+"/api/apps/"+key+"/hide", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s 应 200, got %d", method, key, res.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		lst, _ := out["hidden"].([]any)
		return lst
	}
	if got := call("PUT", "app-a@搬运源"); len(got) != 1 {
		t.Fatalf("首次禁用应入集 1 个：%#v", got)
	}
	if got := call("PUT", "app-a@搬运源"); len(got) != 1 {
		t.Fatalf("重复禁用应幂等：%#v", got)
	}
	if s.Cfg.HiddenApps[0] != "app-a@搬运源" {
		t.Fatalf("key 应原样存：%#v", s.Cfg.HiddenApps)
	}
	if got := call("PUT", "app-b"); len(got) != 2 {
		t.Fatalf("应 2 个：%#v", got)
	}
	if got := call("DELETE", "app-a@搬运源"); len(got) != 1 || s.Cfg.HiddenApps[0] != "app-b" {
		t.Fatalf("恢复后应剩 app-b：%#v", s.Cfg.HiddenApps)
	}
	call("DELETE", "app-b") // 幂等
	// 上限守卫
	s.Cfg.HiddenApps = make([]string, maxHiddenApps)
	for i := range s.Cfg.HiddenApps {
		s.Cfg.HiddenApps[i] = "x"
	}
	req, _ := http.NewRequest("PUT", ts.URL+"/api/apps/overflow/hide", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("超上限应 409, got %d", res.StatusCode)
	}
}

// auto_correct 旧字段的迁移语义：GET 仍透出（无 omitempty），但 PUT 只把它当作
// 「等价 update_policy=upstream（强制上游）」的兼容输入——独立开关不再保留。
func TestAutoCorrectSettingsRoundTrip(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{}
	s := &Server{Cfg: cfg, Src: source.NewManager(cfg), Pipe: &pipeline.Pipeline{}}

	if got := settingsGetStruct(t, s); got.AutoCorrect {
		t.Fatalf("默认应为 false：%#v", got)
	}
	putSettingsBody(t, s, `{}`) // 缺省 = 不改动
	if cfg.AutoCorrect || cfg.UpdatePolicy != "" {
		t.Fatalf("未提交不应改动：auto=%v policy=%q", cfg.AutoCorrect, cfg.UpdatePolicy)
	}
	// 旧客户端：auto_correct=true ⇒ 迁移为强制上游（独立开关不再落盘）
	putSettingsBody(t, s, `{"auto_correct": true}`)
	if cfg.AutoCorrect {
		t.Fatal("独立开关不应再保留（恒 false）")
	}
	if cfg.UpdatePolicy != "upstream" || settingsGetStruct(t, s).UpdatePolicy != "upstream" {
		t.Fatalf("auto_correct=true 应迁移为 update_policy=upstream，got %q", cfg.UpdatePolicy)
	}
	// 显式策略值优先（同请求里带上 false 也不影响）
	putSettingsBody(t, s, `{"auto_correct": false, "update_policy": "lineage"}`)
	if cfg.UpdatePolicy != "lineage" {
		t.Fatalf("显式 update_policy 应生效，got %q", cfg.UpdatePolicy)
	}
}
