package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 0.2.33 修复：自动淘汰只处理「本程序自己命名的备份」，
// 用户手工放进备份目录的无关 tar.gz 一份都不能删（反向验证见注释）。
func TestPruneBackupsKeepsForeignArchives(t *testing.T) {
	dir := t.TempDir()
	// 25 份自动备份（名字可解析出时间戳）
	for i := 1; i <= 25; i++ {
		name := fmt.Sprintf("moo-mate-backup-202601%02d-000000.tar.gz", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 2 份用户归档：mtime 设得很旧，若淘汰池口径不对（按后缀认 + mtime 排序）会最先被删
	old := time.Unix(1000, 0)
	foreign := []string{"family-photos.tar.gz", "other-app.tgz"}
	for _, n := range foreign {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	c := &companion{hosted: map[string][]byte{}, cfg: &mateConfig{BackupDir: dir}}
	removed := c.pruneBackups()
	if removed != 5 {
		t.Fatalf("应淘汰 5 份自动备份，实际 %d", removed)
	}
	for _, n := range foreign {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("用户归档 %s 被误删: %v", n, err)
		}
	}
	if got := len(c.autoBackupNames()); got != 20 {
		t.Fatalf("自动备份应剩 20 份，实际 %d", got)
	}
}

// 备份目录里只有用户归档时，淘汰一个都不能动。
func TestPruneBackupsNoAutoBackups(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("archive-%02d.tar.gz", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := &companion{hosted: map[string][]byte{}, cfg: &mateConfig{BackupDir: dir}}
	if removed := c.pruneBackups(); removed != 0 {
		t.Fatalf("非自动备份不应被删，实际删了 %d 份", removed)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 30 {
		t.Fatalf("文件数应为 30，实际 %d", len(entries))
	}
}
