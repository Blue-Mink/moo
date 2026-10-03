package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 内存层读写
func TestReadmeStore_MemRoundTrip(t *testing.T) {
	st := newReadmeStore("")
	st.Put("readme|u1", []byte("# hello"), "text/markdown")
	b, ct, ok := st.Get("readme|u1")
	if !ok || ct != "text/markdown" || string(b) != "# hello" {
		t.Fatalf("内存层读回失败: ok=%v ct=%q", ok, ct)
	}
}

// 磁盘层跨实例（模拟重启）
func TestReadmeStore_DiskSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := newReadmeStore(dir)
	st.Put("readme|u1", []byte("# body one"), "text/markdown")
	st.Put("readme|u2", []byte("# body two"), "text/plain")
	st.Flush()

	st2 := newReadmeStore(dir)
	b, ct, ok := st2.Get("readme|u1")
	if !ok || string(b) != "# body one" || ct != "text/markdown" {
		t.Fatalf("重启后读回失败: ok=%v", ok)
	}
	if _, _, ok := st2.Get("readme|u2"); !ok {
		t.Fatal("第二个 README 未读回")
	}
}

// 负缓存
func TestReadmeStore_Negative(t *testing.T) {
	st := newReadmeStore("")
	st.MarkNegative("readme|dead")
	if !st.IsNegative("readme|dead") {
		t.Fatal("标记后应为负")
	}
	st.neg["readme|dead"] = time.Now().Add(-time.Second)
	if st.IsNegative("readme|dead") {
		t.Fatal("过期负缓存应失效")
	}
}

// 磁盘条目损坏 → Get 拒绝并清理
func TestReadmeStore_CorruptDiskEntry(t *testing.T) {
	dir := t.TempDir()
	st := newReadmeStore(dir)
	st.Put("readme|u1", []byte("# real"), "text/markdown")
	st.Flush()
	p := filepath.Join(dir, "index.json")
	_ = os.Remove(p)
	// 手动构造：索引在但文件是空
	st2 := newReadmeStore(dir)
	st2.mu.Lock()
	st2.index["readme|u1"] = readmeDiskRef{File: "missing.md", CT: "text/markdown", TS: time.Now().Unix()}
	st2.mu.Unlock()
	if _, _, ok := st2.Get("readme|u1"); ok {
		t.Fatal("缺失文件条目不应命中")
	}
}

// 到顶淘汰：不清空全部
func TestReadmeStore_EvictionKeepsRecent(t *testing.T) {
	st := newReadmeStore("")
	st.mem = map[string]readmeMemEntry{}
	for i := 0; i < readmeMemMax; i++ {
		st.mem[kvKey(i)] = readmeMemEntry{body: []byte("x"), ct: "text/markdown", expires: time.Now().Add(time.Minute)}
	}
	st.Put("hot", []byte("y"), "text/markdown")
	if n := len(st.mem); n > readmeMemMax/2+2 || n < readmeMemMax/2-2 {
		t.Fatalf("淘汰后条目数异常: %d", n)
	}
	if _, _, ok := st.Get("hot"); !ok {
		t.Fatal("新写入条目不应被淘汰")
	}
}
