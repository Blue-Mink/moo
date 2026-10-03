// 官方 TPK 目录 → 标准 FPK 重打包（0.6.261）。
//
// 背景：官方 TPK 型应用（原生/docker）经 daemon cloud 通道
// （platform.DownloadCloud，unix socket 免登录）下载后落盘为目录
// /vol1/appcenter-downloads/<app>-<ver>-tpk/，其布局与 FPK staging
// 一致：manifest + app.tgz + cmd/ + config/ + wizard/ + ICON*。
// 把目录顶层条目原样打进 tar.gz 即得可安装 FPK——测试机实测 1Panel
// 1.0.12 重打包 41.6MB、结构正确（wizard 端口向导全保留）。
package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// repackTpkDirToFpk 把 TPK 目录重打包为标准 FPK（tar.gz），写入 dstPath。
// 校验：顶层必须含 manifest 与 app.tgz（appcenter 安装解包的硬依赖），
// 缺失时明确报错，绝不产出装不上的包。
// 写入策略：先落 .part 临时文件，全部成功后原子改名——半截产物不会被
// 设置页「已下载 FPK」列表拾取。
func repackTpkDirToFpk(srcDir, dstPath string) error {
	top, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("读取 TPK 目录失败: %w", err)
	}
	names := make([]string, 0, len(top))
	for _, e := range top {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	hasManifest, hasAppTgz := false, false
	for _, n := range names {
		switch n {
		case "manifest":
			hasManifest = true
		case "app.tgz":
			hasAppTgz = true
		}
	}
	if !hasManifest {
		return fmt.Errorf("TPK 目录缺少 manifest，无法重打包: %s", srcDir)
	}
	if !hasAppTgz {
		return fmt.Errorf("TPK 目录缺少 app.tgz，无法重打包: %s", srcDir)
	}

	tmp := dstPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建目标 FPK 失败: %w", err)
	}
	cleanup := func() {
		f.Close()
		os.Remove(tmp)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// 跳过隐藏/临时条目（断点残留、系统文件），防脏包。
		if strings.HasPrefix(filepath.Base(p), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		switch {
		case info.IsDir():
			hdr.Name += "/"
			hdr.Mode = 0o755
			return tw.WriteHeader(hdr)
		case info.Mode()&fs.ModeSymlink != 0:
			// TPK 目录正常不含符号链接；一律跳过（防 tar 路径逃逸）。
			return nil
		default:
			hdr.Mode = 0o644
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, err = tw.Write(data)
			return err
		}
	})
	if walkErr != nil {
		cleanup()
		return fmt.Errorf("打包 TPK 目录失败: %w", walkErr)
	}
	if err := tw.Close(); err != nil {
		cleanup()
		return fmt.Errorf("写入 FPK 失败: %w", err)
	}
	if err := gz.Close(); err != nil {
		cleanup()
		return fmt.Errorf("写入 FPK 失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("写入 FPK 失败: %w", err)
	}
	if err := os.Rename(tmp, dstPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("落盘 FPK 失败: %w", err)
	}
	return nil
}
