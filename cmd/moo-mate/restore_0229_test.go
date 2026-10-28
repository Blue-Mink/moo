package main

// restore_0229_test.go —— 0.2.29：恢复备份 / 数据目录切换的安全与正确性。
//
// 钉住：
//  ① 合法备份（根层 index.json）→ 覆盖索引 + 热加载；
//  ② 无 index.json / 非法 JSON / 非 gzip → 拒绝且不动现有索引；
//  ③ 备份文件名白名单（防目录穿越）；
//  ④ copyDir 迁移 backups 后内容一致。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func makeTarGz(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestRestoreIndexFromTar(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")
	if err := os.WriteFile(idxPath, []byte(`{"version":1,"apps":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &companion{idx: newIndex(idxPath), hosted: map[string][]byte{}}

	backup := makeTarGz(t, map[string]string{
		"config.json":     `{"sync_interval":"1h"}`,
		"index.json":      `{"version":1,"apps":{"demo":{"appname":"demo","display_name":"Demo","version":"1.0.0","download_url":"https://example.com/demo.fpk"}}}`,
		"../evil.json":    `{"x":1}`,
		"backups/old.tar": "ignored",
	})
	n, err := c.restoreIndexFromTar(backup)
	if err != nil {
		t.Fatalf("合法备份应恢复成功: %v", err)
	}
	if n != 1 {
		t.Fatalf("应恢复 1 个应用, got %d", n)
	}
	if got := c.idx.get("demo"); got == nil || got.Version != "1.0.0" {
		t.Fatalf("索引未热加载: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.json")); err == nil {
		t.Fatal("备份里的 ../evil.json 不应被写出")
	}
}

func TestRestoreRejectsBadBackups(t *testing.T) {
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.json")
	orig := `{"version":1,"apps":{"keep":{"appname":"keep","version":"1"}}}`
	if err := os.WriteFile(idxPath, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &companion{idx: newIndex(idxPath), hosted: map[string][]byte{}}

	cases := map[string]*bytes.Buffer{
		"无 index.json": makeTarGz(t, map[string]string{"config.json": `{}`}),
		"非法 JSON":      makeTarGz(t, map[string]string{"index.json": `{bad`}),
		"非 gzip":       bytes.NewBufferString("not a gzip"),
	}
	for name, r := range cases {
		if _, err := c.restoreIndexFromTar(r); err == nil {
			t.Fatalf("%s 应报错", name)
		}
		if got := c.idx.get("keep"); got == nil {
			t.Fatalf("%s 失败后不应破坏现有索引", name)
		}
	}
}

func TestBackupFileNameWhitelist(t *testing.T) {
	ok := []string{"moo-mate-backup-20261028-080000.tar.gz", "x.tgz"}
	for _, n := range ok {
		if !isBackupFileName(n) {
			t.Fatalf("%q 应被接受", n)
		}
	}
	bad := []string{"", "../index.json", "a/b.tar.gz", "x.zip", "..tar.gz"}
	for _, n := range bad {
		if isBackupFileName(n) {
			t.Fatalf("%q 应被拒绝", n)
		}
	}
}

func TestCopyDirMigratesBackups(t *testing.T) {
	src := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.tar.gz"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "backups")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copyDir: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "a.tar.gz"))
	if err != nil || string(b) != "abc" {
		t.Fatalf("备份文件未正确迁移: %v %q", err, b)
	}
}

func TestBackupNameTime(t *testing.T) {
	at, ok := backupNameTime("moo-mate-backup-20261022-141235.tar.gz")
	if !ok || at.Format("2006-01-02 15:04:05") != "2026-10-22 14:12:35" {
		t.Fatalf("文件名时间解析错误: %v %v", at, ok)
	}
	if _, ok := backupNameTime("other.tar.gz"); ok {
		t.Fatal("非标准备份名不应解析出时间")
	}
}
