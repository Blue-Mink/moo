package api

// 0.6.313 B4 单测：图标内存层 env 化 + Put 直接写盘（纯磁盘层独立成立）。
// 配套文件：catalog_06313_test.go、cmd/server/memlimit_06313_test.go。

import (
	"bytes"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// bigNoisePNG 生成 ≥32KB、最长边 512px 的噪声 PNG（降采样自愈测试用：
// 随机噪声不可压缩，重编码到 256px 后必然变小 → changed=true 确定）。
func bigNoisePNG(t *testing.T) []byte {
	t.Helper()
	w, h := 512, 512
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(42))
	for i := range img.Pix {
		img.Pix[i] = uint8(rnd.Intn(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if buf.Len() < iconDownsampleMin {
		t.Fatalf("测试图应 ≥%d 字节，实际 %d", iconDownsampleMin, buf.Len())
	}
	return buf.Bytes()
}

// Put 直接 os.WriteFile：文件存在且字节一致 + index 同步更新（格式零变化）。
func TestIconStore_06313_PutWritesDiskDirectly(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.memMax = 0 // 纯磁盘，隔离 mem 路径
	st.Put("s@app", testPNG, "image/png")

	f := filepath.Join(dir, diskIconFile("s@app", "image/png"))
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("Put 后磁盘文件应存在: %v", err)
	}
	if !bytes.Equal(b, testPNG) {
		t.Fatal("磁盘字节应与 Put 入参一致")
	}
	st.mu.Lock()
	r, ok := st.index["s@app"]
	st.mu.Unlock()
	if !ok || r.File != diskIconFile("s@app", "image/png") || r.CT != "image/png" {
		t.Fatalf("index 条目不正确: %+v (ok=%v)", r, ok)
	}
}

// 内存层禁用（memMax=0）：Put 不写 mem、Get 磁盘命中且不回灌、Has 走 index。
func TestIconStore_06313_MemDisabledDiskOnly(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.memMax = 0
	st.Put("s@app", testPNG, "image/png")
	if len(st.mem) != 0 {
		t.Fatal("内存层禁用时 Put 不应写 mem")
	}
	b, ct, ok := st.Get("s@app")
	if !ok || ct != "image/png" || !bytes.Equal(b, testPNG) {
		t.Fatalf("内存层禁用时 Get 应磁盘命中: ok=%v ct=%q", ok, ct)
	}
	if len(st.mem) != 0 {
		t.Fatal("内存层禁用时 Get 不应回灌 mem")
	}
	if !st.Has("s@app") {
		t.Fatal("Has 应经 index 命中")
	}
	if st.Has("s@nope") {
		t.Fatal("不存在键不应命中")
	}
}

// MOO_ICON_MEM_MAX 解析：0=禁用 / 1024=旧行为 / 非法回退默认。
func TestIconStore_06313_MemMaxEnv(t *testing.T) {
	t.Setenv("MOO_ICON_MEM_MAX", "0")
	if st := newIconStore(t.TempDir()); st.memMax != 0 {
		t.Fatalf("env 0 应禁用内存层，实际 %d", st.memMax)
	}
	t.Setenv("MOO_ICON_MEM_MAX", "1024")
	if st := newIconStore(t.TempDir()); st.memMax != 1024 {
		t.Fatalf("env 1024 应回到旧行为，实际 %d", st.memMax)
	}
	t.Setenv("MOO_ICON_MEM_MAX", "junk")
	if st := newIconStore(t.TempDir()); st.memMax != iconMemMax {
		t.Fatalf("非法值应回退默认 %d，实际 %d", iconMemMax, st.memMax)
	}
}

// 超 2000 淘汰的文件↔索引配对（B4 关注点：文件删与索引删必须配对，
// 无孤儿文件=无磁盘泄漏；cleanupOrphans 兜底之外的主动验证）。
// 注：淘汰排序是 0.6.312 既有的「右到左插入排序」（并非严格 LRU，
// 淘汰哪几条依赖 map 迭代序，属遗留行为、不在本批次范围）——故只断言
// 不变量：条数回到 2000 + 盘上每个文件都被 index 引用。
func TestIconStore_06313_EvictionPairsFilesAndIndex(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.memMax = 0
	// 3 个真实文件 + 1999 个假条目（盘上无文件）= 2002 > 2000
	for i := 0; i < 3; i++ {
		st.Put(kvKey(i), testPNG, "image/png")
	}
	now := time.Now().Unix()
	for i := 0; i < 1999; i++ {
		k := "fake@" + kvKey(i)
		st.index[k] = iconDiskRef{File: diskIconFile(k, "image/png"), CT: "image/png", TS: now - int64(20000 - i)}
	}
	st.Put("real-new", testPNG, "image/png")
	if n := len(st.index); n != iconDiskMax {
		t.Fatalf("淘汰后 index 应 %d 条，实际 %d", iconDiskMax, n)
	}
	// 不变量：盘上每个图标文件都被 index 引用（淘汰=文件+索引成对删除）
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fileCount := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "index.json") {
			continue
		}
		fileCount++
		inIdx := false
		for _, r := range st.index {
			if r.File == e.Name() {
				inIdx = true
				break
			}
		}
		if !inIdx {
			t.Fatalf("孤儿文件（不在 index）: %s", e.Name())
		}
	}
	if fileCount > 4 {
		t.Fatalf("真实文件应 ≤4（3 旧 + 1 新，淘汰可能命中真实条目），实际 %d", fileCount)
	}
	// 反向不变量：真实键若在 index 里，其文件必须在盘上（索引不指向不存在的文件）
	for _, k := range append([]string{"real-new"}, kvKey(0), kvKey(1), kvKey(2)) {
		if r, ok := st.index[k]; ok {
			if _, err := os.Stat(filepath.Join(dir, r.File)); err != nil {
				t.Fatalf("index 指向不存在的文件: %s → %s", k, r.File)
			}
		}
	}
}

