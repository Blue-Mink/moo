package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
)

// 0.6.512 加速源清单目录化：本文件覆盖 internal/api/mirrors_remote.go 的 7 个函数
// （parseMirrorList / fetchMirrorFile / refreshMirrorLists / WarmMirrorLists /
// mirrorsSync / mirrorsRestore / mirrorsHidden）。全部走本地 httptest，绝不触外网；
// 每个用例结束都把 config 的远端清单与屏蔽集合还原，避免用例间串味。

func resetMirrorGlobals(t *testing.T) {
	t.Helper()
	config.SetRemoteMirrorOptions(nil, nil)
	config.SetHiddenMirrors(nil)
	t.Cleanup(func() {
		config.SetRemoteMirrorOptions(nil, nil)
		config.SetHiddenMirrors(nil)
	})
}

func newMirrorsTestServer(t *testing.T, listBase string) *Server {
	t.Helper()
	t.Setenv("MOO_DATA", t.TempDir())
	resetMirrorGlobals(t)
	return &Server{Cfg: &config.Config{ListBaseURL: listBase}}
}

// listSrv 起一个只提供两份清单的本地服务（内容为参数）。
func listSrv(t *testing.T, gh, dk string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/github_mirrors.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(gh))
	})
	mux.HandleFunc("/docker_mirrors.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(dk))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestParseMirrorList_512 一行一条 key|label|url|description；跳过注释/空行/缺列，
// 同 key 只留第一条，description 里的竖线原样保留，BOM 必须剥掉。
func TestParseMirrorList_512(t *testing.T) {
	body := "\ufeff# 注释行\n" +
		"\n" +
		"gh-proxy|GH-Proxy|https://gh.example/|香港节点\n" +
		"hk|HK|https://hk.example/|备注|含竖线\n" +
		"auto|智能||\n" +
		"onlykey\n" +
		"gh-proxy|重复项应被忽略|https://dup.example/\n" +
		"dup|Dup2|https://dup.example/|\n"
	got := parseMirrorList(body)
	if len(got) != 4 {
		t.Fatalf("应解析出 4 条（去重后），实际 %d：%+v", len(got), got)
	}
	if got[0].Key != "gh-proxy" || got[0].Label != "GH-Proxy" ||
		got[0].URL != "https://gh.example/" || got[0].Description != "香港节点" {
		t.Fatalf("首条解析错误：%+v", got[0])
	}
	if got[1].Description != "备注|含竖线" {
		t.Fatalf("description 应保留竖线，实际 %q", got[1].Description)
	}
	if got[2].Key != "auto" || got[2].URL != "" || got[2].Description != "" {
		t.Fatalf("两列条目应允许空 URL/描述：%+v", got[2])
	}
	if got[3].Key != "dup" {
		t.Fatalf("同 key 只留第一条，实际末条 %+v", got[3])
	}
	if n := len(parseMirrorList("# only comments\n\n")); n != 0 {
		t.Fatalf("纯注释应得空列表，实际 %d", n)
	}
}

// TestFetchMirrorFile_512 新路径优先；404 与「不足 2 条」都要报错（不能把残缺清单当成功）。
func TestFetchMirrorFile_512(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/github_mirrors.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("a|A|https://a.example/|\nb|B||\n"))
	})
	mux.HandleFunc("/one.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("a|A|https://a.example/|\n"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	s := newMirrorsTestServer(t, ts.URL)

	opts, err := s.fetchMirrorFile(context.Background(), "github_mirrors.txt")
	if err != nil {
		t.Fatalf("正常清单不应报错：%v", err)
	}
	if len(opts) != 2 || opts[1].Key != "b" || opts[1].URL != "" {
		t.Fatalf("解析结果不符：%+v", opts)
	}
	if _, err := s.fetchMirrorFile(context.Background(), "docker_mirrors.txt"); err == nil {
		t.Fatal("404 清单应报错")
	}
	if _, err := s.fetchMirrorFile(context.Background(), "one.txt"); err == nil ||
		!strings.Contains(err.Error(), "内容为空或格式不符") {
		t.Fatalf("少于 2 条应报格式错误，实际 %v", err)
	}
}

