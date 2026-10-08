package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"moo/internal/config"
	"moo/internal/source"
)

// TestDetailStore_Roundtrip F8 磁盘层 roundtrip：Put → 内存命中；Flush 后
// 新实例（冷启动新进程）→ 磁盘命中；不存在的版本键 miss。
func TestDetailStore_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	st := newDetailStore(dir)
	key := detailKey("src", "demo", "1.2.3")
	p := &detailPayload{
		Cl:      "1.2.3: 修复；1.2.2: 初始",
		Rel:     map[string]string{"1.2.3": "修复"},
		Entries: []ChangelogEntry{{Version: "1.2.3", Text: "修复"}, {Version: "1.2.2", Text: "初始"}},
	}
	st.Put(key, p)
	got, ok := st.Get(key)
	if !ok || got.Cl != p.Cl || got.Rel["1.2.3"] != "修复" {
		t.Fatalf("内存层命中失败: %+v", got)
	}
	st.Flush()
	st2 := newDetailStore(dir)
	got2, ok2 := st2.Get(key)
	if !ok2 || got2.Cl != p.Cl || len(got2.Entries) != 2 {
		t.Fatalf("磁盘层命中失败: %+v", got2)
	}
	if _, ok3 := st2.Get(detailKey("src", "demo", "9.9.9")); ok3 {
		t.Error("不存在的版本键应 miss")
	}
}

// TestDetailStore_LRUEvict F8 容量：磁盘层超 diskMax 按 ts 升序淘汰（最旧
// 先走，文件删除）；内存层超 memMax 保留最近一半。
func TestDetailStore_LRUEvict(t *testing.T) {
	dir := t.TempDir()
	st := newDetailStore(dir)
	st.diskMax = 2
	st.memMax = 4
	for _, v := range []string{"a", "b", "c"} {
		st.Put(detailKey("s", v, "1.0.0"), &detailPayload{Cl: v})
		st.Flush() // 让 ts 拉开（同毫秒内也按插入序稳定淘汰）
	}
	if len(st.index) != 2 {
		t.Fatalf("磁盘层应淘汰到 diskMax=2, got %d", len(st.index))
	}
	// 直接断言磁盘索引（内存层仍留有 a——两级独立容量）
	if _, ok := st.index[detailKey("s", "a", "1.0.0")]; ok {
		t.Error("最旧的 a 磁盘条目应被淘汰")
	}
	if _, ok := st.index[detailKey("s", "c", "1.0.0")]; !ok {
		t.Error("最新的 c 磁盘条目应保留")
	}
	st.mem = map[string]*detailMemEntry{} // 清掉磁盘阶段条目，单独测内存淘汰
	for i, v := range []string{"m1", "m2", "m3", "m4", "m5"} {
		st.mem[detailKey("s", v, "2.0.0")] = &detailMemEntry{
			payload: &detailPayload{Cl: v},
			expires: time.Now().Add(time.Duration(10+i) * time.Minute),
		}
	}
	st.evictMemLocked()
	if len(st.mem) != st.memMax/2 {
		t.Fatalf("内存层应保留 memMax/2=%d, got %d", st.memMax/2, len(st.mem))
	}
	if _, ok := st.mem[detailKey("s", "m5", "2.0.0")]; !ok {
		t.Error("expires 最新的 m5 应保留")
	}
}

// TestDetailStoreSink_StripsAndLazyLoads F8 主链路：源同步触发钩子 →
// 重字段从常驻内存剥离 + 写入磁盘层（含 F7② 预解析条目）；目录卡不携带
// changelog 全文；详情接口懒载回填。
func TestDetailStoreSink_StripsAndLazyLoads(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	mux := http.NewServeMux()
	mux.HandleFunc("/fnpack.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"demo":{"version":"1.0.0","display_name":"演示","changelog":"1.0.0: 首次发布","download_url":"https://example.com/demo.fpk"}}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := &config.Config{}
	m := source.NewManager(cfg)
	s := &Server{Src: m, Cfg: cfg}
	m.OnFetched = s.DetailStoreSink
	if _, err := m.AddSource("tst", ts.URL+"/fnpack.json"); err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	// ① 内存已剥离
	a := m.Get("tst", "demo")
	if a == nil || a.Changelog != "" {
		t.Fatalf("同步后 changelog 应剥离, got %q", a.Changelog)
	}
	// ② 目录卡不携带 changelog 全文/条目（预解析在磁盘层）
	c := s.cachedCatalog("zh-CN")
	i := -1
	for j := range c {
		if c[j].AppName == "demo" {
			i = j
		}
	}
	if i < 0 {
		t.Fatal("目录应含 demo")
	}
	if c[i].Changelog != "" || len(c[i].ChangelogEntries) != 0 {
		t.Fatalf("目录卡不应保留 changelog 全文: %+v", c[i].Changelog)
	}
	// ③ 详情接口懒载回填（changelog + 预解析条目）
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET /api/apps/{key}", s.appDetail)
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps/demo", nil).WithContext(t.Context()))
	if rec.Code != 200 {
		t.Fatalf("detail %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"changelog":"1.0.0: 首次发布"`, `"changelog_entries"`, `"version":"1.0.0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("详情应懒载 changelog（缺 %q）: %s", want, body)
		}
	}
	// ④ 详情磁盘层文件已落盘（重启可恢复）
	st := s.detailStore()
	st.Flush()
	if fi, err := os.Stat(st.dir); err != nil || !fi.IsDir() {
		t.Errorf("详情磁盘层目录应存在: %v", err)
	}
}

// TestAppDetail_LazyFallback F8 过渡期回落：应用 changelog 仍在常驻内存
// （升级后恢复的旧源缓存快照，OnFetched 尚未重新剥离）而详情磁盘层无条目
// → appDetail 现解析一次补齐条目（F7 把解析移出重建热路径后，这是唯一
// 的非预解析入口）。
func TestAppDetail_LazyFallback(t *testing.T) {
	t.Setenv("MOO_DATA", t.TempDir())
	cfg := &config.Config{Sources: []config.SourceRef{
		{Name: "x", URL: "https://example.com/x/fnpack.json"},
	}}
	m := source.NewManager(cfg)
	m.SeedCacheForTest(map[string]map[string]*source.App{
		"x": {"demo": {Name: "demo", Source: "x", Version: "2.0.0", DisplayName: "演示",
			Changelog: "2.0.0: 新增功能A"}},
	})
	s := &Server{Src: m, Cfg: cfg} // 不挂 OnFetched → 不剥离、不写磁盘层
	if p, ok := s.detailStore().Get(detailKey("x", "demo", "2.0.0")); ok {
		t.Fatalf("前提：详情磁盘层应无条目, got %+v", p)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/apps/{key}", s.appDetail)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/apps/demo", nil).WithContext(t.Context()))
	if rec.Code != 200 {
		t.Fatalf("detail %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"changelog":"2.0.0: 新增功能A"`, `"changelog_entries"`, `"2.0.0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("内存回落应现解析 changelog（缺 %q）: %s", want, body)
		}
	}
	// 回落不写磁盘层（保持「源重新同步后才落盘」的单一写入口）
	if p, ok := s.detailStore().Get(detailKey("x", "demo", "2.0.0")); ok {
		t.Errorf("回落路径不应写入磁盘层, got %+v", p)
	}
}
