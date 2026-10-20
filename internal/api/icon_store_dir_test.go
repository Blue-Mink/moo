package api

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePNG 造 len 字节的假图标（带 PNG 魔数，满足 Get 的 looksLikeImage 校验）。
func fakePNG(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	for i := 8; i < n; i++ {
		b[i] = byte(i)
	}
	return b
}

// 0.6.332 F1（全量测试 P1-1）：cache/icons 不存在时 Put 必须自建目录并落盘。
// 修复前 Put 直接 os.WriteFile → ENOENT → 提前 return，磁盘层永久自锁。
func TestIconStorePutCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "icons") // 父目录在、icons 不在
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("前置条件失败：%s 应不存在（err=%v）", dir, err)
	}
	st := newIconStore(dir)
	defer st.Stop()

	key := "demo@src"
	data := fakePNG(200)
	st.Put(key, data, "image/png")

	ref, ok := st.index[key]
	if !ok {
		t.Fatalf("Put 后索引未更新（磁盘条目缺失）")
	}
	p := filepath.Join(dir, ref.File)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("Put 后磁盘文件不存在: %v（MkdirAll 未生效）", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("磁盘字节与写入不一致: got %d B want %d B", len(got), len(data))
	}

	// 落盘后新进程（新 store）应能读到 index.json + 文件
	st.Flush()
	st2 := newIconStore(dir)
	defer st2.Stop()
	b, ct, ok := st2.Get(key)
	if !ok || !bytes.Equal(b, data) || ct != "image/png" {
		t.Fatalf("新 store 磁盘层未命中: ok=%v ct=%q len=%d", ok, ct, len(b))
	}
}

// healEntry 同样要自建目录：运行期目录被回收（cleanAppCacheWithDays）后
// 降采样自愈写回不能失败。
func TestIconStoreHealEntryCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache", "icons")
	st := newIconStore(dir)
	defer st.Stop()

	key := "demo@src"
	st.Put(key, fakePNG(200), "image/png")
	ref, ok := st.index[key]
	if !ok {
		t.Fatalf("Put 后索引未更新")
	}

	// 模拟运行期目录被清空（回收空目录）
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("清理目录失败: %v", err)
	}

	small := fakePNG(120)
	st.healEntry(key, small, "image/png")

	got, err := os.ReadFile(filepath.Join(dir, ref.File))
	if err != nil {
		t.Fatalf("healEntry 后磁盘文件不存在: %v（MkdirAll 未生效）", err)
	}
	if !bytes.Equal(got, small) {
		t.Fatalf("healEntry 写入字节不一致: got %d B want %d B", len(got), len(small))
	}
}

// 落盘失败日志必须限流（同一 store 每 iconDiskErrEvery 最多 1 条），
// 否则目录不可写/磁盘满时每个图标刷一行（实测单轮 2699 行）。
func TestIconStoreDiskErrLogRateLimited(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocked")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("造前置文件失败: %v", err)
	}
	dir := filepath.Join(blocker, "icons") // 父路径是普通文件 → MkdirAll 恒失败
	st := newIconStore(dir)
	defer st.Stop()

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	for _, k := range []string{"a@1", "b@1", "c@1"} {
		st.Put(k, fakePNG(80), "image/png")
	}
	if n := strings.Count(buf.String(), "图标落盘失败"); n != 1 {
		t.Fatalf("同一窗口内应只打 1 条落盘失败日志，实际 %d 条:\n%s", n, buf.String())
	}

	// 窗口过期后再失败 → 允许新 1 条，并回报刚才被省略的次数
	buf.Reset()
	st.failMu.Lock()
	st.failLast = time.Now().Add(-2 * iconDiskErrEvery)
	st.failMu.Unlock()
	st.Put("d@1", fakePNG(80), "image/png")
	out := buf.String()
	if n := strings.Count(out, "图标落盘失败"); n != 1 {
		t.Fatalf("新窗口应只打 1 条，实际 %d 条:\n%s", n, out)
	}
	if !strings.Contains(out, "另有 2 次") {
		t.Fatalf("聚合行应带「另有 2 次同类失败已省略」，实际:\n%s", out)
	}
}
