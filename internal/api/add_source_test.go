package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moo/internal/config"
	"moo/internal/source"
)

// 0.6.326d2：详情页「添加为源」只发 {url}（不带 name）。此前后端 addSource
// 把空 name 直传 AddSource → manager 判空恒 400「源名称与地址均不能为空」。
// 修复后与批量端点 batchSources 同口径：空名从地址推导（SourceNameFromURL）。
//
// 测试用本地 httptest 源（不触外部网络）：AddSource 会立即抓取验证可达性。
// idxURL = 远端「源索引」地址（本地假源）；api = 被测 HTTP 服务
type addSrcFx struct {
	s      *Server
	api    *httptest.Server
	idxURL string
}

func newAddSourceFixture(t *testing.T) addSrcFx {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	// 任意路径都回一份最小可解析索引，保证 AddSource 的首拉成功
	idx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示",` +
			`"download_url":"https://example.com/demo.fpk"}}`))
	}))
	t.Cleanup(idx.Close)

	s := &Server{Cfg: config.Default(), Src: source.NewManager(config.Default())}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sources", s.addSource)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return addSrcFx{s: s, api: ts, idxURL: idx.URL}
}

func postAddSource(t *testing.T, ts *httptest.Server, payload map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	res, err := http.Post(ts.URL+"/api/sources", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestAddSourceEmptyNameFallback(t *testing.T) {
	fx := newAddSourceFixture(t)
	s, ts := fx.s, fx.api
	url := fx.idxURL + "/fnpack.json" // 与真实形态一致：指向索引文件

	base := len(s.Src.Sources())
	// 1) 空 name（详情页形态）→ 200，源名 = SourceNameFromURL(url)
	code, out := postAddSource(t, ts, map[string]any{"url": url})
	if code != http.StatusOK {
		t.Fatalf("空 name 应 200（修复前恒 400）, got %d %#v", code, out)
	}
	src, _ := out["source"].(map[string]any)
	if src == nil {
		t.Fatalf("应返回 source 对象: %#v", out)
	}
	want := sourceNameFromURL(url)
	if want == "" {
		t.Fatalf("夹具地址应能推导出名字: %q", url)
	}
	if src["name"] != want {
		t.Fatalf("源名应为 %q（SourceNameFromURL）, got %v", want, src["name"])
	}
	if src["url"] != url {
		t.Fatalf("源地址应回显 %q, got %v", url, src["url"])
	}
	if n := len(s.Src.Sources()); n != base+1 {
		t.Fatalf("应新增 1 个源（%d -> %d）", base, n)
	}

	// 2) 显式 name 优先（不被兜底覆盖）
	code, out = postAddSource(t, ts, map[string]any{"url": fx.idxURL + "/other/fnpack.json", "name": "我的自定义源"})
	if code != http.StatusOK {
		t.Fatalf("显式 name 应 200, got %d %#v", code, out)
	}
	src, _ = out["source"].(map[string]any)
	if src["name"] != "我的自定义源" {
		t.Fatalf("显式 name 应优先, got %v", src["name"])
	}

	// 3) url 为空仍 400
	code, out = postAddSource(t, ts, map[string]any{"url": ""})
	if code != http.StatusBadRequest {
		t.Fatalf("空 url 应 400, got %d %#v", code, out)
	}

	// 4) 重复地址 → 仍报 ErrExists（400），且不重复入库
	before := len(s.Src.Sources())
	code, out = postAddSource(t, ts, map[string]any{"url": url})
	if code != http.StatusBadRequest {
		t.Fatalf("重复地址应 400, got %d %#v", code, out)
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Fatalf("重复地址应带错误信息: %#v", out)
	}
	if n := len(s.Src.Sources()); n != before {
		t.Fatalf("重复添加不应改源数: %d -> %d", before, n)
	}
}
