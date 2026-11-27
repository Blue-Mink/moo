package official

// 2026-11-26 加固 B（主修复）：会话文件原子写 + 启动清理。
//
// 为什么必须有：旧实现 os.WriteFile 直接截断 official_session.json，磁盘满 / 进程
// 被杀时会话文件就变成半截 JSON——下次启动解析失败 = 用户被强制重新授权（而令牌
// 本身可能还有效）。改成同目录 tmp(O_EXCL 0600) → Write → Sync → Rename；
// 任何一步失败都保留旧文件，并在启动时清掉上次残留的 tmp。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAtomicTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManagerWithBase(t.TempDir(), "http://127.0.0.1:5666")
}

func sampleSession(token string) *Session {
	return &Session{
		AccessToken:  token,
		RefreshToken: "rt-" + token,
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scopes:       []string{"trim.appcenter.all"},
		UpdatedAt:    time.Now().UnixMilli(),
	}
}

// tmpLeftovers 列出目录里 official_session.json.tmp* 残骸（应为空）。
func tmpLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), sessionTmpName) {
			out = append(out, e.Name())
		}
	}
	return out
}

// a) 保存成功：无 .tmp 残留、权限 0600、JSON 可解析且内容正确。
func TestSaveSessionAtomic_NoTmpLeftoverMode0600(t *testing.T) {
	m := newAtomicTestManager(t)
	if err := m.saveSessionLocked(sampleSession("at-1")); err != nil {
		t.Fatalf("saveSessionLocked: %v", err)
	}
	if left := tmpLeftovers(t, m.dataDir); len(left) != 0 {
		t.Fatalf("保存成功后不应残留临时文件，实际 %v", left)
	}
	fi, err := os.Stat(m.SessionFile())
	if err != nil {
		t.Fatalf("Stat 会话文件: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("会话文件权限应为 0600，实际 %o", perm)
	}
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got Session
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("会话文件不是合法 JSON: %v (%q)", err, string(b))
	}
	if got.AccessToken != "at-1" || got.RefreshToken != "rt-at-1" {
		t.Errorf("内容不符: %+v", got)
	}
}

// b) 临时文件创建失败（数据目录只读 → CreateTemp 返回 EACCES）→ 返回 error，
//
//	旧会话文件内容与权限原封不动（绝不能截断旧文件）。
//	批次 1 F1：原子写改用 os.CreateTemp 唯一名后，原来「同名目录占位制造
//	EEXIST」的手法不再必然失败（唯一名永远撞不上），故改为只读目录注入错误。
func TestSaveSessionAtomic_FailureKeepsOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限，无法用只读目录制造 CreateTemp 失败")
	}
	m := newAtomicTestManager(t)
	if err := m.saveSessionLocked(sampleSession("old-token")); err != nil {
		t.Fatalf("前置保存: %v", err)
	}
	before, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("读旧文件: %v", err)
	}
	fiBefore, err := os.Stat(m.SessionFile())
	if err != nil {
		t.Fatalf("Stat 旧文件: %v", err)
	}
	// 只读数据目录 → CreateTemp 必然失败（且不会静默覆盖本体）。
	if err := os.Chmod(m.dataDir, 0o500); err != nil {
		t.Fatalf("把数据目录改为只读: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(m.dataDir, 0o700) })
	err = m.saveSessionLocked(sampleSession("new-token"))
	if err == nil {
		t.Fatal("临时文件无法创建时必须返回 error（不能静默成功）")
	}
	after, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("失败后仍应能读旧文件: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("失败路径改动了旧会话文件（旧=%q 现=%q）", string(before), string(after))
	}
	fiAfter, err := os.Stat(m.SessionFile())
	if err != nil {
		t.Fatalf("Stat 旧文件(失败后): %v", err)
	}
	if fiBefore.Mode().Perm() != fiAfter.Mode().Perm() {
		t.Errorf("失败路径改动了权限: %o → %o", fiBefore.Mode().Perm(), fiAfter.Mode().Perm())
	}
	if fiAfter.Mode().Perm() != 0o600 {
		t.Errorf("旧文件权限应保持 0600，实际 %o", fiAfter.Mode().Perm())
	}
	var got Session
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatalf("旧文件应仍是完整 JSON: %v", err)
	}
	if got.AccessToken != "old-token" {
		t.Errorf("旧文件内容被覆盖: %+v", got)
	}
}

