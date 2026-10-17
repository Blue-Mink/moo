package source

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// ── 0.6.326e 上游仓「添加为源」可用性：候选集缺口 + 内容校验 ──────────────────
//
// 缺口（G1）：协议识别的索引文件名集合是 5 个（source_key.go 的
// searchKeyIndexFiles），而候选生成只给 2 个（jsonNamesFor）→ 只发布
// fndepot.json / fndepot_v2.json / apps.json 的仓库添加即失败。
// 下面用 httptest 造「只挂某一种索引」的仓库钉死行为（野外无 fndepot.json 单挂仓，
// 故以合成仓复现；apps.json 单挂仓野外有实例：conversun/fnos-apps）。

func newIndexServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range files {
		b := body
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(b))
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

const sampleFnpack = `{"demo":{"display_name":"演示","version":"1.0.0",
  "download_url":"https://example.com/demo.fpk","size":"3.2"}}`

// G2：jsonNamesFor 必须覆盖协议全集且顺序固定（moo → fnpack → fndepot →
// fndepot_v2 → apps），与 source_key.go 的识别集合一致。
func TestJsonNamesForCoversProtocolSet(t *testing.T) {
	want := []string{"moo.json", "fnpack.json", "fndepot.json", "fndepot_v2.json", "apps.json"}
	got := jsonNamesFor(true)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("jsonNamesFor 顺序/集合错：%v，期望 %v", got, want)
	}
	if strings.Join(jsonNamesFor(false), ",") != strings.Join(want, ",") {
		t.Fatalf("非 GitHub 分支应与 GitHub 分支同集合，实际 %v", jsonNamesFor(false))
	}
	// 与 source_key.go 的识别集合必须完全一致**（集合，不比顺序）**——
	// 两处漂移正是本次缺口的根因（识别 5 个、只拉 2 个）。
	norm := func(in []string) string {
		c := append([]string{}, in...)
		sort.Strings(c)
		return strings.Join(c, ",")
	}
	if norm(jsonNamesFor(true)) != norm(searchKeyIndexFiles) {
		t.Fatalf("jsonNamesFor 与 searchKeyIndexFiles 集合漂移：%v vs %v", got, searchKeyIndexFiles)
	}
}

// G2：同一前缀下 5 个文件名的相对顺序必须保持（moo 优先，apps 垫底）。
func TestBuildCandidatesIndexNameOrder(t *testing.T) {
	got := buildCandidates("https://github.com/Blue-Mink/FnDepot")
	idx := func(name string) int {
		for i, u := range got {
			if indexNameOf(u) == name {
				return i
			}
		}
		return -1
	}
	prev := -1
	for _, n := range []string{"moo.json", "fnpack.json", "fndepot.json", "fndepot_v2.json", "apps.json"} {
		i := idx(n)
		if i < 0 {
			t.Fatalf("缺少 %s 候选：%v", n, got)
		}
		if i < prev {
			t.Fatalf("%s 顺序错（%d < %d）：%v", n, i, prev, got)
		}
		prev = i
	}
	// 镜像前缀与 jsDelivr 也必须覆盖扩展名（三处循环同步扩展）
	joined := strings.Join(got, "\n")
	for _, wantSub := range []string{
		"raw.githubusercontent.com/Blue-Mink/FnDepot/main/fndepot.json",
		"raw.githubusercontent.com/Blue-Mink/FnDepot/main/apps.json",
		"cdn.jsdelivr.net/gh/Blue-Mink/FnDepot/apps.json",
	} {
		if !strings.Contains(joined, wantSub) {
			t.Fatalf("缺少候选 %s\n实际：%v", wantSub, got)
		}
	}
}

