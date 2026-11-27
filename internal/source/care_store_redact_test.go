package source

// 批次 1 F2（N4）：care 状态文件「只属主可读写」+ 源地址脱敏落盘。
//
// 背景：`cache/source_care.json` 里有源 URL（可能带 userinfo / ?token=）与失败
// 原因；旧实现 chmod 0644 落盘、且 URL 原样写入 —— 备份/拷走/贴排障记录都会
// 把凭据带出去。
//
// 反向验证（必须遵守，改了实现要重跑）：
//   - 把 careerr.go 的 chmod 改回 0o644 → TestCareFilePerm0600 FAIL
//   - 把 setCareRecordLocked 的 redactSourceURL 去掉 → TestCareRecordURLRedactedOnDisk FAIL
//   - 把 careRecordLocked 的比较改回「原文 vs 原文」→
//     TestCareRecordSameSourceAcrossQueryChange FAIL（fail_streak 每轮被重置）

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"moo/internal/config"
)

const careRedactSourceName = "s"

// careRedactManager 单源管理器：复核结论固定为「确定性失效」（计数每轮 +1）。
func careRedactManager(t *testing.T, url string) (*Manager, *config.Config) {
	t.Helper()
	cfg := &config.Config{Sources: []config.SourceRef{{Name: careRedactSourceName, URL: url}}}
	m := NewManager(cfg)
	m.CareProbe = func(name, u string) CareVerdict { return CarePermanentlyDead }
	return m, cfg
}

// careRedactRound 单源失败轮（Count 必须 < 阈值，否则会被当成「有应用」）。
func careRedactRound(errText string) []SourceStatus {
	return []SourceStatus{{Name: careRedactSourceName, Error: errText}}
}

func careRedactStreak(m *Manager) int {
	for _, st := range m.Sources() {
		if st.Name == careRedactSourceName {
			return st.FailStreak
		}
	}
	return -1
}

// ① 落盘权限必须是 0600（同 config.json / 会话文件口径）。
func TestCareFilePerm0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, _ := careRedactManager(t, "https://s.example/fnpack.json")
	m.SetCarePath(path)
	if acts := m.AutoCarePass(careRedactRound("所有候选地址均失败: x: HTTP 404")); len(acts) != 0 {
		t.Fatalf("首轮不该有动作, got %v", acts)
	}
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat care 文件: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("care 文件权限应为 0600（仅属主可读写），实际 %o", perm)
	}
}

// ② 带 userinfo/query 的源地址写入后不得把凭据带进文件。
func TestCareRecordURLRedactedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source_care.json")
	m, _ := careRedactManager(t, "https://u:tok@source.example/apps?token=1#frag")
	m.SetCarePath(path)
	m.AutoCarePass(careRedactRound("所有候选地址均失败: x: HTTP 404"))
	if err := m.SaveCare(); err != nil {
		t.Fatalf("SaveCare: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 care 文件: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "tok") || strings.Contains(s, "token=1") || strings.Contains(s, "u@") {
		t.Errorf("落盘内容不得含 userinfo/query 凭据: %s", s)
	}
	if !strings.Contains(s, "https://source.example/apps") {
		t.Errorf("应保留 scheme://host/path（换仓库判定要用）, got %s", s)
	}
	// 内存记录同口径（/api/sources 直接从内存读，不能一边脱敏一边泄露）。
	var f careFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("care 文件不是合法 JSON: %v", err)
	}
	rec, ok := f.Records[careRedactSourceName]
	if !ok {
		t.Fatalf("应写入 %s 的记录, got %+v", careRedactSourceName, f.Records)
	}
	if rec.URL != "https://source.example/apps" {
		t.Errorf("记录 URL 应已脱敏为 scheme://host/path, got %q", rec.URL)
	}
}

// ③ 同一仓库只改 query（签名/分支参数）仍算同源：fail_streak 必须连续累加，
// 不得因「脱敏值 vs 原值」的比较而每轮清零（那会让连续失败门槛永远达不到）。
func TestCareRecordSameSourceAcrossQueryChange(t *testing.T) {
	m, cfg := careRedactManager(t, "https://source.example/apps?v=1")
	for i := 1; i <= 3; i++ {
		m.AutoCarePass(careRedactRound("所有候选地址均失败: x: HTTP 404"))
		if got := careRedactStreak(m); got != i {
			t.Fatalf("第 %d 轮后 fail_streak 应为 %d（只改 query 不算换仓库）, got %d", i, i, got)
		}
		// 每轮换一个 query，模拟带签名/时间戳参数的源地址
		cfg.Sources[0].URL = "https://source.example/apps?v=" + string(rune('0'+i+1))
	}
	if got := careRedactStreak(m); got != 3 {
		t.Fatalf("连续失败计数应为 3, got %d", got)
	}
	// 换路径 = 真换了仓库：计数必须重置（反方向护栏，防「脱敏脱过头」）。
	cfg.Sources[0].URL = "https://source.example/other-apps"
	m.AutoCarePass(careRedactRound("所有候选地址均失败: x: HTTP 404"))
	if got := careRedactStreak(m); got != 1 {
		t.Fatalf("换仓库后计数应重置为 1, got %d", got)
	}
}
