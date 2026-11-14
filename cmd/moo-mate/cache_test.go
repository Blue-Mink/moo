package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPurgeAppArtifacts：删除应用时要连本机派生痕迹一起清（磁盘图标 / 内存图标 /
// 图标失败负缓存），但绝不能顺手删掉不属于该应用的临时文件。
func TestPurgeAppArtifacts(t *testing.T) {
	boot := t.TempDir()
	c := &companion{
		bootDir: boot,
		icons:   map[string][]byte{},
		hosted:  map[string][]byte{},
	}
	dir := c.iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	app := "demo.app"
	iconPath := filepath.Join(dir, iconCacheName(app)+".png")
	if err := os.WriteFile(iconPath, []byte("PNG"), 0o644); err != nil {
		t.Fatalf("写入图标: %v", err)
	}
	tmpPath := filepath.Join(dir, "unrelated.png.tmp")
	if err := os.WriteFile(tmpPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("写入临时文件: %v", err)
	}
	c.iconMu.Lock()
	c.icons[app] = []byte("PNG")
	c.iconMu.Unlock()
	mateIconFailMu.Lock()
	mateIconFailAt[app] = time.Now()
	mateIconFailMu.Unlock()
	defer func() {
		mateIconFailMu.Lock()
		delete(mateIconFailAt, app)
		mateIconFailMu.Unlock()
	}()

	purged, err := c.purgeAppArtifacts(app)
	if err != nil {
		t.Fatalf("purgeAppArtifacts: %v", err)
	}
	if !purged {
		t.Fatalf("磁盘/内存都有残留，应返回 purged=true")
	}
	if _, err := os.Stat(iconPath); !os.IsNotExist(err) {
		t.Fatalf("磁盘图标未删除：%v", err)
	}
	c.iconMu.Lock()
	_, stillCached := c.icons[app]
	c.iconMu.Unlock()
	if stillCached {
		t.Fatalf("内存图标缓存未清除")
	}
	mateIconFailMu.Lock()
	_, stillFailed := mateIconFailAt[app]
	mateIconFailMu.Unlock()
	if stillFailed {
		t.Fatalf("图标失败负缓存未清除")
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("误删了与本应用无关的临时文件：%v", err)
	}

	// 幂等：再来一次没有残留可删 → false, nil
	if purged2, err := c.purgeAppArtifacts(app); err != nil || purged2 {
		t.Fatalf("重复清理应为 (false, nil)，实际 (%v, %v)", purged2, err)
	}
}

// TestHandleCacheCleanAll：0.2.70 设置页两个清理按钮合并为一个后，scope=all 必须
// 同时删掉图标文件（png/webp）与 *.tmp 残留，不碰目录里的其它文件，
// 并清掉内存图标缓存与图标失败负缓存（否则页面还拿旧图标、被负缓存挡住重拉）。
func TestHandleCacheCleanAll(t *testing.T) {
	boot := t.TempDir()
	c := &companion{bootDir: boot, icons: map[string][]byte{}, hosted: map[string][]byte{}}
	dir := c.iconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range map[string]string{
		"a.png": "PNG", "b.webp": "WEBP", "write.png.tmp": "tmp", "keep.txt": "keep",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入 %s: %v", name, err)
		}
	}
	c.iconMu.Lock()
	c.icons["demo.app"] = []byte("PNG")
	c.iconMu.Unlock()
	mateIconFailMu.Lock()
	mateIconFailAt["demo.app"] = time.Now()
	mateIconFailMu.Unlock()
	defer func() {
		mateIconFailMu.Lock()
		delete(mateIconFailAt, "demo.app")
		mateIconFailMu.Unlock()
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/cache/clean", strings.NewReader(`{"scope":"all"}`))
	c.handleCacheClean(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Scope   string `json:"scope"`
		Removed int    `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if resp.Scope != "all" {
		t.Fatalf("scope=%q，期望 all", resp.Scope)
	}
	if resp.Removed != 3 {
		t.Fatalf("removed=%d，期望 3（a.png + b.webp + write.png.tmp）", resp.Removed)
	}
	for _, gone := range []string{"a.png", "b.webp", "write.png.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s 未被删除：%v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.txt")); err != nil {
		t.Fatalf("误删了无关文件 keep.txt：%v", err)
	}
	c.iconMu.Lock()
	_, stillCached := c.icons["demo.app"]
	c.iconMu.Unlock()
	if stillCached {
		t.Fatalf("内存图标缓存未清除")
	}
	mateIconFailMu.Lock()
	_, stillFailed := mateIconFailAt["demo.app"]
	mateIconFailMu.Unlock()
	if stillFailed {
		t.Fatalf("图标失败负缓存未清除")
	}
}
