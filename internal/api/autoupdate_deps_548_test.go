package api

// autoupdate_deps_548_test.go —— 自动更新「依赖占用」策略（0.6.548）。
//
// 三层：①纯函数（策略/窗口/校验/名单）②假 daemon 的停→升→启序列 ③未允许时零副作用。

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"moo/internal/platform"
)

func TestAutoUpdateDepPolicyOf(t *testing.T) {
	cases := map[string]string{
		"":         depPolicySkip,
		"skip":     depPolicySkip,
		"notify":   depPolicyNotify,
		"stop":     depPolicyStop,
		"  STOP  ": depPolicyStop,
		"bogus":    depPolicySkip,
	}
	for in, want := range cases {
		if got := autoUpdateDepPolicyOf(in); got != want {
			t.Fatalf("autoUpdateDepPolicyOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHHMM(t *testing.T) {
	ok := map[string]int{"00:00": 0, "09:30": 570, "23:59": 1439}
	for in, want := range ok {
		got, valid := parseHHMM(in)
		if !valid || got != want {
			t.Fatalf("parseHHMM(%q) = %d,%v want %d,true", in, got, valid, want)
		}
	}
	for _, in := range []string{"", "9:30", "24:00", "12:60", "12-30", "ab:cd"} {
		if _, valid := parseHHMM(in); valid {
			t.Fatalf("parseHHMM(%q) 应判非法", in)
		}
	}
}

func TestInAutoUpdateWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 13, h, m, 0, 0, time.Local) }
	cases := []struct {
		name       string
		start, end string
		now        time.Time
		want       bool
	}{
		{"全天起点", "00:00", "23:59", at(0, 0), true},
		{"全天深夜", "00:00", "23:59", at(23, 59), true},
		{"同日内", "09:00", "18:00", at(9, 0), true},
		{"同日上界", "09:00", "18:00", at(18, 0), true},
		{"同日早于", "09:00", "18:00", at(8, 59), false},
		{"同日晚于", "09:00", "18:00", at(18, 1), false},
		{"跨夜深夜", "22:00", "08:00", at(23, 0), true},
		{"跨夜凌晨", "22:00", "08:00", at(3, 0), true},
		{"跨夜下界", "22:00", "08:00", at(8, 0), true},
		{"跨夜之外早", "22:00", "08:00", at(21, 59), false},
		{"跨夜之外晚", "22:00", "08:00", at(8, 1), false},
		{"起止相同=全天", "10:00", "10:00", at(3, 0), true},
		{"脏值=不阻断", "xx", "yy", at(3, 0), true},
	}
	for _, c := range cases {
		if got := inAutoUpdateWindow(c.now, c.start, c.end); got != c.want {
			t.Fatalf("%s: inAutoUpdateWindow(%s,%s) = %v, want %v", c.name, c.start, c.end, got, c.want)
		}
	}
}

func TestValidateAutoUpdateWindow(t *testing.T) {
	if s, e, err := validateAutoUpdateWindow("", ""); err != nil || s != "00:00" || e != "23:59" {
		t.Fatalf("空值应回默认全天，得到 %q %q %v", s, e, err)
	}
	if _, _, err := validateAutoUpdateWindow("09:00", "18:00"); err != nil {
		t.Fatalf("同日合法窗口被拒: %v", err)
	}
	if _, _, err := validateAutoUpdateWindow("22:00", "08:00"); err != nil {
		t.Fatalf("跨夜合法窗口被拒: %v", err)
	}
	bad := [][2]string{
		{"10:00", "10:00"}, // 起止相同
		{"23:30", "00:20"}, // 50 分钟 < 1h
		{"9:00", "18:00"},  // 格式
		{"24:00", "18:00"}, // 越界
	}
	for _, b := range bad {
		if _, _, err := validateAutoUpdateWindow(b[0], b[1]); err == nil {
			t.Fatalf("(%s,%s) 应被判非法", b[0], b[1])
		}
	}
}

