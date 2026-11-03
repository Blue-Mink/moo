package api

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestFpkLocal 造独立实例（不碰全局 fpkLocalInstance，避免测试互相污染）。
func newTestFpkLocal() *fpkLocal {
	return &fpkLocal{
		index:       map[string]localFpkInfo{},
		cache:       map[string]localFpkInfo{},
		parsing:     map[string]bool{},
		missed:      map[string]time.Time{},
		byPath:      map[string]pathEntry{},
		parsingPath: map[string]bool{},
	}
}

// writeTestFpk 写一个最小可用 FPK（tar.gz + 根目录 manifest），manifest 为 key = value。
func writeTestFpk(t *testing.T, path, manifest string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试包失败: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "manifest", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("写 tar 头失败: %v", err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
}

// waitParsed 等待某路径的后台解析结束（Pending 转 false）。
func waitParsed(t *testing.T, f *fpkLocal, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !f.Pending(path) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("后台解析未在 5s 内结束: %s", path)
}

// writeTestFpkEntries 写一个含任意条目的 FPK（tar.gz），用于 i18n 等场景。
func writeTestFpkEntries(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("创建测试包失败: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("写 tar 头失败: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("写 %s 失败: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
}

// 0.6.391：manifest 用 ${common.display_name}/${common.desc} 占位时，
// 从 i18n/zh（优先）取真名；否则「已下载的 FPK」列表只剩包名。
func TestParseFpkManifestResolvesI18nPlaceholder(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fn-execute.fpk")
	writeTestFpkEntries(t, p, map[string]string{
		"manifest": "appname = fn-execute\nversion = 1.0.2\ndisplay_name = ${common.display_name}\ndesc = ${common.desc}\n",
		"i18n/zh":  "[common]\ndisplay_name=\"执行器\"\ndesc=\"执行器说明\"\n",
		"i18n/en":  "[common]\ndisplay_name=\"Execute\"\ndesc=\"Executor\"\n",
	})
	info, err := parseFpkManifest(p)
	if err != nil {
		t.Fatalf("parseFpkManifest: %v", err)
	}
	if info.DisplayName != "执行器" {
		t.Fatalf("display_name = %q, want 执行器（zh 优先）", info.DisplayName)
	}
	if info.Desc != "执行器说明" {
		t.Fatalf("desc = %q, want 执行器说明", info.Desc)
	}
	if info.AppName != "fn-execute" || info.Version != "1.0.2" {
		t.Fatalf("manifest 其它字段被破坏: %+v", info)
	}
}

// 已有真实 display_name 的包不受 i18n 影响（不覆盖）。
func TestParseFpkManifestKeepsExplicitDisplayName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "demo.fpk")
	writeTestFpkEntries(t, p, map[string]string{
		"manifest": "appname = demo\ndisplay_name = 显式名字\n",
		"i18n/zh":  "[common]\ndisplay_name=\"i18n 名字\"\n",
	})
	info, err := parseFpkManifest(p)
	if err != nil {
		t.Fatalf("parseFpkManifest: %v", err)
	}
	if info.DisplayName != "显式名字" {
		t.Fatalf("display_name = %q, want 显式名字", info.DisplayName)
	}
}

// 0.6.332 F4：Pending 反映路径级后台解析标记。
func TestFpkLocalPending(t *testing.T) {
	f := newTestFpkLocal()
	p := "/tmp/whatever.fpk"
	if f.Pending(p) {
		t.Fatalf("初始不应处于解析中")
	}
	f.mu.Lock()
	f.parsingPath[p] = true
	f.mu.Unlock()
	if !f.Pending(p) {
		t.Fatalf("标记后 Pending 应为 true")
	}
	f.mu.Lock()
	delete(f.parsingPath, p)
	f.mu.Unlock()
	if f.Pending(p) {
		t.Fatalf("清除标记后 Pending 应为 false")
	}
}

// 首次 DisplayFor = 未就绪（后台解析），稍后拿到 display_name / appname。
func TestFpkLocalDisplayForParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "demo_1.0.0_x86.fpk")
	writeTestFpk(t, p, "appname = demo\ndisplay_name = 演示应用\nversion = 1.0.0\n")

	f := newTestFpkLocal()
	if info, ok := f.DisplayFor(p); ok || info.DisplayName != "" {
		t.Fatalf("首次调用应返回未就绪，实际 ok=%v info=%+v", ok, info)
	}
	waitParsed(t, f, p)

	info, ok := f.DisplayFor(p)
	if !ok {
		t.Fatalf("解析完成后 DisplayFor 应命中")
	}
	if info.AppName != "demo" || info.DisplayName != "演示应用" || info.Version != "1.0.0" {
		t.Fatalf("manifest 解析结果不符: %+v", info)
	}
	if f.Pending(p) {
		t.Fatalf("解析完成后 Pending 应为 false")
	}
}

// 损坏包：解析失败落负缓存 → 不会重复解析，也不会永远显示「解析中」。
func TestFpkLocalCorruptNegativeCache(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "broken.fpk")
	if err := os.WriteFile(p, []byte("not an fpk archive"), 0o644); err != nil {
		t.Fatalf("写坏包失败: %v", err)
	}

	f := newTestFpkLocal()
	if _, ok := f.DisplayFor(p); ok {
		t.Fatalf("损坏包不应解析成功")
	}
	waitParsed(t, f, p)

	f.mu.Lock()
	e, has := f.byPath[p]
	f.mu.Unlock()
	if !has || e.ok {
		t.Fatalf("损坏包应落负缓存条目（ok=false），实际 has=%v ok=%v", has, e.ok)
	}

	// 第二次调用不得重新开解析（否则首开列表会反复重解析损坏包）
	if _, ok := f.DisplayFor(p); ok {
		t.Fatalf("负缓存后仍返回命中")
	}
	if f.Pending(p) {
		t.Fatalf("负缓存后不应重新进入解析中")
	}
}

// parsing 字段按 omitempty 输出（老客户端不受影响）。
func TestFpkDownloadFileParsingJSON(t *testing.T) {
	b, err := json.Marshal(FpkDownloadFile{Name: "a.fpk"})
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	if strings.Contains(string(b), "parsing") {
		t.Fatalf("parsing=false 时不应出现在 JSON 里: %s", b)
	}
	b, err = json.Marshal(FpkDownloadFile{Name: "a.fpk", Parsing: true})
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	if !strings.Contains(string(b), `"parsing":true`) {
		t.Fatalf("parsing=true 应出现在 JSON 里: %s", b)
	}
}