// TestRefreshMirrorLists_512 两份都成功 → 同时生效；一份成功 → 另一份回落内置；
// 两份都失败 → 整体报错且不改动既有远端清单。
func TestRefreshMirrorLists_512(t *testing.T) {
	ghBody := "a|A|https://a.example/|\nb|B||\n"
	dkBody := "d1|D1|https://d1.example/|\nd2|D2||\n"

	t.Run("两份都成功", func(t *testing.T) {
		ts := listSrv(t, ghBody, dkBody)
		s := newMirrorsTestServer(t, ts.URL)
		gh, dk, err := s.refreshMirrorLists(context.Background())
		if err != nil || gh != 2 || dk != 2 {
			t.Fatalf("got (%d,%d,%v)，期望 (2,2,nil)", gh, dk, err)
		}
		rg, rd := config.RemoteMirrorOptions()
		if len(rg) != 2 || len(rd) != 2 || rg[0].Key != "a" || rd[0].Key != "d1" {
			t.Fatalf("远端清单未生效：gh=%+v dk=%+v", rg, rd)
		}
	})

	t.Run("一份成功一份失败", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/github_mirrors.txt", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(ghBody))
		})
		ts := httptest.NewServer(mux) // docker 清单 404
		defer ts.Close()
		s := newMirrorsTestServer(t, ts.URL)
		gh, dk, err := s.refreshMirrorLists(context.Background())
		if err != nil || gh != 2 || dk != 0 {
			t.Fatalf("got (%d,%d,%v)，期望 (2,0,nil)", gh, dk, err)
		}
		rg, rd := config.RemoteMirrorOptions()
		if len(rg) != 2 || len(rd) != 0 {
			t.Fatalf("docker 应回落内置（远端为空）：gh=%d dk=%d", len(rg), len(rd))
		}
	})

	t.Run("两份都失败", func(t *testing.T) {
		ts := httptest.NewServer(http.NotFoundHandler())
		defer ts.Close()
		s := newMirrorsTestServer(t, ts.URL)
		config.SetRemoteMirrorOptions([]config.MirrorOption{{Key: "sentinel"}}, nil)
		if _, _, err := s.refreshMirrorLists(context.Background()); err == nil {
			t.Fatal("两份都失败应报错")
		}
		if rg, _ := config.RemoteMirrorOptions(); len(rg) != 1 || rg[0].Key != "sentinel" {
			t.Fatalf("失败时不应覆盖既有远端清单：%+v", rg)
		}
	})
}

func decodeJSONMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, raw)
	}
	return m
}

func hiddenKeys(t *testing.T, m map[string]any) []string {
	t.Helper()
	arr, ok := m["hidden"].([]any)
	if !ok {
		t.Fatalf("响应缺 hidden 数组：%v", m)
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		out = append(out, v.(string))
	}
	return out
}

// TestMirrorsSyncHandler_512 清单刷新成功 → 200 + 两份条数；清单不可达 → 502。
func TestMirrorsSyncHandler_512(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		withGhAssets(t, []string{"assets/probe.bin"})
		payload := strings.Repeat("x", 1600*1024)
		mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v2/") {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write([]byte(payload))
		}))
		defer mirror.Close()
		line := func(k, l string) string { return k + "|" + l + "|" + mirror.URL + "/|\n" }
		ts := listSrv(t, line("m1", "本地1")+line("m2", "本地2"), line("d1", "本地1")+line("d2", "本地2"))
		s := newMirrorsTestServer(t, ts.URL)
		s.Mirrors = NewMirrorMonitor()
		w := httptest.NewRecorder()
		s.mirrorsSync(w, httptest.NewRequest(http.MethodPost, "/api/mirrors/sync", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 %d，响应 %s", w.Code, w.Body.String())
		}
		m := decodeJSONMap(t, w.Body.Bytes())
		if m["ok"] != true || m["github"].(float64) != 2 || m["docker"].(float64) != 2 {
			t.Fatalf("响应不符：%v", m)
		}
	})

	t.Run("清单不可达", func(t *testing.T) {
		ts := httptest.NewServer(http.NotFoundHandler())
		defer ts.Close()
		s := newMirrorsTestServer(t, ts.URL)
		s.Mirrors = NewMirrorMonitor()
		w := httptest.NewRecorder()
		s.mirrorsSync(w, httptest.NewRequest(http.MethodPost, "/api/mirrors/sync", nil))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("状态码 %d，期望 502（%s）", w.Code, w.Body.String())
		}
	})
}