func TestDepAllAllowed(t *testing.T) {
	deps := []platform.DepBlocker{{AppName: "a", Status: "running"}, {AppName: "b", Status: "running"}}
	if ok, missing := depAllAllowed(deps, []string{"a", "b"}); !ok || len(missing) != 0 {
		t.Fatalf("全命中应为 true，得到 %v %v", ok, missing)
	}
	if ok, missing := depAllAllowed(deps, []string{" A "}); ok || !reflect.DeepEqual(missing, []string{"b"}) {
		t.Fatalf("部分命中应拒绝，得到 %v %v", ok, missing)
	}
	if ok, missing := depAllAllowed(deps, nil); ok || len(missing) != 2 {
		t.Fatalf("空名单应一个都不许，得到 %v %v", ok, missing)
	}
	if ok, _ := depAllAllowed(deps, []string{"a", "", "b"}); !ok {
		t.Fatal("空串应被忽略")
	}
}

func TestStringSliceEqualFold(t *testing.T) {
	if !stringSliceEqualFold([]string{"A", "b"}, []string{"a", "B"}) {
		t.Fatal("大小写不同应视为相同")
	}
	if stringSliceEqualFold([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("长度不同应视为不同")
	}
	if !stringSliceEqualFold(nil, []string{"", " "}) {
		t.Fatal("空值与 nil 应等价")
	}
}

// ① 全命中白名单：停两个依赖方 → 升级 → 依次启动恢复。
func TestWithUpgradeDepsCheckedAllAllowed(t *testing.T) {
	tally := startDepDaemon(t, "")
	s := depTestServer(t)
	var stopped []string
	ran := false
	err := s.withUpgradeDepsChecked(context.Background(), "target-app", []string{"dep-a", "dep-b"}, nil,
		func(ns []string) { stopped = append([]string(nil), ns...) },
		func(ctx context.Context, p func(string, float64)) error { ran = true; return nil })
	if err != nil {
		t.Fatalf("全命中应成功: %v", err)
	}
	if !ran {
		t.Fatal("升级体未执行")
	}
	if !reflect.DeepEqual(stopped, []string{"dep-a", "dep-b"}) {
		t.Fatalf("停用名单不符: %v", stopped)
	}
	want := []string{"stop:dep-a", "stop:dep-b", "start:dep-a", "start:dep-b"}
	if got := tally.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("调用序列 = %v, want %v", got, want)
	}
}

// ② 未全部命中：一个都不许停，升级体不执行。
func TestWithUpgradeDepsCheckedNotAllowed(t *testing.T) {
	tally := startDepDaemon(t, "")
	s := depTestServer(t)
	ran := false
	err := s.withUpgradeDepsChecked(context.Background(), "target-app", []string{"dep-a"}, nil, nil,
		func(ctx context.Context, p func(string, float64)) error { ran = true; return nil })
	var na *DepNotAllowedError
	if !errors.As(err, &na) {
		t.Fatalf("应返回 DepNotAllowedError，得到 %v", err)
	}
	if !reflect.DeepEqual(na.Missing, []string{"dep-b"}) {
		t.Fatalf("未允许清单不符: %v", na.Missing)
	}
	if ran || tally.countOf("stop:") != 0 {
		t.Fatalf("未允许时不得停用或升级（stop=%d ran=%v）", tally.countOf("stop:"), ran)
	}
}

// ③ 第二个依赖方停不掉：回滚已停的，升级不执行。
func TestWithUpgradeDepsCheckedRollback(t *testing.T) {
	tally := startDepDaemon(t, "dep-b")
	s := depTestServer(t)
	ran := false
	err := s.withUpgradeDepsChecked(context.Background(), "target-app", []string{"dep-a", "dep-b"}, nil, nil,
		func(ctx context.Context, p func(string, float64)) error { ran = true; return nil })
	if err == nil {
		t.Fatal("停不掉应报错")
	}
	if ran {
		t.Fatal("停用失败后不应升级")
	}
	want := []string{"stop:dep-a", "start:dep-a"}
	if got := tally.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("回滚序列 = %v, want %v", got, want)
	}
}

