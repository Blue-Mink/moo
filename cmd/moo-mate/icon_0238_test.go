package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 0.2.38：图标抽取 / 候选链 / handleIcon 磁盘缓存单测。

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 200, G: 30, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractFPKIcon_PrefersLarger_0238(t *testing.T) {
	small := makeTestPNG(t, 64, 64)
	large := makeTestPNG(t, 256, 256)
	fpk := makeTestTarGz(t, map[string][]byte{
		"manifest":     []byte("appname = demo\nversion = 1.0.0\n"),
		"ICON.PNG":     small,
		"ICON_256.PNG": large,
	})
	icon, ext, ok := extractFPKIcon(fpk)
	if !ok || ext != "png" {
		t.Fatalf("应抽到 PNG 图标: ok=%v ext=%s", ok, ext)
	}
	if len(icon) != len(large) {
		t.Errorf("应选尺寸更大的 ICON_256.PNG（got %d B, want %d B）", len(icon), len(large))
	}
}

func TestExtractFPKIcon_AppTgzFallback_0238(t *testing.T) {
	icon := makeTestPNG(t, 128, 128)
	appTgz := makeTestTarGz(t, map[string][]byte{
		"ui/images/ICON.PNG": icon,
		"moo-mate":           []byte("not-an-icon"),
	})
	fpk := makeTestTarGz(t, map[string][]byte{
		"manifest": []byte("appname = demo\nversion = 1.0.0\n"),
		"app.tgz":  appTgz,
	})
	got, ext, ok := extractFPKIcon(fpk)
	if !ok || ext != "png" || len(got) != len(icon) {
		t.Fatalf("应从 app.tgz 的 ui/images 抽到图标: ok=%v ext=%s len=%d", ok, ext, len(got))
	}
}

func TestExtractFPKIcon_RejectsBadData_0238(t *testing.T) {
	fpk := makeTestTarGz(t, map[string][]byte{
		"manifest":  []byte("appname = demo\n"),
		"ICON.PNG":  []byte("this is not a png"),
		"ICON2.PNG": []byte("#VALUE!"),
	})
	if _, _, ok := extractFPKIcon(fpk); ok {
		t.Error("坏图标文件不应通过")
	}
}

func TestInspectFPKCarriesIcon_0238(t *testing.T) {
	icon := makeTestPNG(t, 32, 32)
	fpk := makeTestTarGz(t, map[string][]byte{
		"manifest": []byte("appname = demo\nversion = 2.0.0\n"),
		"ICON.PNG": icon,
	})
	meta, err := inspectFPK(bytes.NewReader(fpk))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Manifest["appname"] != "demo" {
		t.Errorf("manifest 解析失败: %v", meta.Manifest)
	}
	if len(meta.Icon) != len(icon) || meta.IconExt != "png" {
		t.Errorf("inspectFPK 应顺带返回图标: len=%d ext=%s", len(meta.Icon), meta.IconExt)
	}
}

func TestMateIconCandidates_0238(t *testing.T) {
	gh := mateIconCandidates("fn-ccswitch", "https://github.com/Maluslock/FnDepot/")
	if len(gh) == 0 {
		t.Fatal("GitHub 候选为空")
	}
	joined := strings.Join(gh, "\n")
	for _, want := range []string{
		"https://raw.githubusercontent.com/Maluslock/FnDepot/main/fn-ccswitch/ICON.PNG",
		"https://raw.githubusercontent.com/Maluslock/FnDepot/master/ICON.PNG",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少候选 %s\n实际:\n%s", want, joined)
		}
	}
	gt := mateIconCandidates("demo", "https://gitee.com/hhxs2025/fn-appstores")
	if len(gt) == 0 || !strings.Contains(strings.Join(gt, "\n"), "https://gitee.com/hhxs2025/fn-appstores/raw/master/demo/ICON.PNG") {
		t.Errorf("Gitee 候选不符: %v", gt)
	}
	if got := mateIconCandidates("demo", "http://nas.example.lan:5660"); len(got) != 0 {
		t.Errorf("非仓库地址不应产生候选: %v", got)
	}
}

