package api

import (
	"os"
	"path/filepath"
	"testing"
)

// 0.6.320：系统空间（/var/apps）官方系统应用图标本地兜底。
// trim.security 等 15 个应用的图标是面板相对路径（需 ost 登录态，
// 0.6.255 纯 OAuth 后恒 404），本机 /var/apps/<app>/ICON.PNG 是唯一来源。

// 系统空间布局：图标在应用根目录。
func TestLocalAppIconFrom_SystemSpaceRoot(t *testing.T) {
	sysDir := filepath.Join(t.TempDir(), "var-apps", "trim.demo")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "ICON.PNG"), []byte("sys-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, ok := localAppIconFrom("trim.demo", []string{sysDir})
	if !ok || string(b) != "sys-png" {
		t.Fatalf("应命中系统空间根目录图标: ok=%v b=%q", ok, b)
	}
}

// 双布局同存：用户空间优先（保持旧行为）。
func TestLocalAppIconFrom_UserSpaceFirst(t *testing.T) {
	u := filepath.Join(t.TempDir(), "appcenter", "demo", "ui", "images")
	if err := os.MkdirAll(u, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(u, "icon_0.png"), []byte("user-icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := filepath.Join(t.TempDir(), "var-apps", "demo")
	if err := os.MkdirAll(s, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s, "ICON.PNG"), []byte("sys-icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, ok := localAppIconFrom("demo", []string{u, s})
	if !ok || string(b) != "user-icon" {
		t.Fatalf("应优先用户空间图标: ok=%v b=%q", ok, b)
	}
}

// 系统空间也兼容 ui/images 子目录布局。
func TestLocalAppIconFrom_SystemSpaceUIImages(t *testing.T) {
	s := filepath.Join(t.TempDir(), "var-apps", "trim.x", "ui", "images")
	if err := os.MkdirAll(s, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s, "ICON.png"), []byte("sys-ui-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, ok := localAppIconFrom("trim.x", []string{s})
	if !ok || string(b) != "sys-ui-png" {
		t.Fatalf("应命中系统空间 ui/images: ok=%v b=%q", ok, b)
	}
}

// 安全回归：路径穿越/空名拒读（0.6.207 审核约束）。
func TestLocalAppIconFrom_PathTraversal(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../etc", "a/b", `a\b`} {
		if _, ok := localAppIconFrom(bad, nil); ok {
			t.Fatalf("appName=%q 应拒读", bad)
		}
	}
}

// 全部未命中：诚实返回 false（调用方继续 404）。
func TestLocalAppIconFrom_AllMiss(t *testing.T) {
	if _, ok := localAppIconFrom("ghost", []string{t.TempDir()}); ok {
		t.Fatal("无图标应返回 false")
	}
	// 空文件不算命中
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "ICON.PNG"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := localAppIconFrom("any", []string{d}); ok {
		t.Fatal("空文件图标应视为未命中")
	}
}