func TestShouldCacheDepBlocked(t *testing.T) {
	cases := []struct {
		policy  string
		canStop bool
		want    bool
	}{
		{depPolicySkip, false, true},
		{depPolicySkip, true, true},
		{depPolicyNotify, false, true},
		{depPolicyStop, true, true},  // 时段内仍被占用：值得静默 2h
		{depPolicyStop, false, false}, // 时段外：不能写，否则挡住整窗
	}
	for _, c := range cases {
		if got := shouldCacheDepBlocked(c.policy, c.canStop); got != c.want {
			t.Fatalf("shouldCacheDepBlocked(%s,%v) = %v, want %v", c.policy, c.canStop, got, c.want)
		}
	}
}

// 升级失败也必须把停掉的依赖方恢复（defer 语义）。
func TestWithUpgradeDepsCheckedRestoresOnUpgradeError(t *testing.T) {
	tally := startDepDaemon(t, "")
	s := depTestServer(t)
	upErr := errors.New("升级失败")
	err := s.withUpgradeDepsChecked(context.Background(), "target-app", []string{"dep-a", "dep-b"}, nil, nil,
		func(ctx context.Context, p func(string, float64)) error { return upErr })
	if !errors.Is(err, upErr) {
		t.Fatalf("应原样返回升级错误，得到 %v", err)
	}
	want := []string{"stop:dep-a", "stop:dep-b", "start:dep-a", "start:dep-b"}
	if got := tally.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("升级失败后的序列 = %v, want %v", got, want)
	}
}

func TestAutoUpdateSkipHitAny(t *testing.T) {
	until := time.Now().Add(time.Hour).Format(time.RFC3339)
	m := map[string]autoUpdateSkipEntry{}
	k := autoUpdateSkipKey("Demo", "1.2.0", "abc", autoUpdateSkipReasonBlocked)
	m[k] = autoUpdateSkipEntry{App: "Demo", Version: "1.2.0", Sha: "abc",
		Reason: autoUpdateSkipReasonBlocked, Until: until}
	if !autoUpdateSkipHitAny(m, "demo", "1.2.0", "abc") {
		t.Fatal("blocked 条目应被命中（依赖类缓存靠这条生效）")
	}
	if autoUpdateSkipHitAny(m, "demo", "1.2.1", "abc") {
		t.Fatal("目标版本不同不应命中")
	}
	// wizard 类条目也应被同一入口命中（两类缓存都要查）。
	wm := map[string]autoUpdateSkipEntry{}
	wk := autoUpdateSkipKey("Demo", "1.2.0", "abc", autoUpdateSkipReasonWizard)
	wm[wk] = autoUpdateSkipEntry{App: "Demo", Version: "1.2.0", Sha: "abc",
		Reason: autoUpdateSkipReasonWizard, Until: until}
	if !autoUpdateSkipHitAny(wm, "demo", "1.2.0", "abc") {
		t.Fatal("wizard 条目也应命中（否则升级确认过的东西每轮重试）")
	}
	// 过期条目由 autoUpdateSkips() 读取时剔除，命中函数本身不判时间。
}

func TestAutoUpdateBlockedReason(t *testing.T) {
	failed := map[string]bool{"nodejs_v24": true}
	if got := autoUpdateBlockedReason("nodejs_v24", failed, depPolicyStop, true); got != "" {
		t.Fatalf("stop 档 + 时段内应忽略平台标记（改由 Moo 尝试停依赖），得到 %q", got)
	}
	if got := autoUpdateBlockedReason("nodejs_v24", failed, depPolicyStop, false); got == "" {
		t.Fatal("stop 档但在时段外仍应按平台标记跳过")
	}
	if got := autoUpdateBlockedReason("nodejs_v24", failed, depPolicySkip, true); got == "" {
		t.Fatal("skip 档行为不得改变")
	}
	if got := autoUpdateBlockedReason("other", failed, depPolicyStop, true); got != "" {
		t.Fatalf("未被平台标记的应用不应有原因，得到 %q", got)
	}
}
