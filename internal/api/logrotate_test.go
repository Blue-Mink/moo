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
	payload := bytes.Repeat([]byte("log-line\n"), int((logRotateMaxSize+4096)/9+1))
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

// 0.6.321 F4：归档 5 代顺移——连续 6 轮转后 .1~.5 保留（.5=第 2 轮，
// 第 1 轮被丢弃），顺移序逐档验证。
func TestRotateMooLogFiveGenerations(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "moo.log")
	old := logRotateMaxSize
	logRotateMaxSize = 1 << 20 // 1MB 阈值，免写 5MB
	defer func() { logRotateMaxSize = old }()

	for round := 1; round <= 6; round++ {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		// 首行标记轮次 + 填充到 >1MB
		head := []byte{}
		head = append(head, []byte("R"+string(rune('0'+round))+" marker\n")...)
		block := make([]byte, 4096)
		for i := range block {
			block[i] = 'x'
		}
		for int64(len(head)) < logRotateMaxSize+512 {
			head = append(head, block...)
		}
		if _, err := f.Write(head); err != nil {
			t.Fatal(err)
		}
		f.Close()
		rotateMooLog(dir)
	}
	// 期望顺移序：.1=R6 .2=R5 .3=R4 .4=R3 .5=R2（R1 被丢弃）
	want := map[int]string{1: "R6", 2: "R5", 3: "R4", 4: "R3", 5: "R2"}
	for i, m := range want {
		b, err := os.ReadFile(filepath.Join(dir, "moo.log."+string(rune('0'+i))))
		if err != nil {
			t.Fatalf("归档 .%d 应存在: %v", i, err)
		}
		if len(b) < 4 || string(b[:2]) != m {
			t.Errorf("归档 .%d 首行应为 %s（顺移序），实际 %q", i, m, b[:4])
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "moo.log.6")); !os.IsNotExist(err) {
		t.Error(".6 不应存在（超出 5 代被丢弃）")
	}
	// 当前文件已清零
	fi, err := os.Stat(p)
	if err != nil || fi.Size() != 0 {
		t.Errorf("当前文件应清零: %v size=%v", err, fi.Size())
	}
}
