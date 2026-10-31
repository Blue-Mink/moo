package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 内置索引的存在性 + 可解析性：快照坏了要能立刻发现（否则新装就是空列表）。
func TestDefaultIndexSnapshotParses(t *testing.T) {
	d, err := decodeDefaultIndex()
	if err != nil {
		t.Fatalf("内置索引解压/解析失败: %v", err)
	}
	if len(d.Apps) < 100 {
		t.Fatalf("内置索引条数异常: %d（应数百条）", len(d.Apps))
	}
	for name, a := range d.Apps {
		if a == nil || a.Appname == "" {
			t.Fatalf("条目 %q 缺 appname", name)
		}
		break
	}
	t.Logf("内置索引: %d 条", len(d.Apps))
}

// 空目录（等同于全新安装）→ 写入快照，且能被 index 读回。
func TestBootstrapWritesOnMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	n, err := bootstrapDefaultIndex(p)
	if err != nil {
		t.Fatalf("bootstrap 失败: %v", err)
	}
	want := defaultSeedApps()
	if n != want || n == 0 {
		t.Fatalf("写入条数 %d != 快照 %d", n, want)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("索引文件未生成: %v", err)
	}
	ix := newIndex(p) // newIndex 内会再调一次 bootstrap，但文件已存在应跳过
	if got := len(ix.list()); got != want {
		t.Fatalf("读回条数 %d != %d", got, want)
	}
}

// 已有数据（哪怕是空文件）→ 绝不覆盖。这是「升级不动用户数据」的硬边界。
func TestBootstrapKeepsExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	original := `{"version":1,"apps":{"only-one":{"appname":"only-one"}}}`
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := bootstrapDefaultIndex(p)
	if err != nil {
		t.Fatalf("bootstrap 报错: %v", err)
	}
	if n != 0 {
		t.Fatalf("已有文件时不应写入，实际写 %d 条", n)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("已有索引被改动:\n%s", got)
	}
	ix := newIndex(p)
	if len(ix.list()) != 1 {
		t.Fatalf("读回应为 1 条，实际 %d", len(ix.list()))
	}
}

// 空文件同样视为「存在」→ 不覆盖（避免把用户手动清空的索引重新灌满）。
func TestBootstrapKeepsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := bootstrapDefaultIndex(p)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("空文件也应跳过，实际写入 %d", n)
	}
	if st, _ := os.Stat(p); st.Size() != 0 {
		t.Fatalf("空文件被写入内容（%d B）", st.Size())
	}
}