// c) 启动清理：删掉 .tmp* 残留，保留 official_session.json 本体与 *.bak 等无关文件。
func TestLoadSession_CleansTmpLeftovers(t *testing.T) {
	m := newAtomicTestManager(t)
	if err := m.saveSessionLocked(sampleSession("at-live")); err != nil {
		t.Fatalf("前置保存: %v", err)
	}
	dir := m.dataDir
	leftovers := []string{
		sessionTmpName,
		sessionTmpName + ".1234567",
		sessionTmpName + "-crash",
	}
	for _, n := range leftovers {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{半截"), 0o600); err != nil {
			t.Fatalf("造残留 %s: %v", n, err)
		}
	}
	// 无关文件：必须原样保留（清理只认 official_session.json.tmp 前缀）。
	keep := []string{sessionFileName + ".bak", sessionFileName + ".bak-removed", "official_session.tmp"}
	for _, n := range keep {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("keep"), 0o600); err != nil {
			t.Fatalf("造保留文件 %s: %v", n, err)
		}
	}

	m.LoadSession()

	if left := tmpLeftovers(t, dir); len(left) != 0 {
		t.Fatalf("LoadSession 应清掉 .tmp 残留，仍存在 %v", left)
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("无关文件 %s 不应被清理: %v", n, err)
		}
	}
	s := m.Session()
	if s == nil || s.AccessToken != "at-live" {
		t.Fatalf("LoadSession 应加载到正式会话文件，实际 %+v", s)
	}
}

// d) 残留的唯一名临时文件不得毒化后续保存（批次 1 F1 回归防护）。
//
// 旧实现 = 固定名 official_session.json.tmp + O_EXCL：只要进程活着时留下一次
// 残骸（写到一半被杀 / 磁盘满），此后**每次**保存都 EEXIST 失败，只能靠重启
// 时的 cleanupSessionTmp 自愈。改 CreateTemp 后残留只是垃圾，保存照常成功。
// 反向验证：把实现改回固定名 + O_EXCL，本用例必须 FAIL。
func TestSaveSessionAtomic_SurvivesTmpLeftover(t *testing.T) {
	m := newAtomicTestManager(t)
	// 三条残留：固定名本体 + 两个 CreateTemp 风格的名字（模拟上 N 次崩溃）。
	for _, n := range []string{sessionTmpName, sessionTmpName + "1234567", sessionTmpName + "a1b2c3d4"} {
		if err := os.WriteFile(filepath.Join(m.dataDir, n), []byte("{半截"), 0o600); err != nil {
			t.Fatalf("造残留 %s: %v", n, err)
		}
	}
	if err := m.saveSessionLocked(sampleSession("after-leftover")); err != nil {
		t.Fatalf("存在 .tmp 残留时保存必须成功（旧实现固定名 O_EXCL 会 EEXIST）: %v", err)
	}
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("读会话文件: %v", err)
	}
	var got Session
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("会话文件不是合法 JSON（可能把残留当本体写了）: %v (%q)", err, string(b))
	}
	if got.AccessToken != "after-leftover" {
		t.Errorf("会话内容应为最新一次保存，实际 %+v", got)
	}
	// 残留原样留着（保存不负责清理），且本体权限仍是 0600。
	if fi, err := os.Stat(m.SessionFile()); err != nil {
		t.Fatalf("Stat 会话文件: %v", err)
	} else if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("会话文件权限应为 0600，实际 %o", perm)
	}
}

// e) 并发保存：唯一名互不冲突（旧实现会互相 EEXIST），全部成功且本体可解析。
func TestSaveSessionAtomic_ConcurrentSaves(t *testing.T) {
	m := newAtomicTestManager(t)
	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.saveSessionLocked(sampleSession(fmt.Sprintf("c-%d", i))); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发保存不应失败（唯一名互不冲突）: %v", err)
	}
	if left := tmpLeftovers(t, m.dataDir); len(left) != 0 {
		t.Fatalf("并发保存后不应残留临时文件，实际 %v", left)
	}
	b, err := os.ReadFile(m.SessionFile())
	if err != nil {
		t.Fatalf("读会话文件: %v", err)
	}
	var got Session
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("并发保存后本体应是完整 JSON: %v", err)
	}
	if !strings.HasPrefix(got.AccessToken, "c-") {
		t.Errorf("本体应是最后一次落盘的内容，实际 %+v", got)
	}
}
