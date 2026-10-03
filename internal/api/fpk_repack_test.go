package api

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// writeTpkDir 造一个仿真 TPK 目录（manifest + app.tgz + cmd/ + wizard/ + ICON）。
func writeTpkDir(t *testing.T, root string) {
	t.Helper()
	mustWrite := func(rel, content string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("manifest", "appname = test-app\nversion = 1.0.0\n", 0o644)
	mustWrite("app.tgz", "APP-TGZ-BYTES-PLACEHOLDER", 0o644)
	mustWrite("cmd/install_callback", "#!/bin/bash\nexit 0\n", 0o755)
	mustWrite("wizard/config", `[{"stepTitle":"端口"}]`, 0o644)
	mustWrite("ICON.PNG", "PNGDATA", 0o644)
	mustWrite(".hidden-junk", "SHOULD-BE-SKIPPED", 0o644) // 隐藏条目必须被跳过
}

// readTarEntries 解开 FPK 返回 条目名→内容（目录记为 ""）。
func readTarEntries(t *testing.T, fpkPath string) map[string]string {
	t.Helper()
	f, err := os.Open(fpkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("不是 gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeDir {
			out[hdr.Name] = ""
			continue
		}
		buf := make([]byte, hdr.Size)
		if _, err := io.ReadFull(tr, buf); err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(buf)
	}
	return out
}

func TestRepackTpkDirToFpk_StructureAndContent(t *testing.T) {
	src := t.TempDir()
	writeTpkDir(t, src)
	dst := filepath.Join(t.TempDir(), "test-app-1.0.0.fpk")
	if err := repackTpkDirToFpk(src, dst); err != nil {
		t.Fatalf("重打包失败: %v", err)
	}
	// 成功路径：无 .part 残留
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Error("成功路径不应残留 .part 临时文件")
	}
	entries := readTarEntries(t, dst)
	want := map[string]string{
		"manifest":            "appname = test-app\nversion = 1.0.0\n",
		"app.tgz":             "APP-TGZ-BYTES-PLACEHOLDER",
		"cmd/install_callback": "#!/bin/bash\nexit 0\n",
		"wizard/config":        `[{"stepTitle":"端口"}]`,
		"ICON.PNG":             "PNGDATA",
	}
	for name, content := range want {
		got, ok := entries[name]
		if !ok {
			t.Errorf("缺少条目 %s（实际: %v）", name, keysOf(entries))
			continue
		}
		if got != content {
			t.Errorf("条目 %s 内容不符: got %q want %q", name, got, content)
		}
	}
	if _, ok := entries[".hidden-junk"]; ok {
		t.Error("隐藏条目不应被打进 FPK")
	}
	if len(entries) != len(want)+2 { // +2 = cmd/ 与 wizard/ 目录条目
		t.Errorf("条目数量异常: %v", keysOf(entries))
	}
}

func TestRepackTpkDirToFpk_MissingManifestOrAppTgz(t *testing.T) {
	src := t.TempDir()
	writeTpkDir(t, src)
	if err := os.Remove(filepath.Join(src, "manifest")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "x.fpk")
	if err := repackTpkDirToFpk(src, dst); err == nil {
		t.Error("缺 manifest 必须报错")
	} else if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("失败路径不应产出目标文件")
	}
	// 恢复 manifest、删 app.tgz
	if err := os.WriteFile(filepath.Join(src, "manifest"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "app.tgz")); err != nil {
		t.Fatal(err)
	}
	if err := repackTpkDirToFpk(src, dst); err == nil {
		t.Error("缺 app.tgz 必须报错")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestReadFpkMembers 向导探测直读路径：从 FPK tar 里只取 wizard/install +
// manifest，不全量解包；不存在的成员不返回。
func TestReadFpkMembers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "out.fpk")
	writeTpkDir(t, src)
	wz := `[{"stepTitle":"配置","items":[{"type":"text","field":"wizard_path","label":"备份目录","helpText":"提示文案"}]}]`
	if err := os.MkdirAll(filepath.Join(src, "wizard"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "wizard", "install"), []byte(wz), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repackTpkDirToFpk(src, dst); err != nil {
		t.Fatal(err)
	}

	members, err := readFpkMembers(dst, []string{"wizard/install", "manifest", "wizard/nope"})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(members["wizard/install"]); got != wz {
		t.Errorf("wizard/install = %q, want 原文一致", got)
	}
	if v := fpkManifestVersion(members["manifest"]); v != "1.0.0" {
		t.Errorf("manifest version = %q, want 1.0.0", v)
	}
	if _, ok := members["wizard/nope"]; ok {
		t.Error("不存在的成员不得返回")
	}

	// 非 tar.gz 文件必须报错（坏包不得静默吞成「无向导」）
	bad := filepath.Join(dir, "bad.fpk")
	if err := os.WriteFile(bad, []byte("not a tar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFpkMembers(bad, []string{"wizard/install"}); err == nil {
		t.Error("非 tar.gz 必须报错")
	}
}
