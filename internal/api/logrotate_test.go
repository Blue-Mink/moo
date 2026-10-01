package api

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 0.6.216 P2（N3）：moo.log 大小轮转
func TestRotateMooLog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "moo.log")

	// 未超阈值 → 不动
	if err := os.WriteFile(p, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateMooLog(dir)
	if fi, _ := os.Stat(p); fi.Size() != int64(len("small")) {
		t.Fatalf("小文件被误轮转: %d", fi.Size())
	}

	// 超阈值 → 归档 + 截断
	payload := bytes.Repeat([]byte("log-line\n"), (logRotateMaxSize+4096)/9+1)
	if err := os.WriteFile(p, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := int64(len(payload))
	rotateMooLog(dir)

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("当前文件丢失: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("当前文件未截断: %d", fi.Size())
	}
	fi1, err := os.Stat(p + ".1")
	if err != nil {
		t.Fatalf("归档缺失: %v", err)
	}
	if fi1.Size() != orig {
		t.Fatalf("归档大小不符: %d != %d", fi1.Size(), orig)
	}

	// 二次轮转 → .1 顺移到 .2
	if err := os.WriteFile(p, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	rotateMooLog(dir)
	if _, err := os.Stat(p + ".2"); err != nil {
		t.Fatalf(".2 缺失: %v", err)
	}
}
