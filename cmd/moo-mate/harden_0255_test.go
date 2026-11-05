package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// F2（0.2.55）：/api/index 顺序必须确定且与插入顺序无关。
// 旧实现直接迭代 map → 每次响应字节不同（同长度不同 md5）。
func TestIndexListSortedStable_0255(t *testing.T) {
	ix := newIndex(filepath.Join(t.TempDir(), "index.json"))
	names := []string{"zeta.app", "alpha.app", "mike.app", "beta.app", "yankee.app"}
	for _, n := range names {
		if err := ix.upsert(&indexedApp{
			appEntry:  appEntry{DisplayName: n, Version: "1.0.0"},
			Appname:   n,
			Publisher: "p",
		}); err != nil {
			t.Fatalf("upsert %s: %v", n, err)
		}
	}
	want := []string{"alpha.app", "beta.app", "mike.app", "yankee.app", "zeta.app"}
	for round := 0; round < 20; round++ {
		got := ix.list()
		if len(got) != len(want) {
			t.Fatalf("条数 %d != %d", len(got), len(want))
		}
		for i, a := range got {
			if a.Appname != want[i] {
				t.Fatalf("第 %d 轮第 %d 位 = %q，期望 %q", round, i, a.Appname, want[i])
			}
		}
	}
	// 走真实 handler：两次响应必须逐字节一致。
	c := newCompanion("http://127.0.0.1:1", "http://127.0.0.1:2", "", nil, ix)
	body := func() string {
		rec := httptest.NewRecorder()
		c.handleListIndex(rec, httptest.NewRequest(http.MethodGet, "/api/index", nil))
		return rec.Body.String()
	}
	first, second := body(), body()
	if first != second {
		t.Fatalf("两次 /api/index 响应不一致（%d vs %d 字节）", len(first), len(second))
	}
	if !strings.Contains(first, `"apps"`) {
		t.Fatalf("响应缺少 apps 字段")
	}
}

// F4（0.2.55）：损坏 index.json 必须被改名隔离（不删除、不覆盖），
// 并通过 diag() 暴露错误与隔离文件名。
func TestCorruptIndexQuarantined_0255(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.json")
	bad := `{"apps": [ {"appname": "broken"` // 半截 JSON（模拟断电/磁盘错误）
	if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	ix := newIndex(p)
	if n := len(ix.list()); n != 0 {
		t.Fatalf("损坏索引应回落空索引，实际 %d 条", n)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("原路径应已被改名隔离（stat err=%v）", err)
	}
	matches, _ := filepath.Glob(p + ".bad-*")
	if len(matches) != 1 {
		t.Fatalf("期望 1 个隔离文件，实际 %v", matches)
	}
	got, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("隔离文件不可读: %v", err)
	}
	if string(got) != bad {
		t.Fatalf("隔离文件内容被改动: %q", string(got))
	}
	msg, badFile := ix.diag()
	if !strings.Contains(msg, "索引解析失败") || badFile == "" {
		t.Fatalf("diag 未暴露解析错误: msg=%q bad=%q", msg, badFile)
	}
	if badFile != filepath.Base(matches[0]) {
		t.Fatalf("diag 隔离文件名 %q != %q", badFile, filepath.Base(matches[0]))
	}
	// 隔离后仍可正常写入新索引，且隔离文件必须继续存在（可取证）。
	if err := ix.upsert(&indexedApp{Appname: "new.app", appEntry: appEntry{Version: "1.0.0"}}); err != nil {
		t.Fatalf("隔离后写入失败: %v", err)
	}
	if _, err := os.Stat(matches[0]); err != nil {
		t.Fatalf("隔离文件写入后被清理: %v", err)
	}
}

// F3（0.2.55）：并发备份必须各自落独立文件，不得同名互相覆盖。
// 旧实现 os.Create(O_TRUNC) + 秒级文件名 → 5 并发只留 1 个文件，5 个都报成功。
func TestCreateBackupToDirConcurrentUnique_0255(t *testing.T) {
	dataDir := t.TempDir()
	idxPath := filepath.Join(dataDir, "index.json")
	if err := os.WriteFile(idxPath, []byte(`{"version":1,"apps":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := newIndex(idxPath)
	c := newCompanion("http://127.0.0.1:1", "http://127.0.0.1:2", "", nil, idx)
	c.setConfig(filepath.Join(dataDir, "config.json"), &mateConfig{}, nil)

	const n = 5
	var wg sync.WaitGroup
	namesCh := make(chan string, n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, size, err := c.createBackupToDir()
			if err != nil {
				errCh <- err
				return
			}
			if size <= 0 {
				errCh <- fmt.Errorf("%s 大小 %d", name, size)
				return
			}
			namesCh <- name
		}()
	}
	wg.Wait()
	close(namesCh)
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发备份失败: %v", err)
	}
	seen := map[string]bool{}
	for name := range namesCh {
		if seen[name] {
			t.Fatalf("备份文件名重复（互相覆盖）: %s", name)
		}
		seen[name] = true
		if _, ok := backupNameTime(name); !ok {
			t.Fatalf("文件名不符合 backupNameRE 约定: %s", name)
		}
		fi, err := os.Stat(filepath.Join(dataDir, "backups", name))
		if err != nil || fi.Size() <= 0 {
			t.Fatalf("备份文件缺失或为空: %s (%v)", name, err)
		}
	}
	if len(seen) != n {
		t.Fatalf("期望 %d 个唯一备份，实际 %d", n, len(seen))
	}
}