// TestMirrorsRestoreHandler_512 「恢复」清空本机屏蔽并重拉清单；清单失败也宣告
// restored=true，但要回 ok=false + error，且屏蔽同样已清空。
func TestMirrorsRestoreHandler_512(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		ts := listSrv(t, "a|A|https://a.example/|\nb|B||\n", "d1|D1||\nd2|D2||\n")
		s := newMirrorsTestServer(t, ts.URL)
		s.Cfg.HiddenMirrors = []string{"a", "b"}
		config.SetHiddenMirrors([]string{"a", "b"})
		w := httptest.NewRecorder()
		s.mirrorsRestore(w, httptest.NewRequest(http.MethodPost, "/api/mirrors/restore", nil))
		m := decodeJSONMap(t, w.Body.Bytes())
		if m["ok"] != true || m["github"].(float64) != 2 || m["docker"].(float64) != 2 {
			t.Fatalf("响应不符：%v", m)
		}
		if s.Cfg.HiddenMirrors != nil || len(config.HiddenMirrors()) != 0 {
			t.Fatalf("屏蔽未清空：cfg=%v runtime=%v", s.Cfg.HiddenMirrors, config.HiddenMirrors())
		}
		raw, err := os.ReadFile(config.DataDir() + "/config.json")
		if err != nil {
			t.Fatalf("配置未落盘：%v", err)
		}
		if strings.Contains(string(raw), "hidden_mirrors") {
			t.Fatalf("落盘配置仍含 hidden_mirrors：%s", raw)
		}
	})

	t.Run("清单失败", func(t *testing.T) {
		ts := httptest.NewServer(http.NotFoundHandler())
		defer ts.Close()
		s := newMirrorsTestServer(t, ts.URL)
		s.Cfg.HiddenMirrors = []string{"a"}
		config.SetHiddenMirrors([]string{"a"})
		w := httptest.NewRecorder()
		s.mirrorsRestore(w, httptest.NewRequest(http.MethodPost, "/api/mirrors/restore", nil))
		m := decodeJSONMap(t, w.Body.Bytes())
		if m["ok"] != false || m["restored"] != true || m["error"] == nil || m["error"] == "" {
			t.Fatalf("响应不符：%v", m)
		}
		if s.Cfg.HiddenMirrors != nil || len(config.HiddenMirrors()) != 0 {
			t.Fatal("清单失败时屏蔽也应已清空")
		}
	})
}

// TestMirrorsHiddenHandler_512 keys 全量覆盖 / add 追加 / remove 移除；
// auto、custom、direct 永不可屏蔽；坏 JSON → 400。
func TestMirrorsHiddenHandler_512(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	s := newMirrorsTestServer(t, ts.URL)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.mirrorsHidden(w, httptest.NewRequest(http.MethodPost, "/api/mirrors/hidden", strings.NewReader(body)))
		return w
	}
	has := func(keys []string, want ...string) bool {
		set := map[string]bool{}
		for _, k := range keys {
			set[k] = true
		}
		for _, k := range want {
			if !set[k] {
				return false
			}
		}
		return len(set) == len(want)
	}

	m := decodeJSONMap(t, post(`{"keys":["a","auto","custom","direct","b"]}`).Body.Bytes())
	if keys := hiddenKeys(t, m); !has(keys, "a", "b") {
		t.Fatalf("keys 覆盖 + 特殊项过滤不符：%v", keys)
	}
	m = decodeJSONMap(t, post(`{"add":["c"]}`).Body.Bytes())
	if keys := hiddenKeys(t, m); !has(keys, "a", "b", "c") {
		t.Fatalf("add 追加不符：%v", keys)
	}
	m = decodeJSONMap(t, post(`{"remove":["a"]}`).Body.Bytes())
	if keys := hiddenKeys(t, m); !has(keys, "b", "c") {
		t.Fatalf("remove 移除不符：%v", keys)
	}
	m = decodeJSONMap(t, post(`{"keys":[]}`).Body.Bytes())
	if keys := hiddenKeys(t, m); len(keys) != 0 {
		t.Fatalf("空 keys 应全部恢复显示：%v", keys)
	}
	if w := post(`{bad json`); w.Code != http.StatusBadRequest {
		t.Fatalf("坏 JSON 应 400，实际 %d", w.Code)
	}
}

// TestWarmMirrorLists_512 启动预热在后台拉取并生效（本地服务，3s 内应完成）。
func TestWarmMirrorLists_512(t *testing.T) {
	ts := listSrv(t, "a|A||\nb|B||\n", "d1|D1||\nd2|D2||\n")
	s := newMirrorsTestServer(t, ts.URL)
	s.WarmMirrorLists()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		gh, dk := config.RemoteMirrorOptions()
		if len(gh) == 2 && len(dk) == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("WarmMirrorLists 3s 内未生效")
}