func TestIconCacheName_0238(t *testing.T) {
	a := iconCacheName("com.dustinky.qwenpaw")
	b := iconCacheName("com/dustinky/qwenpaw")
	if strings.ContainsAny(a, `/\`) || strings.ContainsAny(b, `/\`) {
		t.Fatalf("缓存名不应含路径分隔符: %q %q", a, b)
	}
	if a == b {
		t.Error("归一后同名的应用应通过哈希区分")
	}
	if iconCacheName(strings.Repeat("很长的名字", 40)) == "" {
		t.Error("超长名应生成非空缓存名")
	}
}

func TestHandleIconDiskCache_0238(t *testing.T) {
	boot := t.TempDir()
	idx := newIndex(filepath.Join(boot, "index.json"))
	if err := idx.upsert(&indexedApp{appEntry: appEntry{IconURL: ""}, Appname: "demo", Source: "https://github.com/o/r"}); err != nil {
		t.Fatal(err)
	}
	c := newCompanion("unix:///tmp/none.sock", "http://127.0.0.1:1", "", nil, idx)
	c.bootDir = boot
	icon := makeTestPNG(t, 48, 48)
	c.saveIconFile("demo", icon, "png")
	if _, err := os.Stat(filepath.Join(c.iconDir(), iconCacheName("demo")+".png")); err != nil {
		t.Fatalf("图标未落盘: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/icon/demo", nil)
	req.SetPathValue("appname", "demo")
	rec := httptest.NewRecorder()
	c.handleIcon(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应命中磁盘缓存 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	// 未知应用 → 404
	req2 := httptest.NewRequest(http.MethodGet, "/api/icon/nope", nil)
	req2.SetPathValue("appname", "nope")
	rec2 := httptest.NewRecorder()
	c.handleIcon(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("未知应用应 404，实际 %d", rec2.Code)
	}
}

func TestExtractFpkIconStream_StopsAtRootIcon_0238(t *testing.T) {
	big := makeTestPNG(t, 256, 256)
	smallInner := makeTestPNG(t, 64, 64)
	appTgz := makeTestTarGz(t, map[string][]byte{"ui/images/ICON.PNG": smallInner})
	fpk := makeTestTarGz(t, map[string][]byte{
		"manifest": []byte("appname = demo\n"),
		"ICON.PNG": big,
		"app.tgz":  appTgz,
	})
	got, ext, ok := extractFpkIconStream(bytes.NewReader(fpk))
	if !ok || ext != "png" || len(got) != len(big) {
		t.Fatalf("应流式命中包根图标: ok=%v ext=%s len=%d want=%d", ok, ext, len(got), len(big))
	}
}

// makeTestTarGzOrdered 按给定顺序写 tar.gz（map 版顺序随机，测「app.tgz 在前」时必须有序）。
func makeTestTarGzOrdered(t *testing.T, names []string, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// 实测 2026-11-04：新版 FPK 把 app.tgz 放在包根第一个条目、包根没有 ICON.PNG
// （QwenPaw / EasyTier 都是这个布局）→ 必须进入 app.tgz 的 ui/images 找图标。
func TestExtractFpkIconStream_AppTgzFirst_0238(t *testing.T) {
	inner := makeTestPNG(t, 256, 256)
	appTgz := makeTestTarGz(t, map[string][]byte{"ui/images/ICON.PNG": inner})
	fpk := makeTestTarGzOrdered(t, []string{"app.tgz", "manifest"}, map[string][]byte{
		"app.tgz":  appTgz,
		"manifest": []byte("appname = demo\n"),
	})
	got, ext, ok := extractFpkIconStream(bytes.NewReader(fpk))
	if !ok || ext != "png" || len(got) != len(inner) {
		t.Fatalf("app.tgz 在前的包也应能从内部抽到图标: ok=%v ext=%s len=%d want=%d", ok, ext, len(got), len(inner))
	}
}
