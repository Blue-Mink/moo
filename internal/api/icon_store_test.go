package api

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"moo/internal/source"
)

var testPNG = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 64)...)

// 内存层读写 + TTL
// 0.6.313 B4：改用临时目录——磁盘层随 Put 直接写盘独立成立，测试不再
// 依赖「dir 空=仅内存层」的旧构造（且对 MOO_ICON_MEM_MAX env 免疫）。
func TestIconStore_MemRoundTrip(t *testing.T) {
	st := newIconStore(t.TempDir())
	st.Put("s@app", testPNG, "image/png")
	b, ct, ok := st.Get("s@app")
	if !ok || ct != "image/png" || len(b) != len(testPNG) {
		t.Fatalf("内存层读回失败: ok=%v ct=%q", ok, ct)
	}
	if _, _, ok := st.Get("nope"); ok {
		t.Fatal("未缓存键不应命中")
	}
}

// 磁盘层：写盘 → 新 store 实例读回（模拟重启）
func TestIconStore_DiskSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.Put("s@app", testPNG, "image/png")
	st.Put("s@app2", testPNG, "image/jpeg")
	st.Flush() // 强制同步落盘

	st2 := newIconStore(dir)
	b, ct, ok := st2.Get("s@app")
	if !ok || ct != "image/png" || len(b) != len(testPNG) {
		t.Fatalf("重启后读回失败: ok=%v ct=%q", ok, ct)
	}
	if _, _, ok := st2.Get("s@app2"); !ok {
		t.Fatal("第二个图标未读回")
	}
}

// 负缓存
func TestIconStore_Negative(t *testing.T) {
	st := newIconStore(t.TempDir()) // 0.6.313 B4：统一临时目录
	if st.IsNegative("x") {
		t.Fatal("初始不应为负")
	}
	st.MarkNegative("x")
	if !st.IsNegative("x") {
		t.Fatal("标记后应为负")
	}
}

// 磁盘条目损坏 → Get 拒绝并清理
func TestIconStore_CorruptDiskEntry(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.Put("s@app", testPNG, "image/png")
	st.Flush()
	// 找到落盘文件，写坏它
	f := filepath.Join(dir, diskIconFile("s@app", "image/png"))
	if err := os.WriteFile(f, []byte("not an image at all, longer than 64 bytes to pass length gate"), 0o644); err != nil {
		t.Fatal(err)
	}
	st2 := newIconStore(dir)
	if _, _, ok := st2.Get("s@app"); ok {
		t.Fatal("损坏磁盘条目不应命中")
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatal("损坏条目应被清理")
	}
}

// 负缓存过期（直接改时间）
func TestIconStore_NegativeExpiry(t *testing.T) {
	st := newIconStore(t.TempDir()) // 0.6.313 B4：统一临时目录
	st.neg["x"] = time.Now().Add(-time.Second)
	if st.IsNegative("x") {
		t.Fatal("过期负缓存应失效")
	}
}

// 到顶淘汰：不清空全部，保留最近的 memMax/2（用户热图标不被 warm 连锅端）
// 0.6.313 B4：memMax 跟随 env（默认 iconMemMax=256）；改用临时目录
//（mem 直接操作不受影响，Put 的磁盘写落临时目录）。
func TestIconStore_EvictionKeepsRecent(t *testing.T) {
	st := newIconStore(t.TempDir())
	st.mem = map[string]iconMemEntry{}
	for i := 0; i < iconMemMax; i++ {
		st.mem[kvKey(i)] = iconMemEntry{data: testPNG, ctype: "image/png", expires: time.Now().Add(time.Minute)}
	}
	// 第 1025 个 Put 触发淘汰
	st.Put("hot-app", testPNG, "image/png")
	// 淘汰后：总数应回落到 iconMemMax/2+1 附近，而非 1
	if n := len(st.mem); n > iconMemMax/2+2 || n < iconMemMax/2-2 {
		t.Fatalf("淘汰后内存条目数异常: %d（期望 ~%d）", n, iconMemMax/2)
	}
	if _, ok := st.mem["hot-app"]; !ok {
		t.Fatal("刚写入的热条目不应被淘汰")
	}
	// 过期条目优先清掉
	st.mem = map[string]iconMemEntry{}
	for i := 0; i < iconMemMax; i++ {
		st.mem[kvKey(i)] = iconMemEntry{data: testPNG, ctype: "image/png", expires: time.Now().Add(-time.Minute)}
	}
	st.Put("hot-app2", testPNG, "image/png")
	if n := len(st.mem); n != 1 {
		t.Fatalf("全过期场景应只剩新条目: %d", n)
	}
}

