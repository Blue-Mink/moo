package source

import (
	"path/filepath"
	"testing"
)

// 2026-11-27 FIXPLAN 盲点 #3 · C2：判定可观测字段（LastVerdict / VerdictAt）。
// 这里只测「结论映射」与「落盘 → 重启仍可读」；JSON 形状与 omitempty 在
// internal/api 侧用例里测（那才是对外契约）。

func careStatus(t *testing.T, m *Manager, name string) SourceStatus {
	t.Helper()
	for _, st := range m.Sources() {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("源 %s 不在 Sources() 里", name)
	return SourceStatus{}
}

// 结论 → 稳定字符串（对外契约只允许 alive/dead/uncertain 三值）。
func TestCareVerdictLabel(t *testing.T) {
	cases := []struct {
		v    CareVerdict
		want string
	}{
		{CareAlive, "alive"},
		{CarePermanentlyDead, "dead"},
		{CareUncertain, "uncertain"},
		// 双通道未确认（镜像够不着）→ 不冒充 dead；它计入失败轮的事实由
		// fail_streak 体现（见 care_gap_0575_test.go）。
		{CareDirectGoneMirrorsUnreachable, "uncertain"},
	}
	for _, c := range cases {
		if got := careVerdictLabel(c.v); got != c.want {
			t.Fatalf("careVerdictLabel(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// 一轮复核后写入；未复核的源保持空（不写假结论）。
func TestCareVerdictWrittenPerRound(t *testing.T) {
	m, _ := careFailManager(t, CareAlive)
	t.Cleanup(m.WaitCareFlush)
	// 只有 a 刷新失败（会被复核），b/c 健康（不探测）。
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))

	a := careStatus(t, m, "a")
	if a.LastVerdict != "alive" || a.VerdictAt == 0 {
		t.Fatalf("复核过 alive 的源应带 last_verdict/verdict_at, got %q/%d", a.LastVerdict, a.VerdictAt)
	}
	if b := careStatus(t, m, "b"); b.LastVerdict != "" || b.VerdictAt != 0 {
		t.Fatalf("未复核的源不得凭空出现结论, got %q/%d", b.LastVerdict, b.VerdictAt)
	}
	// 下一轮换结论：按轮覆盖（不是历史累积）。
	m.CareProbe = func(name, url string) CareVerdict { return CarePermanentlyDead }
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 404", 2))
	if a2 := careStatus(t, m, "a"); a2.LastVerdict != "dead" {
		t.Fatalf("第二轮应覆盖成 dead, got %q", a2.LastVerdict)
	}
}

// ④ 落盘 → 重启（新 Manager 从同一路径加载）后仍能读到判定。
func TestCareVerdictPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, cfg := careFailManager(t, CareAlive)
	t.Cleanup(m.WaitCareFlush)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	m.AutoCarePass(careFailRound([]string{"a"}, "x: HTTP 502", 2))
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}
	m.WaitCareFlush()
	want := careStatus(t, m, "a")

	m2 := NewManager(cfg)
	t.Cleanup(m2.WaitCareFlush)
	if n := m2.SetCarePath(path); n == 0 {
		t.Fatal("重启后应恢复含判定字段的记录")
	}
	got := careStatus(t, m2, "a")
	if got.LastVerdict != "alive" || got.VerdictAt != want.VerdictAt {
		t.Fatalf("重启后判定应保持 alive/%d, got %q/%d", want.VerdictAt, got.LastVerdict, got.VerdictAt)
	}
}

// 判定字段必须能「独立」被恢复：SetCarePath 的恢复判据要覆盖新字段，否则
// 只含 LastVerdict/VerdictAt 的记录会在重启时被静默丢掉。
// 说明：当前写入路径（careapply 的失败分支）总会同时带 last_error，所以这个
// 形状眼下不会自然出现；本用例直接构造它来验证判据本身，避免被 last_error
// 分支掩盖（反向验证见 _audit_0578/BLINDSPOT_4C.md 的变异 F）。
func TestCareVerdictOnlyRecordRestored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, cfg := careFailManager(t, CareAlive)
	t.Cleanup(m.WaitCareFlush)
	if n := m.SetCarePath(path); n != 0 {
		t.Fatalf("空文件应恢复 0 条, got %d", n)
	}
	m.careMu.Lock()
	m.careRecs["a"] = careRec{URL: "https://a.example/fnpack.json", LastVerdict: "alive", VerdictAt: 1700000000}
	m.careMu.Unlock()
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}
	m2 := NewManager(cfg)
	t.Cleanup(m2.WaitCareFlush)
	if n := m2.SetCarePath(path); n != 1 {
		t.Fatalf("只含判定字段的记录应被恢复 1 条, got %d", n)
	}
	if st := careStatus(t, m2, "a"); st.LastVerdict != "alive" || st.VerdictAt != 1700000000 {
		t.Fatalf("重启后判定应保持 alive/1700000000, got %q/%d", st.LastVerdict, st.VerdictAt)
	}
}
