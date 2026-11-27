package source

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 误判侧④：失败 4 轮 + 重启（新 Manager 从同一路径加载计数）+ 再 4 轮 →
// 不停用；再凑够 5 轮且跨 24h 才停用。证明持久化真的把门槛守住了。
func TestCareFailPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, cfg := careFailManager(t, CarePermanentlyDead)
	// 异步落盘（AutoCarePass 触发）要跑完，否则 t.TempDir 清理时会撞上
	// 「directory not empty」（实测偶发）。等 WaitGroup 归零才放行清理。
	var waiters []*Manager
	t.Cleanup(func() {
		for _, mm := range waiters {
			mm.WaitCareFlush()
		}
	})
	waiters = append(waiters, m)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	for i := 0; i < 4; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("计数文件应已落盘: %v", err)
	}

	// 「重启」：同一份 cfg 换一个新 Manager（内存全空）
	m2 := NewManager(cfg)
	waiters = append(waiters, m2)
	m2.CareProbe = func(name, url string) CareVerdict { return CarePermanentlyDead }
	if n := m2.SetCarePath(path); n == 0 {
		t.Fatal("重启后应从磁盘恢复计数")
	}
	if got := careFailCount(m2, "a"); got != 4 {
		t.Fatalf("重启后计数应保持 4（不清零）, got %d", got)
	}
	for i := 0; i < 4; i++ {
		if acts := m2.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); acts != nil {
			t.Fatalf("重启后第 %d 轮（累计 %d，<24h）不应停用: %v", i+1, 5+i, acts)
		}
	}
	if careSource(cfg, "a").IsEnabled() == false {
		t.Fatal("累计 8 轮但不足 24h 不得停用")
	}
	careAge(m2, "a", 25*time.Hour)
	if acts := m2.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2)); len(acts) == 0 {
		t.Fatal("跨过 24h 后应停用（持久化 + 双门槛共同生效）")
	}
}

// 损坏/缺失文件必须安全降级（不 panic、不阻塞启动）。
func TestCareStoreDegradesSafely(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := careFailManager(t, CareUncertain)
	if n := m.SetCarePath(bad); n != 0 {
		t.Fatalf("损坏文件应恢复 0 条, got %d", n)
	}
	if n := m.SetCarePath(filepath.Join(dir, "missing.json")); n != 0 {
		t.Fatalf("缺失文件应恢复 0 条, got %d", n)
	}
	if n := m.SetCarePath(""); n != 0 {
		t.Fatalf("空路径应恢复 0 条, got %d", n)
	}
	if err := m.SaveCare(); err != nil {
		t.Fatalf("空路径落盘应静默成功: %v", err)
	}
}

// 空源计数也要持久化（P1-4 覆盖两条通道），且改名后计数跟着走。
func TestCareEmptyPersistAndRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "care.json")
	m, cfg := careFailManager(t, CareUncertain)
	m.SetCarePath(path)
	waiters := []*Manager{m}
	t.Cleanup(func() { // 等异步落盘结束再删临时目录（否则偶发 not empty）
		for _, mm := range waiters {
			mm.WaitCareFlush()
		}
	})
	m.AutoCarePass(careFailRound(nil, "", 0)) // a/b/c 全空 → 各 1
	if err := m.SaveCare(); err != nil {
		t.Fatal(err)
	}
	for _, st := range m.Sources() {
		if st.EmptyStreak != 1 {
			t.Fatalf("源 %s empty_streak 应为 1（P1-2 赋值）, got %d", st.Name, st.EmptyStreak)
		}
	}
	m2 := NewManager(cfg)
	waiters = append(waiters, m2)
	if n := m2.SetCarePath(path); n == 0 {
		t.Fatal("空源计数应随文件恢复")
	}
	if got := m2.Sources()[0].EmptyStreak; got != 1 {
		t.Fatalf("重启后 empty_streak 应保持 1, got %d", got)
	}
	if err := m2.RenameSource("a", "a2"); err != nil {
		t.Fatal(err)
	}
	for _, st := range m2.Sources() {
		if st.Name == "a2" && st.EmptyStreak != 1 {
			t.Fatalf("改名后计数应迁移, got %d", st.EmptyStreak)
		}
	}
}

// 2026-11-26（全量测试暴露的乱序落盘）：异步落盘拿的是「调度时」快照，
// 若它晚于更新的同步 SaveCare 完成，旧实现会把计数覆盖回旧值（实测 4 → 2）。
// 现在按 seq 单调过滤：旧快照必须被丢弃。
func TestSaveCareStaleSnapshotDoesNotRegress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, _ := careFailManager(t, CarePermanentlyDead)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	for i := 0; i < 2; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	pathA, fa, seqA := m.careSnapshotWithSeq() // 旧快照：2 轮
	for i := 0; i < 2; i++ {
		m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	}
	_, fb, seqB := m.careSnapshotWithSeq() // 新快照：4 轮

	// 故意乱序：新快照先写，旧快照后写。
	if err := m.persistCare(path, fb, seqB); err != nil {
		t.Fatalf("写新快照失败: %v", err)
	}
	if err := m.persistCare(pathA, fa, seqA); err != nil {
		t.Fatalf("写旧快照失败: %v", err)
	}

	m2 := NewManager(m.cfg)
	if n := m2.SetCarePath(path); n == 0 {
		t.Fatal("重启后应从磁盘恢复计数")
	}
	if got := careFailCount(m2, "a"); got != 4 {
		t.Fatalf("旧快照不得覆盖新快照（应保持 4）, got %d", got)
	}
	if seqA >= seqB {
		t.Fatalf("快照序号必须单调, seqA=%d seqB=%d", seqA, seqB)
	}
}