func kvKey(i int) string {
	return fmt.Sprintf("s@app%04d", i)
}

// 0.6.200 图标缓存 key 带版本：应用升级换图标（icon_url 不变、内容更新）
// 时版本变化 → 新 key → 缓存不命中 → 重新抓取，杜绝 7 天磁盘层供旧图标。
func TestIconCacheKey_Versioned(t *testing.T) {
	a1 := &source.App{Name: "demo", Source: "testsrv", Version: "1.0.0"}
	a2 := &source.App{Name: "demo", Source: "testsrv", Version: "1.0.0"}
	a3 := &source.App{Name: "demo", Source: "testsrv", Version: "2.0.0"}
	a4 := &source.App{Name: "demo2", Source: "testsrv", Version: "1.0.0"}
	a5 := &source.App{Name: "demo", Source: "othersrv", Version: "1.0.0"}

	if k1, k2 := iconCacheKey(a1), iconCacheKey(a2); k1 != k2 {
		t.Fatalf("同版本 key 应稳定: %q vs %q", k1, k2)
	}
	if k1, k3 := iconCacheKey(a1), iconCacheKey(a3); k1 == k3 {
		t.Fatalf("版本变化必须换 key: %q", k1)
	}
	if k1, k4 := iconCacheKey(a1), iconCacheKey(a4); k1 == k4 {
		t.Fatalf("不同应用必须换 key: %q", k1)
	}
	if k1, k5 := iconCacheKey(a1), iconCacheKey(a5); k1 == k5 {
		t.Fatalf("不同源必须换 key: %q", k1)
	}
	// 版本为空也稳定（不 panic、不产生尾随歧义）
	e1 := &source.App{Name: "demo", Source: "testsrv"}
	if iconCacheKey(e1) == "" || iconCacheKey(e1) != iconCacheKey(&source.App{Name: "demo", Source: "testsrv"}) {
		t.Fatalf("空版本 key 应稳定非空: %q", iconCacheKey(e1))
	}
}

// 0.6.289 空白墙治本：版本键位移（Source@Name@旧版本 条目在盘、
// Source@Name@新版本 键 miss）时，GetStale 按前缀取 TS 最新的旧图标兜底。
func TestIconStore_GetStalePrefixFallback(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.Put("src@demo@1.0.0", testPNG, "image/png")
	// 模拟版本刷新后的新键（盘上不存在）
	if _, _, ok := st.Get("src@demo@2.0.0"); ok {
		t.Fatal("新键本应 miss")
	}
	b, ct, ok := st.GetStale("src@demo@")
	if !ok || ct != "image/png" || len(b) != len(testPNG) {
		t.Fatalf("前缀回退未命中旧图标: ok=%v ct=%q", ok, ct)
	}
	// 多版本时取 TS 最新
	other := append([]byte{}, testPNG...)
	other[9] = 0x42
	st.Put("src@demo@1.5.0", other, "image/png")
	b2, _, ok2 := st.GetStale("src@demo@")
	if !ok2 || b2[9] != 0x42 {
		t.Fatal("多旧版本应取最新 TS 条目")
	}
	if _, _, ok3 := st.GetStale("nosuch@"); ok3 {
		t.Fatal("无匹配前缀不应命中")
	}
	// 重启后（磁盘索引重建）依然可回退
	st.Flush()
	st2 := newIconStore(dir)
	if _, _, ok4 := st2.GetStale("src@demo@"); !ok4 {
		t.Fatal("重启后前缀回退失效")
	}
}

// iconCacheKey 与预热键一致性（Has 用真实键才有效）
func TestIconWarmKeyMatchesCacheKey(t *testing.T) {
	a := &source.App{Name: "demo", Source: "src", Version: "1.2.3"}
	if got := iconCacheKey(a); got != "src@demo@1.2.3" {
		t.Fatalf("iconCacheKey=%q", got)
	}
	st := newIconStore(t.TempDir())
	st.Put(iconCacheKey(a), testPNG, "image/png")
	if !st.Has(iconCacheKey(a)) {
		t.Fatal("Has 应命中真实缓存键")
	}
}
