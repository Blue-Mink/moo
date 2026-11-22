package api

// P0-2：操作快照落盘 / 恢复（operations.json）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moo/internal/operation"
)

func TestRestoreOperationsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.json")
	start := time.Now().Add(-time.Minute)
	p := NewOpsPersister(path)
	p.Save([]operation.View{
		{ID: "run-1", Kind: "update", Target: "fnmonitor", State: operation.StateRunning, StartedAt: start},
		{ID: "done-1", Kind: "install", Target: "gitea", State: operation.StateDone, StartedAt: start},
		{ID: ""}, // 空条目应被丢弃
	})
	q := operation.NewQueue()
	restored, interrupted := RestoreOperations(q, path)
	if restored != 2 || interrupted != 1 {
		t.Fatalf("restored=%d interrupted=%d，期望 2/1", restored, interrupted)
	}
	h := q.History()
	if len(h) != 2 {
		t.Fatalf("history = %d，期望 2", len(h))
	}
	for _, v := range h {
		if v.ID == "run-1" && (v.State != operation.StateError || !strings.Contains(v.Err, "重启")) {
			t.Fatalf("run-1 应标记重启中断: %+v", v)
		}
		if v.ID == "done-1" && v.State != operation.StateDone {
			t.Fatalf("done-1 不应被改: %+v", v)
		}
	}
}

// 文件损坏/不存在：返回 0，不 panic、不影响启动。
func TestRestoreOperationsBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.json")
	if n, intr := RestoreOperations(operation.NewQueue(), path); n != 0 || intr != 0 {
		t.Fatalf("不存在文件应返回 0/0，实际 %d/%d", n, intr)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, intr := RestoreOperations(operation.NewQueue(), path); n != 0 || intr != 0 {
		t.Fatalf("损坏文件应返回 0/0，实际 %d/%d", n, intr)
	}
}

// P2（2026-11-22）：超大快照按「无快照」处理，不整体读进内存。
func TestRestoreOperationsRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.json")
	p := NewOpsPersister(path)
	// 每条 ~1KB 错误串 × 3000 条 ≈ 3MB，超过 2MB 上限；若仍按老逻辑会恢复 20 条。
	views := make([]operation.View, 0, 3000)
	big := strings.Repeat("x", 1024)
	for i := 0; i < 3000; i++ {
		views = append(views, operation.View{ID: "op-" + big[:8] + string(rune('a'+i%26)), Kind: "install", Target: "t", State: operation.StateDone, Err: big})
	}
	p.Save(views)
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= opsPersistMaxBytes {
		t.Fatalf("夹具未超过上限: size=%v err=%v", fi, err)
	}
	q := operation.NewQueue()
	restored, interrupted := RestoreOperations(q, path)
	if restored != 0 || interrupted != 0 {
		t.Fatalf("超大快照应被拒绝，实际 restored=%d interrupted=%d", restored, interrupted)
	}
	if n := len(q.History()); n != 0 {
		t.Fatalf("超大快照不得恢复任何条目，实际 %d", n)
	}
}