// G2 附带：用户从浏览器地址栏复制的 /tree/<分支>[/子目录] 形态必须能解析出
// owner/repo（否则候选全落在 repo/tree/main/... 上 → 一律 404）。
func TestBuildCandidatesStripsGitHubSubpath(t *testing.T) {
	got := buildCandidates("https://github.com/Blue-Mink/FnDepot/tree/main")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "raw.githubusercontent.com/Blue-Mink/FnDepot/main/fnpack.json") {
		t.Fatalf("/tree/main 形态未解析出 owner/repo：%v", got)
	}
	if strings.Contains(joined, "FnDepot/tree/main/main/") {
		t.Fatalf("候选里残留子路径段：%v", got)
	}
}

// G1：只挂 fndepot.json 的仓库必须可拉取（修前 jsonNamesFor 只有 2 个 → 失败）。
func TestFnDepotJSONOnlyRepositoryIsReachable(t *testing.T) {
	ts := newIndexServer(t, map[string]string{"/fndepot.json": sampleFnpack})
	f := NewFnDepot("probe", ts.URL)
	apps, err := f.Fetch()
	if err != nil {
		t.Fatalf("只挂 fndepot.json 的仓库应可拉取，实际失败：%v", err)
	}
	if len(apps) != 1 || apps["demo"] == nil || apps["demo"].Version != "1.0.0" {
		t.Fatalf("解析结果错：%+v", apps)
	}
}

// G1：fndepot_v2.json（V2 包裹）同样必须可拉取。
func TestFnDepotV2JSONOnlyRepositoryIsReachable(t *testing.T) {
	v2 := `{"schema_version":"2.0","source_info":{"name":"x"},
	  "apps":{"demo":{"display_name":"演示","version":"2.0.0","download_url":"https://e.com/d.fpk"}}}`
	ts := newIndexServer(t, map[string]string{"/fndepot_v2.json": v2})
	if _, err := NewFnDepot("probe", ts.URL).Fetch(); err != nil {
		t.Fatalf("只挂 fndepot_v2.json 的仓库应可拉取，实际失败：%v", err)
	}
}

// G2：apps.json 兜底必须校验内容确为应用集合 —— 工程项目自带的无关
// apps.json（无 display_name/version/download_url）不得被当成源。
func TestAppsJSONUnrelatedRejected(t *testing.T) {
	ts := newIndexServer(t, map[string]string{
		"/apps.json": `{"build":{"outputs":["dist/a.js"],"hash":"deadbeef"}}`,
	})
	if _, err := NewFnDepot("probe", ts.URL).Fetch(); err == nil {
		t.Fatal("无关 apps.json 不应被接受为源")
	}
}

// G2：apps.json 内容确为应用集合时应被接受（如 conversun/fnos-apps 形态）。
func TestAppsJSONValidAccepted(t *testing.T) {
	ts := newIndexServer(t, map[string]string{
		"/apps.json": `{"schema_version":1,"apps":{"demo":{"display_name":"演示",
		  "version":"1.0.0","download_url":"https://e.com/demo.fpk"}}}`,
	})
	apps, err := NewFnDepot("probe", ts.URL).Fetch()
	if err != nil {
		t.Fatalf("合法 apps.json 应可拉取，实际失败：%v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("解析结果错：%+v", apps)
	}
}

// G3：ProbeIndex 探测命中文件名，供「添加为源」预判可加性（探测成功 ⇒ 添加必成功）。
func TestProbeIndex(t *testing.T) {
	ts := newIndexServer(t, map[string]string{"/fndepot.json": sampleFnpack})
	if got := ProbeIndex(ts.URL); got != "fndepot.json" {
		t.Fatalf("ProbeIndex = %q，期望 fndepot.json", got)
	}
	empty := newIndexServer(t, map[string]string{"/README.md": "# hi"})
	if got := ProbeIndex(empty.URL); got != "" {
		t.Fatalf("无索引仓库 ProbeIndex 应为空，实际 %q", got)
	}
	// 无关 apps.json 同样不算「有索引」
	bad := newIndexServer(t, map[string]string{"/apps.json": `{"build":{"x":1}}`})
	if got := ProbeIndex(bad.URL); got != "" {
		t.Fatalf("无关 apps.json 不应判为有索引，实际 %q", got)
	}
}