// GetStale 纯磁盘前缀扫描（memMax=0 跳过 mem 扫描），TS 最新者胜出。
func TestIconStore_06313_GetStaleDiskOnly(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.memMax = 0
	b1 := append([]byte{}, testPNG...)
	b1[9] = 0x11
	b2 := append([]byte{}, testPNG...)
	b2[9] = 0x22
	st.Put("src@demo@1.0.0", b1, "image/png")
	st.Put("src@demo@1.5.0", b2, "image/png")
	// 同秒 Put 的 TS 相等会不确定 → 手工拉开 TS
	st.mu.Lock()
	now := time.Now().Unix()
	r1 := st.index["src@demo@1.0.0"]
	r1.TS = now - 10
	r2 := st.index["src@demo@1.5.0"]
	r2.TS = now - 5
	st.index["src@demo@1.0.0"] = r1
	st.index["src@demo@1.5.0"] = r2
	st.mu.Unlock()

	b, ct, ok := st.GetStale("src@demo@")
	if !ok || ct != "image/png" || b[9] != 0x22 {
		t.Fatalf("前缀扫描应取 TS 最新的旧条目: ok=%v", ok)
	}
	if _, _, ok := st.GetStale("nosuch@"); ok {
		t.Fatal("无匹配前缀不应命中")
	}
	// 重启后（磁盘索引重建）依然可回退
	st.Flush()
	st2 := newIconStore(dir)
	st2.memMax = 0
	if _, _, ok := st2.GetStale("src@demo@"); !ok {
		t.Fatal("重启后前缀回退应仍生效")
	}
}

// healEntry 纯磁盘版：大图标读时降采样 + 新文件落盘 + index 更新（幂等）。
func TestIconStore_06313_HealEntryDisk(t *testing.T) {
	dir := t.TempDir()
	st := newIconStore(dir)
	st.memMax = 0
	big := bigNoisePNG(t)
	key := "src@big@1.0.0"
	st.Put(key, big, "image/png")

	// 读时自愈：降采样后返回小图
	b1, ct1, ok := st.Get(key)
	if !ok {
		t.Fatal("Get 应命中")
	}
	if ct1 != "image/png" || len(b1) >= len(big) {
		t.Fatalf("大图标应降采样返回: %d → %d", len(big), len(b1))
	}
	// 磁盘文件已重写为降采样字节
	f := filepath.Join(dir, diskIconFile(key, "image/png"))
	disk, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, b1) {
		t.Fatal("healEntry 应把降采样字节落盘")
	}
	// 二次读直接命中小图（幂等，不再触发降采样）
	b2, _, ok := st.Get(key)
	if !ok || !bytes.Equal(b2, b1) {
		t.Fatal("二次读应与首次一致（幂等）")
	}
}
